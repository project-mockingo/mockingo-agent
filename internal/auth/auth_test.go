package auth

import "testing"

func TestBearerMatches(t *testing.T) {
	t.Parallel()
	if !BearerMatches("Bearer token", "token") {
		t.Fatal("valid token rejected")
	}
	for _, header := range []string{"", "token", "Bearer wrong", "bearer token"} {
		if BearerMatches(header, "token") {
			t.Fatalf("invalid header accepted: %q", header)
		}
	}
}
