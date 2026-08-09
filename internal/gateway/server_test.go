package gateway

import (
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

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apiError {
	t.Helper()
	var value apiError
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPublicOfflineAndUnknownSemanticsUseReadOnlyCatalog(t *testing.T) {
	catalog := endpoint.NewMemoryCatalog("spring-demo.mockingo.click")
	server := NewServer(Config{BaseDomain: "mockingo.click", PublicScheme: "https", Catalog: catalog})
	for hostname, want := range map[string]int{
		"spring-demo.mockingo.click": http.StatusBadGateway,
		"missing.mockingo.click":     http.StatusNotFound,
	} {
		request := httptest.NewRequest(http.MethodGet, "http://gateway/hello", nil)
		request.Host = hostname
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Errorf("%s = %d %s, want %d", hostname, recorder.Code, recorder.Body.String(), want)
		}
	}
}

type failingCatalog struct{ endpoint.Catalog }

func (f failingCatalog) Ping(context.Context) error { return errors.New("down") }

func TestHealthEndpoints(t *testing.T) {
	server := NewServer(Config{BaseDomain: "localhost", PublicScheme: "http", Catalog: failingCatalog{endpoint.NewMemoryCatalog()}})
	for path, want := range map[string]int{"/health/live": http.StatusOK, "/health/ready": http.StatusServiceUnavailable} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://gateway"+path, nil))
		if recorder.Code != want {
			t.Errorf("%s = %d, want %d", path, recorder.Code, want)
		}
	}
}

func TestLegacyManagementRoutesAreAbsent(t *testing.T) {
	server := NewServer(Config{BaseDomain: "mockingo.click", PublicScheme: "https"})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "http://gateway/api/v1/tunnels", nil),
		httptest.NewRequest(http.MethodGet, "http://gateway/api/v1/endpoints", nil),
		httptest.NewRequest(http.MethodPost, "http://gateway/api/v1/endpoints", nil),
		httptest.NewRequest(http.MethodDelete, "http://gateway/api/v1/endpoints/demo", nil),
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", request.Method, request.URL.Path, recorder.Code)
		}
	}
}

func TestPublicRequestSizeLimit(t *testing.T) {
	server := NewServer(Config{BaseDomain: "localhost", PublicScheme: "http"})
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
