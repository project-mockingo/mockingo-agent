package gatewayconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment       string
	Address           string
	BaseDomain        string
	GatewayHost       string
	PublicScheme      string
	DatabaseURL       string
	TrustedProxyCIDRs string
	LogLevel          string
	MetricsEnabled    bool

	BackendURL              string
	BackendJWKSURL          string
	TicketIssuer            string
	TicketAudience          string
	TunnelProtocolVersion   int
	BackendCallbackToken    string
	GatewayInternalToken    string
	GatewayInstanceID       string
	JWKSRefreshInterval     time.Duration
	JWKSHTTPTimeout         time.Duration
	BackendCallbackTimeout  time.Duration
	BackendCallbackAttempts int
	BackendCallbackBackoff  time.Duration
	TicketClockSkew         time.Duration
	ReplayCacheMaxEntries   int
	InternalStatusMaxBatch  int
}

func Load() (Config, error) {
	environment := strings.ToLower(env("MOCKINGO_ENV", "production"))
	baseDomain := strings.ToLower(strings.TrimSuffix(env("MOCKINGO_BASE_DOMAIN", "mockingo.click"), "."))
	publicScheme := strings.ToLower(env("MOCKINGO_PUBLIC_SCHEME", "https"))
	gatewayHost := strings.ToLower(strings.TrimSuffix(env("MOCKINGO_GATEWAY_HOST", "gateway.mockingo.com"), "."))
	backendURL := strings.TrimRight(env("MOCKINGO_BACKEND_URL", "https://api.mockingo.com"), "/")
	value := Config{
		Environment: environment, Address: env("MOCKINGO_GATEWAY_ADDR", ":9090"),
		BaseDomain: baseDomain, GatewayHost: gatewayHost, PublicScheme: publicScheme,
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		TrustedProxyCIDRs: os.Getenv("MOCKINGO_TRUSTED_PROXY_CIDRS"), LogLevel: strings.ToLower(env("MOCKINGO_LOG_LEVEL", "info")),
		BackendURL: backendURL, BackendJWKSURL: strings.TrimRight(env("MOCKINGO_BACKEND_JWKS_URL", backendURL+"/.well-known/mockingo-tunnel-jwks.json"), "/"),
		TicketIssuer:         strings.TrimRight(env("MOCKINGO_TUNNEL_TICKET_ISSUER", backendURL), "/"),
		TicketAudience:       env("MOCKINGO_TUNNEL_TICKET_AUDIENCE", "mockingo-gateway"),
		BackendCallbackToken: os.Getenv("MOCKINGO_BACKEND_CALLBACK_TOKEN"),
		GatewayInternalToken: os.Getenv("MOCKINGO_GATEWAY_INTERNAL_TOKEN"),
		GatewayInstanceID:    env("MOCKINGO_GATEWAY_INSTANCE_ID", hostname()),
	}
	var err error
	if value.MetricsEnabled, err = boolEnv("MOCKINGO_METRICS_ENABLED", false); err != nil {
		return Config{}, err
	}
	value.TunnelProtocolVersion, err = intEnv("MOCKINGO_TUNNEL_PROTOCOL_VERSION", 1)
	if err != nil {
		return Config{}, err
	}
	value.BackendCallbackAttempts, err = intEnv("MOCKINGO_BACKEND_CALLBACK_ATTEMPTS", 3)
	if err != nil {
		return Config{}, err
	}
	value.ReplayCacheMaxEntries, err = intEnv("MOCKINGO_REPLAY_CACHE_MAX_ENTRIES", 100000)
	if err != nil {
		return Config{}, err
	}
	value.InternalStatusMaxBatch, err = intEnv("MOCKINGO_INTERNAL_STATUS_MAX_BATCH", 500)
	if err != nil {
		return Config{}, err
	}
	for _, duration := range []struct {
		key      string
		fallback string
		target   *time.Duration
	}{
		{"MOCKINGO_JWKS_REFRESH_INTERVAL", "15m", &value.JWKSRefreshInterval},
		{"MOCKINGO_JWKS_HTTP_TIMEOUT", "5s", &value.JWKSHTTPTimeout},
		{"MOCKINGO_BACKEND_CALLBACK_TIMEOUT", "5s", &value.BackendCallbackTimeout},
		{"MOCKINGO_BACKEND_CALLBACK_BACKOFF", "100ms", &value.BackendCallbackBackoff},
		{"MOCKINGO_TICKET_CLOCK_SKEW", "5s", &value.TicketClockSkew},
	} {
		*duration.target, err = time.ParseDuration(env(duration.key, duration.fallback))
		if err != nil {
			return Config{}, fmt.Errorf("%s must be a valid duration", duration.key)
		}
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := strconv.FormatBool(fallback)
	if configured := os.Getenv(key); configured != "" {
		value = configured
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func intEnv(key string, fallback int) (int, error) {
	value := fallback
	if configured := os.Getenv(key); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		value = parsed
	}
	return value, nil
}

func validateHTTPURL(name, raw string, production bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL without credentials, query, or fragment", name)
	}
	if production && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use HTTPS in production", name)
	}
	return nil
}

func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "production" {
		return errors.New("MOCKINGO_ENV must be development or production")
	}
	production := c.Environment == "production"
	if c.PublicScheme != "http" && c.PublicScheme != "https" {
		return errors.New("MOCKINGO_PUBLIC_SCHEME must be http or https")
	}
	if c.BaseDomain == "" || strings.ContainsAny(c.BaseDomain, "/:@ ") || strings.Contains(c.BaseDomain, "..") {
		return errors.New("MOCKINGO_BASE_DOMAIN is invalid")
	}
	if c.GatewayHost != "" && (strings.ContainsAny(c.GatewayHost, "/:@ ") || strings.Contains(c.GatewayHost, "..")) {
		return errors.New("MOCKINGO_GATEWAY_HOST is invalid")
	}
	if production && c.GatewayHost == "" {
		return errors.New("MOCKINGO_GATEWAY_HOST is required in production")
	}
	if production && (strings.EqualFold(c.GatewayHost, c.BaseDomain) || strings.HasSuffix(strings.ToLower(c.GatewayHost), "."+strings.ToLower(c.BaseDomain))) {
		return errors.New("the gateway service host must not use the public tunnel base domain")
	}
	if production {
		if c.DatabaseURL == "" {
			return errors.New("DATABASE_URL is required in production")
		}
		if c.PublicScheme != "https" {
			return errors.New("production public URLs must use HTTPS")
		}
	}
	if err := validateHTTPURL("MOCKINGO_BACKEND_URL", c.BackendURL, production); err != nil {
		return err
	}
	if err := validateHTTPURL("MOCKINGO_BACKEND_JWKS_URL", c.BackendJWKSURL, production); err != nil {
		return err
	}
	if err := validateHTTPURL("MOCKINGO_TUNNEL_TICKET_ISSUER", c.TicketIssuer, production); err != nil {
		return err
	}
	backendURL, _ := url.Parse(c.BackendURL)
	if production && (strings.EqualFold(backendURL.Hostname(), c.BaseDomain) || strings.HasSuffix(strings.ToLower(backendURL.Hostname()), "."+strings.ToLower(c.BaseDomain))) {
		return errors.New("the backend service URL must not use the public tunnel base domain")
	}
	if c.TicketIssuer == "" || c.TicketAudience == "" {
		return errors.New("tunnel ticket issuer and audience are required")
	}
	if production && (c.BackendCallbackToken == "" || c.GatewayInternalToken == "") {
		return errors.New("backend callback and gateway internal tokens are required in production")
	}
	if c.BackendCallbackToken == "" {
		return errors.New("MOCKINGO_BACKEND_CALLBACK_TOKEN is required")
	}
	if c.GatewayInternalToken == "" {
		return errors.New("MOCKINGO_GATEWAY_INTERNAL_TOKEN is required")
	}
	if c.GatewayInstanceID == "" {
		return errors.New("MOCKINGO_GATEWAY_INSTANCE_ID is required")
	}
	if c.TunnelProtocolVersion != 1 {
		return errors.New("MOCKINGO_TUNNEL_PROTOCOL_VERSION must be 1")
	}
	if c.JWKSRefreshInterval <= 0 || c.JWKSHTTPTimeout <= 0 || c.BackendCallbackTimeout <= 0 || c.BackendCallbackBackoff <= 0 || c.TicketClockSkew <= 0 {
		return errors.New("gateway ticket and callback durations must be positive")
	}
	if c.BackendCallbackAttempts < 1 || c.BackendCallbackAttempts > 10 {
		return errors.New("MOCKINGO_BACKEND_CALLBACK_ATTEMPTS must be between 1 and 10")
	}
	if c.ReplayCacheMaxEntries < 1 || c.InternalStatusMaxBatch < 1 {
		return errors.New("gateway replay and internal batch limits must be positive")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("unsupported MOCKINGO_LOG_LEVEL %q", c.LogLevel)
	}
	return nil
}
