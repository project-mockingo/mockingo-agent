package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mockingo/mockingo-agent/internal/atomicfile"
)

// Config contains non-secret OAuth metadata. OAuth access and refresh tokens
// are stored separately in the operating-system credential store.
type Config struct {
	APIURL        string     `json:"apiUrl,omitempty"`
	OAuthIssuer   string     `json:"oauthIssuer,omitempty"`
	OAuthClientID string     `json:"oauthClientId,omitempty"`
	OAuthScopes   string     `json:"oauthScopes,omitempty"`
	UserID        string     `json:"userId,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

var ErrNotConfigured = errors.New("configuration not found; run 'mockingo login' first")

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	return filepath.Join(dir, "mockingo", "config.json"), nil
}

func validHTTPURL(raw string) bool {
	parsed, err := url.ParseRequestURI(raw)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func (c Config) Validate() error {
	if c.APIURL != "" && !validHTTPURL(c.APIURL) {
		return errors.New("API URL must be an http or https URL without credentials, query, or fragment")
	}
	if c.OAuthIssuer != "" && !validHTTPURL(c.OAuthIssuer) {
		return errors.New("OAuth issuer must be an http or https URL without credentials, query, or fragment")
	}
	if c.OAuthIssuer != "" && strings.TrimSpace(c.OAuthClientID) == "" {
		return errors.New("OAuth client ID is required with an OAuth issuer")
	}
	if c.OAuthClientID != "" && strings.TrimSpace(c.OAuthIssuer) == "" {
		return errors.New("OAuth issuer is required with an OAuth client ID")
	}
	return nil
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary configuration: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary configuration: %w", err)
	}
	if err := atomicfile.Replace(temporaryPath, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure configuration file: %w", err)
	}
	return nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, ErrNotConfigured
		}
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func LoadOptional(path string) (Config, error) {
	cfg, err := Load(path)
	if errors.Is(err, ErrNotConfigured) {
		return Config{}, nil
	}
	return cfg, err
}

func (c *Config) SetOAuth(apiURL, issuer, clientID string, scopes []string, userID string, expiresAt time.Time) {
	c.APIURL = strings.TrimRight(apiURL, "/")
	c.OAuthIssuer = strings.TrimRight(issuer, "/")
	c.OAuthClientID = clientID
	c.OAuthScopes = strings.Join(scopes, " ")
	c.UserID = userID
	expires := expiresAt.UTC()
	c.ExpiresAt = &expires
}

func (c *Config) ClearOAuth() {
	c.OAuthIssuer = ""
	c.OAuthClientID = ""
	c.OAuthScopes = ""
	c.UserID = ""
	c.ExpiresAt = nil
}

func (c Config) Empty() bool {
	return c.APIURL == "" && c.OAuthIssuer == "" && c.OAuthClientID == "" && c.OAuthScopes == "" && c.UserID == "" && c.ExpiresAt == nil
}
