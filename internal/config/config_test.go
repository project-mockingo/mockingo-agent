package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{APIURL: "http://localhost:9090", Token: "development-token"}
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
