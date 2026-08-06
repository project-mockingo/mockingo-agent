package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/internal/auth"
	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/internal/naming"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

type Config struct {
	BaseDomain        string
	PublicScheme      string
	APIPublicURL      string
	APIToken          string
	DevToken          string
	RequestTimeout    time.Duration
	Repository        endpoint.Repository
	TrustedProxyCIDRs []*net.IPNet
	ReadyCheck        func(context.Context) error
	MetricsEnabled    bool
	Logger            *slog.Logger
}

type Server struct {
	config       Config
	repository   endpoint.Repository
	store        *Store
	upgrader     websocket.Upgrader
	shuttingDown atomic.Bool
	metrics      metrics
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(value)
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking is unavailable")
	}
	return hijacker.Hijack()
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func NewServer(config Config) *Server {
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 60 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Repository == nil {
		config.Repository = endpoint.NewMemoryRepository()
	}
	if config.APIToken == "" {
		config.APIToken = config.DevToken
	}
	return &Server{
		config: config, repository: config.Repository, store: NewStore(),
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }},
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	requestID, _ := randomHex(12)
	recorder := &statusWriter{ResponseWriter: w}
	w = recorder
	defer func() {
		duration := time.Since(started)
		s.metrics.observeRequest(duration)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		clientIP, _, _ := forwardedHeaders(r, s.config.PublicScheme, s.config.TrustedProxyCIDRs)
		endpointName, _, _ := ParseEndpointHost(r.Host, s.config.BaseDomain)
		s.config.Logger.Info("http request", "request_id", requestID, "method", r.Method, "host", r.Host, "path", r.URL.Path, "status", status, "duration_ms", duration.Milliseconds(), "endpoint_name", endpointName, "remote_ip", clientIP)
	}()
	switch {
	case r.URL.Path == "/health/live":
		writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
	case r.URL.Path == "/health/ready":
		s.handleReady(w, r)
	case r.URL.Path == "/metrics" && s.config.MetricsEnabled:
		s.handleMetrics(w, r)
	case r.URL.Path == "/api/v1/tunnels":
		s.handleCreate(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/tunnels/"):
		s.handleTunnel(w, r)
	case r.URL.Path == "/api/v1/endpoints":
		s.handleEndpoints(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/endpoints/"):
		s.handleEndpoint(w, r)
	default:
		s.handlePublic(w, r)
	}
}

func (s *Server) requireAPIAuth(w http.ResponseWriter, r *http.Request) bool {
	if !auth.BearerMatches(r.Header.Get("Authorization"), s.config.APIToken) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "Authentication required."})
		return false
	}
	return true
}

type createRequest struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	LocalPort int    `json:"localPort"`
}

type createResponse struct {
	ID           string `json:"id"`
	EndpointID   string `json:"endpointId"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	PublicURL    string `json:"publicUrl"`
	ConnectURL   string `json:"connectUrl"`
	SessionToken string `json:"sessionToken"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
		return
	}
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "gateway_unavailable", Message: "The gateway is shutting down."})
		return
	}
	if !s.requireAPIAuth(w, r) {
		return
	}
	defer r.Body.Close()
	var request createRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.registrationError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON request.")
		return
	}
	request.Name = strings.ToLower(request.Name)
	if request.Protocol != "http" {
		s.registrationError(w, http.StatusBadRequest, "unsupported_protocol", "Only the HTTP protocol is supported.")
		return
	}
	if err := naming.Validate(request.Name); err != nil {
		s.registrationError(w, http.StatusBadRequest, "invalid_endpoint_name", err.Error())
		return
	}
	if request.LocalPort < 1 || request.LocalPort > 65535 {
		s.registrationError(w, http.StatusBadRequest, "invalid_port", "localPort must be between 1 and 65535.")
		return
	}
	value, err := s.reserveEndpoint(r.Context(), request.Name)
	if err != nil {
		s.registrationError(w, http.StatusServiceUnavailable, "database_unavailable", "Endpoint storage is unavailable.")
		return
	}
	tunnel, token, err := s.store.Create(value, request.LocalPort)
	if errors.Is(err, ErrNameConnected) {
		s.registrationError(w, http.StatusConflict, "endpoint_already_connected", "This endpoint already has an active tunnel.")
		return
	}
	if err != nil {
		s.registrationError(w, http.StatusInternalServerError, "internal_error", "Could not create tunnel session.")
		return
	}
	apiPublicURL := s.config.APIPublicURL
	if apiPublicURL == "" {
		apiPublicURL = s.config.PublicScheme + "://" + r.Host
	}
	connectURL, err := tunnelConnectURL(apiPublicURL, tunnel.ID)
	if err != nil {
		s.store.DeleteSession(tunnel.ID)
		s.registrationError(w, http.StatusInternalServerError, "internal_error", "Gateway public URL is invalid.")
		return
	}
	writeJSON(w, http.StatusCreated, createResponse{
		ID: tunnel.ID, EndpointID: value.ID, Name: value.Name, Hostname: value.Hostname,
		PublicURL: s.config.PublicScheme + "://" + value.Hostname, ConnectURL: connectURL, SessionToken: token,
	})
}

func (s *Server) registrationError(w http.ResponseWriter, status int, code, message string) {
	s.metrics.registrationErrors.Add(1)
	writeJSON(w, status, apiError{Code: code, Message: message})
}

func (s *Server) reserveEndpoint(ctx context.Context, name string) (endpoint.Endpoint, error) {
	value, err := s.repository.GetEndpointByName(ctx, name)
	if err == nil {
		return value, nil
	}
	if !errors.Is(err, endpoint.ErrNotFound) {
		return endpoint.Endpoint{}, err
	}
	id, err := randomUUID()
	if err != nil {
		return endpoint.Endpoint{}, err
	}
	now := time.Now().UTC()
	value = endpoint.Endpoint{ID: id, Name: name, Hostname: name + "." + strings.ToLower(strings.TrimSuffix(s.config.BaseDomain, ".")), CreatedAt: now, UpdatedAt: now}
	value, err = s.repository.CreateEndpoint(ctx, value)
	if errors.Is(err, endpoint.ErrConflict) {
		return s.repository.GetEndpointByName(ctx, name)
	}
	return value, err
}

func tunnelConnectURL(apiPublicURL, id string) (string, error) {
	parsed, err := url.Parse(apiPublicURL)
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid API public URL")
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errors.New("invalid API public URL scheme")
	}
	parsed.Path = "/api/v1/tunnels/" + id + "/connect"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/tunnels/")
	if id, found := strings.CutSuffix(rest, "/connect"); found && !strings.Contains(id, "/") {
		s.handleConnect(w, r, id)
		return
	}
	if strings.Contains(rest, "/") || rest == "" || r.Method != http.MethodDelete {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "Not found."})
		return
	}
	if !s.requireAPIAuth(w, r) {
		return
	}
	s.store.DeleteSession(rest)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
		return
	}
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "gateway_unavailable", Message: "The gateway is shutting down."})
		return
	}
	if err := s.store.AuthenticateSession(id, r.Header.Get("Authorization")); err != nil {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_session", Message: "Invalid or expired tunnel session."})
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := newConnection(ws)
	reconnected, err := s.store.Attach(id, conn)
	if err != nil {
		conn.close()
		return
	}
	if reconnected {
		s.metrics.reconnects.Add(1)
	}
	s.config.Logger.Info("tunnel connected", "tunnel_session_id", id)
	defer func() {
		conn.close()
		s.store.Disconnect(id, conn)
		s.config.Logger.Info("tunnel disconnected", "tunnel_session_id", id)
	}()
	_ = conn.readLoop()
}

type endpointResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hostname  string    `json:"hostname"`
	PublicURL string    `json:"publicUrl"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) endpointResponse(value endpoint.Endpoint) endpointResponse {
	status := "offline"
	if s.store.Connected(value.ID) {
		status = "connected"
	}
	return endpointResponse{ID: value.ID, Name: value.Name, Hostname: value.Hostname, PublicURL: s.config.PublicScheme + "://" + value.Hostname, Status: status, CreatedAt: value.CreatedAt.UTC()}
}

func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
		return
	}
	if !s.requireAPIAuth(w, r) {
		return
	}
	values, err := s.repository.ListEndpoints(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "database_unavailable", Message: "Endpoint storage is unavailable."})
		return
	}
	responses := make([]endpointResponse, 0, len(values))
	for _, value := range values {
		responses = append(responses, s.endpointResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": responses})
}

func (s *Server) handleEndpoint(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPIAuth(w, r) {
		return
	}
	name := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/api/v1/endpoints/"))
	if strings.Contains(name, "/") || naming.Validate(name) != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_endpoint_name", Message: "Invalid endpoint name."})
		return
	}
	value, err := s.repository.GetEndpointByName(r.Context(), name)
	if errors.Is(err, endpoint.ErrNotFound) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, http.StatusNotFound, apiError{Code: "endpoint_not_found", Message: "No endpoint is registered with this name."})
		}
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "database_unavailable", Message: "Endpoint storage is unavailable."})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.endpointResponse(value))
	case http.MethodDelete:
		s.store.DeleteEndpoint(value.ID)
		if err := s.repository.DeleteEndpoint(r.Context(), name); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "database_unavailable", Message: "Endpoint storage is unavailable."})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "Method not allowed."})
	}
}

func (s *Server) handlePublic(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Host")) > 1 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_host", Message: "The Host header is invalid."})
		return
	}
	_, hostname, err := ParseEndpointHost(r.Host, s.config.BaseDomain)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_host", Message: "The Host header is invalid."})
		return
	}
	if r.ContentLength > tunnelprotocol.MaxBodySize {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "request_too_large", Message: "The request body exceeds the 10 MiB limit."})
		return
	}
	value, err := s.repository.GetEndpointByHostname(r.Context(), hostname)
	if errors.Is(err, endpoint.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{Code: "endpoint_not_found", Message: "No Mockingo endpoint is registered for this hostname."})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "database_unavailable", Message: "Endpoint storage is unavailable."})
		return
	}
	defer r.Body.Close()
	body, err := tunnelprotocol.ReadBody(r.Body)
	if errors.Is(err, tunnelprotocol.ErrBodyTooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Code: "request_too_large", Message: "The request body exceeds the 10 MiB limit."})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", Message: "Could not read the request."})
		return
	}
	conn := s.store.ConnectionByEndpoint(value.ID)
	if conn == nil {
		writeJSON(w, http.StatusBadGateway, apiError{Code: "tunnel_offline", Message: "The Mockingo endpoint is currently offline."})
		return
	}
	headers := tunnelprotocol.FilterHeaders(r.Header)
	headers.Del("Forwarded")
	headers.Del("X-Forwarded-For")
	headers.Del("X-Forwarded-Host")
	headers.Del("X-Forwarded-Proto")
	clientIP, forwardedHost, forwardedProto := forwardedHeaders(r, s.config.PublicScheme, s.config.TrustedProxyCIDRs)
	headers.Set("X-Forwarded-Host", forwardedHost)
	headers.Set("X-Forwarded-Proto", forwardedProto)
	if clientIP != "" {
		headers.Set("X-Forwarded-For", clientIP)
	}
	requestID, err := randomHex(16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "bad_gateway", Message: "Gateway request creation failed."})
		return
	}
	message := tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeRequest, RequestID: requestID, Method: r.Method, Path: r.URL.RequestURI(), Headers: headers, BodyBase64: base64.StdEncoding.EncodeToString(body)}
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
	defer cancel()
	response, err := conn.roundTrip(ctx, message)
	if errors.Is(err, context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, apiError{Code: "gateway_timeout", Message: "The tunnel request timed out."})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Code: "bad_gateway", Message: "The tunnel request failed."})
		return
	}
	if response.Type == tunnelprotocol.TypeError {
		if response.ErrorCode == "timeout" {
			writeJSON(w, http.StatusGatewayTimeout, apiError{Code: "gateway_timeout", Message: "The local application timed out."})
		} else {
			writeJSON(w, http.StatusBadGateway, apiError{Code: "bad_gateway", Message: "The local application could not handle the request."})
		}
		return
	}
	responseBody, err := base64.StdEncoding.DecodeString(response.BodyBase64)
	if err != nil || len(responseBody) > tunnelprotocol.MaxBodySize {
		writeJSON(w, http.StatusBadGateway, apiError{Code: "bad_gateway", Message: "The tunnel returned an invalid response."})
		return
	}
	for key, values := range tunnelprotocol.FilterHeaders(http.Header(response.Headers)) {
		for _, headerValue := range values {
			w.Header().Add(key, headerValue)
		}
	}
	status := response.Status
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	_, _ = w.Write(responseBody)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.shuttingDown.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	err := s.repository.Ping(ctx)
	if err == nil && s.config.ReadyCheck != nil {
		err = s.config.ReadyCheck(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	values, err := s.repository.ListEndpoints(r.Context())
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metrics.write(w, s.store.ActiveCount(), len(values))
}

func (s *Server) BeginShutdown() {
	s.shuttingDown.Store(true)
	s.store.CloseAll()
}

func (s *Server) Shutdown(context.Context) error {
	s.BeginShutdown()
	s.repository.Close()
	return nil
}

func (s *Server) Close() { _ = s.Shutdown(context.Background()) }

func (s *Server) String() string { return fmt.Sprintf("Mockingo gateway for %s", s.config.BaseDomain) }
