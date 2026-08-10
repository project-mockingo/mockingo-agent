package cli

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/mockingo/mockingo-agent/internal/config"
)

func TestParseLoginOptionsUsesProductionAPIWithoutEmbeddingOAuthEnvironment(t *testing.T) {
	t.Setenv("MOCKINGO_API_URL", "")
	t.Setenv("MOCKINGO_OAUTH_ISSUER", "")
	t.Setenv("MOCKINGO_OAUTH_CLIENT_ID", "")
	t.Setenv("MOCKINGO_OAUTH_SCOPES", "")

	options, err := parseLoginOptions(nil, config.Config{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.APIURL != defaultAPIURL || options.Issuer != "" || options.ClientID != "" || options.Scopes != "" {
		t.Fatalf("unexpected defaults: %#v", options)
	}
	if options.IssuerExplicit || options.ClientIDExplicit || options.ScopesExplicit {
		t.Fatalf("unexpected explicit OAuth options: %#v", options)
	}
}

func TestRemovedLoginTokenOptionsAreRejectedAndAbsentFromHelp(t *testing.T) {
	for _, option := range []string{"--token", "--api-token"} {
		if _, err := parseLoginOptions([]string{option, "test-only-static-token"}, config.Config{}, io.Discard); err == nil {
			t.Fatalf("removed option %s was accepted", option)
		}
	}
	var help bytes.Buffer
	_, err := parseLoginOptions([]string{"--help"}, config.Config{}, &help)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
	if strings.Contains(help.String(), "--token") || strings.Contains(help.String(), "api-token") {
		t.Fatalf("removed option appears in help: %s", help.String())
	}
}

func TestParseLoginOptionsPrefersSavedOAuthConfiguration(t *testing.T) {
	t.Setenv("MOCKINGO_API_URL", "")
	t.Setenv("MOCKINGO_OAUTH_ISSUER", "")
	t.Setenv("MOCKINGO_OAUTH_CLIENT_ID", "")
	t.Setenv("MOCKINGO_OAUTH_SCOPES", "")
	existing := config.Config{
		APIURL:        "https://api.saved.example",
		OAuthIssuer:   "https://issuer.saved.example",
		OAuthClientID: "saved-client",
	}

	options, err := parseLoginOptions(nil, existing, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.APIURL != existing.APIURL || options.Issuer != existing.OAuthIssuer || options.ClientID != existing.OAuthClientID {
		t.Fatalf("saved configuration was not preserved: %#v", options)
	}
}

func TestParseLoginOptionsMarksOAuthOverridesExplicit(t *testing.T) {
	t.Setenv("MOCKINGO_OAUTH_ISSUER", "https://issuer.env.example")
	t.Setenv("MOCKINGO_OAUTH_CLIENT_ID", "client_env")
	t.Setenv("MOCKINGO_OAUTH_SCOPES", "openid")
	options, err := parseLoginOptions([]string{"--issuer", "https://issuer.flag.example", "--client-id", "client_flag", "--scopes", "openid profile"}, config.Config{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !options.IssuerExplicit || !options.ClientIDExplicit || !options.ScopesExplicit {
		t.Fatalf("overrides not marked explicit: %#v", options)
	}
}
