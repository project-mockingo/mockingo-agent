package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

func testServer() *Server {
	return NewServer(Config{BaseDomain: "localhost", PublicScheme: "http", DevToken: "development-token"})
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apiError {
	t.Helper()
	var value apiError
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestEndpointOfflineNotFoundPersistenceAndDeletion(t *testing.T) {
	t.Parallel()
	repository := endpoint.NewMemoryRepository()
	server := NewServer(Config{BaseDomain: "mockingo.click", PublicScheme: "https", APIToken: "test-token", Repository: repository})
	created := createTunnelRequest(t, server, "test-token", "spring-demo")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	request.Host = "spring-demo.mockingo.click"
	offline := httptest.NewRecorder()
	server.ServeHTTP(offline, request)
	if offline.Code != http.StatusBadGateway || decodeError(t, offline).Code != "tunnel_offline" {
		t.Fatalf("offline = %d %s", offline.Code, offline.Body.String())
	}

	restarted := NewServer(Config{BaseDomain: "mockingo.click", PublicScheme: "https", APIToken: "test-token", Repository: repository})
	request = httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	request.Host = "spring-demo.mockingo.click"
	offline = httptest.NewRecorder()
	restarted.ServeHTTP(offline, request)
	if offline.Code != http.StatusBadGateway || decodeError(t, offline).Code != "tunnel_offline" {
		t.Fatalf("after restart = %d %s", offline.Code, offline.Body.String())
	}
	second := createTunnelRequest(t, restarted, "test-token", "spring-demo")
	var secondRegistration createResponse
	_ = json.Unmarshal(second.Body.Bytes(), &secondRegistration)
	var firstRegistration createResponse
	_ = json.Unmarshal(created.Body.Bytes(), &firstRegistration)
	if second.Code != http.StatusCreated || secondRegistration.EndpointID != firstRegistration.EndpointID || secondRegistration.Hostname != firstRegistration.Hostname {
		t.Fatalf("reregistration changed endpoint: first=%#v second=%#v", firstRegistration, secondRegistration)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "http://gateway/api/v1/endpoints/spring-demo", nil)
	deleteRequest.Header.Set("Authorization", "Bearer test-token")
	deleted := httptest.NewRecorder()
	restarted.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
	request.Host = "spring-demo.mockingo.click"
	missing := httptest.NewRecorder()
	restarted.ServeHTTP(missing, request)
	if missing.Code != http.StatusNotFound || decodeError(t, missing).Code != "endpoint_not_found" {
		t.Fatalf("missing = %d %s", missing.Code, missing.Body.String())
	}
	recreated := createTunnelRequest(t, restarted, "test-token", "spring-demo")
	if recreated.Code != http.StatusCreated {
		t.Fatalf("recreate = %d %s", recreated.Code, recreated.Body.String())
	}
}

type failingRepository struct{ endpoint.Repository }

func (f failingRepository) Ping(context.Context) error { return errors.New("down") }

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()
	server := NewServer(Config{BaseDomain: "localhost", PublicScheme: "http", APIToken: "token", Repository: failingRepository{endpoint.NewMemoryRepository()}})
	for path, want := range map[string]int{"/health/live": http.StatusOK, "/health/ready": http.StatusServiceUnavailable} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway"+path, nil))
		if recorder.Code != want {
			t.Errorf("%s = %d, want %d", path, recorder.Code, want)
		}
	}
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
