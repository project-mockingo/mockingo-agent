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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/internal/gateway/backendcallback"
	"github.com/mockingo/mockingo-cli/internal/gateway/ticketauth"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

type Config struct {
	BaseDomain             string
	GatewayHost            string
	PublicScheme           string
	RequestTimeout         time.Duration
	Catalog                endpoint.Catalog
	TrustedProxyCIDRs      []*net.IPNet
	MetricsEnabled         bool
	Metrics                *Metrics
	TicketVerifier         *ticketauth.Verifier
	CallbackClient         backendcallback.BackendCallbackClient
	GatewayInternalToken   string
	GatewayInstanceID      string
	InternalStatusMaxBatch int
	BackendCallbackBudget  time.Duration
	Logger                 *slog.Logger
}

type Server struct {
	config       Config
	catalog      endpoint.Catalog
	store        *Store
	upgrader     websocket.Upgrader
	shuttingDown atomic.Bool
	metrics      *Metrics
	callbackWG   sync.WaitGroup
	tunnelWG     sync.WaitGroup
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
	if config.Catalog == nil {
		config.Catalog = endpoint.NewMemoryCatalog()
	}
	if config.CallbackClient == nil {
		config.CallbackClient = backendcallback.NoOpClient{}
	}
	if config.InternalStatusMaxBatch <= 0 {
		config.InternalStatusMaxBatch = 500
	}
	if config.BackendCallbackBudget <= 0 {
		config.BackendCallbackBudget = 15 * time.Second
	}
	if config.Metrics == nil {
		config.Metrics = NewMetrics()
	}
	return &Server{
		config: config, catalog: config.Catalog, store: NewStore(), metrics: config.Metrics,
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
	w.Header().Set("X-Request-ID", requestID)
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
	_, _, publicHostErr := ParseEndpointHost(r.Host, s.config.BaseDomain)
	isPublicHost := publicHostErr == nil
	switch {
	case strings.HasPrefix(r.URL.Path, "/internal/") && !isPublicHost:
		s.handleInternal(w, r)
	case r.URL.Path == "/v1/connect" && !isPublicHost:
		s.handleTicketConnect(w, r, requestID)
	case r.URL.Path == "/health/live" && !isPublicHost:
		writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
	case r.URL.Path == "/health/ready" && !isPublicHost:
		s.handleReady(w, r)
	case r.URL.Path == "/metrics" && !isPublicHost && s.config.MetricsEnabled:
		s.handleMetrics(w)
	case isPublicHost:
		s.handlePublic(w, r)
	default:
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "Not found."})
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
	conn := s.store.ConnectionByHostname(hostname)
	if conn == nil {
		exists, lookupErr := s.catalog.ExistsByHostname(r.Context(), hostname)
		if lookupErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "database_unavailable", Message: "Endpoint storage is unavailable."})
			return
		}
		if !exists {
			writeJSON(w, http.StatusNotFound, apiError{Code: "endpoint_not_found", Message: "No Mockingo endpoint is registered for this hostname."})
			return
		}
		writeJSON(w, http.StatusBadGateway, apiError{Code: "tunnel_offline", Message: "The Mockingo endpoint is currently offline."})
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
	proxyRequestID, err := randomHex(16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "bad_gateway", Message: "Gateway request creation failed."})
		return
	}
	message := tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeRequest, RequestID: proxyRequestID, Method: r.Method, Path: r.URL.RequestURI(), Headers: headers, BodyBase64: base64.StdEncoding.EncodeToString(body)}
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
	err := s.catalog.Ping(ctx)
	if err == nil && s.config.TicketVerifier != nil && !s.config.TicketVerifier.Ready() {
		err = errors.New("ticket verifier has no usable keys")
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metrics.write(w, s.store.ActiveCount())
}

func (s *Server) BeginShutdown() {
	if s.shuttingDown.Swap(true) {
		return
	}
	for _, conn := range s.store.CloseAll(DisconnectGatewayShutdown) {
		conn.closeGracefully(DisconnectGatewayShutdown)
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.BeginShutdown()
	done := make(chan struct{})
	go func() {
		s.tunnelWG.Wait()
		s.callbackWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.catalog.Close()
	return nil
}

func (s *Server) Close() { _ = s.Shutdown(context.Background()) }

func (s *Server) String() string { return fmt.Sprintf("Mockingo gateway for %s", s.config.BaseDomain) }
