package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const loginConfigurationPath = "/.well-known/mockingo-agent.json"

// LoginConfiguration contains the public OAuth settings advertised by a
// Mockingo control plane. Secrets must never be added to this response.
type LoginConfiguration struct {
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"clientId"`
	Scopes   []string `json:"scopes"`
}

// DiscoverLoginConfiguration retrieves the OAuth environment associated with
// apiURL so the agent does not embed development or production Clerk settings.
func DiscoverLoginConfiguration(ctx context.Context, client *http.Client, apiURL string) (LoginConfiguration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+loginConfigurationPath, nil)
	if err != nil {
		return LoginConfiguration{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	httpClient := *client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return LoginConfiguration{}, fmt.Errorf("request Mockingo login configuration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return LoginConfiguration{}, fmt.Errorf("Mockingo API login configuration returned HTTP %d", response.StatusCode)
	}
	var configuration LoginConfiguration
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize))
	if err := decoder.Decode(&configuration); err != nil {
		return LoginConfiguration{}, fmt.Errorf("decode Mockingo login configuration: %w", err)
	}
	configuration.Issuer = strings.TrimRight(strings.TrimSpace(configuration.Issuer), "/")
	configuration.ClientID = strings.TrimSpace(configuration.ClientID)
	configuration.Scopes = normalizeScopes(configuration.Scopes)
	if configuration.Issuer == "" || configuration.ClientID == "" {
		return LoginConfiguration{}, errors.New("Mockingo API returned incomplete login configuration")
	}
	if len(configuration.Scopes) == 0 {
		return LoginConfiguration{}, errors.New("Mockingo API returned no OAuth scopes")
	}
	return configuration, nil
}

func normalizeScopes(values []string) []string {
	seen := make(map[string]struct{})
	var scopes []string
	for _, value := range values {
		for _, scope := range strings.Fields(value) {
			if _, exists := seen[scope]; exists {
				continue
			}
			seen[scope] = struct{}{}
			scopes = append(scopes, scope)
		}
	}
	return scopes
}
