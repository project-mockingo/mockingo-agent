package tunnelhttp

import (
	"net/http"
	"testing"
)

func TestFilterHeaders(t *testing.T) {
	headers := http.Header{"Connection": {"X-Remove, keep-alive"}, "X-Remove": {"secret"}, "Keep-Alive": {"timeout=5"}, "X-Keep": {"value"}}
	got := FilterHeaders(headers)
	if got.Get("X-Keep") != "value" || got.Get("X-Remove") != "" || got.Get("Connection") != "" || got.Get("Keep-Alive") != "" {
		t.Fatalf("unexpected filtered headers: %#v", got)
	}
}
