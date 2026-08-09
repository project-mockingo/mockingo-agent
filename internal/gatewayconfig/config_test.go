package gatewayconfig

import (
	"testing"
	"time"
)

func TestProductionValidation(t *testing.T) {
	t.Parallel()
	valid := Config{
		Environment: "production", Address: ":9090", BaseDomain: "mockingo.click", GatewayHost: "gateway.mockingo.com", PublicScheme: "https",
		APIPublicURL: "https://gateway.mockingo.com", APIToken: "0123456789abcdef0123456789abcdef",
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
		func(c *Config) { c.APIPublicURL = "https://user:pass@gateway.mockingo.com" },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid production config unexpectedly accepted: %#v", candidate)
		}
	}
}

func TestTicketConfigurationValidation(t *testing.T) {
	valid := Config{
		Environment: "production", Address: ":9090", BaseDomain: "mockingo.click", GatewayHost: "gateway.mockingo.com", PublicScheme: "https",
		APIPublicURL: "https://gateway.mockingo.com", APIToken: "0123456789abcdef0123456789abcdef", DatabaseURL: "postgres://example", LogLevel: "info",
		TicketAuthEnabled: true, LegacyTunnelAuthEnabled: true,
		BackendURL: "https://api.mockingo.com", BackendJWKSURL: "https://api.mockingo.com/.well-known/mockingo-tunnel-jwks.json",
		TicketIssuer: "https://api.mockingo.com", TicketAudience: "mockingo-gateway", TunnelProtocolVersion: 1,
		BackendCallbackToken: "callback", GatewayInternalToken: "internal", GatewayInstanceID: "gateway-1",
		JWKSRefreshInterval: time.Minute, JWKSHTTPTimeout: time.Second, BackendCallbackTimeout: time.Second,
		BackendCallbackAttempts: 3, BackendCallbackBackoff: time.Millisecond, TicketClockSkew: time.Second,
		ReplayCacheMaxEntries: 100, InternalStatusMaxBatch: 10,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.BackendURL = "http://api.mockingo.com" },
		func(c *Config) { c.BackendURL = "https://api.mockingo.click" },
		func(c *Config) { c.BackendJWKSURL = "https://user:pass@api.mockingo.com/jwks" },
		func(c *Config) {
			c.GatewayHost = "gateway.mockingo.click"
			c.APIPublicURL = "https://gateway.mockingo.click"
		},
		func(c *Config) { c.TicketIssuer = "" },
		func(c *Config) { c.TicketAudience = "" },
		func(c *Config) { c.BackendCallbackToken = "" },
		func(c *Config) { c.GatewayInternalToken = "" },
		func(c *Config) { c.TunnelProtocolVersion = 2 },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid ticket config accepted: %#v", candidate)
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
