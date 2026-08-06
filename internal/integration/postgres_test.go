//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mockingo/mockingo-cli/internal/agent"
	"github.com/mockingo/mockingo-cli/internal/database"
	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/internal/gateway"
)

func postgresRepository(t *testing.T) *endpoint.PostgresRepository {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if err := database.Ready(ctx, pool); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return endpoint.NewPostgresRepository(pool)
}

func TestPostgresFullTunnelRestartLifecycle(t *testing.T) {
	repository := postgresRepository(t)
	defer repository.Close()
	name := uniqueName()
	defer repository.DeleteEndpoint(context.Background(), name) //nolint:errcheck
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "postgres-tunnel-ok") }))
	defer local.Close()
	_, portText, err := net.SplitHostPort(local.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var localPort int
	if _, err := fmt.Sscanf(portText, "%d", &localPort); err != nil {
		t.Fatal(err)
	}

	startGatewayAndAgent := func() (*httptest.Server, agent.Registration, context.CancelFunc, <-chan error) {
		handler := gateway.NewServer(gateway.Config{BaseDomain: "mockingo.click", PublicScheme: "https", APIToken: "test-token", Repository: repository, RequestTimeout: 3 * time.Second})
		server := httptest.NewServer(handler)
		registration, err := agent.Register(context.Background(), server.Client(), server.URL, "test-token", name, localPort)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		registration.ConnectURL = strings.Replace(registration.ConnectURL, "wss://", "ws://", 1)
		ctx, cancel := context.WithCancel(context.Background())
		connected := make(chan struct{}, 1)
		done := make(chan error, 1)
		tunnelAgent := agent.New(agent.Config{ConnectURL: registration.ConnectURL, SessionToken: registration.SessionToken, PublicURL: registration.PublicURL, LocalPort: localPort, RequestTimeout: 3 * time.Second, OnState: func(state string) {
			if strings.HasPrefix(state, "Connected") {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		}})
		go func() { done <- tunnelAgent.Run(ctx) }()
		select {
		case <-connected:
		case <-time.After(3 * time.Second):
			cancel()
			server.Close()
			t.Fatal("agent did not connect")
		}
		return server, registration, cancel, done
	}
	requestPublic := func(server *httptest.Server) (int, string) {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/hello", nil)
		request.Host = name + ".mockingo.click"
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}

	server1, first, cancel1, done1 := startGatewayAndAgent()
	if status, body := requestPublic(server1); status != http.StatusOK || body != "postgres-tunnel-ok" {
		t.Fatalf("forwarded response = %d %q", status, body)
	}
	cancel1()
	select {
	case err := <-done1:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first agent did not stop")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, _ := requestPublic(server1)
		if status == http.StatusBadGateway {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("endpoint did not become offline")
		}
		time.Sleep(20 * time.Millisecond)
	}
	server1.Close()

	handler2 := gateway.NewServer(gateway.Config{BaseDomain: "mockingo.click", PublicScheme: "https", APIToken: "test-token", Repository: repository, RequestTimeout: 3 * time.Second})
	offlineServer := httptest.NewServer(handler2)
	if status, _ := requestPublic(offlineServer); status != http.StatusBadGateway {
		t.Fatalf("restart status = %d", status)
	}
	offlineServer.Close()

	server2, second, cancel2, done2 := startGatewayAndAgent()
	if second.EndpointID != first.EndpointID || second.Hostname != first.Hostname {
		t.Fatalf("endpoint changed across restart: %#v %#v", first, second)
	}
	if status, body := requestPublic(server2); status != http.StatusOK || body != "postgres-tunnel-ok" {
		t.Fatalf("reconnected response = %d %q", status, body)
	}
	cancel2()
	select {
	case err := <-done2:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second agent did not stop")
	}
	if err := agent.DeleteEndpoint(context.Background(), server2.Client(), server2.URL, "test-token", name); err != nil {
		t.Fatal(err)
	}
	if status, _ := requestPublic(server2); status != http.StatusNotFound {
		t.Fatalf("deleted status = %d", status)
	}
	recreated, err := agent.Register(context.Background(), server2.Client(), server2.URL, "test-token", name, localPort)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.EndpointID == first.EndpointID {
		t.Fatal("recreated endpoint retained deleted ID")
	}
	server2.Close()
}

func uniqueName() string { return fmt.Sprintf("pg-%x", time.Now().UnixNano()) }

func TestPostgresRepositoryDuplicateConcurrency(t *testing.T) {
	repository := postgresRepository(t)
	defer repository.Close()
	name := uniqueName()
	defer repository.DeleteEndpoint(context.Background(), name) //nolint:errcheck
	var successes atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			now := time.Now().UTC()
			value := endpoint.Endpoint{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", id), Name: name, Hostname: name + ".mockingo.click", CreatedAt: now, UpdatedAt: now}
			_, err := repository.CreateEndpoint(context.Background(), value)
			if err == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(err, endpoint.ErrConflict) {
				t.Errorf("create: %v", err)
			}
		}(i)
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful creates = %d, want 1", successes.Load())
	}
	values, err := repository.ListEndpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, value := range values {
		if value.Name == name {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("stored rows for %s = %d", name, found)
	}
}

func TestPostgresEndpointPersistenceLifecycle(t *testing.T) {
	repository := postgresRepository(t)
	defer repository.Close()
	name := uniqueName()
	defer repository.DeleteEndpoint(context.Background(), name) //nolint:errcheck
	server1 := gateway.NewServer(gateway.Config{BaseDomain: "mockingo.click", PublicScheme: "https", APIToken: "test-token", Repository: repository})
	register := func(handler http.Handler) map[string]any {
		body, _ := json.Marshal(map[string]any{"name": name, "protocol": "http", "localPort": 8080})
		request := httptest.NewRequest(http.MethodPost, "http://gateway/api/v1/tunnels", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("register = %d %s", recorder.Code, recorder.Body.String())
		}
		var response map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return response
	}
	first := register(server1)
	server2 := gateway.NewServer(gateway.Config{BaseDomain: "mockingo.click", PublicScheme: "https", APIToken: "test-token", Repository: repository})
	public := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	public.Host = name + ".mockingo.click"
	recorder := httptest.NewRecorder()
	server2.ServeHTTP(recorder, public)
	if recorder.Code != http.StatusBadGateway || !bytes.Contains(recorder.Body.Bytes(), []byte(`"tunnel_offline"`)) {
		t.Fatalf("after restart = %d %s", recorder.Code, recorder.Body.String())
	}
	second := register(server2)
	if first["endpointId"] != second["endpointId"] || first["hostname"] != second["hostname"] {
		t.Fatalf("endpoint changed: %#v %#v", first, second)
	}
	request := httptest.NewRequest(http.MethodDelete, "http://gateway/api/v1/endpoints/"+name, nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder = httptest.NewRecorder()
	server2.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", recorder.Code, recorder.Body.String())
	}
	public = httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	public.Host = name + ".mockingo.click"
	recorder = httptest.NewRecorder()
	server2.ServeHTTP(recorder, public)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("deleted route = %d %s", recorder.Code, recorder.Body.String())
	}
	third := register(server2)
	if third["endpointId"] == first["endpointId"] {
		t.Fatal("recreated endpoint retained deleted ID")
	}
}
