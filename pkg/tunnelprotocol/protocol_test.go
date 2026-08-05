package tunnelprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestMessageJSONRoundTrip(t *testing.T) {
	t.Parallel()
	want := Message{Version: 1, Type: TypeRequest, RequestID: "id", Method: "POST", Path: "/x?q=1", Headers: map[string][]string{"X-Test": {"yes"}}, BodyBase64: "e30="}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestFilterHeaders(t *testing.T) {
	t.Parallel()
	headers := http.Header{"Connection": {"X-Remove, keep-alive"}, "X-Remove": {"secret"}, "Keep-Alive": {"timeout=5"}, "X-Keep": {"value"}}
	got := FilterHeaders(headers)
	if got.Get("X-Keep") != "value" || got.Get("X-Remove") != "" || got.Get("Connection") != "" || got.Get("Keep-Alive") != "" {
		t.Fatalf("unexpected filtered headers: %#v", got)
	}
}

func TestReadBodyLimit(t *testing.T) {
	t.Parallel()
	if _, err := ReadBody(bytes.NewReader(make([]byte, MaxBodySize))); err != nil {
		t.Fatalf("exact limit failed: %v", err)
	}
	if _, err := ReadBody(bytes.NewReader(make([]byte, MaxBodySize+1))); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("got %v, want ErrBodyTooLarge", err)
	}
}
