package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mockingo/mockingo-agent/internal/oauth"
)

const maxResponseSize = 1 << 20

var refreshLocks sync.Map

func accountLock(account string) *sync.Mutex {
	value, _ := refreshLocks.LoadOrStore(account, &sync.Mutex{})
	return value.(*sync.Mutex)
}

type Client struct {
	HTTP         *http.Client
	APIURL       string
	Issuer       string
	ClientID     string
	Scopes       []string
	Metadata     oauth.Metadata
	Store        oauth.CredentialStore
	ExpirySkew   time.Duration
	Now          func() time.Time
	defaultsOnce sync.Once
}

type Me struct {
	UserID               string `json:"userId"`
	AuthenticationMethod string `json:"authenticationMethod"`
}

var (
	ErrNotSignedIn = errors.New("You are not signed in to Mockingo.\n\nRun:\n  mockingo login")
	ErrSignedOut   = errors.New("Your Mockingo session has expired.\n\nRun:\n  mockingo login")
)

func (c *Client) defaults() {
	c.defaultsOnce.Do(func() {
		if c.HTTP == nil {
			c.HTTP = &http.Client{Timeout: 30 * time.Second}
		}
		if c.ExpirySkew == 0 {
			c.ExpirySkew = time.Minute
		}
		if c.Now == nil {
			c.Now = time.Now
		}
	})
}

func (c *Client) credentials(ctx context.Context, forceRefresh bool) (oauth.OAuthCredentials, error) {
	c.defaults()
	account := oauth.Account(c.Issuer, c.ClientID)
	credentials, err := c.Store.Get(account)
	if err != nil {
		if errors.Is(err, oauth.ErrCredentialsNotFound) {
			return oauth.OAuthCredentials{}, ErrNotSignedIn
		}
		return oauth.OAuthCredentials{}, err
	}
	if !forceRefresh && c.Now().Add(c.ExpirySkew).Before(credentials.ExpiresAt) {
		return credentials, nil
	}
	lock := accountLock(account)
	lock.Lock()
	defer lock.Unlock()
	// Re-read after waiting so concurrent requests reuse the rotated token.
	latest, err := c.Store.Get(account)
	if err != nil {
		if errors.Is(err, oauth.ErrCredentialsNotFound) {
			return oauth.OAuthCredentials{}, ErrNotSignedIn
		}
		return oauth.OAuthCredentials{}, err
	}
	if !forceRefresh && c.Now().Add(c.ExpirySkew).Before(latest.ExpiresAt) {
		return latest, nil
	}
	if forceRefresh && latest.AccessToken != credentials.AccessToken && c.Now().Add(c.ExpirySkew).Before(latest.ExpiresAt) {
		return latest, nil
	}
	refreshed, err := oauth.Refresh(ctx, c.HTTP, c.Metadata, c.ClientID, latest.RefreshToken, latest.Scope)
	if err != nil {
		if oauth.IsInvalidGrant(err) {
			_ = c.Store.Delete(account)
			return oauth.OAuthCredentials{}, ErrSignedOut
		}
		return oauth.OAuthCredentials{}, fmt.Errorf("refresh Mockingo session: %w", err)
	}
	latest.AccessToken = refreshed.AccessToken
	latest.TokenType = refreshed.TokenType
	latest.ExpiresAt = refreshed.ExpiresAt
	if len(refreshed.Scope) > 0 {
		latest.Scope = refreshed.Scope
	}
	if refreshed.RefreshToken != "" {
		latest.RefreshToken = refreshed.RefreshToken
	}
	if err := c.Store.Set(account, latest); err != nil {
		return oauth.OAuthCredentials{}, fmt.Errorf("save refreshed credentials: %w", err)
	}
	return latest, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	credentials, err := c.credentials(ctx, false)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.APIURL, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-store")
		httpClient := *c.HTTP
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusUnauthorized || attempt == 1 {
			return response, nil
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		credentials, err = c.credentials(ctx, true)
		if err != nil {
			return nil, err
		}
	}
	panic("unreachable")
}

func (c *Client) Me(ctx context.Context) (Me, error) {
	response, err := c.do(ctx, http.MethodGet, "/api/v1/me", nil)
	if err != nil {
		return Me{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Me{}, fmt.Errorf("Mockingo API rejected the request (HTTP %d)", response.StatusCode)
	}
	var me Me
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(&me); err != nil {
		return Me{}, fmt.Errorf("decode Mockingo identity response: %w", err)
	}
	if me.UserID == "" || me.AuthenticationMethod != "clerk_oauth" {
		return Me{}, errors.New("Mockingo API returned an invalid OAuth identity")
	}
	return me, nil
}

// VerifyLogin validates a freshly issued token without persisting it or trying
// refresh, so a backend rejection cannot leave partial credentials behind.
func VerifyLogin(ctx context.Context, client *http.Client, apiURL, accessToken string) (Me, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/api/v1/me", nil)
	if err != nil {
		return Me{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	httpClient := *client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return Me{}, fmt.Errorf("verify login with Mockingo API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Me{}, fmt.Errorf("Mockingo API rejected the OAuth access token (HTTP %d)", response.StatusCode)
	}
	var me Me
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(&me); err != nil {
		return Me{}, fmt.Errorf("decode Mockingo identity response: %w", err)
	}
	if me.UserID == "" || me.AuthenticationMethod != "clerk_oauth" {
		return Me{}, errors.New("Mockingo API did not confirm Clerk OAuth authentication")
	}
	return me, nil
}
