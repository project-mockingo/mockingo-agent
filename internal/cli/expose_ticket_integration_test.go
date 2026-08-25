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
	"github.com/project-mockingo/mockingo-agent/internal/config"
	"github.com/project-mockingo/mockingo-agent/internal/oauth"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
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
	proxyReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, proxyPortText, _ := net.SplitHostPort(proxyReservation.Addr().String())
	_ = proxyReservation.Close()
	var proxyPort int
	_, _ = fmt.Sscanf(proxyPortText, "%d", &proxyPort)

	connected := make(chan struct{}, 1)
	captureConnected := make(chan struct{}, 1)
	var tunnelAuthorization atomic.Value
	var captureAuthorization atomic.Value
	endpointID := "a68c2994-75f8-4f47-a099-ccec9a52a738"
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("gateway URL = %s", r.URL.String())
		}
		switch r.URL.Path {
		case "/v1/connect":
			tunnelAuthorization.Store(r.Header.Get("Authorization"))
			ws, upgradeErr := upgrader.Upgrade(w, r, nil)
			if upgradeErr != nil {
				return
			}
			if writeErr := ws.WriteJSON(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeRequest, RequestID: "probe-1", Method: http.MethodGet, Path: "/probe"}); writeErr != nil {
				t.Errorf("send tunnel request: %v", writeErr)
				_ = ws.Close()
				return
			}
			var response tunnelprotocol.Message
			if readErr := ws.ReadJSON(&response); readErr != nil || response.Type != tunnelprotocol.TypeResponse || response.RequestID != "probe-1" || response.Status != http.StatusNoContent {
				t.Errorf("tunnel response = %#v, %v", response, readErr)
				_ = ws.Close()
				return
			}
			connected <- struct{}{}
			_, _, _ = ws.ReadMessage()
			_ = ws.Close()
		case "/v1/dependency-capture/connect":
			captureAuthorization.Store(r.Header.Get("Authorization"))
			ws, upgradeErr := upgrader.Upgrade(w, r, nil)
			if upgradeErr != nil {
				return
			}
			_ = ws.WriteJSON(tunnelprotocol.Message{
				Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeDependencyConfig,
				DependencyConfig: &tunnelprotocol.DependencyBehaviorSnapshot{EndpointID: endpointID, Behaviors: []tunnelprotocol.DependencyBehavior{}},
			})
			captureConnected <- struct{}{}
			_, _, _ = ws.ReadMessage()
			_ = ws.Close()
		default:
			t.Errorf("gateway URL = %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
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
	var captureSessionRequests atomic.Int32
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
				"endpoint": map[string]any{"id": endpointID, "name": "spring-demo", "hostname": "spring-demo.mockingo.click", "publicUrl": "https://spring-demo.mockingo.click"},
				"tunnel":   map[string]any{"sessionId": "19994344-267b-4fc2-953e-c859751bae97", "connectUrl": strings.Replace(gatewayServer.URL, "http://", "ws://", 1) + "/v1/connect", "ticket": "gateway-ticket", "expiresAt": time.Now().Add(time.Minute).UTC(), "protocolVersion": 1},
			})
		case "/api/v1/endpoints/spring-demo/dependency-capture-sessions":
			captureSessionRequests.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"endpoint": map[string]any{"id": endpointID, "name": "spring-demo"},
				"capture": map[string]any{
					"sessionId":  "8af1b4e2-7573-4467-a82d-c69cba5aaebd",
					"connectUrl": strings.Replace(gatewayServer.URL, "http://", "ws://", 1) + "/v1/dependency-capture/connect",
					"ticket":     "dependency-ticket", "expiresAt": time.Now().Add(time.Minute).UTC(), "protocolVersion": 1,
				},
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
		done <- app.Run(ctx, []string{"expose", "--name", "spring-demo", "--http", fmt.Sprint(port), "--proxy-port", fmt.Sprint(proxyPort), "--expected-gateway-host", "127.0.0.1", "--allow-insecure-gateway", "--reconnect=false"})
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("expose did not connect: %s", output.String())
	}
	select {
	case <-captureConnected:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("dependency capture did not connect: %s", output.String())
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if sessionRequests.Load() != 1 || captureSessionRequests.Load() != 1 || tunnelAuthorization.Load() != "Bearer gateway-ticket" || captureAuthorization.Load() != "Bearer dependency-ticket" {
		t.Fatalf("session requests = %d/%d, gateway auth = %v/%v", sessionRequests.Load(), captureSessionRequests.Load(), tunnelAuthorization.Load(), captureAuthorization.Load())
	}
	text := output.String()
	for _, secret := range []string{"oauth-access", "oauth-refresh", "gateway-ticket", "dependency-ticket"} {
		if strings.Contains(text, secret) {
			t.Fatalf("output leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "https://spring-demo.mockingo.click") {
		t.Fatalf("public URL missing: %s", text)
	}
	if !strings.Contains(text, fmt.Sprintf("Proxy          http://127.0.0.1:%d", proxyPort)) || !strings.Contains(text, "Dependency replay configuration loaded: 0 active.") {
		t.Fatalf("dependency proxy output missing: %s", text)
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
