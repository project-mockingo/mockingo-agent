package wiremock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDirectoryBodiesHeadersDelayAndOrder(t *testing.T) {
	root := t.TempDir()
	mappings := filepath.Join(root, "mappings")
	files := filepath.Join(root, "__files", "responses")
	if err := os.MkdirAll(mappings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(mappings, "b.json"), `{"name":"second","priority":2,"request":{"method":"ANY","urlPath":"/binary"},"response":{"status":201,"headers":{"X-Multi":["one","two"]},"bodyFileName":"responses/data.bin","fixedDelayMilliseconds":15}}`)
	write(t, filepath.Join(mappings, "a.json"), `{"name":"first","request":{"method":"GET","url":"/weather?city=Prague"},"response":{"status":200,"jsonBody":{"city":"Prague"}}}`)
	if err := os.WriteFile(filepath.Join(files, "data.bin"), []byte{0, 1, 2, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	definitions, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 || definitions[0].Name != "first" || definitions[1].Name != "second" {
		t.Fatalf("definitions loaded in wrong order: %#v", definitions)
	}
	if definitions[0].Response.Headers.Get("Content-Type") != "application/json" || string(definitions[0].Response.Body) != `{"city":"Prague"}` {
		t.Fatalf("json response = %#v", definitions[0].Response)
	}
	if got := definitions[1]; got.Priority != 2 || got.Response.FixedDelay != 15*time.Millisecond || len(got.Response.Headers.Values("X-Multi")) != 2 || len(got.Response.Body) != 4 || got.Response.Body[3] != 255 {
		t.Fatalf("binary response = %#v", got)
	}
}

func TestDirectMappingAndValidationFailures(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "mapping.json")
	write(t, valid, `{"request":{"method":"POST","urlPathTemplate":"/users/{id}"},"response":{"status":204,"body":""}}`)
	definitions, err := Load(valid)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("direct mapping = %#v, %v", definitions, err)
	}
	tests := map[string]string{
		"malformed.json":    `{"request":`,
		"status.json":       `{"request":{"method":"GET","urlPath":"/"},"response":{"status":700}}`,
		"bodyPatterns.json": `{"request":{"method":"POST","urlPath":"/","bodyPatterns":[{"equalTo":"x"}]},"response":{"status":200}}`,
		"scenario.json":     `{"scenarioName":"checkout","request":{"method":"GET","urlPath":"/"},"response":{"status":200}}`,
		"transformer.json":  `{"request":{"method":"GET","urlPath":"/"},"response":{"status":200,"transformers":["response-template"]}}`,
		"proxy.json":        `{"request":{"method":"GET","urlPath":"/"},"response":{"status":200,"proxyBaseUrl":"https://example.com"}}`,
		"traversal.json":    `{"request":{"method":"GET","urlPath":"/"},"response":{"status":200,"bodyFileName":"../../secret"}}`,
	}
	for name, body := range tests {
		path := filepath.Join(root, name)
		write(t, path, body)
		if _, err := Load(path); err == nil {
			t.Errorf("%s unexpectedly loaded", name)
		}
	}
}

func TestBodyFileMissingAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "__files"), 0o755); err != nil {
		t.Fatal(err)
	}
	mapping := `{"request":{"method":"GET","urlPath":"/"},"response":{"status":200,"bodyFileName":"escape"}}`
	write(t, filepath.Join(root, "mappings", "missing.json"), mapping)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "bodyFileName") {
		t.Fatalf("missing body file error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "secret")
	link := filepath.Join(root, "__files", "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
