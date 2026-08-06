package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestForwardedHeadersTrust(t *testing.T) {
	t.Parallel()
	networks, err := ParseTrustedProxyCIDRs("127.0.0.1/32,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	direct := httptest.NewRequest("GET", "http://gateway/", nil)
	direct.Host = "demo.mockingo.click"
	direct.RemoteAddr = "192.0.2.5:1234"
	direct.Header.Set("X-Forwarded-For", "203.0.113.99")
	direct.Header.Set("X-Forwarded-Proto", "https")
	ip, host, scheme := forwardedHeaders(direct, "http", networks)
	if ip != "192.0.2.5" || host != direct.Host || scheme != "http" {
		t.Fatalf("untrusted forwarding = %q %q %q", ip, host, scheme)
	}
	trusted := httptest.NewRequest("GET", "http://gateway/", nil)
	trusted.Host = "demo.mockingo.click"
	trusted.RemoteAddr = "127.0.0.1:1234"
	trusted.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.8")
	trusted.Header.Set("X-Forwarded-Proto", "https")
	ip, _, scheme = forwardedHeaders(trusted, "http", networks)
	if ip != "203.0.113.8" || scheme != "https" {
		t.Fatalf("trusted forwarding = %q %q", ip, scheme)
	}
}
