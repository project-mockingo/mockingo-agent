package tunnelprotocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var goldenMessages = map[string]Message{
	"http_request.json": {
		Version: Version, Type: TypeRequest, RequestID: "0123456789abcdef",
		Method: "POST", Path: "/widgets?q=1",
		Headers:    map[string][]string{"Content-Type": {"application/json"}, "X-Test": {"one", "two"}},
		BodyBase64: "eyJuYW1lIjoibW9ja2luZ28ifQ==",
	},
	"http_response.json": {
		Version: Version, Type: TypeResponse, RequestID: "0123456789abcdef",
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		BodyBase64: "eyJvayI6dHJ1ZX0=", Status: 201,
	},
	"ping.json": {Version: Version, Type: TypePing},
	"pong.json": {Version: Version, Type: TypePong},
	"protocol_error.json": {
		Version: Version, Type: TypeError, RequestID: "0123456789abcdef",
		ErrorCode: ErrorCodeLocalUnreachable, Error: "local forwarding failed",
	},
}

func TestGoldenSerialization(t *testing.T) {
	t.Parallel()
	for name, message := range goldenMessages {
		name, message := name, message
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.TrimSuffix(want, []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Fatalf("wire bytes changed\n got: %s\nwant: %s", got, want)
			}
			decoded, err := Decode(want)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(roundTrip, want) {
				t.Fatalf("round-trip bytes changed\n got: %s\nwant: %s", roundTrip, want)
			}
		})
	}
}

func TestUpdateGolden(t *testing.T) {
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		t.Skip("set UPDATE_GOLDEN=1 explicitly to update fixtures")
	}
	for name, message := range goldenMessages {
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		if err := os.WriteFile(filepath.Join("testdata", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
