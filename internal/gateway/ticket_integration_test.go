package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/internal/gateway/backendcallback"
	"github.com/mockingo/mockingo-cli/internal/gateway/ticketauth"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

const (
	ticketEndpointID = "e9949642-8b35-4247-ac5d-c076a463058d"
	ticketSessionID  = "8db7aeef-a927-48d1-9190-99076fbe3c71"
)

type fakeCallbacks struct {
	mu           sync.Mutex
	connected    []backendcallback.ConnectedEvent
	disconnected []backendcallback.DisconnectedEvent
	rejected     []backendcallback.RejectedEvent
	connectedErr error
	notify       chan struct{}
}

func newFakeCallbacks() *fakeCallbacks { return &fakeCallbacks{notify: make(chan struct{}, 20)} }

func (f *fakeCallbacks) signal() {
	select {
	case f.notify <- struct{}{}:
	default:
	}
}

func (f *fakeCallbacks) Connected(_ context.Context, event backendcallback.ConnectedEvent) error {
	f.mu.Lock()
	f.connected = append(f.connected, event)
	err := f.connectedErr
	f.mu.Unlock()
	f.signal()
	return err
}

func (f *fakeCallbacks) Disconnected(_ context.Context, event backendcallback.DisconnectedEvent) error {
	f.mu.Lock()
	f.disconnected = append(f.disconnected, event)
	f.mu.Unlock()
	f.signal()
	return nil
}

func (f *fakeCallbacks) Rejected(_ context.Context, event backendcallback.RejectedEvent) error {
	f.mu.Lock()
	f.rejected = append(f.rejected, event)
	f.mu.Unlock()
	f.signal()
	return nil
}

type ticketHarness struct {
	privateKey *rsa.PrivateKey
	cache      *ticketauth.JWKSCache
	callbacks  *fakeCallbacks
	handler    *Server
	server     *httptest.Server
}

func newTicketHarness(t *testing.T) *ticketHarness {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _ := jwk.Import(&privateKey.PublicKey)
	_ = publicKey.Set(jwk.KeyIDKey, "ticket-key")
	_ = publicKey.Set(jwk.AlgorithmKey, jwa.RS256())
	_ = publicKey.Set(jwk.KeyUsageKey, string(jwk.ForSignature))
	set := jwk.NewSet()
	_ = set.AddKey(publicKey)
	jwksBody, _ := json.Marshal(set)
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwksBody) }))
	t.Cleanup(jwksServer.Close)
	cache := ticketauth.NewJWKSCache(ticketauth.JWKSConfig{URL: jwksServer.URL, HTTPClient: jwksServer.Client(), RefreshInterval: time.Hour})
	if err := cache.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifier := ticketauth.NewVerifier(ticketauth.Config{
		Issuer: "https://api.mockingo.com", Audience: "mockingo-gateway", ProtocolVersion: 1,
		ClockSkew: time.Second, Keys: cache, ReplayMax: 100,
	})
	repository := endpoint.NewMemoryRepository()
	now := time.Now().UTC()
	_, err = repository.CreateEndpoint(context.Background(), endpoint.Endpoint{
		ID: ticketEndpointID, Name: "spring-demo", Hostname: "spring-demo.mockingo.click", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	callbacks := newFakeCallbacks()
	handler := NewServer(Config{
		BaseDomain: "mockingo.click", GatewayHost: "gateway.mockingo.com", PublicScheme: "https",
		Repository: repository, TicketVerifier: verifier, CallbackClient: callbacks,
		GatewayInternalToken: "internal-secret", GatewayInstanceID: "gateway-1",
		InternalStatusMaxBatch: 10, BackendCallbackBudget: time.Second,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = handler.Shutdown(shutdownCtx)
	})
	return &ticketHarness{privateKey: privateKey, cache: cache, callbacks: callbacks, handler: handler, server: server}
}

func (h *ticketHarness) ticket(t *testing.T, sessionID string, mutate func(*jwt.Builder)) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	builder := jwt.NewBuilder().Issuer("https://api.mockingo.com").Audience([]string{"mockingo-gateway"}).
		Subject("user_123").JwtID(sessionID).IssuedAt(now).NotBefore(now).Expiration(now.Add(time.Minute)).
		Claim("endpointId", ticketEndpointID).Claim("endpointName", "spring-demo").
		Claim("protocol", "http").Claim("localPort", 8080).Claim("protocolVersion", 1)
	if mutate != nil {
		mutate(builder)
	}
	token, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	_ = headers.Set(jws.KeyIDKey, "ticket-key")
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256(), h.privateKey, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func (h *ticketHarness) dial(t *testing.T, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	return websocket.DefaultDialer.Dial(strings.Replace(h.server.URL, "http://", "ws://", 1)+"/v1/connect", headers)
}

func waitCallback(t *testing.T, callbacks *fakeCallbacks) {
	t.Helper()
	select {
	case <-callbacks.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("callback was not observed")
	}
}

func TestTicketTunnelStatusProxyReplayCollisionAndDisconnect(t *testing.T) {
	harness := newTicketHarness(t)
	ticket := harness.ticket(t, ticketSessionID, nil)
	ws, response, err := harness.dial(t, ticket)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	waitCallback(t, harness.callbacks)

	status := internalRequest(t, harness.server, "/internal/v1/tunnels/status", "internal-secret", []byte(`{"endpointIds":["`+ticketEndpointID+`","`+ticketEndpointID+`"]}`))
	if status.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(status.Body)
		t.Fatalf("status = %d %s", status.StatusCode, body)
	}
	var statusBody struct {
		Statuses map[string]tunnelStatus `json:"statuses"`
	}
	_ = json.NewDecoder(status.Body).Decode(&statusBody)
	_ = status.Body.Close()
	if got := statusBody.Statuses[ticketEndpointID]; got.Status != "connected" || got.SessionID != ticketSessionID || got.LocalPort != 8080 {
		t.Fatalf("connected status = %#v", got)
	}

	proxyDone := make(chan *http.Response, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, harness.server.URL+"/internal/v1/tunnels/status", nil)
		request.Host = "spring-demo.mockingo.click"
		response, _ := harness.server.Client().Do(request)
		proxyDone <- response
	}()
	var message tunnelprotocol.Message
	if err := ws.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.Type != tunnelprotocol.TypeRequest || message.Path != "/internal/v1/tunnels/status" {
		t.Fatalf("proxied message = %#v", message)
	}
	_ = ws.WriteJSON(tunnelprotocol.Message{Version: 1, Type: tunnelprotocol.TypeResponse, RequestID: message.RequestID, Status: http.StatusOK, BodyBase64: base64.StdEncoding.EncodeToString([]byte("public-app"))})
	proxied := <-proxyDone
	body, _ := io.ReadAll(proxied.Body)
	_ = proxied.Body.Close()
	if proxied.StatusCode != http.StatusOK || string(body) != "public-app" {
		t.Fatalf("public internal-looking path = %d %q", proxied.StatusCode, body)
	}

	second, replayResponse, replayErr := harness.dial(t, ticket)
	if second != nil {
		_ = second.Close()
	}
	if replayErr == nil || replayResponse == nil || replayResponse.StatusCode != http.StatusConflict {
		t.Fatalf("replay = response %#v error %v", replayResponse, replayErr)
	}
	_ = replayResponse.Body.Close()
	waitCallback(t, harness.callbacks)

	otherTicket := harness.ticket(t, "b6e54ad1-f3c6-4ea0-acad-d11353cab4a8", nil)
	second, collisionResponse, collisionErr := harness.dial(t, otherTicket)
	if second != nil {
		_ = second.Close()
	}
	if collisionErr == nil || collisionResponse == nil || collisionResponse.StatusCode != http.StatusConflict {
		t.Fatalf("collision = response %#v error %v", collisionResponse, collisionErr)
	}
	_ = collisionResponse.Body.Close()
	waitCallback(t, harness.callbacks)

	disconnect := internalRequest(t, harness.server, "/internal/v1/tunnels/"+ticketEndpointID+"/disconnect", "internal-secret", nil)
	if disconnect.StatusCode != http.StatusNoContent {
		t.Fatalf("disconnect = %d", disconnect.StatusCode)
	}
	_ = disconnect.Body.Close()
	_, _, _ = ws.ReadMessage()
	waitCallback(t, harness.callbacks)

	// Repeated disconnects remain idempotent and do not duplicate callbacks.
	disconnect = internalRequest(t, harness.server, "/internal/v1/tunnels/"+ticketEndpointID+"/disconnect", "internal-secret", nil)
	_ = disconnect.Body.Close()
	offline := internalRequest(t, harness.server, "/internal/v1/tunnels/status", "internal-secret", []byte(`{"endpointIds":["`+ticketEndpointID+`"]}`))
	var offlineBody struct {
		Statuses map[string]tunnelStatus `json:"statuses"`
	}
	_ = json.NewDecoder(offline.Body).Decode(&offlineBody)
	_ = offline.Body.Close()
	if offlineBody.Statuses[ticketEndpointID].Status != "offline" {
		t.Fatalf("offline status = %#v", offlineBody.Statuses[ticketEndpointID])
	}
	time.Sleep(50 * time.Millisecond)
	harness.callbacks.mu.Lock()
	defer harness.callbacks.mu.Unlock()
	if len(harness.callbacks.connected) != 1 || len(harness.callbacks.disconnected) != 1 || harness.callbacks.disconnected[0].Reason != DisconnectInternal {
		t.Fatalf("callbacks: connected=%d disconnected=%#v", len(harness.callbacks.connected), harness.callbacks.disconnected)
	}
}

func internalRequest(t *testing.T, server *httptest.Server, path, token string, body []byte) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestInternalAuthenticationValidationAndLegacySwitch(t *testing.T) {
	harness := newTicketHarness(t)
	for _, token := range []string{"", harness.ticket(t, ticketSessionID, nil), "wrong"} {
		response := internalRequest(t, harness.server, "/internal/v1/tunnels/status", token, []byte(`{"endpointIds":[]}`))
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d", token, response.StatusCode)
		}
		_ = response.Body.Close()
	}
	for _, body := range [][]byte{[]byte(`{"endpointIds":["bad"]}`)} {
		response := internalRequest(t, harness.server, "/internal/v1/tunnels/status", "internal-secret", body)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid batch status = %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}
	harness.handler.config.InternalStatusMaxBatch = 1
	response := internalRequest(t, harness.server, "/internal/v1/tunnels/status", "internal-secret", []byte(`{"endpointIds":["e9949642-8b35-4247-ac5d-c076a463058d","8db7aeef-a927-48d1-9190-99076fbe3c71"]}`))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized batch status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	registration := createTunnelRequest(t, harness.handler, "", "legacy-demo")
	if registration.Code != http.StatusForbidden {
		t.Fatalf("disabled legacy registration = %d %s", registration.Code, registration.Body.String())
	}
}

func TestConnectedCallbackFailureClosesAndUnregistersTunnel(t *testing.T) {
	harness := newTicketHarness(t)
	harness.callbacks.connectedErr = errors.New("backend unavailable")
	ws, _, err := harness.dial(t, harness.ticket(t, ticketSessionID, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	waitCallback(t, harness.callbacks)
	_, _, _ = ws.ReadMessage()
	waitCallback(t, harness.callbacks)
	deadline := time.Now().Add(time.Second)
	for harness.handler.store.Connected(ticketEndpointID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if harness.handler.store.Connected(ticketEndpointID) {
		t.Fatal("tunnel remained registered after connected callback failure")
	}
	harness.callbacks.mu.Lock()
	defer harness.callbacks.mu.Unlock()
	if len(harness.callbacks.disconnected) != 1 || harness.callbacks.disconnected[0].Reason != DisconnectBackendSyncFailed {
		t.Fatalf("disconnected callbacks = %#v", harness.callbacks.disconnected)
	}
}

func TestGatewayShutdownDisconnectsTicketTunnelOnce(t *testing.T) {
	harness := newTicketHarness(t)
	ws, _, err := harness.dial(t, harness.ticket(t, ticketSessionID, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	waitCallback(t, harness.callbacks)
	harness.handler.BeginShutdown()
	_, _, _ = ws.ReadMessage()
	waitCallback(t, harness.callbacks)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := harness.handler.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	harness.callbacks.mu.Lock()
	defer harness.callbacks.mu.Unlock()
	if len(harness.callbacks.disconnected) != 1 || harness.callbacks.disconnected[0].Reason != DisconnectGatewayShutdown {
		t.Fatalf("shutdown callbacks = %#v", harness.callbacks.disconnected)
	}
}
