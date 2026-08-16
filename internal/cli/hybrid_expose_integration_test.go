package cli

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestExposeWireMockHybridUsesNormalTunnelAndKeepsMocksLocal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wiremock")
	if err := os.MkdirAll(filepath.Join(root, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	mapping := `{"request":{"method":"POST","urlPath":"/payment"},"response":{"status":503,"body":"mock-payment"}}`
	if err := os.WriteFile(filepath.Join(root, "mappings", "payment.json"), []byte(mapping), 0o600); err != nil {
		t.Fatal(err)
	}

	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamRequests.Add(1)
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/orders?source=public" || request.Header.Get("X-Trace") != "trace-1" {
			t.Errorf("upstream request = %s %s %#v", request.Method, request.URL.RequestURI(), request.Header)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(request.Body)
		if body.String() != "order-body" {
			t.Errorf("upstream body = %q", body.String())
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("real-order"))
	}))
	defer upstream.Close()
	_, portText, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)

	connected := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		exchanges := []struct {
			message tunnelprotocol.Message
			status  int
			body    string
		}{
			{message: tunnelprotocol.Message{Version: 1, Type: tunnelprotocol.TypeRequest, RequestID: "mocked", Method: http.MethodPost, Path: "/payment", BodyBase64: base64.StdEncoding.EncodeToString([]byte("never-forward"))}, status: 503, body: "mock-payment"},
			{message: tunnelprotocol.Message{Version: 1, Type: tunnelprotocol.TypeRequest, RequestID: "real", Method: http.MethodPost, Path: "/orders?source=public", Headers: map[string][]string{"X-Trace": {"trace-1"}}, BodyBase64: base64.StdEncoding.EncodeToString([]byte("order-body"))}, status: 201, body: "real-order"},
		}
		for _, exchange := range exchanges {
			if err := connection.WriteJSON(exchange.message); err != nil {
				t.Errorf("send request: %v", err)
				return
			}
			var response tunnelprotocol.Message
			if err := connection.ReadJSON(&response); err != nil {
				t.Errorf("read response: %v", err)
				return
			}
			body, _ := base64.StdEncoding.DecodeString(response.BodyBase64)
			if response.Status != exchange.status || string(body) != exchange.body {
				t.Errorf("response = %#v body=%q", response, body)
			}
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
			_ = json.NewEncoder(writer).Encode(map[string]string{"userId": "user_hybrid", "authenticationMethod": "clerk_oauth"})
		case "/api/v1/tunnel-sessions":
			sessionRequests.Add(1)
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if len(body) != 4 || body["endpointName"] != "shop" || body["localPort"] != float64(port) {
				t.Errorf("session body contains hybrid metadata or misses normal fields: %#v", body)
			}
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"endpoint": map[string]any{"id": "endpoint-hybrid", "name": "shop", "hostname": "shop.mockingo.click", "publicUrl": "https://shop.mockingo.click"},
				"tunnel":   map[string]any{"sessionId": "session-hybrid", "connectUrl": strings.Replace(gateway.URL, "http://", "ws://", 1) + "/v1/connect", "ticket": "hybrid-ticket", "expiresAt": time.Now().Add(time.Minute).UTC(), "protocolVersion": 1},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	expires := time.Now().Add(time.Hour)
	if err := config.Save(configPath, config.Config{APIURL: backend.URL, OAuthIssuer: issuer.URL, OAuthClientID: "client", OAuthScopes: "openid", UserID: "user_hybrid", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	store := newCLIStore()
	store.values[oauth.Account(issuer.URL, "client")] = oauth.OAuthCredentials{AccessToken: "oauth-access", RefreshToken: "oauth-refresh", TokenType: "Bearer", Scope: []string{"openid"}, ExpiresAt: expires, UserID: "user_hybrid"}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: configPath, HTTPClient: &http.Client{Timeout: 3 * time.Second}, Credentials: store}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- app.Run(ctx, []string{"expose", "--name", "shop", "--http", fmt.Sprint(port), "--wiremock", root, "--expected-gateway-host", "127.0.0.1", "--allow-insecure-gateway", "--reconnect=false", "--verbose"})
	}()
	select {
	case <-connected:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("hybrid expose did not connect: %s", output.String())
	}
	if code := <-done; code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if upstreamRequests.Load() != 1 || sessionRequests.Load() != 1 {
		t.Fatalf("upstream=%d sessions=%d", upstreamRequests.Load(), sessionRequests.Load())
	}
	text := output.String()
	for _, expected := range []string{"Mocks:      WireMock", "Mappings:", "503 MOCK", "201 FORWARD", "https://shop.mockingo.click"} {
		if !strings.Contains(text, expected) {
			t.Errorf("output missing %q: %s", expected, text)
		}
	}
}
