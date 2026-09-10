//go:build windows

package cli

import (
	"errors"
	"net/url"
	"testing"

	"github.com/project-mockingo/mockingo-agent/internal/oauth"
	"golang.org/x/sys/windows"
)

func TestWindowsBrowserLauncherPreservesAuthorizationURL(t *testing.T) {
	const redirect = "http://127.0.0.1:53682/oauth/callback"
	target, err := oauth.AuthorizationURL(oauth.AuthorizationRequest{
		Endpoint: "https://clerk.example/oauth/authorize?audience=cli",
		ClientID: "client_123", RedirectURI: redirect,
		Scopes: []string{"openid", "profile", "email", "offline_access"},
		State:  "test-state", Challenge: "test-challenge", Nonce: "test-nonce",
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = openBrowserWithShellExecute(target, func(hwnd windows.Handle, verb, file, args, cwd *uint16, show int32) error {
		called = true
		if hwnd != 0 || windows.UTF16PtrToString(verb) != "open" || args != nil || cwd != nil || show != windows.SW_SHOWNORMAL {
			t.Error("expected direct URL opening with the default browser")
		}
		got := windows.UTF16PtrToString(file)
		if got != target {
			t.Errorf("launcher changed authorization URL: got %q, want %q", got, target)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if values := parsed.Query()["redirect_uri"]; len(values) != 1 || values[0] != redirect {
			t.Errorf("launcher changed redirect_uri: %q", values)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("browser launcher called = %t, error = %v", called, err)
	}
}

func TestWindowsBrowserLauncherReportsFailure(t *testing.T) {
	want := errors.New("no browser association")
	err := openBrowserWithShellExecute("https://clerk.example/oauth/authorize", func(windows.Handle, *uint16, *uint16, *uint16, *uint16, int32) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("launcher error = %v, want %v", err, want)
	}
}

func TestWindowsBrowserLauncherRejectsTruncatedURL(t *testing.T) {
	err := openBrowserWithShellExecute("https://clerk.example/oauth/authorize\x00?redirect_uri=ignored", func(windows.Handle, *uint16, *uint16, *uint16, *uint16, int32) error {
		t.Fatal("launcher must not open a URL containing a NUL")
		return nil
	})
	if err == nil {
		t.Fatal("expected invalid URL error")
	}
}
