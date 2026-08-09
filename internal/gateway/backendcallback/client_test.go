package backendcallback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallbackTokenPathSerializationAndRetryBounds(t *testing.T) {
	var calls atomic.Int32
	connectedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer callback-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Request-ID") != "request-1" {
			t.Errorf("request ID = %q", r.Header.Get("X-Request-ID"))
		}
		if r.URL.Path != "/internal/v1/gateway/tunnel-sessions/8db7aeef-a927-48d1-9190-99076fbe3c71/connected" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["endpointId"] != "e9949642-8b35-4247-ac5d-c076a463058d" || body["gatewayInstanceId"] != "gateway-1" || body["connectedAt"] != "2026-08-09T12:00:00Z" {
			t.Errorf("body = %#v", body)
		}
		if call < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewHTTPClient(Config{BackendURL: server.URL, Token: "callback-secret", HTTPClient: server.Client(), Attempts: 3, InitialBackoff: time.Millisecond})
	ctx := WithRequestID(context.Background(), "request-1")
	err := client.Connected(ctx, ConnectedEvent{
		SessionID: "8db7aeef-a927-48d1-9190-99076fbe3c71", EndpointID: "e9949642-8b35-4247-ac5d-c076a463058d",
		GatewayInstanceID: "gateway-1", ConnectedAt: connectedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestCallbackDefinitiveFailureDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	client := NewHTTPClient(Config{BackendURL: server.URL, Token: "secret", HTTPClient: server.Client(), Attempts: 5, InitialBackoff: time.Millisecond})
	err := client.Rejected(context.Background(), RejectedEvent{SessionID: "session", EndpointID: "endpoint", RejectedAt: time.Now().UTC(), Reason: "endpoint_already_connected"})
	callbackError, ok := err.(*CallbackError)
	if !ok || !callbackError.Definitive || callbackError.StatusCode != http.StatusConflict {
		t.Fatalf("error = %#v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestCallbackPermanentOutageIsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewHTTPClient(Config{BackendURL: server.URL, Token: "secret", HTTPClient: server.Client(), Attempts: 3, InitialBackoff: time.Millisecond})
	if err := client.Disconnected(context.Background(), DisconnectedEvent{SessionID: "session"}); err == nil {
		t.Fatal("permanent failure unexpectedly succeeded")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}
