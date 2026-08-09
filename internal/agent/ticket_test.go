package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTicketReconnectAcquiresFreshSession(t *testing.T) {
	var connections atomic.Int32
	var mu sync.Mutex
	var tickets []string
	connected := make(chan struct{}, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connect" || r.URL.RawQuery != "" {
			t.Errorf("unsafe connect URL: %s", r.URL.String())
		}
		ticket := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		tickets = append(tickets, ticket)
		mu.Unlock()
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connections.Add(1)
		connected <- struct{}{}
		_ = ws.Close()
	}))
	defer server.Close()
	connectURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/connect"
	var issued atomic.Int32
	provider := func(context.Context) (Session, error) {
		n := issued.Add(1)
		return Session{SessionID: fmt.Sprintf("session-%d", n), ConnectURL: connectURL, Ticket: fmt.Sprintf("ticket-%d", n)}, nil
	}
	initial, _ := provider(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	agent := New(Config{InitialSession: &initial, AcquireSession: provider, Retryable: func(error) bool { return true }, ReconnectEnabled: true, ReconnectInitialDelay: time.Millisecond, ReconnectMaxDelay: 2 * time.Millisecond, LocalPort: 8080, RequestTimeout: time.Second})
	go func() { done <- agent.Run(ctx) }()
	for range 2 {
		select {
		case <-connected:
		case <-time.After(2 * time.Second):
			t.Fatal("agent did not connect twice")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tickets) < 2 || tickets[0] != "ticket-1" || tickets[1] != "ticket-2" || tickets[0] == tickets[1] {
		t.Fatalf("tickets = %#v", tickets)
	}
	if initial.Ticket != "" {
		t.Fatal("initial ticket reference was not cleared")
	}
}

func TestCancellationDuringWebSocketDial(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	initial := Session{ConnectURL: strings.Replace(server.URL, "http://", "ws://", 1), Ticket: "one-use"}
	agent := New(Config{InitialSession: &initial, AcquireSession: func(context.Context) (Session, error) { return Session{}, nil }, ReconnectEnabled: true, LocalPort: 8080, RequestTimeout: time.Second})
	go func() { done <- agent.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("dial did not cancel")
	}
}

func TestBackoffBoundAndSessionRedaction(t *testing.T) {
	if got := nextDelayMax(20*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("backoff = %s", got)
	}
	if text := fmt.Sprintf("%+v", Session{Ticket: "secret"}); strings.Contains(text, "secret") {
		t.Fatalf("session leaked ticket: %s", text)
	}
}

func TestTemporarySessionConflictIsRetried(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connected <- struct{}{}
		_, _, _ = ws.ReadMessage()
		_ = ws.Close()
	}))
	defer server.Close()
	connectURL := strings.Replace(server.URL, "http://", "ws://", 1)
	conflict := errors.New("session conflict")
	var requests atomic.Int32
	states := make(chan string, 4)
	provider := func(context.Context) (Session, error) {
		if requests.Add(1) == 1 {
			return Session{}, conflict
		}
		return Session{SessionID: "fresh", ConnectURL: connectURL, Ticket: "fresh-ticket"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	agent := New(Config{AcquireSession: provider, Retryable: func(err error) bool { return errors.Is(err, conflict) }, TemporaryConflict: func(err error) bool { return errors.Is(err, conflict) }, ReconnectEnabled: true, ReconnectInitialDelay: time.Millisecond, ReconnectMaxDelay: 2 * time.Millisecond, LocalPort: 8080, RequestTimeout: time.Second, OnState: func(state string) { states <- state }})
	go func() { done <- agent.Run(ctx) }()
	select {
	case <-connected:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("agent did not recover from conflict")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("session requests = %d", requests.Load())
	}
	select {
	case state := <-states:
		if state != "Waiting for the previous tunnel session to close..." {
			t.Fatalf("state = %q", state)
		}
	default:
		t.Fatal("missing conflict state")
	}
}
