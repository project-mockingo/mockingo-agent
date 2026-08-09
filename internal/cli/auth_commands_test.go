package cli

import (
	"io"
	"testing"

	"github.com/mockingo/mockingo-cli/internal/config"
)

func TestParseLoginOptionsUsesProductionDefaultsWithoutArguments(t *testing.T) {
	t.Setenv("MOCKINGO_API_URL", "")
	t.Setenv("MOCKINGO_OAUTH_ISSUER", "")
	t.Setenv("MOCKINGO_OAUTH_CLIENT_ID", "")

	options, err := parseLoginOptions(nil, config.Config{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.APIURL != defaultAPIURL || options.Issuer != defaultOAuthIssuer || options.ClientID != defaultOAuthClientID {
		t.Fatalf("unexpected defaults: %#v", options)
	}
}

func TestParseLoginOptionsPrefersSavedOAuthConfiguration(t *testing.T) {
	t.Setenv("MOCKINGO_API_URL", "")
	t.Setenv("MOCKINGO_OAUTH_ISSUER", "")
	t.Setenv("MOCKINGO_OAUTH_CLIENT_ID", "")
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
