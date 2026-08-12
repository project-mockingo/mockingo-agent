package tunnelprotocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	message := Message{Version: Version, Type: TypeRequest, RequestID: "id", Method: "GET", Path: "/"}
	data, err := Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != message.Version || got.Type != message.Type || got.RequestID != message.RequestID || got.Method != message.Method || got.Path != message.Path {
		t.Fatalf("got %#v, want %#v", got, message)
	}
}

func TestDecodeFailures(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		err  error
	}{
		{"malformed", []byte(`{"version":`), ErrInvalidMessage},
		{"trailing", []byte(`{"version":1,"type":"ping"}{}`), ErrInvalidMessage},
		{"unknown type", []byte(`{"version":1,"type":"chunk"}`), ErrUnknownMessageType},
		{"unsupported version", []byte(`{"version":2,"type":"ping"}`), ErrUnsupportedVersion},
		{"missing request id", []byte(`{"version":1,"type":"request","method":"GET","path":"/"}`), ErrInvalidMessage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.data); !errors.Is(err, test.err) {
				t.Fatalf("got %v, want %v", err, test.err)
			}
		})
	}
}

func TestOversizedMessage(t *testing.T) {
	data := bytes.Repeat([]byte{' '}, MaxMessageSize+1)
	if _, err := Decode(data); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("got %v, want ErrMessageTooLarge", err)
	}
}

func TestReadBodyLimit(t *testing.T) {
	if _, err := ReadBody(strings.NewReader(strings.Repeat("x", MaxBodySize))); err != nil {
		t.Fatalf("exact limit failed: %v", err)
	}
	if _, err := ReadBody(strings.NewReader(strings.Repeat("x", MaxBodySize+1))); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("got %v, want ErrBodyTooLarge", err)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"version":1,"type":"ping"}`))
	f.Add([]byte(`{"version":1,"type":"request","requestId":"id","method":"GET","path":"/"}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Decode(data)
	})
}
