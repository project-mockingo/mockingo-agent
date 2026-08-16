package mock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestEngineMatchingAndPriority(t *testing.T) {
	definitions := []MockDefinition{
		{ID: "later", Priority: 5, Request: RequestMatcher{Method: HTTPMethod(http.MethodGet), Path: PathMatcher{Type: PathExactPath, Value: "/users/1"}}},
		{ID: "template", Priority: 2, Request: RequestMatcher{Method: HTTPMethod(http.MethodGet), Path: PathMatcher{Type: PathTemplate, Value: "/users/{id}"}}},
		{ID: "url", Priority: 1, Request: RequestMatcher{Method: MethodAny, Path: PathMatcher{Type: PathExactURL, Value: "/weather?city=Prague"}}},
	}
	engine := Compile(definitions)
	tests := []struct {
		method, target, want string
	}{
		{http.MethodPost, "/weather?city=Prague", "url"},
		{http.MethodGet, "/users/1", "template"},
		{http.MethodGet, "/users/alice", "template"},
		{http.MethodPost, "/users/1", ""},
		{http.MethodGet, "/users/1/orders", ""},
		{http.MethodGet, "/weather?city=Brno", ""},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.target, nil)
		got, found := engine.Match(request)
		if test.want == "" && found {
			t.Fatalf("%s %s unexpectedly matched %s", test.method, test.target, got.ID)
		}
		if test.want != "" && (!found || got.ID != test.want) {
			t.Fatalf("%s %s matched %#v, want %s", test.method, test.target, got, test.want)
		}
	}
}

func TestEqualPriorityUsesLoadOrderAndCompileIsImmutable(t *testing.T) {
	definitions := []MockDefinition{
		{ID: "first", Priority: 5, Request: RequestMatcher{Method: MethodAny, Path: PathMatcher{Type: PathExactPath, Value: "/"}}, Response: ResponseDefinition{Headers: http.Header{"X-Test": {"one"}}, Body: []byte("one")}},
		{ID: "second", Priority: 5, Request: RequestMatcher{Method: MethodAny, Path: PathMatcher{Type: PathExactPath, Value: "/"}}},
	}
	engine := Compile(definitions)
	definitions[0].ID = "mutated"
	definitions[0].Response.Headers.Set("X-Test", "mutated")
	definitions[0].Response.Body[0] = 'x'
	got, found := engine.Match(httptest.NewRequest(http.MethodGet, "/", nil))
	if !found || got.ID != "first" || got.Response.Headers.Get("X-Test") != "one" || string(got.Response.Body) != "one" {
		t.Fatalf("compiled definition was mutated: %#v", got)
	}
	got.Response.Body[0] = 'z'
	again, _ := engine.Match(httptest.NewRequest(http.MethodGet, "/", nil))
	if string(again.Response.Body) != "one" {
		t.Fatal("Match returned mutable engine state")
	}
}

func TestHandlerResponsesHEADAndNotFound(t *testing.T) {
	engine := Compile([]MockDefinition{{
		Priority: 1, Request: RequestMatcher{Method: MethodAny, Path: PathMatcher{Type: PathExactPath, Value: "/ok"}},
		Response: ResponseDefinition{Status: http.StatusCreated, Headers: http.Header{"X-Multi": {"one", "two"}}, Body: []byte("response")},
	}})
	handler := Handler{Engine: engine, Done: make(chan struct{})}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if response.Code != http.StatusCreated || response.Body.String() != "response" || len(response.Header().Values("X-Multi")) != 2 {
		t.Fatalf("response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/ok", nil))
	if head.Code != http.StatusCreated || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "8" {
		t.Fatalf("HEAD response = %d %#v %q", head.Code, head.Header(), head.Body.String())
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if missing.Code != http.StatusNotFound || missing.Header().Get("Content-Type") != "application/json" || missing.Body.String() != "{\"code\":\"mock_not_found\",\"message\":\"No mock matched this request.\"}\n" {
		t.Fatalf("missing response = %d %q", missing.Code, missing.Body.String())
	}
}

func TestFixedDelayCancellationAndConcurrentMatching(t *testing.T) {
	engine := Compile([]MockDefinition{{
		Priority: 1, Request: RequestMatcher{Method: MethodAny, Path: PathMatcher{Type: PathExactPath, Value: "/"}},
		Response: ResponseDefinition{Status: http.StatusOK, FixedDelay: time.Second},
	}})
	done := make(chan struct{})
	handler := Handler{Engine: engine, Done: done}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	finished := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		close(finished)
	}()
	cancel()
	select {
	case <-finished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("cancelled delay did not stop")
	}
	shutdownFinished := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		close(shutdownFinished)
	}()
	close(done)
	select {
	case <-shutdownFinished:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("shutdown did not cancel delayed response")
	}
	var group sync.WaitGroup
	for range 50 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, found := engine.Match(httptest.NewRequest(http.MethodGet, "/", nil)); !found {
				t.Error("concurrent match failed")
			}
		}()
	}
	group.Wait()
}

func TestFixedDelayCompletes(t *testing.T) {
	engine := Compile([]MockDefinition{{
		Priority: 1, Request: RequestMatcher{Method: MethodAny, Path: PathMatcher{Type: PathExactPath, Value: "/"}},
		Response: ResponseDefinition{Status: http.StatusOK, Body: []byte("done"), FixedDelay: 25 * time.Millisecond},
	}})
	started := time.Now()
	response := httptest.NewRecorder()
	Handler{Engine: engine}.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("delay completed too early: %s", elapsed)
	}
	if response.Code != http.StatusOK || response.Body.String() != "done" {
		t.Fatalf("delayed response = %d %q", response.Code, response.Body.String())
	}
}
