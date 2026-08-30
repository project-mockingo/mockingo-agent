package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	captureUploaded := make(chan tunnelprotocol.DependencyInteraction, 1)
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
			var message tunnelprotocol.Message
			if readErr := ws.ReadJSON(&message); readErr == nil && message.Type == tunnelprotocol.TypeDependencyInteraction && message.Dependency != nil {
				captureUploaded <- *message.Dependency
			}
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
	var endpointReserved atomic.Bool
	var prematureCaptureRequests atomic.Int32
	dependencyOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docker-dependency" {
			t.Errorf("dependency path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("dependency response"))
	}))
	defer dependencyOrigin.Close()
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
			// Give an incorrectly eager dependency-capture request time to expose
			// the endpoint-reservation race before completing this request.
			time.Sleep(100 * time.Millisecond)
			endpointReserved.Store(true)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"endpoint": map[string]any{"id": endpointID, "name": "spring-demo", "hostname": "spring-demo.mockingo.click", "publicUrl": "https://spring-demo.mockingo.click"},
				"tunnel":   map[string]any{"sessionId": "19994344-267b-4fc2-953e-c859751bae97", "connectUrl": strings.Replace(gatewayServer.URL, "http://", "ws://", 1) + "/v1/connect", "ticket": "gateway-ticket", "expiresAt": time.Now().Add(time.Minute).UTC(), "protocolVersion": 1},
			})
		case "/api/v1/endpoints/spring-demo/dependency-capture-sessions":
			captureSessionRequests.Add(1)
			if !endpointReserved.Load() {
				prematureCaptureRequests.Add(1)
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": http.StatusNotFound, "code": "endpoint_not_found", "message": "Endpoint not found.",
				})
				return
			}
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
		done <- app.Run(ctx, []string{"expose", "--name", "spring-demo", "--http", fmt.Sprint(port), "--proxy-bind", "0.0.0.0", "--proxy-port", fmt.Sprint(proxyPort), "--expected-gateway-host", "127.0.0.1", "--allow-insecure-gateway", "--reconnect=false"})
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("expose did not connect: %s", output.String())
	}
	select {
	case <-captureConnected:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("dependency capture did not connect: %s", output.String())
	}
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	proxyTransport := http.DefaultTransport.(*http.Transport).Clone()
	proxyTransport.Proxy = http.ProxyURL(proxyURL)
	proxyClient := &http.Client{Transport: proxyTransport, Timeout: 3 * time.Second}
	response, err := proxyClient.Get(dependencyOrigin.URL + "/docker-dependency")
	if err != nil {
		cancel()
		t.Fatalf("request through non-loopback proxy: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	proxyTransport.CloseIdleConnections()
	if response.StatusCode != http.StatusAccepted || string(body) != "dependency response" {
		cancel()
		t.Fatalf("dependency response = %d %q", response.StatusCode, body)
	}
	select {
	case event := <-captureUploaded:
		originURL, _ := url.Parse(dependencyOrigin.URL)
		originHost, originPortText, _ := net.SplitHostPort(originURL.Host)
		var originPort int
		_, _ = fmt.Sscanf(originPortText, "%d", &originPort)
		if event.Host != originHost || event.Port != originPort || event.Request.Path != "/docker-dependency" || event.Response.Status != http.StatusAccepted {
			cancel()
			t.Fatalf("dependency event used wrong target: %+v", event)
		}
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("dependency event was not uploaded")
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if sessionRequests.Load() != 1 || captureSessionRequests.Load() != 1 || tunnelAuthorization.Load() != "Bearer gateway-ticket" || captureAuthorization.Load() != "Bearer dependency-ticket" {
		t.Fatalf("session requests = %d/%d, gateway auth = %v/%v", sessionRequests.Load(), captureSessionRequests.Load(), tunnelAuthorization.Load(), captureAuthorization.Load())
	}
	if prematureCaptureRequests.Load() != 0 {
		t.Fatalf("dependency capture requested before endpoint reservation: %d requests", prematureCaptureRequests.Load())
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
	if !strings.Contains(text, fmt.Sprintf("Listening      0.0.0.0:%d", proxyPort)) ||
		!strings.Contains(text, fmt.Sprintf("Docker Desktop http://host.docker.internal:%d", proxyPort)) ||
		!strings.Contains(text, "WARNING: Dependency proxy is listening outside localhost.") ||
		!strings.Contains(text, "Dependency replay configuration loaded: 0 active.") {
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

func TestExposeDependencyProxyBindFailureIsAtomic(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer local.Close()
	_, localPortText, _ := net.SplitHostPort(local.Listener.Addr().String())

	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Close()
	_, proxyPortText, _ := net.SplitHostPort(reservation.Addr().String())

	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			_ = json.NewEncoder(w).Encode(oauth.Metadata{
				Issuer: issuer.URL, AuthorizationEndpoint: issuer.URL + "/authorize",
				TokenEndpoint: issuer.URL + "/token", GrantTypesSupported: []string{"authorization_code", "refresh_token"},
				CodeChallengeMethods: []string{"S256"}, TokenAuthMethods: []string{"none"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer issuer.Close()

	var sessionRequests atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"userId": "user_123", "authenticationMethod": "clerk_oauth"})
		default:
			if strings.Contains(r.URL.Path, "sessions") {
				sessionRequests.Add(1)
			}
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer controlPlane.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	expires := time.Now().Add(time.Hour)
	if err := config.Save(path, config.Config{APIURL: controlPlane.URL, OAuthIssuer: issuer.URL, OAuthClientID: "client", OAuthScopes: "openid", UserID: "user_123", ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	store := newCLIStore()
	store.values[oauth.Account(issuer.URL, "client")] = oauth.OAuthCredentials{
		AccessToken: "oauth-access", RefreshToken: "oauth-refresh", TokenType: "Bearer",
		Scope: []string{"openid"}, ExpiresAt: expires, UserID: "user_123",
	}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: path, HTTPClient: &http.Client{Timeout: 3 * time.Second}, Credentials: store}
	code := app.Run(context.Background(), []string{
		"expose", "--name", "spring-demo", "--http", localPortText,
		"--proxy-port", proxyPortText,
	})
	if code == 0 || !strings.Contains(output.String(), "cannot start dependency proxy on 127.0.0.1:"+proxyPortText) || !strings.Contains(output.String(), "use --proxy-port") {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if sessionRequests.Load() != 0 {
		t.Fatalf("proxy bind failure opened %d backend sessions", sessionRequests.Load())
	}
}
