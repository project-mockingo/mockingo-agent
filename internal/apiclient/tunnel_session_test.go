package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/project-mockingo/mockingo-agent/internal/oauth"
)

func validTunnelResponse(now time.Time) TunnelSessionResponse {
	return TunnelSessionResponse{
		Endpoint: EndpointResponse{ID: "endpoint-uuid", Name: "spring-demo", Hostname: "spring-demo.mockingo.click", PublicURL: "https://spring-demo.mockingo.click"},
		Tunnel:   TunnelResponse{SessionID: "session-uuid", ConnectURL: "wss://gateway.mockingo.com/v1/connect", Ticket: "secret-ticket", ExpiresAt: now.Add(time.Minute), ProtocolVersion: 1},
	}
}

func TestCreateTunnelSessionRequestAndResponse(t *testing.T) {
	now := time.Now().UTC()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tunnel-sessions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer oauth-access" || r.Header.Get("X-Request-ID") == "" || r.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("required headers missing")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, found := body["ownerUserId"]; found || len(body) != 4 || body["endpointName"] != "spring-demo" || body["protocol"] != "http" || body["localPort"] != float64(8080) || body["protocolVersion"] != float64(1) {
			t.Errorf("body = %#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(validTunnelResponse(now))
	}))
	defer server.Close()
	store := &memoryStore{value: oauth.OAuthCredentials{AccessToken: "oauth-access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresAt: now.Add(time.Hour)}}
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: "issuer", ClientID: "client", Store: store, Now: func() time.Time { return now }}
	request := TunnelSessionRequest{EndpointName: "spring-demo", Protocol: "http", LocalPort: 8080, ProtocolVersion: 1}
	got, err := client.CreateTunnelSession(context.Background(), request, TunnelSessionValidation{ExpectedGatewayHosts: []string{"gateway.mockingo.com"}, Now: func() time.Time { return now }})
	if err != nil || got.Tunnel.SessionID != "session-uuid" {
		t.Fatalf("response = %+v, error = %v", got, err)
	}
}

func TestTunnelSessionResponseValidation(t *testing.T) {
	now := time.Now().UTC()
	request := TunnelSessionRequest{EndpointName: "spring-demo", Protocol: "http", LocalPort: 8080, ProtocolVersion: 1}
	validation := TunnelSessionValidation{ExpectedGatewayHosts: []string{"gateway.mockingo.com"}, Now: func() time.Time { return now }}
	tests := map[string]func(*TunnelSessionResponse){
		"endpoint name mismatch": func(r *TunnelSessionResponse) { r.Endpoint.Name = "other" },
		"invalid public URL":     func(r *TunnelSessionResponse) { r.Endpoint.PublicURL = "http://spring-demo.mockingo.click" },
		"invalid endpoint hostname": func(r *TunnelSessionResponse) {
			r.Endpoint.Hostname = "spring-demo.mockingo.com"
			r.Endpoint.PublicURL = "https://spring-demo.mockingo.com"
		},
		"invalid connect scheme": func(r *TunnelSessionResponse) { r.Tunnel.ConnectURL = "ws://gateway.mockingo.com/v1/connect" },
		"untrusted gateway host": func(r *TunnelSessionResponse) { r.Tunnel.ConnectURL = "wss://evil.example/v1/connect" },
		"expired ticket":         func(r *TunnelSessionResponse) { r.Tunnel.ExpiresAt = now.Add(-time.Second) },
		"missing ticket":         func(r *TunnelSessionResponse) { r.Tunnel.Ticket = "" },
		"unsupported version":    func(r *TunnelSessionResponse) { r.Tunnel.ProtocolVersion = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			response := validTunnelResponse(now)
			mutate(&response)
			if err := ValidateTunnelSession(request, response, validation); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
	local := validTunnelResponse(now)
	local.Tunnel.ConnectURL = "ws://127.0.0.1:9090/v1/connect"
	if err := ValidateTunnelSession(request, local, TunnelSessionValidation{ExpectedGatewayHosts: []string{"127.0.0.1"}, AllowInsecureLocal: true, Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("explicit local gateway rejected: %v", err)
	}
}

func TestProblemDetailAndRetryClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-123")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"status":409,"code":"tunnel_session_pending","message":"pending","requestId":"req-123"}`))
	}))
	defer server.Close()
	now := time.Now()
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: "issuer", ClientID: "problem", Store: &memoryStore{value: oauth.OAuthCredentials{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresAt: now.Add(time.Hour)}}}
	_, err := client.CreateTunnelSession(context.Background(), TunnelSessionRequest{}, TunnelSessionValidation{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Problem.Code != "tunnel_session_pending" || !IsRetryable(err) || !strings.Contains(err.Error(), "req-123") {
		t.Fatalf("error = %#v (%v)", apiErr, err)
	}
	nonRetryable := &APIError{Problem: Problem{Status: 400, Code: "invalid_endpoint_name"}}
	if IsRetryable(nonRetryable) {
		t.Fatal("invalid endpoint classified retryable")
	}
	if !IsRetryable(&APIError{Problem: Problem{Status: http.StatusServiceUnavailable, Code: "database_unavailable"}}) {
		t.Fatal("backend 503 classified non-retryable")
	}
}

func TestTunnelTicketRedaction(t *testing.T) {
	response := validTunnelResponse(time.Now())
	if text := fmt.Sprintf("%+v", response); strings.Contains(text, "secret-ticket") || !strings.Contains(text, "<redacted>") {
		t.Fatalf("unsafe formatting: %s", text)
	}
}

func TestCancellationDuringTunnelSessionRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	now := time.Now()
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: "issuer", ClientID: "cancel", Store: &memoryStore{value: oauth.OAuthCredentials{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", ExpiresAt: now.Add(time.Hour)}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.CreateTunnelSession(ctx, TunnelSessionRequest{}, TunnelSessionValidation{})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		close(release)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("request did not cancel")
	}
}
