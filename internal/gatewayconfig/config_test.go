package gatewayconfig

import "testing"

func TestProductionValidation(t *testing.T) {
	t.Parallel()
	valid := Config{
		Environment: "production", Address: ":9090", BaseDomain: "mockingo.click", PublicScheme: "https",
		APIPublicURL: "https://api.mockingo.click", APIToken: "0123456789abcdef0123456789abcdef",
		DatabaseURL: "postgres://example", LogLevel: "info",
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.APIToken = "development-token" },
		func(c *Config) { c.APIToken = "short" },
		func(c *Config) { c.DatabaseURL = "" },
		func(c *Config) { c.PublicScheme = "http" },
		func(c *Config) { c.APIPublicURL = "https://user:pass@api.mockingo.click" },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid production config unexpectedly accepted: %#v", candidate)
		}
	}
}

func TestDevelopmentValidation(t *testing.T) {
	t.Parallel()
	value := Config{Environment: "development", Address: ":9090", BaseDomain: "localhost", PublicScheme: "http", APIPublicURL: "http://localhost:9090", APIToken: "development-token", LogLevel: "debug"}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
}
