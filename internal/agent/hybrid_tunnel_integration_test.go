package agent

import (
	"context"
	"encoding/base64"
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
	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
	openapiadapter "github.com/project-mockingo/mockingo-agent/internal/mock/openapi"
	"github.com/project-mockingo/mockingo-agent/internal/mock/wiremock"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type tunnelExchange struct {
	request tunnelprotocol.Message
	status  int
	body    string
	route   Route
}

func upstreamPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	_, text, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(text, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return port
}

func runHybridTunnel(t *testing.T, engine mockengine.Engine, upstream *spyUpstream, exchanges []tunnelExchange) {
	t.Helper()
	done := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		for i, exchange := range exchanges {
			exchange.request.Version = tunnelprotocol.Version
			exchange.request.Type = tunnelprotocol.TypeRequest
			exchange.request.RequestID = fmt.Sprintf("request-%d", i)
			if err := connection.WriteJSON(exchange.request); err != nil {
				t.Errorf("write request %d: %v", i, err)
				return
			}
			var response tunnelprotocol.Message
			if err := connection.ReadJSON(&response); err != nil {
				t.Errorf("read response %d: %v", i, err)
				return
			}
			body, err := base64.StdEncoding.DecodeString(response.BodyBase64)
			if err != nil || response.Type != tunnelprotocol.TypeResponse || response.Status != exchange.status || string(body) != exchange.body {
				t.Errorf("response %d = %#v body=%q err=%v", i, response, body, err)
			}
		}
		done <- struct{}{}
		_, _, _ = connection.ReadMessage()
	}))
	defer gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connectURL := strings.Replace(gateway.URL, "http://", "ws://", 1)
	initial := Session{EndpointID: "endpoint", EndpointName: "hybrid", SessionID: "session", ConnectURL: connectURL, Ticket: "ticket", PublicURL: "https://hybrid.mockingo.click"}
	var routes = make(chan Route, len(exchanges))
	forwarder := NewLocalForwarder(time.Second)
	tunnelAgent := New(Config{
		InitialSession: &initial,
		AcquireSession: func(context.Context) (Session, error) { return Session{}, context.Canceled },
		LocalPort:      upstreamPort(t, upstream.server), RequestTimeout: time.Second,
		Handler:   NewHybridHandler(engine, forwarder, func(err error) { t.Errorf("render error: %v", err) }),
		OnRequest: func(_ string, _ string, _ int, route Route) { routes <- route },
	})
	agentDone := make(chan error, 1)
	go func() { agentDone <- tunnelAgent.Run(ctx) }()
	select {
	case <-done:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("hybrid tunnel timed out")
	}
	if err := <-agentDone; err != nil {
		t.Fatal(err)
	}
	for i, exchange := range exchanges {
		select {
		case route := <-routes:
			if route != exchange.route {
				t.Errorf("route %d = %s, want %s", i, route, exchange.route)
			}
		default:
			t.Errorf("route %d was not reported", i)
		}
	}
}

func TestWireMockHybridThroughTunnel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wiremock")
	if err := os.MkdirAll(filepath.Join(root, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	mockedMapping := `{"priority":1,"request":{"method":"GET","urlPath":"/mocked"},"response":{"status":200,"body":"mock"}}`
	paymentMapping := `{"priority":1,"request":{"method":"POST","urlPath":"/payment"},"response":{"status":503,"body":"unavailable"}}`
	if err := os.WriteFile(filepath.Join(root, "mappings", "mocked.json"), []byte(mockedMapping), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mappings", "payment.json"), []byte(paymentMapping), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions, err := wiremock.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	upstream := newSpyUpstream(t, http.StatusNotFound, "local-404")
	runHybridTunnel(t, mockengine.Compile(definitions), upstream, []tunnelExchange{
		{request: tunnelprotocol.Message{Method: http.MethodGet, Path: "/mocked"}, status: 200, body: "mock", route: RouteMock},
		{request: tunnelprotocol.Message{Method: http.MethodPost, Path: "/payment", BodyBase64: base64.StdEncoding.EncodeToString([]byte("charge"))}, status: 503, body: "unavailable", route: RouteMock},
		{request: tunnelprotocol.Message{Method: http.MethodGet, Path: "/does-not-exist"}, status: 404, body: "local-404", route: RouteForward},
	})
	if upstream.count.Load() != 1 {
		t.Fatalf("upstream count = %d, want only unmatched request", upstream.count.Load())
	}
}

func TestOpenAPIHybridThroughTunnel(t *testing.T) {
	document := `openapi: 3.0.3
info:
  title: Partial API
  version: 1.0.0
paths:
  /weather:
    get:
      responses:
        '200':
          description: weather
          content:
            application/json:
              example:
                temperature: 23
  /pets/{id}:
    get:
      parameters:
        - in: path
          name: id
          required: true
          schema: {type: string}
      responses:
        '204': {description: found}
`
	path := filepath.Join(t.TempDir(), "partial.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions, err := openapiadapter.Load(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	upstream := newSpyUpstream(t, http.StatusOK, "real-users")
	runHybridTunnel(t, mockengine.Compile(definitions), upstream, []tunnelExchange{
		{request: tunnelprotocol.Message{Method: http.MethodGet, Path: "/weather"}, status: 200, body: "{\n  \"temperature\": 23\n}\n", route: RouteMock},
		{request: tunnelprotocol.Message{Method: http.MethodGet, Path: "/pets/pet-1"}, status: 204, body: "", route: RouteMock},
		{request: tunnelprotocol.Message{Method: http.MethodPost, Path: "/users?active=true", Headers: map[string][]string{"X-Trace": {"abc"}}, BodyBase64: base64.StdEncoding.EncodeToString([]byte("users-body"))}, status: 200, body: "real-users", route: RouteForward},
	})
	if upstream.count.Load() != 1 {
		t.Fatalf("upstream count = %d, want 1", upstream.count.Load())
	}
	real := <-upstream.requests
	if real.method != http.MethodPost || real.uri != "/users?active=true" || real.header.Get("X-Trace") != "abc" || string(real.body) != "users-body" {
		t.Fatalf("forwarded OpenAPI miss = %#v", real)
	}
}

func TestHybridHandlerSurvivesReconnect(t *testing.T) {
	upstream := newSpyUpstream(t, http.StatusOK, "real")
	engine := mockengine.Compile([]mockengine.MockDefinition{definition(http.MethodGet, "/mocked", http.StatusOK, "mock")})
	handler := NewHybridHandler(engine, NewLocalForwarder(time.Second), nil)
	var connections atomic.Int32
	served := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		connectionNumber := connections.Add(1)
		paths := []string{"/mocked"}
		if connectionNumber > 1 {
			paths = append(paths, "/real")
		}
		for i, path := range paths {
			_ = connection.WriteJSON(tunnelprotocol.Message{Version: 1, Type: tunnelprotocol.TypeRequest, RequestID: fmt.Sprintf("%d-%d", connectionNumber, i), Method: http.MethodGet, Path: path})
			var response tunnelprotocol.Message
			if err := connection.ReadJSON(&response); err != nil {
				t.Errorf("read reconnect response: %v", err)
				return
			}
			body, _ := base64.StdEncoding.DecodeString(response.BodyBase64)
			want := "mock"
			if path == "/real" {
				want = "real"
			}
			if response.Status != http.StatusOK || string(body) != want {
				t.Errorf("%s after connection %d = %#v %q", path, connectionNumber, response, body)
			}
		}
		if connectionNumber > 1 {
			served <- struct{}{}
			_, _, _ = connection.ReadMessage()
		}
	}))
	defer gateway.Close()
	connectURL := strings.Replace(gateway.URL, "http://", "ws://", 1)
	initial := Session{SessionID: "one", ConnectURL: connectURL, Ticket: "one"}
	var acquired atomic.Int32
	provider := func(context.Context) (Session, error) {
		n := acquired.Add(1)
		return Session{SessionID: fmt.Sprintf("next-%d", n), ConnectURL: connectURL, Ticket: fmt.Sprintf("ticket-%d", n)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	tunnelAgent := New(Config{
		InitialSession: &initial, AcquireSession: provider, Retryable: func(error) bool { return true },
		ReconnectEnabled: true, ReconnectInitialDelay: time.Millisecond, ReconnectMaxDelay: 2 * time.Millisecond,
		LocalPort: upstreamPort(t, upstream.server), RequestTimeout: time.Second, Handler: handler,
	})
	go func() { done <- tunnelAgent.Run(ctx) }()
	select {
	case <-served:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("hybrid reconnect timed out")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if connections.Load() < 2 || acquired.Load() < 1 || upstream.count.Load() != 1 || tunnelAgent.handler != handler {
		t.Fatalf("connections=%d acquired=%d upstream=%d handlerReused=%v", connections.Load(), acquired.Load(), upstream.count.Load(), tunnelAgent.handler == handler)
	}
}
