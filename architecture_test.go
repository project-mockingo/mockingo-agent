package architecture_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIDoesNotContainGatewayServer(t *testing.T) {
	for _, path := range []string{"cmd/mockingo-gateway", "internal/gateway", "internal/gatewayconfig", "internal/endpoint", "internal/database"} {
		if _, err := os.Stat(filepath.FromSlash(path)); !os.IsNotExist(err) {
			t.Errorf("gateway-owned path returned: %s", path)
		}
	}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".toolcache" || entry.Name() == ".stage3f") {
			return filepath.SkipDir
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, route := range []string{"/internal/v1/tunnels/status", "/health/live", "/health/ready"} {
			if strings.Contains(string(data), route) {
				t.Errorf("%s contains gateway server route %q", path, route)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
