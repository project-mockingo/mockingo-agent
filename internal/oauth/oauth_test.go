package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPKCEAndStateGeneration(t *testing.T) {
	verifier, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) < 43 || len(verifier) > 128 || strings.ContainsAny(verifier, "=+/ ") {
		t.Fatalf("invalid verifier shape: length %d", len(verifier))
	}
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("RFC 7636 challenge = %q", got)
	}
	stateA, _ := GenerateState()
	stateB, _ := GenerateState()
	if len(stateA) < 32 || stateA == stateB {
		t.Fatal("states are missing entropy")
	}
}

func TestAuthorizationURL(t *testing.T) {
	raw, err := AuthorizationURL(AuthorizationRequest{Endpoint: "https://issuer.example/oauth/authorize?audience=cli", ClientID: "client id", RedirectURI: "http://127.0.0.1:53682/oauth/callback", Scopes: []string{"openid", "email"}, State: "state", Challenge: "challenge", Nonce: "nonce"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(raw)
	query := parsed.Query()
	for key, want := range map[string]string{"audience": "cli", "client_id": "client id", "response_type": "code", "redirect_uri": "http://127.0.0.1:53682/oauth/callback", "scope": "openid email", "state": "state", "code_challenge": "challenge", "code_challenge_method": "S256", "nonce": "nonce"} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func validMetadata(issuer string) Metadata {
	return Metadata{Issuer: issuer, AuthorizationEndpoint: issuer + "/authorize", TokenEndpoint: issuer + "/token", GrantTypesSupported: []string{"authorization_code", "refresh_token"}, CodeChallengeMethods: []string{"S256"}, TokenAuthMethods: []string{"none"}}
}

func TestMetadataValidation(t *testing.T) {
	issuer := "https://clerk.example"
	base := validMetadata(issuer)
	if err := ValidateMetadata(base, issuer); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Metadata)
	}{
		{"issuer mismatch", func(m *Metadata) { m.Issuer = "https://other.example" }},
		{"no code grant", func(m *Metadata) { m.GrantTypesSupported = []string{"refresh_token"} }},
		{"no refresh grant", func(m *Metadata) { m.GrantTypesSupported = []string{"authorization_code"} }},
		{"no S256", func(m *Metadata) { m.CodeChallengeMethods = []string{"plain"} }},
		{"confidential only", func(m *Metadata) { m.TokenAuthMethods = []string{"client_secret_basic"} }},
		{"insecure endpoint", func(m *Metadata) { m.TokenEndpoint = "http://remote.example/token" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if err := ValidateMetadata(candidate, issuer); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRedirectURI(t *testing.T) {
	got, err := RedirectURI("127.0.0.1", 53682, "/oauth/callback")
	if err != nil || got != "http://127.0.0.1:53682/oauth/callback" {
		t.Fatalf("RedirectURI() = %q, %v", got, err)
	}
	for _, host := range []string{"localhost", "0.0.0.0", "192.168.1.5"} {
		if _, err := RedirectURI(host, 53682, "/oauth/callback"); err == nil {
			t.Fatalf("accepted host %q", host)
		}
	}
}

func TestCallbackValidationAndDenial(t *testing.T) {
	tests := []struct {
		name, query, wantError string
		wantOAuth              string
	}{
		{"success", "?code=abc&state=expected", "", ""},
		{"mismatch", "?code=abc&state=wrong", "state", ""},
		{"missing code", "?state=expected", "authorization code", ""},
		{"denied", "?error=access_denied&error_description=no&state=expected", "", "access_denied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			callback, redirect, err := StartCallback("127.0.0.1", 0, "/oauth/callback", "expected")
			if err != nil {
				t.Fatal(err)
			}
			defer callback.Close()
			response, err := http.Get(redirect + test.query)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if strings.Contains(string(body), "abc") {
				t.Fatal("authorization code leaked into callback HTML")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := callback.Wait(ctx)
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v", err)
			}
			if result.OAuthError != test.wantOAuth {
				t.Fatalf("OAuth error = %q", result.OAuthError)
			}
		})
	}
}

func TestOccupiedCallbackPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, _, err := StartCallback("127.0.0.1", port, "/oauth/callback", "state"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestCallbackTimeout(t *testing.T) {
	callback, _, err := StartCallback("127.0.0.1", 0, "/oauth/callback", "state")
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := callback.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestTokenResponseParsing(t *testing.T) {
	now := time.Unix(1000, 0)
	response, err := ParseTokenResponse(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","scope":"openid offline_access","expires_in":3600}`), now, []string{"openid", "offline_access"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if response.AccessToken != "access" || response.RefreshToken != "refresh" || !response.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected response: %#v", response)
	}
	if _, err := ParseTokenResponse(strings.NewReader(`{"access_token":"access","token_type":"Bearer","scope":"offline_access","expires_in":3600}`), now, []string{"offline_access"}, true); err == nil || !strings.Contains(err.Error(), "refresh_token") {
		t.Fatalf("missing refresh error = %v", err)
	}
}

func TestTokenExchangeFailureIsNotRetried(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "code already used"})
	}))
	defer server.Close()
	_, err := ExchangeCode(context.Background(), server.Client(), Metadata{TokenEndpoint: server.URL}, "client", "code", "verifier", "http://127.0.0.1/callback", []string{"openid"})
	if err == nil || requests != 1 || !IsInvalidGrant(err) {
		t.Fatalf("error = %v, requests = %d", err, requests)
	}
}

func TestFileCredentialStorePermissionsAndDelete(t *testing.T) {
	store := FileStore{Directory: t.TempDir()}
	account := Account("https://issuer.example", "client")
	want := OAuthCredentials{AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer", Scope: []string{"openid"}, ExpiresAt: time.Now().Add(time.Hour), UserID: "user_1"}
	if err := store.Set(account, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(account)
	if err != nil || got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		entries, _ := os.ReadDir(store.Directory)
		info, _ := entries[0].Info()
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
	}
	if err := store.Delete(account); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(account); !errors.Is(err, ErrCredentialsNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
}

type fakeStore struct {
	mu    sync.Mutex
	value OAuthCredentials
	err   error
}

func (s *fakeStore) Get(string) (OAuthCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.err
}
func (s *fakeStore) Set(_ string, c OAuthCredentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil && !errors.Is(s.err, ErrCredentialsNotFound) {
		return s.err
	}
	s.value, s.err = c, nil
	return nil
}
func (s *fakeStore) Delete(string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = ErrCredentialsNotFound
	return nil
}

func TestFallbackRequiresOptIn(t *testing.T) {
	primary := &fakeStore{err: errors.New("keyring unavailable")}
	file := &fakeStore{err: ErrCredentialsNotFound}
	store := FallbackStore{Primary: primary, File: file}
	err := store.Set("account", OAuthCredentials{AccessToken: "a"})
	if err == nil || !strings.Contains(err.Error(), "opt in") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscovery(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := validMetadata(server.URL)
		json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	metadata, err := Discover(context.Background(), server.Client(), server.URL)
	if err != nil || metadata.TokenEndpoint != server.URL+"/token" {
		t.Fatalf("Discover() = %#v, %v", metadata, err)
	}
}
