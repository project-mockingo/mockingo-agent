package gateway

import "testing"

func TestParseEndpointHost(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		host, base, name string
	}{
		{"demo.mockingo.click", "mockingo.click", "demo"},
		{"DEMO.MOCKINGO.CLICK:443", "mockingo.click", "demo"},
		{"demo.localhost:9090", "localhost", "demo"},
	} {
		name, _, err := ParseEndpointHost(test.host, test.base)
		if err != nil || name != test.name {
			t.Errorf("ParseEndpointHost(%q) = %q, %v", test.host, name, err)
		}
	}
	invalid := []string{
		"api.mockingo.click", "a.b.mockingo.click", "mockingo.click", "127.0.0.1",
		"demo.example.com", "demo.mockingo.click:bad", "demo.mockingo.click,evil.example",
		"démø.mockingo.click", " demo.mockingo.click", "demo..mockingo.click",
	}
	for _, host := range invalid {
		if _, _, err := ParseEndpointHost(host, "mockingo.click"); err == nil {
			t.Errorf("ParseEndpointHost(%q) unexpectedly succeeded", host)
		}
	}
}
