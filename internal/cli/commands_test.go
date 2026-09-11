package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAndExposeHelp(t *testing.T) {
	for _, command := range []string{"run", "expose"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := &App{Stdout: &stdout, Stderr: &stderr}
			if code := app.Run(context.Background(), []string{command, "--help"}); code != 0 {
				t.Fatalf("exit code = %d: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Usage: mockingo run --name NAME --http PORT") {
				t.Fatalf("unexpected help: %s", stdout.String())
			}
			if command == "expose" {
				if !strings.Contains(stderr.String(), "mockingo expose is deprecated; use mockingo run instead") {
					t.Fatalf("missing deprecation warning: %s", stderr.String())
				}
			} else if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	}
}

func TestVersionCommand(t *testing.T) {
	previous := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = previous })
	// Version must work without configuration and ignore runtime settings.
	t.Setenv("MOCKINGO_TUNNEL_PROTOCOL_VERSION", "invalid")
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"release", nil, 0, "v1.2.3\n"},
		{"help", []string{"--help"}, 0, "Usage: mockingo version\n"},
		{"short help", []string{"-h"}, 0, "Usage: mockingo version\n"},
		{"unknown flag", []string{"--unknown"}, 2, ""},
		{"unexpected argument", []string{"extra"}, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := &App{Stdout: &stdout, Stderr: &stderr, ConfigPath: filepath.Join(t.TempDir(), "missing.json")}
			code := app.Run(context.Background(), append([]string{"version"}, tc.args...))
			if code != tc.code || stdout.String() != tc.output {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if (stderr.Len() != 0) != (tc.code != 0) {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	}
	Version = "dev"
	var stdout bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stdout}
	if code := app.Run(context.Background(), []string{"version"}); code != 0 || stdout.String() != "dev\n" {
		t.Fatalf("development version: code=%d output=%q", code, stdout.String())
	}
}
