package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/internal/auth"
	"github.com/mockingo/mockingo-cli/internal/naming"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

type Config struct {
	BaseDomain     string
	PublicScheme   string
	DevToken       string
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

type Server struct {
	config   Config
	store    *Store
	upgrader websocket.Upgrader
}

func NewServer(config Config) *Server {
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 60 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Server{
		config:   config,
		store:    NewStore(),
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/tunnels" {
		s.handleCreate(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/tunnels/") {
		s.handleTunnel(w, r)
		return
	}
	s.handlePublic(w, r)
}

func (s *Server) requireAPIAuth(w http.ResponseWriter, r *http.Request) bool {
	if !auth.BearerMatches(r.Header.Get("Authorization"), s.config.DevToken) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "authentication required"})
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
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	PublicURL    string `json:"publicUrl"`
	ConnectURL   string `json:"connectUrl"`
	SessionToken string `json:"sessionToken"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "method not allowed"})
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
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "invalid JSON request"})
		return
	}
	if request.Protocol != "http" {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "unsupported_protocol", Message: "only the http protocol is supported"})
		return
	}
	if err := naming.Validate(request.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_name", Message: err.Error()})
		return
	}
	if request.LocalPort < 1 || request.LocalPort > 65535 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_port", Message: "localPort must be between 1 and 65535"})
		return
	}
	tunnel, err := s.store.Create(request.Name, request.LocalPort)
	if errors.Is(err, ErrNameConnected) {
		writeJSON(w, http.StatusConflict, apiError{Code: "name_connected", Message: "tunnel name is already connected"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "could not create tunnel"})
		return
	}
	hostname := request.Name + "." + s.config.BaseDomain
	websocketScheme := "ws"
	if s.config.PublicScheme == "https" {
		websocketScheme = "wss"
	}
	response := createResponse{
		ID: tunnel.ID, Name: request.Name, Hostname: hostname,
		PublicURL:    s.config.PublicScheme + "://" + hostname,
		ConnectURL:   websocketScheme + "://" + r.Host + "/api/v1/tunnels/" + tunnel.ID + "/connect",
		SessionToken: tunnel.SessionToken,
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/tunnels/")
	if id, found := strings.CutSuffix(rest, "/connect"); found {
		s.handleConnect(w, r, id)
		return
	}
	if strings.Contains(rest, "/") || rest == "" || r.Method != http.MethodDelete {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "not found"})
		return
	}
	if !s.requireAPIAuth(w, r) {
		return
	}
	s.store.Delete(rest)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, apiError{Code: "method_not_allowed", Message: "method not allowed"})
		return
	}
	if err := s.store.AuthenticateSession(id, r.Header.Get("Authorization")); err != nil {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_session", Message: "invalid tunnel session"})
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := newConnection(ws)
	if err := s.store.Attach(id, conn); err != nil {
		conn.close()
		return
	}
	s.config.Logger.Info("tunnel connected", "tunnel_id", id)
	defer func() {
		conn.close()
		s.store.Disconnect(id, conn)
		s.config.Logger.Info("tunnel disconnected", "tunnel_id", id)
	}()
	_ = conn.readLoop()
}

func (s *Server) tunnelName(host string) (string, bool) {
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	suffix := "." + strings.ToLower(strings.TrimSuffix(s.config.BaseDomain, "."))
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(host, suffix)
	if strings.Contains(name, ".") || naming.Validate(name) != nil {
		return "", false
	}
	return name, true
}

func (s *Server) handlePublic(w http.ResponseWriter, r *http.Request) {
	name, ok := s.tunnelName(r.Host)
	if !ok {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}
	if r.ContentLength > tunnelprotocol.MaxBodySize {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	defer r.Body.Close()
	body, err := tunnelprotocol.ReadBody(r.Body)
	if errors.Is(err, tunnelprotocol.ErrBodyTooLarge) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		http.Error(w, "could not read request", http.StatusBadRequest)
		return
	}
	conn := s.store.ConnectionByName(name)
	if conn == nil {
		http.Error(w, "tunnel is not connected", http.StatusBadGateway)
		return
	}
	headers := tunnelprotocol.FilterHeaders(r.Header)
	headers.Set("X-Forwarded-Host", r.Host)
	headers.Set("X-Forwarded-Proto", s.config.PublicScheme)
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP != "" {
		if previous := headers.Get("X-Forwarded-For"); previous != "" {
			headers.Set("X-Forwarded-For", previous+", "+clientIP)
		} else {
			headers.Set("X-Forwarded-For", clientIP)
		}
	}
	requestID, err := randomHex(16)
	if err != nil {
		http.Error(w, "gateway error", http.StatusInternalServerError)
		return
	}
	message := tunnelprotocol.Message{
		Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeRequest,
		RequestID: requestID, Method: r.Method, Path: r.URL.RequestURI(),
		Headers: headers, BodyBase64: base64.StdEncoding.EncodeToString(body),
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
	defer cancel()
	response, err := conn.roundTrip(ctx, message)
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		return
	}
	if err != nil {
		http.Error(w, "tunnel is not connected", http.StatusBadGateway)
		return
	}
	if response.Type == tunnelprotocol.TypeError {
		status := http.StatusBadGateway
		if response.ErrorCode == "timeout" {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	responseBody, err := base64.StdEncoding.DecodeString(response.BodyBase64)
	if err != nil || len(responseBody) > tunnelprotocol.MaxBodySize {
		http.Error(w, "invalid tunnel response", http.StatusBadGateway)
		return
	}
	for key, values := range tunnelprotocol.FilterHeaders(http.Header(response.Headers)) {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := response.Status
	if status < 100 || status > 999 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	_, _ = w.Write(responseBody)
}

func (s *Server) Close() {
	// Active tunnel connections are owned by registrations; tests and the
	// process-level HTTP server close them through registration deletion.
}

func (s *Server) String() string {
	return fmt.Sprintf("Mockingo gateway for %s", s.config.BaseDomain)
}
