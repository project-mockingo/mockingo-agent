package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

func testServer() *Server {
	return NewServer(Config{BaseDomain: "localhost", PublicScheme: "http", DevToken: "development-token"})
}

func createTunnelRequest(t *testing.T, server http.Handler, token, name string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "protocol": "http", "localPort": 8080})
	request := httptest.NewRequest(http.MethodPost, "http://gateway/api/v1/tunnels", bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func TestGatewayAuthentication(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"", "wrong"} {
		recorder := createTunnelRequest(t, testServer(), token, "demo")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d, want 401", token, recorder.Code)
		}
		var response apiError
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Code != "unauthorized" {
			t.Fatalf("unexpected JSON error: %#v (%v)", response, err)
		}
	}
}

func TestTunnelRegistrationConflictWhenConnected(t *testing.T) {
	t.Parallel()
	server := testServer()
	first := createTunnelRequest(t, server, "development-token", "demo")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	var registration createResponse
	if err := json.Unmarshal(first.Body.Bytes(), &registration); err != nil {
		t.Fatal(err)
	}
	server.store.mu.Lock()
	server.store.byID[registration.ID].connection = &connection{done: make(chan struct{})}
	server.store.mu.Unlock()
	second := createTunnelRequest(t, server, "development-token", "demo")
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409: %s", second.Code, second.Body.String())
	}
}

func TestPublicRequestSizeLimit(t *testing.T) {
	t.Parallel()
	server := testServer()
	request := httptest.NewRequest(http.MethodPost, "http://gateway/upload", io.NopCloser(io.LimitReader(zeroReader{}, tunnelprotocol.MaxBodySize+1)))
	request.Host = "demo.localhost"
	request.ContentLength = tunnelprotocol.MaxBodySize + 1
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
