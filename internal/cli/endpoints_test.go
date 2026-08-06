package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mockingo/mockingo-cli/internal/config"
)

func TestEndpointDeleteAcceptsForceAfterName(t *testing.T) {
	t.Parallel()
	deleted := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deleted <- r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{APIURL: server.URL, Token: "token"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{Stdin: bytes.NewBuffer(nil), Stdout: &output, Stderr: &output, ConfigPath: path}
	if code := app.Run(context.Background(), []string{"endpoints", "delete", "spring-demo", "--force"}); code != 0 {
		t.Fatalf("code = %d, output = %s", code, output.String())
	}
	if path := <-deleted; path != "/api/v1/endpoints/spring-demo" {
		t.Fatalf("delete path = %q", path)
	}
}
