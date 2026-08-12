package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/project-mockingo/mockingo-agent/internal/oauth"
)

type memoryStore struct {
	mu      sync.Mutex
	value   oauth.OAuthCredentials
	missing bool
}

func (s *memoryStore) Get(string) (oauth.OAuthCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.missing {
		return oauth.OAuthCredentials{}, oauth.ErrCredentialsNotFound
	}
	return s.value, nil
}
func (s *memoryStore) Set(_ string, value oauth.OAuthCredentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value, s.missing = value, false
	return nil
}
func (s *memoryStore) Delete(string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.missing = true
	return nil
}

func TestRefreshRotationAndAuthenticatedMe(t *testing.T) {
	var refreshes atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			refreshes.Add(1)
			if r.FormValue("refresh_token") != "old-refresh" || r.FormValue("client_id") != "client" {
				t.Errorf("bad refresh request")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "token_type": "Bearer", "scope": "openid", "expires_in": 3600})
		case "/api/v1/me":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(Me{UserID: "user_1", AuthenticationMethod: "clerk_oauth"})
		}
	}))
	defer server.Close()
	store := &memoryStore{value: oauth.OAuthCredentials{AccessToken: "old-access", RefreshToken: "old-refresh", TokenType: "Bearer", Scope: []string{"openid"}, ExpiresAt: time.Now().Add(-time.Minute)}}
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: server.URL, ClientID: "client", Metadata: oauth.Metadata{TokenEndpoint: server.URL + "/token"}, Store: store}
	me, err := client.Me(context.Background())
	if err != nil || me.UserID != "user_1" {
		t.Fatalf("Me() = %#v, %v", me, err)
	}
	if store.value.RefreshToken != "new-refresh" || refreshes.Load() != 1 {
		t.Fatalf("credentials = %#v, refreshes = %d", store.value, refreshes.Load())
	}
}

func TestRefreshSingleFlight(t *testing.T) {
	var refreshes atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			time.Sleep(25 * time.Millisecond)
			json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "rotated", "token_type": "Bearer", "scope": "openid", "expires_in": 3600})
			return
		}
		json.NewEncoder(w).Encode(Me{UserID: "user_1", AuthenticationMethod: "clerk_oauth"})
	}))
	defer server.Close()
	store := &memoryStore{value: oauth.OAuthCredentials{AccessToken: "old", RefreshToken: "refresh", TokenType: "Bearer", Scope: []string{"openid"}, ExpiresAt: time.Now().Add(-time.Hour)}}
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: server.URL, ClientID: "single-flight", Metadata: oauth.Metadata{TokenEndpoint: server.URL + "/token"}, Store: store}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Me(context.Background()); err != nil {
				t.Errorf("Me: %v", err)
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
}

func TestInvalidRefreshDeletesCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer server.Close()
	store := &memoryStore{value: oauth.OAuthCredentials{AccessToken: "old", RefreshToken: "bad", TokenType: "Bearer", ExpiresAt: time.Now().Add(-time.Hour)}}
	client := &Client{HTTP: server.Client(), APIURL: server.URL, Issuer: server.URL, ClientID: "client-invalid", Metadata: oauth.Metadata{TokenEndpoint: server.URL + "/token"}, Store: store}
	_, err := client.Me(context.Background())
	if !errors.Is(err, ErrSignedOut) || !store.missing {
		t.Fatalf("error = %v, missing = %v", err, store.missing)
	}
}

func TestVerifyLoginRejectsBackendResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Me{UserID: "user_1", AuthenticationMethod: "session_cookie"})
	}))
	defer server.Close()
	if _, err := VerifyLogin(context.Background(), server.Client(), server.URL, "access"); err == nil {
		t.Fatal("expected backend authentication-method rejection")
	}
}
