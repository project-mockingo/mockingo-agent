package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mockingo/mockingo-cli/internal/agent"
	"github.com/mockingo/mockingo-cli/internal/gateway"
)

type receivedRequest struct {
	Method string
	URI    string
	Header string
	Body   string
}

func TestEndToEndTunnel(t *testing.T) {
	received := make(chan receivedRequest, 20)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- receivedRequest{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Get("X-Integration"), Body: string(body)}
		w.Header().Set("X-Local-Response", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, "local:%s", r.URL.RequestURI())
	}))
	defer local.Close()
	_, portText, err := net.SplitHostPort(local.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var localPort int
	if _, err := fmt.Sscanf(portText, "%d", &localPort); err != nil {
		t.Fatal(err)
	}

	gatewayServer := httptest.NewServer(gateway.NewServer(gateway.Config{
		BaseDomain: "localhost", PublicScheme: "http", DevToken: "development-token", RequestTimeout: 3 * time.Second,
	}))
	defer gatewayServer.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	registration, err := agent.Register(context.Background(), client, gatewayServer.URL, "development-token", "demo", localPort)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	connected := make(chan struct{}, 1)
	agentDone := make(chan error, 1)
	tunnelAgent := agent.New(agent.Config{
		ConnectURL: registration.ConnectURL, SessionToken: registration.SessionToken,
		LocalPort: localPort, RequestTimeout: 3 * time.Second,
		OnState: func(string) {
			select {
			case connected <- struct{}{}:
			default:
			}
		},
	})
	go func() { agentDone <- tunnelAgent.Run(ctx) }()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not connect")
	}

	response := publicRequest(t, client, gatewayServer.URL+"/hello?value=1", http.MethodPost, "payload")
	assertResponse(t, response, "/hello?value=1")
	select {
	case got := <-received:
		if got.Method != http.MethodPost || got.URI != "/hello?value=1" || got.Header != "present" || got.Body != "payload" {
			t.Fatalf("local request = %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("local application did not receive request")
	}

	const concurrent = 8
	var wait sync.WaitGroup
	errors := make(chan error, concurrent)
	for i := 0; i < concurrent; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			path := fmt.Sprintf("/concurrent/%d", index)
			response, err := doPublic(client, gatewayServer.URL+path, http.MethodGet, "")
			if err != nil {
				errors <- err
				return
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusCreated || string(body) != "local:"+path {
				errors <- fmt.Errorf("%s: status %d body %q", path, response.StatusCode, body)
			}
		}(i)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	cancel()
	select {
	case err := <-agentDone:
		if err != nil {
			t.Fatalf("agent shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not stop")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := doPublic(client, gatewayServer.URL+"/after-disconnect", http.MethodGet, "")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusBadGateway {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("public request did not become 502 after disconnect (last error %v)", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func publicRequest(t *testing.T, client *http.Client, url, method, body string) *http.Response {
	t.Helper()
	response, err := doPublic(client, url, method, body)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func doPublic(client *http.Client, url, method, body string) (*http.Response, error) {
	request, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	request.Host = "demo.localhost"
	request.Header.Set("X-Integration", "present")
	return client.Do(request)
}

func assertResponse(t *testing.T, response *http.Response, path string) {
	t.Helper()
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated || response.Header.Get("X-Local-Response") != "yes" || string(body) != "local:"+path {
		t.Fatalf("status=%d header=%q body=%q", response.StatusCode, response.Header.Get("X-Local-Response"), body)
	}
}
