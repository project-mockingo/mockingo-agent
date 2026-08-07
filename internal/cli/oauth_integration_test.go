package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mockingo/mockingo-cli/internal/config"
	"github.com/mockingo/mockingo-cli/internal/oauth"
)

type cliMemoryStore struct {
	mu     sync.Mutex
	values map[string]oauth.OAuthCredentials
}

func newCLIStore() *cliMemoryStore {
	return &cliMemoryStore{values: make(map[string]oauth.OAuthCredentials)}
}
func (s *cliMemoryStore) Get(account string) (oauth.OAuthCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[account]
	if !ok {
		return oauth.OAuthCredentials{}, oauth.ErrCredentialsNotFound
	}
	return value, nil
}
func (s *cliMemoryStore) Set(account string, value oauth.OAuthCredentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[account] = value
	return nil
}
func (s *cliMemoryStore) Delete(account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, account)
	return nil
}

type oauthFixture struct {
	issuer, api *httptest.Server
	client      *http.Client
}

func newOAuthFixture(t *testing.T, deny bool) oauthFixture {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me" || r.Header.Get("Authorization") != "Bearer access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"userId": "user_123", "authenticationMethod": "clerk_oauth"})
	}))
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			json.NewEncoder(w).Encode(oauth.Metadata{Issuer: issuer.URL, AuthorizationEndpoint: issuer.URL + "/authorize", TokenEndpoint: issuer.URL + "/token", GrantTypesSupported: []string{"authorization_code", "refresh_token"}, CodeChallengeMethods: []string{"S256"}, TokenAuthMethods: []string{"none"}})
		case "/authorize":
			redirect, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
			query := redirect.Query()
			query.Set("state", r.URL.Query().Get("state"))
			if deny {
				query.Set("error", "access_denied")
				query.Set("error_description", "user denied consent")
			} else {
				query.Set("code", "authorization-code")
			}
			redirect.RawQuery = query.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code_verifier") == "" || r.FormValue("client_id") != "client_123" || r.FormValue("code") != "authorization-code" {
				t.Errorf("bad token request: %#v", r.Form)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "refresh_token": "refresh-token", "token_type": "Bearer", "scope": "openid profile email offline_access", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	t.Cleanup(issuer.Close)
	return oauthFixture{issuer: issuer, api: api, client: issuer.Client()}
}

func TestOAuthLoginWhoamiJSONAndLogout(t *testing.T) {
	fixture := newOAuthFixture(t, false)
	store := newCLIStore()
	path := filepath.Join(t.TempDir(), "config.json")
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, Stdin: bytes.NewReader(nil), ConfigPath: path, HTTPClient: fixture.client, Credentials: store}
	app.OpenBrowser = func(target string) error {
		response, err := fixture.client.Get(target)
		if err == nil {
			response.Body.Close()
		}
		return err
	}
	args := []string{"login", "--api-url", fixture.api.URL, "--issuer", fixture.issuer.URL, "--client-id", "client_123", "--callback-port", "0"}
	if code := app.Run(context.Background(), args); code != 0 {
		t.Fatalf("login code = %d\n%s", code, output.String())
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.UserID != "user_123" || cfg.Token != "" || cfg.LegacyToken != "" {
		t.Fatalf("config = %#v, %v", cfg, err)
	}
	output.Reset()
	if code := app.Run(context.Background(), []string{"whoami", "--json"}); code != 0 {
		t.Fatalf("whoami code = %d: %s", code, output.String())
	}
	if strings.Contains(output.String(), "access-token") || strings.Contains(output.String(), "refresh-token") {
		t.Fatal("whoami JSON exposed a token")
	}
	var identity map[string]string
	if err := json.Unmarshal(output.Bytes(), &identity); err != nil || identity["userId"] != "user_123" {
		t.Fatalf("identity = %#v, %v, output %s", identity, err, output.String())
	}
	output.Reset()
	if code := app.Run(context.Background(), []string{"logout"}); code != 0 {
		t.Fatalf("logout code = %d: %s", code, output.String())
	}
	if code := app.Run(context.Background(), []string{"logout"}); code != 0 {
		t.Fatalf("idempotent logout code = %d: %s", code, output.String())
	}
}

func TestOAuthConsentDenied(t *testing.T) {
	fixture := newOAuthFixture(t, true)
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: filepath.Join(t.TempDir(), "config.json"), HTTPClient: fixture.client, Credentials: newCLIStore()}
	app.OpenBrowser = func(target string) error {
		response, err := fixture.client.Get(target)
		if response != nil {
			response.Body.Close()
		}
		return err
	}
	code := app.Run(context.Background(), []string{"login", "--api-url", fixture.api.URL, "--issuer", fixture.issuer.URL, "--client-id", "client_123", "--callback-port", "0"})
	if code == 0 || !strings.Contains(output.String(), "authorization was denied") {
		t.Fatalf("code = %d, output = %s", code, output.String())
	}
}

func TestBrowserFailurePrintsFallbackAndCanComplete(t *testing.T) {
	fixture := newOAuthFixture(t, false)
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: filepath.Join(t.TempDir(), "config.json"), HTTPClient: fixture.client, Credentials: newCLIStore()}
	app.OpenBrowser = func(target string) error {
		response, requestErr := fixture.client.Get(target)
		if response != nil {
			response.Body.Close()
		}
		if requestErr != nil {
			return requestErr
		}
		return errors.New("browser unavailable")
	}
	code := app.Run(context.Background(), []string{"login", "--api-url", fixture.api.URL, "--issuer", fixture.issuer.URL, "--client-id", "client_123", "--callback-port", "0"})
	if code != 0 || !strings.Contains(output.String(), "browser could not be opened") || !strings.Contains(output.String(), fixture.issuer.URL+"/authorize") {
		t.Fatalf("code = %d, output = %s", code, output.String())
	}
}

func TestLegacyCredentialSeparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	expires := time.Now().Add(time.Hour)
	existing := config.Config{APIURL: "https://api.mockingo.com", OAuthIssuer: "https://clerk.example", OAuthClientID: "client", OAuthScopes: "openid", UserID: "user_1", ExpiresAt: &expires}
	if err := config.Save(path, existing); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := &App{Stdout: &output, Stderr: &output, ConfigPath: path}
	if code := app.Run(context.Background(), []string{"login", "--api-url", "https://gateway.example", "--token", "legacy-secret"}); code != 0 {
		t.Fatalf("code = %d: %s", code, output.String())
	}
	cfg, _ := config.Load(path)
	legacyURL, token, ok := cfg.Legacy()
	if !ok || legacyURL != "https://gateway.example" || token != "legacy-secret" || cfg.OAuthIssuer != existing.OAuthIssuer || cfg.APIURL != existing.APIURL {
		t.Fatalf("credentials were not separated: %#v", cfg)
	}
}
