package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mockingo/mockingo-cli/internal/auth"
)

const maxInternalBody = 1 << 20

type statusRequest struct {
	EndpointIDs []string `json:"endpointIds"`
}

type tunnelStatus struct {
	Status      string     `json:"status"`
	SessionID   string     `json:"sessionId,omitempty"`
	Protocol    string     `json:"protocol,omitempty"`
	LocalPort   int        `json:"localPort,omitempty"`
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`
}

func (s *Server) handleInternal(w http.ResponseWriter, r *http.Request) {
	if !s.requireInternalAuth(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/internal/v1/tunnels/status":
		s.handleInternalStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/internal/v1/tunnels/") && strings.HasSuffix(r.URL.Path, "/disconnect"):
		s.handleInternalDisconnect(w, r)
	default:
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "Not found."})
	}
}

func (s *Server) requireInternalAuth(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !auth.BearerMatches(values[0], s.config.GatewayInternalToken) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "Authentication required."})
		return false
	}
	return true
}

func (s *Server) handleInternalStatus(w http.ResponseWriter, r *http.Request) {
	s.metrics.internalStatusRequests.Add(1)
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInternalBody))
	decoder.DisallowUnknownFields()
	var request statusRequest
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "Invalid status request."})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "Invalid status request."})
		return
	}
	if len(request.EndpointIDs) > s.config.InternalStatusMaxBatch {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "batch_too_large", Message: "The status batch is too large."})
		return
	}
	unique := make([]string, 0, len(request.EndpointIDs))
	seen := make(map[string]struct{}, len(request.EndpointIDs))
	for _, endpointID := range request.EndpointIDs {
		if uuid.Validate(endpointID) != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_endpoint_id", Message: "Every endpoint ID must be a UUID."})
			return
		}
		if _, found := seen[endpointID]; !found {
			seen[endpointID] = struct{}{}
			unique = append(unique, endpointID)
		}
	}
	active := s.store.Statuses(unique)
	statuses := make(map[string]tunnelStatus, len(unique))
	for _, endpointID := range unique {
		if tunnel, found := active[endpointID]; found {
			connectedAt := tunnel.ConnectedAt.UTC()
			statuses[endpointID] = tunnelStatus{Status: "connected", SessionID: tunnel.SessionID, Protocol: tunnel.Protocol, LocalPort: tunnel.LocalPort, ConnectedAt: &connectedAt}
		} else {
			statuses[endpointID] = tunnelStatus{Status: "offline"}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses})
}

func (s *Server) handleInternalDisconnect(w http.ResponseWriter, r *http.Request) {
	s.metrics.internalDisconnectRequests.Add(1)
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
		return
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/tunnels/"), "/disconnect")
	if rest == "" || strings.Contains(rest, "/") || uuid.Validate(rest) != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_endpoint_id", Message: "The endpoint ID must be a UUID."})
		return
	}
	conn, active := s.store.RequestDisconnect(rest, DisconnectInternal)
	if active {
		conn.closeGracefully(DisconnectInternal)
	}
	w.WriteHeader(http.StatusNoContent)
}
