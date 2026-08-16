package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
)

func TestRealLoopbackServerAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	engine := mockengine.Compile([]mockengine.MockDefinition{{
		Priority: 1,
		Request:  mockengine.RequestMatcher{Method: mockengine.HTTPMethod(http.MethodGet), Path: mockengine.PathMatcher{Type: mockengine.PathExactPath, Value: "/weather"}},
		Response: mockengine.ResponseDefinition{Status: http.StatusOK, Body: []byte("Prague")},
	}})
	server, err := Start(ctx, engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get("http://127.0.0.1:" + fmt.Sprint(server.Port()) + "/weather")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "Prague" {
		t.Fatalf("response = %d %q", response.StatusCode, body)
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}
