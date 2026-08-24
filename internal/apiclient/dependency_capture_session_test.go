package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/project-mockingo/mockingo-agent/internal/oauth"
)

func TestCreateDependencyCaptureSessionValidatesEndpointScopedResponse(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/endpoints/integration/dependency-capture-sessions" || r.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"endpoint": map[string]any{"id": "2ad3978c-1bfa-452d-9a8c-fe8a66320d36", "name": "integration"},
			"capture":  map[string]any{"sessionId": "af69c741-adfd-4694-a316-5bfc6eb5bd67", "connectUrl": "wss://gateway.mockingo.com/v1/dependency-capture/connect", "ticket": "secret", "expiresAt": now.Add(time.Minute), "protocolVersion": 1},
		})
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: "issuer", ClientID: "capture", Store: &memoryStore{value: oauth.OAuthCredentials{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: now.Add(time.Hour)}}, Now: func() time.Time { return now }}
	response, err := client.CreateDependencyCaptureSession(context.Background(), "integration", TunnelSessionValidation{ExpectedGatewayHosts: []string{"gateway.mockingo.com"}, Now: func() time.Time { return now }})
	if err != nil || response.Endpoint.Name != "integration" || response.Capture.Ticket != "secret" {
		t.Fatalf("response = %+v error = %v", response, err)
	}
	response.Capture.ConnectURL = "wss://evil.example/v1/dependency-capture/connect"
	if err := validateDependencyCaptureSession("integration", response, TunnelSessionValidation{ExpectedGatewayHosts: []string{"gateway.mockingo.com"}, Now: func() time.Time { return now }}); err == nil {
		t.Fatal("untrusted Gateway capture URL was accepted")
	}
}
