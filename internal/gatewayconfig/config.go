package gatewayconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Environment       string
	Address           string
	BaseDomain        string
	PublicScheme      string
	APIPublicURL      string
	APIToken          string
	DatabaseURL       string
	TrustedProxyCIDRs string
	LogLevel          string
	MetricsEnabled    bool
}

func Load() (Config, error) {
	environment := strings.ToLower(env("MOCKINGO_ENV", "production"))
	baseDomain := strings.ToLower(env("MOCKINGO_BASE_DOMAIN", "mockingo.click"))
	publicScheme := strings.ToLower(env("MOCKINGO_PUBLIC_SCHEME", "https"))
	apiPublicURL := os.Getenv("MOCKINGO_API_PUBLIC_URL")
	if apiPublicURL == "" {
		if environment == "development" && baseDomain == "localhost" {
			apiPublicURL = publicScheme + "://localhost:9090"
		} else {
			apiPublicURL = publicScheme + "://api." + baseDomain
		}
	}
	value := Config{
		Environment: environment, Address: env("MOCKINGO_GATEWAY_ADDR", ":9090"),
		BaseDomain: baseDomain, PublicScheme: publicScheme,
		APIPublicURL: apiPublicURL, DatabaseURL: os.Getenv("DATABASE_URL"),
		TrustedProxyCIDRs: os.Getenv("MOCKINGO_TRUSTED_PROXY_CIDRS"), LogLevel: strings.ToLower(env("MOCKINGO_LOG_LEVEL", "info")),
	}
	allowDev, err := strconv.ParseBool(env("MOCKINGO_ALLOW_DEV_TOKEN", "false"))
	if err != nil {
		return Config{}, errors.New("MOCKINGO_ALLOW_DEV_TOKEN must be true or false")
	}
	value.MetricsEnabled, err = strconv.ParseBool(env("MOCKINGO_METRICS_ENABLED", "false"))
	if err != nil {
		return Config{}, errors.New("MOCKINGO_METRICS_ENABLED must be true or false")
	}
	value.APIToken = os.Getenv("MOCKINGO_API_TOKEN")
	if value.Environment == "development" || allowDev {
		if value.APIToken == "" {
			value.APIToken = os.Getenv("MOCKINGO_DEV_TOKEN")
		}
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "production" {
		return errors.New("MOCKINGO_ENV must be development or production")
	}
	if c.PublicScheme != "http" && c.PublicScheme != "https" {
		return errors.New("MOCKINGO_PUBLIC_SCHEME must be http or https")
	}
	if c.BaseDomain == "" || strings.ContainsAny(c.BaseDomain, "/:@ ") || strings.Contains(c.BaseDomain, "..") {
		return errors.New("MOCKINGO_BASE_DOMAIN is invalid")
	}
	parsed, err := url.Parse(c.APIPublicURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("MOCKINGO_API_PUBLIC_URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if c.APIToken == "" {
		return errors.New("MOCKINGO_API_TOKEN is required")
	}
	if c.Environment == "production" {
		lower := strings.ToLower(c.APIToken)
		if len(c.APIToken) < 32 || strings.Contains(lower, "replace") || strings.Contains(lower, "example") || c.APIToken == "development-token" {
			return errors.New("MOCKINGO_API_TOKEN must be a strong, non-example secret of at least 32 characters in production")
		}
		if c.DatabaseURL == "" {
			return errors.New("DATABASE_URL is required in production")
		}
		if c.PublicScheme != "https" || parsed.Scheme != "https" {
			return errors.New("production public URLs must use HTTPS")
		}
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("unsupported MOCKINGO_LOG_LEVEL %q", c.LogLevel)
	}
	return nil
}
