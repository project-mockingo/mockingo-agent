package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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

func TestMockUsesNormalTunnelAndKeepsConfigurationLocal(t *testing.T) {
	wiremockRoot := filepath.Join(t.TempDir(), "wiremock")
	if err := os.MkdirAll(filepath.Join(wiremockRoot, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	mapping := `{"request":{"method":"GET","url":"/weather?city=Prague"},"response":{"status":200,"headers":{"Content-Type":"application/json"},"jsonBody":{"city":"Prague"}}}`
	if err := os.WriteFile(filepath.Join(wiremockRoot, "mappings", "weather.json"), []byte(mapping), 0o600); err != nil {
		t.Fatal(err)
	}

	connected := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer mock-ticket" {
			t.Errorf("gateway authorization = %q", request.Header.Get("Authorization"))
		}
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(tunnelprotocol.Message{Version: 1, Type: tunnelprotocol.TypeRequest, RequestID: "mock-1", Method: http.MethodGet, Path: "/weather?city=Prague"})
		var response tunnelprotocol.Message
		if err := connection.ReadJSON(&response); err != nil {
			t.Errorf("read response: %v", err)
			return
		}
		body, _ := base64.StdEncoding.DecodeString(response.BodyBase64)
		if response.Status != http.StatusOK || !strings.Contains(string(body), `"Prague"`) {
			t.Errorf("mock response = %#v, %q", response, body)
		}
		connected <- struct{}{}
		_, _, _ = connection.ReadMessage()
	}))
	defer gateway.Close()

	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/.well-known/oauth-authorization-server" {
			_ = json.NewEncoder(writer).Encode(oauth.Metadata{Issuer: issuer.URL, AuthorizationEndpoint: issuer.URL + "/authorize", TokenEndpoint: issuer.URL + "/token", GrantTypesSupported: []string{"authorization_code", "refresh_token"}, CodeChallengeMethods: []string{"S256"}, TokenAuthMethods: []string{"none"}})
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer issuer.Close()

	var sessionRequests atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/me":
			_ = json.NewEncoder(writer).Encode(map[string]string{"userId": "user_mock", "authenticationMethod": "clerk_oauth"})
		case "/api/v1/tunnel-sessions":
			sessionRequests.Add(1)
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["endpointName"] != "weather" || body["protocol"] != "http" || body["protocolVersion"] != float64(1) || body["localPort"].(float64) <= 0 || len(body) != 4 {
				t.Errorf("session request leaked or omitted fields: %#v", body)
			}
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"endpoint": map[string]any{"id": "endpoint-mock", "name": "weather", "hostname": "weather.mockingo.click", "publicUrl": "https://weather.mockingo.click"},
				"tunnel":   map[string]any{"sessionId": "session-mock", "connectUrl": strings.Replace(gateway.URL, "http://", "ws://", 1) + "/v1/connect", "ticket": "mock-ticket", "expiresAt": time.Now().Add(time.Minute).UTC(), "protocolVersion": 1},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	expires := time.Now().Add(time.Hour)
	if err := config.Save(configPath, config.Config{APIURL: backend.URL, OAuthIssuer: issuer.URL, OAuthClientID: "client", OAuthScopes: "openid", UserID: "user_mock", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	store := newCLIStore()
	store.values[oauth.Account(issuer.URL, "client")] = oauth.OAuthCredentials{AccessToken: "oauth-access", RefreshToken: "oauth-refresh", TokenType: "Bearer", Scope: []string{"openid"}, ExpiresAt: expires, UserID: "user_mock"}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: configPath, HTTPClient: &http.Client{Timeout: 3 * time.Second}, Credentials: store}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{"mock", "--name", "weather", "--wiremock", wiremockRoot, "--expected-gateway-host", "127.0.0.1", "--allow-insecure-gateway", "--reconnect=false"})
	}()
	select {
	case <-connected:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("mock did not connect: %s", output.String())
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if sessionRequests.Load() != 1 || !strings.Contains(output.String(), "https://weather.mockingo.click") {
		t.Fatalf("requests = %d output = %s", sessionRequests.Load(), output.String())
	}
	if strings.Contains(output.String(), "127.0.0.1:") || strings.Contains(output.String(), "mock-ticket") {
		t.Fatalf("output exposed local port or secret: %s", output.String())
	}
}

func TestInvalidMockSourceDoesNotReachAuthenticationOrTunnel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"request":{"method":"POST","urlPath":"/","bodyPatterns":[]},"response":{"status":200}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: filepath.Join(t.TempDir(), "missing-config.json")}
	code := app.Run(context.Background(), []string{"mock", "--name", "weather", "--wiremock", path})
	if code == 0 || !strings.Contains(output.String(), "request.bodyPatterns") || strings.Contains(output.String(), "not signed in") {
		t.Fatalf("code = %d output = %s", code, output.String())
	}
}

func TestInvalidHybridExposeSourceDoesNotReachAuthenticationOrTunnel(t *testing.T) {
	tests := []struct {
		name, flag, filename, content, expected string
	}{
		{name: "WireMock", flag: "--wiremock", filename: "bad.json", content: `{"request":{"method":"POST","urlPath":"/","bodyPatterns":[]},"response":{"status":200}}`, expected: "request.bodyPatterns"},
		{name: "OpenAPI", flag: "--openapi", filename: "bad.yaml", content: "openapi: 3.0.3\ninfo: [not-an-object]\n", expected: "OpenAPI"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.filename)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			app := &App{Stdout: &output, Stderr: &output, ConfigPath: filepath.Join(t.TempDir(), "missing-config.json")}
			code := app.Run(context.Background(), []string{"expose", "--name", "shop", "--http", "8080", test.flag, path})
			if code == 0 || !strings.Contains(output.String(), test.expected) || strings.Contains(output.String(), "not signed in") {
				t.Fatalf("code = %d output = %s", code, output.String())
			}
		})
	}
}
