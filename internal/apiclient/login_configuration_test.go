package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDiscoverLoginConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != loginConfigurationPath || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s, Accept %q", r.URL.Path, r.Header.Get("Accept"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   "https://clerk.example.com/",
			"clientId": " client_production ",
			"scopes":   []string{"openid profile", "openid", "offline_access"},
			"version":  1,
		})
	}))
	defer server.Close()

	configuration, err := DiscoverLoginConfiguration(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Issuer != "https://clerk.example.com" || configuration.ClientID != "client_production" {
		t.Fatalf("configuration = %#v", configuration)
	}
	if !reflect.DeepEqual(configuration.Scopes, []string{"openid", "profile", "offline_access"}) {
		t.Fatalf("scopes = %#v", configuration.Scopes)
	}
}

func TestDiscoverLoginConfigurationRejectsRedirectAndIncompleteResponse(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(LoginConfiguration{Issuer: "https://issuer.example", ClientID: "client", Scopes: []string{"openid"}})
	}))
	defer redirectTarget.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := DiscoverLoginConfiguration(context.Background(), redirect.Client(), redirect.URL); err == nil {
		t.Fatal("expected redirect rejection")
	}

	incomplete := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(LoginConfiguration{Issuer: "https://issuer.example"})
	}))
	defer incomplete.Close()
	if _, err := DiscoverLoginConfiguration(context.Background(), incomplete.Client(), incomplete.URL); err == nil {
		t.Fatal("expected incomplete response rejection")
	}
}
