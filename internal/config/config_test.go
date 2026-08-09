package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{APIURL: "https://api.mockingo.com", OAuthIssuer: "https://clerk.example", OAuthClientID: "client", OAuthScopes: "openid", UserID: "user_1"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := filepath.Glob(path)
		if err != nil || len(info) != 1 {
			t.Fatalf("config path missing: %v", err)
		}
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 600", stat.Mode().Perm())
		}
	}
}

func TestLegacyFieldsAreIgnoredAndCleanedOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"apiUrl":"https://api.mockingo.com","oauthIssuer":"https://clerk.example","oauthClientId":"client","oauthScopes":"openid","userId":"user_1","token":"do-not-use","legacyApiUrl":"https://gateway.example","legacyToken":"do-not-use"}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.OAuthIssuer != "https://clerk.example" || cfg.APIURL != "https://api.mockingo.com" {
		t.Fatalf("migrated config = %#v, %v", cfg, err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"token", "legacyApiUrl", "legacyToken", "gatewayToken", "legacyExpose", "authMethod"} {
		if _, found := values[removed]; found {
			t.Fatalf("removed field %q survived migration", removed)
		}
	}
}
