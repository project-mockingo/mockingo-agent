package gatewayconfig

import (
	"testing"
	"time"
)

func validConfig(environment string) Config {
	return Config{
		Environment: environment, Address: ":9090", BaseDomain: "mockingo.click", GatewayHost: "gateway.mockingo.com", PublicScheme: "https",
		DatabaseURL: "postgres://example", LogLevel: "info", BackendURL: "https://api.mockingo.com",
		BackendJWKSURL: "https://api.mockingo.com/.well-known/mockingo-tunnel-jwks.json", TicketIssuer: "https://api.mockingo.com",
		TicketAudience: "mockingo-gateway", TunnelProtocolVersion: 1, BackendCallbackToken: "callback", GatewayInternalToken: "internal",
		GatewayInstanceID: "gateway-1", JWKSRefreshInterval: time.Minute, JWKSHTTPTimeout: time.Second,
		BackendCallbackTimeout: time.Second, BackendCallbackAttempts: 3, BackendCallbackBackoff: time.Millisecond,
		TicketClockSkew: time.Second, ReplayCacheMaxEntries: 100, InternalStatusMaxBatch: 10,
	}
}

func TestProductionValidation(t *testing.T) {
	valid := validConfig("production")
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.DatabaseURL = "" },
		func(c *Config) { c.PublicScheme = "http" },
		func(c *Config) { c.BackendURL = "http://api.mockingo.com" },
		func(c *Config) { c.GatewayHost = "gateway.mockingo.click" },
		func(c *Config) { c.TicketIssuer = "" },
		func(c *Config) { c.BackendCallbackToken = "" },
		func(c *Config) { c.GatewayInternalToken = "" },
		func(c *Config) { c.TunnelProtocolVersion = 2 },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Errorf("invalid production config accepted: %#v", candidate)
		}
	}
}

func TestDevelopmentStillRequiresTicketServices(t *testing.T) {
	value := validConfig("development")
	value.PublicScheme = "http"
	value.BaseDomain = "localhost"
	value.GatewayHost = "localhost"
	value.BackendURL = "http://localhost:8080"
	value.BackendJWKSURL = "http://localhost:8080/.well-known/mockingo-tunnel-jwks.json"
	value.TicketIssuer = "http://localhost:8080"
	value.DatabaseURL = ""
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	value.BackendCallbackToken = ""
	if err := value.Validate(); err == nil {
		t.Fatal("missing callback token was accepted")
	}
}

func TestLoadStartsWithoutRemovedEnvironmentVariables(t *testing.T) {
	values := map[string]string{
		"MOCKINGO_ENV": "development", "MOCKINGO_BASE_DOMAIN": "localhost", "MOCKINGO_GATEWAY_HOST": "localhost", "MOCKINGO_PUBLIC_SCHEME": "http",
		"MOCKINGO_BACKEND_URL": "http://localhost:8080", "MOCKINGO_BACKEND_JWKS_URL": "http://localhost:8080/jwks",
		"MOCKINGO_TUNNEL_TICKET_ISSUER": "http://localhost:8080", "MOCKINGO_TUNNEL_TICKET_AUDIENCE": "mockingo-gateway",
		"MOCKINGO_BACKEND_CALLBACK_TOKEN": "test-only-callback", "MOCKINGO_GATEWAY_INTERNAL_TOKEN": "test-only-internal", "MOCKINGO_GATEWAY_INSTANCE_ID": "gateway-test",
		"MOCKINGO_API_TOKEN": "ignored", "MOCKINGO_LEGACY_TUNNEL_AUTH_ENABLED": "not-a-boolean", "MOCKINGO_TICKET_AUTH_ENABLED": "false",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackendURL != "http://localhost:8080" || cfg.TicketAudience != "mockingo-gateway" {
		t.Fatalf("loaded config = %#v", cfg)
	}
}
