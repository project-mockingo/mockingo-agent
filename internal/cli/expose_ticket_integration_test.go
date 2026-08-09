package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/internal/config"
	"github.com/mockingo/mockingo-cli/internal/oauth"
	"github.com/project-mockingo/mockingo-tunnel-protocol"
)

func TestExposeUsesOAuthControlPlaneAndGatewayTicket(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe" {
			t.Errorf("local path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer local.Close()
	_, portText, _ := net.SplitHostPort(local.Listener.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)

	connected := make(chan struct{}, 1)
	var gatewayAuthorization atomic.Value
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayAuthorization.Store(r.Header.Get("Authorization"))
		if r.URL.RawQuery != "" || r.URL.Path != "/v1/connect" {
			t.Errorf("gateway URL = %s", r.URL.String())
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if err := ws.WriteJSON(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeRequest, RequestID: "probe-1", Method: http.MethodGet, Path: "/probe"}); err != nil {
			t.Errorf("send tunnel request: %v", err)
			_ = ws.Close()
			return
		}
		var response tunnelprotocol.Message
		if err := ws.ReadJSON(&response); err != nil || response.Type != tunnelprotocol.TypeResponse || response.RequestID != "probe-1" || response.Status != http.StatusNoContent {
			t.Errorf("tunnel response = %#v, %v", response, err)
			_ = ws.Close()
			return
		}
		connected <- struct{}{}
		_, _, _ = ws.ReadMessage()
		_ = ws.Close()
	}))
	defer gatewayServer.Close()

	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			_ = json.NewEncoder(w).Encode(oauth.Metadata{Issuer: issuer.URL, AuthorizationEndpoint: issuer.URL + "/authorize", TokenEndpoint: issuer.URL + "/token", GrantTypesSupported: []string{"authorization_code", "refresh_token"}, CodeChallengeMethods: []string{"S256"}, TokenAuthMethods: []string{"none"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer issuer.Close()

	var sessionRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oauth-access" {
			t.Errorf("backend authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"userId": "user_123", "authenticationMethod": "clerk_oauth"})
		case "/api/v1/tunnel-sessions":
			sessionRequests.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, found := body["ownerUserId"]; found || body["endpointName"] != "spring-demo" || body["localPort"] != float64(port) {
				t.Errorf("session body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"endpoint": map[string]any{"id": "endpoint-1", "name": "spring-demo", "hostname": "spring-demo.mockingo.click", "publicUrl": "https://spring-demo.mockingo.click"},
				"tunnel":   map[string]any{"sessionId": "session-1", "connectUrl": strings.Replace(gatewayServer.URL, "http://", "ws://", 1) + "/v1/connect", "ticket": "gateway-ticket", "expiresAt": time.Now().Add(time.Minute).UTC(), "protocolVersion": 1},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	expires := time.Now().Add(time.Hour)
	if err := config.Save(path, config.Config{APIURL: backend.URL, OAuthIssuer: issuer.URL, OAuthClientID: "client", OAuthScopes: "openid", UserID: "user_123", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	store := newCLIStore()
	store.values[oauth.Account(issuer.URL, "client")] = oauth.OAuthCredentials{AccessToken: "oauth-access", RefreshToken: "oauth-refresh", TokenType: "Bearer", Scope: []string{"openid"}, ExpiresAt: expires, UserID: "user_123"}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: path, HTTPClient: &http.Client{Timeout: 3 * time.Second}, Credentials: store}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{"expose", "--name", "spring-demo", "--http", fmt.Sprint(port), "--expected-gateway-host", "127.0.0.1", "--allow-insecure-gateway", "--reconnect=false"})
	}()
	select {
	case <-connected:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("expose did not connect: %s", output.String())
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if sessionRequests.Load() != 1 || gatewayAuthorization.Load() != "Bearer gateway-ticket" {
		t.Fatalf("session requests = %d, gateway auth = %v", sessionRequests.Load(), gatewayAuthorization.Load())
	}
	text := output.String()
	for _, secret := range []string{"oauth-access", "oauth-refresh", "gateway-ticket"} {
		if strings.Contains(text, secret) {
			t.Fatalf("output leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "https://spring-demo.mockingo.click") {
		t.Fatalf("public URL missing: %s", text)
	}
}

func TestExposeDoesNotFallBackToLegacyCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"legacyApiUrl":"https://gateway.example","legacyToken":"test-only-static-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: path}
	code := app.Run(context.Background(), []string{"expose", "--name", "spring-demo", "--http", "8080"})
	if code == 0 || !strings.Contains(output.String(), "You are not signed in to Mockingo") {
		t.Fatalf("code = %d, output = %s", code, output.String())
	}
}
