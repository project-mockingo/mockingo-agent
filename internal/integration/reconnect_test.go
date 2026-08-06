package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mockingo/mockingo-cli/internal/agent"
	"github.com/mockingo/mockingo-cli/internal/gateway"
)

func TestAgentRegistersNewSessionAfterExpiry(t *testing.T) {
	handler := gateway.NewServer(gateway.Config{BaseDomain: "localhost", PublicScheme: "http", APIToken: "development-token"})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	registration, err := agent.Register(context.Background(), client, server.URL, "development-token", "session-test", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Delete(context.Background(), client, server.URL, "development-token", registration.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	done := make(chan error, 1)
	var registrations atomic.Int32
	tunnelAgent := agent.New(agent.Config{
		ConnectURL: registration.ConnectURL, SessionToken: registration.SessionToken, PublicURL: registration.PublicURL,
		LocalPort: 8080, RequestTimeout: 2 * time.Second,
		Reregister: func(ctx context.Context) (agent.Registration, error) {
			registrations.Add(1)
			return agent.Register(ctx, client, server.URL, "development-token", "session-test", 8080)
		},
		OnState: func(state string) {
			if strings.HasPrefix(state, "Connected") {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		},
	})
	go func() { done <- tunnelAgent.Run(ctx) }()
	select {
	case <-connected:
	case <-time.After(4 * time.Second):
		t.Fatal("agent did not reconnect with a new registration")
	}
	if registrations.Load() != 1 {
		t.Fatalf("new registrations = %d, want 1", registrations.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop")
	}
}
