package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
)

type recordedRequest struct {
	method string
	uri    string
	header http.Header
	body   []byte
}

func outgoingRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type spyUpstream struct {
	server   *httptest.Server
	requests chan recordedRequest
	count    atomic.Int32
}

func newSpyUpstream(t *testing.T, status int, responseBody string) *spyUpstream {
	t.Helper()
	spy := &spyUpstream{requests: make(chan recordedRequest, 100)}
	spy.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
		}
		spy.count.Add(1)
		spy.requests <- recordedRequest{method: request.Method, uri: request.URL.RequestURI(), header: request.Header.Clone(), body: body}
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(responseBody))
	}))
	t.Cleanup(spy.server.Close)
	return spy
}

func definition(method, path string, status int, body string) mockengine.MockDefinition {
	return mockengine.MockDefinition{
		Priority: 1,
		Request: mockengine.RequestMatcher{
			Method: mockengine.HTTPMethod(method),
			Path:   mockengine.PathMatcher{Type: mockengine.PathExactPath, Value: path},
		},
		Response: mockengine.ResponseDefinition{Status: status, Body: []byte(body)},
	}
}

func TestLocalForwarderPreservesRequest(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusNotFound, "local-404")
	request := outgoingRequest(t, http.MethodPost, spy.server.URL+"/orders?source=public", bytes.NewBufferString("exact-body\x00"))
	request.Header.Set("X-Request-ID", "request-123")
	response, err := NewLocalForwarder(time.Second).Handle(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusNotFound || string(response.Body) != "local-404" || response.Route != RouteForward {
		t.Fatalf("response = %#v", response)
	}
	got := <-spy.requests
	if got.method != http.MethodPost || got.uri != "/orders?source=public" || string(got.body) != "exact-body\x00" || got.header.Get("X-Request-ID") != "request-123" {
		t.Fatalf("forwarded request = %#v", got)
	}
}

func TestHybridMatchIsAuthoritativeForEveryStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			spy := newSpyUpstream(t, http.StatusOK, "real")
			engine := mockengine.Compile([]mockengine.MockDefinition{definition(http.MethodPost, "/payment", status, "mock")})
			handler := NewHybridHandler(engine, NewLocalForwarder(time.Second), nil)
			request := outgoingRequest(t, http.MethodPost, spy.server.URL+"/payment", bytes.NewBufferString("do-not-forward"))
			response, err := handler.Handle(request)
			if err != nil {
				t.Fatal(err)
			}
			if response.Status != status || string(response.Body) != "mock" || response.Route != RouteMock {
				t.Fatalf("response = %#v", response)
			}
			if spy.count.Load() != 0 {
				t.Fatalf("matched request reached upstream %d times", spy.count.Load())
			}
		})
	}
}

func TestHybridMissAndMethodMismatchForwardExactlyOnce(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusCreated, "real")
	engine := mockengine.Compile([]mockengine.MockDefinition{definition(http.MethodGet, "/mocked", http.StatusOK, "mock")})
	handler := NewHybridHandler(engine, NewLocalForwarder(time.Second), nil)
	for _, target := range []string{"/real?x=1", "/mocked"} {
		request := outgoingRequest(t, http.MethodPost, spy.server.URL+target, bytes.NewBufferString(target))
		response, err := handler.Handle(request)
		if err != nil || response.Route != RouteForward || response.Status != http.StatusCreated {
			t.Fatalf("%s response = %#v, %v", target, response, err)
		}
	}
	if spy.count.Load() != 2 {
		t.Fatalf("upstream count = %d, want 2", spy.count.Load())
	}
}

func TestHybridUsesExactURLTemplateAndPriorityMatching(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusOK, "real")
	definitions := []mockengine.MockDefinition{
		{ID: "lower", Priority: 5, Request: mockengine.RequestMatcher{Method: mockengine.MethodAny, Path: mockengine.PathMatcher{Type: mockengine.PathExactPath, Value: "/priority"}}, Response: mockengine.ResponseDefinition{Status: 200, Body: []byte("lower")}},
		{ID: "higher", Priority: 1, Request: mockengine.RequestMatcher{Method: mockengine.MethodAny, Path: mockengine.PathMatcher{Type: mockengine.PathExactPath, Value: "/priority"}}, Response: mockengine.ResponseDefinition{Status: 201, Body: []byte("higher")}},
		{Priority: 1, Request: mockengine.RequestMatcher{Method: mockengine.MethodAny, Path: mockengine.PathMatcher{Type: mockengine.PathExactURL, Value: "/weather?city=Prague"}}, Response: mockengine.ResponseDefinition{Status: 202}},
		{Priority: 1, Request: mockengine.RequestMatcher{Method: mockengine.MethodAny, Path: mockengine.PathMatcher{Type: mockengine.PathTemplate, Value: "/users/{id}"}}, Response: mockengine.ResponseDefinition{Status: 203}},
	}
	handler := NewHybridHandler(mockengine.Compile(definitions), NewLocalForwarder(time.Second), nil)
	for target, wantStatus := range map[string]int{"/priority": 201, "/weather?city=Prague": 202, "/users/42": 203} {
		response, err := handler.Handle(outgoingRequest(t, http.MethodGet, spy.server.URL+target, nil))
		if err != nil || response.Route != RouteMock || response.Status != wantStatus {
			t.Fatalf("%s response = %#v, %v", target, response, err)
		}
	}
	if spy.count.Load() != 0 {
		t.Fatalf("matched requests reached upstream %d times", spy.count.Load())
	}
}

type failingMockResponder struct{ err error }

func (failingMockResponder) Respond(*http.Request, *mockengine.MockDefinition) (HandlerResponse, error) {
	return HandlerResponse{}, errors.New("test render failure")
}

func TestHybridRenderingFailureReturnsSafe500WithoutForwarding(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusOK, "real")
	var logged atomic.Int32
	handler := NewHybridHandler(mockengine.Compile([]mockengine.MockDefinition{definition(http.MethodGet, "/mocked", 200, "mock")}), NewLocalForwarder(time.Second), func(error) {
		logged.Add(1)
	})
	handler.responder = failingMockResponder{}
	response, err := handler.Handle(outgoingRequest(t, http.MethodGet, spy.server.URL+"/mocked", nil))
	if err != nil || response.Status != http.StatusInternalServerError || response.Route != RouteMock {
		t.Fatalf("response = %#v, %v", response, err)
	}
	if spy.count.Load() != 0 || logged.Load() != 1 {
		t.Fatalf("upstream=%d logged=%d", spy.count.Load(), logged.Load())
	}
}

func TestMatchedMockWorksWhenLocalTargetIsUnavailable(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusOK, "real")
	handler := NewHybridHandler(mockengine.Compile([]mockengine.MockDefinition{definition(http.MethodGet, "/mocked", 200, "mock")}), NewLocalForwarder(250*time.Millisecond), nil)
	spy.server.Close()
	response, err := handler.Handle(outgoingRequest(t, http.MethodGet, spy.server.URL+"/mocked", nil))
	if err != nil || response.Status != http.StatusOK || response.Route != RouteMock || string(response.Body) != "mock" {
		t.Fatalf("mock with unavailable target = %#v, %v", response, err)
	}
	_, err = handler.Handle(outgoingRequest(t, http.MethodGet, spy.server.URL+"/real", nil))
	var handlerErr *HandlerError
	if !errors.As(err, &handlerErr) || handlerErr.Code != "local_unreachable" {
		t.Fatalf("forward with unavailable target error = %v", err)
	}
}

func TestHybridDelayCancellationAndConcurrentForward(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusOK, "real")
	delayed := definition(http.MethodGet, "/slow", http.StatusOK, "slow")
	delayed.Response.FixedDelay = time.Second
	handler := NewHybridHandler(mockengine.Compile([]mockengine.MockDefinition{delayed, definition(http.MethodPost, "/payment", http.StatusServiceUnavailable, "mock")}), NewLocalForwarder(time.Second), nil)

	request := outgoingRequest(t, http.MethodGet, spy.server.URL+"/slow", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	done := make(chan error, 1)
	go func() { _, err := handler.Handle(request); done <- err }()

	started := time.Now()
	forwarded, err := handler.Handle(outgoingRequest(t, http.MethodGet, spy.server.URL+"/real", nil))
	if err != nil || forwarded.Route != RouteForward || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("forward during delayed mock = %#v, %v, elapsed %s", forwarded, err, time.Since(started))
	}
	mocked, err := handler.Handle(outgoingRequest(t, http.MethodPost, spy.server.URL+"/payment", nil))
	if err != nil || mocked.Route != RouteMock || mocked.Status != http.StatusServiceUnavailable {
		t.Fatalf("concurrent mock = %#v, %v", mocked, err)
	}
	cancel()
	select {
	case err := <-done:
		var handlerErr *HandlerError
		if !errors.As(err, &handlerErr) || handlerErr.Code == "" {
			t.Fatalf("cancelled delay error = %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cancelled mock delay did not stop")
	}
	if spy.count.Load() != 1 {
		t.Fatalf("upstream count = %d, want 1", spy.count.Load())
	}
}

func TestHybridConcurrentMixedTraffic(t *testing.T) {
	spy := newSpyUpstream(t, http.StatusOK, "real")
	handler := NewHybridHandler(mockengine.Compile([]mockengine.MockDefinition{definition(http.MethodGet, "/mocked", 200, "mock")}), NewLocalForwarder(time.Second), nil)
	var group sync.WaitGroup
	for i := range 50 {
		group.Add(1)
		go func() {
			defer group.Done()
			path := "/real"
			if i%2 == 0 {
				path = "/mocked"
			}
			if _, err := handler.Handle(outgoingRequest(t, http.MethodGet, spy.server.URL+path, nil)); err != nil {
				t.Errorf("handle %s: %v", path, err)
			}
		}()
	}
	group.Wait()
	if spy.count.Load() != 25 {
		t.Fatalf("upstream count = %d, want 25", spy.count.Load())
	}
}
