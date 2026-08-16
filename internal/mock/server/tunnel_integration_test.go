package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/project-mockingo/mockingo-agent/internal/agent"
	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

func TestMockServerSurvivesTunnelReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := mockengine.Compile([]mockengine.MockDefinition{{
		Priority: 1,
		Request:  mockengine.RequestMatcher{Method: mockengine.HTTPMethod(http.MethodGet), Path: mockengine.PathMatcher{Type: mockengine.PathExactPath, Value: "/weather"}},
		Response: mockengine.ResponseDefinition{Status: http.StatusOK, Body: []byte("same-server")},
	}})
	mockServer, err := Start(ctx, engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = mockServer.Shutdown(shutdownCtx)
	}()
	port := mockServer.Port()

	served := make(chan struct{}, 1)
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, upgradeErr := upgrader.Upgrade(writer, request, nil)
		if upgradeErr != nil {
			return
		}
		defer connection.Close()
		if connections.Add(1) == 1 {
			return
		}
		_ = connection.WriteJSON(tunnelprotocol.Message{Version: 1, Type: tunnelprotocol.TypeRequest, RequestID: "after-reconnect", Method: http.MethodGet, Path: "/weather"})
		var response tunnelprotocol.Message
		if readErr := connection.ReadJSON(&response); readErr != nil {
			t.Errorf("read response: %v", readErr)
			return
		}
		body, _ := base64.StdEncoding.DecodeString(response.BodyBase64)
		if response.Status != http.StatusOK || string(body) != "same-server" {
			t.Errorf("response after reconnect = %#v %q", response, body)
		}
		served <- struct{}{}
		_, _, _ = connection.ReadMessage()
	}))
	defer gateway.Close()
	connectURL := strings.Replace(gateway.URL, "http://", "ws://", 1) + "/v1/connect"
	initial := agent.Session{EndpointID: "endpoint", EndpointName: "weather", SessionID: "one", ConnectURL: connectURL, Ticket: "ticket-one", PublicURL: "https://weather.mockingo.click"}
	var acquired atomic.Int32
	tunnelAgent := agent.New(agent.Config{
		InitialSession: &initial,
		AcquireSession: func(context.Context) (agent.Session, error) {
			acquired.Add(1)
			return agent.Session{EndpointID: "endpoint", EndpointName: "weather", SessionID: "two", ConnectURL: connectURL, Ticket: "ticket-two", PublicURL: "https://weather.mockingo.click"}, nil
		},
		Retryable: func(error) bool { return true }, LocalPort: port, RequestTimeout: time.Second,
		ReconnectEnabled: true, ReconnectInitialDelay: time.Millisecond, ReconnectMaxDelay: 2 * time.Millisecond,
	})
	done := make(chan error, 1)
	go func() { done <- tunnelAgent.Run(ctx) }()
	select {
	case <-served:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("mock was not served after reconnect")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if connections.Load() < 2 || acquired.Load() < 1 || mockServer.Port() != port {
		t.Fatalf("connections=%d acquired=%d port=%d/%d", connections.Load(), acquired.Load(), port, mockServer.Port())
	}
}
