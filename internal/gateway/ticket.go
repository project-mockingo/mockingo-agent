package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/internal/gateway/backendcallback"
	"github.com/mockingo/mockingo-cli/internal/gateway/ticketauth"
)

const (
	DisconnectClientClosed      = "client_closed"
	DisconnectGatewayShutdown   = "gateway_shutdown"
	DisconnectHeartbeatTimeout  = "heartbeat_timeout"
	DisconnectProtocolError     = "protocol_error"
	DisconnectInternal          = "internal_disconnect"
	DisconnectPublicProxyError  = "public_proxy_error"
	DisconnectBackendSyncFailed = "backend_sync_failed"
	DisconnectReplacedDenied    = "replaced_not_allowed"
)

func (s *Server) handleTicketConnect(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
		return
	}
	if s.config.TicketVerifier == nil {
		writeJSON(w, http.StatusForbidden, apiError{Code: "unauthorized", Message: "Tunnel ticket authentication is disabled."})
		return
	}
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "gateway_unavailable", Message: "The gateway is unavailable."})
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		w.Header().Set("Upgrade", "websocket")
		writeJSON(w, http.StatusUpgradeRequired, apiError{Code: "upgrade_required", Message: "A WebSocket upgrade is required."})
		return
	}
	raw, err := ticketauth.ParseBearer(r.Header.Values("Authorization"))
	if err != nil {
		s.metrics.ObserveTicketValidation("invalid")
		s.ticketError(w, http.StatusUnauthorized, "invalid_tunnel_ticket", "The tunnel authorization is invalid or expired.")
		return
	}
	claims, err := s.config.TicketVerifier.Verify(r.Context(), raw)
	if err != nil {
		s.handleTicketValidationError(w, claims, err, requestID)
		return
	}
	if s.config.TicketVerifier.EstablishmentExpired(claims, time.Now()) {
		s.ticketError(w, http.StatusUnauthorized, "expired_tunnel_ticket", "The tunnel authorization is invalid or expired.")
		return
	}
	if value, lookupErr := s.repository.GetEndpointByName(r.Context(), claims.EndpointName); lookupErr == nil {
		if value.ID != claims.EndpointID {
			s.rejected(claims, "endpoint_identity_conflict", requestID)
			s.ticketError(w, http.StatusConflict, "endpoint_identity_conflict", "The endpoint identity conflicts with gateway persistence.")
			return
		}
	} else if !errors.Is(lookupErr, endpoint.ErrNotFound) {
		s.rejected(claims, "gateway_unavailable", requestID)
		s.ticketError(w, http.StatusServiceUnavailable, "gateway_unavailable", "The gateway is temporarily unavailable.")
		return
	}
	if err := s.config.TicketVerifier.Consume(claims.SessionID, claims.ExpiresAt); err != nil {
		if errors.Is(err, ticketauth.ErrReplay) {
			s.rejected(claims, "session_replayed", requestID)
			s.ticketError(w, http.StatusConflict, "tunnel_session_replayed", "This tunnel ticket has already been used.")
		} else {
			s.rejected(claims, "gateway_capacity_reached", requestID)
			s.ticketError(w, http.StatusServiceUnavailable, "gateway_unavailable", "The gateway is temporarily unavailable.")
		}
		return
	}
	active := ActiveTunnel{
		EndpointID: claims.EndpointID, SessionID: claims.SessionID, OwnerUserID: claims.Subject,
		EndpointName: claims.EndpointName, Hostname: claims.EndpointName + "." + strings.ToLower(strings.TrimSuffix(s.config.BaseDomain, ".")),
		Protocol: claims.Protocol, LocalPort: claims.LocalPort, ProtocolVersion: claims.ProtocolVersion,
		ConnectedAt: time.Now().UTC(), AuthMethod: TunnelAuthTicket,
	}
	if err := s.store.ReserveTicket(active, claims.ExpiresAt); err != nil {
		reason := "endpoint_already_connected"
		code := reason
		if errors.Is(err, ErrSessionConnected) {
			reason = "session_replayed"
			code = "tunnel_session_replayed"
		}
		s.rejected(claims, reason, requestID)
		s.ticketError(w, http.StatusConflict, code, "This endpoint or tunnel session is already connected.")
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.store.CancelReserved(claims.SessionID)
		return
	}
	conn := newConnection(ws)
	if s.config.TicketVerifier.EstablishmentExpired(claims, time.Now()) {
		s.store.CancelReserved(claims.SessionID)
		conn.closeGracefully("expired_tunnel_ticket")
		return
	}
	if err := s.store.AttachReserved(claims.SessionID, conn); err != nil {
		s.store.CancelReserved(claims.SessionID)
		conn.closeGracefully(DisconnectProtocolError)
		return
	}
	s.metrics.tunnelConnected(string(TunnelAuthTicket))
	s.tunnelWG.Add(1)
	defer s.tunnelWG.Done()
	defer func() {
		conn.close()
		active, reason, removed := s.store.detach(claims.SessionID, conn, DisconnectClientClosed)
		if removed {
			s.disconnected(active, reason, requestID)
			s.config.Logger.Info("tunnel disconnected", "requestId", requestID, "endpointId", active.EndpointID, "endpointName", active.EndpointName, "sessionId", active.SessionID, "authMethod", active.AuthMethod, "reason", reason)
		}
	}()
	registered, _ := s.store.ActiveBySession(claims.SessionID)
	s.config.Logger.Info("tunnel connected", "requestId", requestID, "gatewayInstanceId", s.config.GatewayInstanceID, "endpointId", registered.EndpointID, "endpointName", registered.EndpointName, "sessionId", registered.SessionID, "protocol", registered.Protocol, "protocolVersion", registered.ProtocolVersion, "authMethod", registered.AuthMethod)
	callbackCtx, cancel := s.callbackContext(requestID)
	err = s.config.CallbackClient.Connected(callbackCtx, backendcallback.ConnectedEvent{
		SessionID: claims.SessionID, EndpointID: claims.EndpointID, GatewayInstanceID: s.config.GatewayInstanceID, ConnectedAt: registered.ConnectedAt.UTC(),
	})
	cancel()
	if err != nil {
		_, _ = s.store.RequestDisconnect(claims.EndpointID, DisconnectBackendSyncFailed)
		conn.closeGracefully(DisconnectBackendSyncFailed)
		return
	}
	_ = conn.readLoop()
}

func (s *Server) handleTicketValidationError(w http.ResponseWriter, claims ticketauth.TunnelTicketClaims, err error, requestID string) {
	switch {
	case errors.Is(err, ticketauth.ErrExpiredTicket):
		s.ticketError(w, http.StatusUnauthorized, "expired_tunnel_ticket", "The tunnel authorization is invalid or expired.")
	case errors.Is(err, ticketauth.ErrUnsupportedProtocol):
		if claims.SessionID != "" {
			s.rejected(claims, "unsupported_protocol", requestID)
		}
		s.ticketError(w, http.StatusConflict, "unsupported_protocol", "This tunnel protocol is not supported.")
	case errors.Is(err, ticketauth.ErrUnsupportedProtocolVersion):
		if claims.SessionID != "" {
			s.rejected(claims, "unsupported_protocol_version", requestID)
		}
		s.ticketError(w, http.StatusConflict, "unsupported_protocol_version", "This tunnel protocol version is not supported.")
	default:
		s.ticketError(w, http.StatusUnauthorized, "invalid_tunnel_ticket", "The tunnel authorization is invalid or expired.")
	}
}

func (s *Server) ticketError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message})
}

func (s *Server) callbackContext(requestID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), s.config.BackendCallbackBudget)
	return backendcallback.WithRequestID(ctx, requestID), cancel
}

func (s *Server) rejected(claims ticketauth.TunnelTicketClaims, reason, requestID string) {
	s.callbackWG.Add(1)
	go func() {
		defer s.callbackWG.Done()
		ctx, cancel := s.callbackContext(requestID)
		defer cancel()
		if err := s.config.CallbackClient.Rejected(ctx, backendcallback.RejectedEvent{
			SessionID: claims.SessionID, EndpointID: claims.EndpointID, RejectedAt: time.Now().UTC(), Reason: reason,
		}); err != nil {
			s.config.Logger.Warn("backend rejection callback failed", "requestId", requestID, "endpointId", claims.EndpointID, "sessionId", claims.SessionID, "reason", reason)
		}
	}()
}

func (s *Server) disconnected(active ActiveTunnel, reason, requestID string) {
	if active.AuthMethod != TunnelAuthTicket {
		return
	}
	s.callbackWG.Add(1)
	go func() {
		defer s.callbackWG.Done()
		ctx, cancel := s.callbackContext(requestID)
		defer cancel()
		if err := s.config.CallbackClient.Disconnected(ctx, backendcallback.DisconnectedEvent{
			SessionID: active.SessionID, EndpointID: active.EndpointID, DisconnectedAt: time.Now().UTC(), Reason: reason,
		}); err != nil {
			s.config.Logger.Warn("backend disconnection callback failed", "requestId", requestID, "endpointId", active.EndpointID, "sessionId", active.SessionID, "reason", reason)
		}
	}()
}
