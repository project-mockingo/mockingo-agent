package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxResponseSize = 1 << 20

type Metadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint,omitempty"`
	RevocationEndpoint    string   `json:"revocation_endpoint,omitempty"`
	GrantTypesSupported   []string `json:"grant_types_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

var metadataCache sync.Map

func doWithoutRedirects(client *http.Client, request *http.Request) (*http.Response, error) {
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return copy.Do(request)
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateSecureURL(raw string, allowLocalHTTP bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("must be an absolute URL without credentials or fragment")
	}
	if parsed.Scheme != "https" {
		host := parsed.Hostname()
		if parsed.Scheme != "http" || !allowLocalHTTP || !isLoopbackHost(host) {
			return nil, errors.New("must use HTTPS (HTTP is allowed only for loopback test servers)")
		}
	}
	return parsed, nil
}

func ValidateMetadata(metadata Metadata, requestedIssuer string) error {
	issuer, err := validateSecureURL(strings.TrimRight(requestedIssuer, "/"), true)
	if err != nil {
		return fmt.Errorf("invalid OAuth issuer: %w", err)
	}
	actualIssuer, err := validateSecureURL(strings.TrimRight(metadata.Issuer, "/"), true)
	if err != nil || actualIssuer.String() != issuer.String() {
		return errors.New("authorization server metadata issuer does not match configured issuer")
	}
	for name, raw := range map[string]string{"authorization endpoint": metadata.AuthorizationEndpoint, "token endpoint": metadata.TokenEndpoint} {
		if _, err := validateSecureURL(raw, isLoopbackHost(issuer.Hostname())); err != nil {
			return fmt.Errorf("invalid %s: %w", name, err)
		}
	}
	for name, raw := range map[string]string{"userinfo endpoint": metadata.UserinfoEndpoint, "revocation endpoint": metadata.RevocationEndpoint} {
		if raw != "" {
			if _, err := validateSecureURL(raw, isLoopbackHost(issuer.Hostname())); err != nil {
				return fmt.Errorf("invalid %s: %w", name, err)
			}
		}
	}
	if !slices.Contains(metadata.GrantTypesSupported, "authorization_code") {
		return errors.New("authorization server does not advertise the authorization_code grant")
	}
	if !slices.Contains(metadata.GrantTypesSupported, "refresh_token") {
		return errors.New("authorization server does not advertise the refresh_token grant")
	}
	if !slices.Contains(metadata.CodeChallengeMethods, "S256") {
		return errors.New("authorization server does not support PKCE S256")
	}
	if !slices.Contains(metadata.TokenAuthMethods, "none") {
		return errors.New("authorization server does not support public clients (token endpoint authentication method none)")
	}
	return nil
}

func Discover(ctx context.Context, client *http.Client, issuer string) (Metadata, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if cached, ok := metadataCache.Load(issuer); ok {
		return cached.(Metadata), nil
	}
	if _, err := validateSecureURL(issuer, true); err != nil {
		return Metadata{}, fmt.Errorf("invalid OAuth issuer: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return Metadata{}, err
	}
	req.Header.Set("Accept", "application/json")
	response, err := doWithoutRedirects(client, req)
	if err != nil {
		return Metadata{}, fmt.Errorf("load OAuth discovery metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Metadata{}, fmt.Errorf("load OAuth discovery metadata: server returned HTTP %d", response.StatusCode)
	}
	var metadata Metadata
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize))
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode OAuth discovery metadata: %w", err)
	}
	if err := ValidateMetadata(metadata, issuer); err != nil {
		return Metadata{}, err
	}
	metadataCache.Store(issuer, metadata)
	return metadata, nil
}

func randomBase64URL(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read cryptographic randomness: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func GenerateVerifier() (string, error) { return randomBase64URL(64) }
func GenerateState() (string, error)    { return randomBase64URL(32) }
func GenerateNonce() (string, error)    { return randomBase64URL(32) }

func Challenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func RedirectURI(host string, port int, path string) (string, error) {
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || host != ip.String() {
		return "", errors.New("callback host must be a loopback IP literal")
	}
	if port < 0 || port > 65535 {
		return "", errors.New("callback port must be between 0 and 65535")
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return "", errors.New("callback path must be an absolute URL path without query or fragment")
	}
	hostPort := net.JoinHostPort(host, strconv.Itoa(port))
	return (&url.URL{Scheme: "http", Host: hostPort, Path: path}).String(), nil
}

type AuthorizationRequest struct {
	Endpoint, ClientID, RedirectURI, State, Challenge, Nonce string
	Scopes                                                   []string
}

func AuthorizationURL(request AuthorizationRequest) (string, error) {
	endpoint, err := url.Parse(request.Endpoint)
	if err != nil || !endpoint.IsAbs() {
		return "", errors.New("invalid authorization endpoint")
	}
	values := endpoint.Query()
	values.Set("client_id", request.ClientID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", request.RedirectURI)
	values.Set("scope", strings.Join(request.Scopes, " "))
	values.Set("state", request.State)
	values.Set("code_challenge", request.Challenge)
	values.Set("code_challenge_method", "S256")
	if request.Nonce != "" {
		values.Set("nonce", request.Nonce)
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String(), nil
}

type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        []string
	ExpiresAt    time.Time
	IDToken      string
}

type rawTokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	TokenType    string          `json:"token_type"`
	Scope        json.RawMessage `json:"scope"`
	ExpiresIn    json.Number     `json:"expires_in"`
	IDToken      string          `json:"id_token"`
	Error        string          `json:"error"`
	Description  string          `json:"error_description"`
}

type TokenRequestError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *TokenRequestError) Error() string {
	message := e.Description
	if message == "" {
		message = e.Code
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return "authorization server rejected token request: " + message
}

func IsInvalidGrant(err error) bool {
	var tokenError *TokenRequestError
	return errors.As(err, &tokenError) && tokenError.Code == "invalid_grant"
}

func parseScope(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.Fields(text), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("scope must be a string or string array")
	}
	return list, nil
}

func ParseTokenResponse(reader io.Reader, now time.Time, requestedScopes []string, requireRefresh bool) (TokenResponse, error) {
	var raw rawTokenResponse
	decoder := json.NewDecoder(io.LimitReader(reader, maxResponseSize))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return TokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if raw.Error != "" {
		if raw.Description != "" {
			return TokenResponse{}, fmt.Errorf("authorization server rejected token request: %s", raw.Description)
		}
		return TokenResponse{}, fmt.Errorf("authorization server rejected token request: %s", raw.Error)
	}
	if raw.AccessToken == "" {
		return TokenResponse{}, errors.New("token response is missing access_token")
	}
	if !strings.EqualFold(raw.TokenType, "Bearer") {
		return TokenResponse{}, errors.New("token response has unsupported token_type")
	}
	seconds, err := raw.ExpiresIn.Int64()
	if err != nil || seconds <= 0 {
		return TokenResponse{}, errors.New("token response has invalid expires_in")
	}
	if requireRefresh && raw.RefreshToken == "" {
		return TokenResponse{}, errors.New("token response is missing refresh_token required for offline access")
	}
	scopes, err := parseScope(raw.Scope)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("invalid token response: %w", err)
	}
	if len(scopes) == 0 {
		scopes = append([]string(nil), requestedScopes...)
	} else {
		for _, requested := range requestedScopes {
			if !slices.Contains(scopes, requested) {
				return TokenResponse{}, fmt.Errorf("token response did not grant requested scope %q", requested)
			}
		}
	}
	return TokenResponse{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, TokenType: "Bearer", Scope: scopes, ExpiresAt: now.Add(time.Duration(seconds) * time.Second), IDToken: raw.IDToken}, nil
}

func TokenRequest(ctx context.Context, client *http.Client, endpoint string, values url.Values, now time.Time, requestedScopes []string, requireRefresh bool) (TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	response, err := doWithoutRedirects(client, req)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("token request failed; the authorization code may already have been consumed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var raw rawTokenResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(&raw)
		message := raw.Description
		if message == "" {
			message = raw.Error
		}
		if message == "" {
			message = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		return TokenResponse{}, &TokenRequestError{StatusCode: response.StatusCode, Code: raw.Error, Description: message}
	}
	return ParseTokenResponse(response.Body, now, requestedScopes, requireRefresh)
}

func ExchangeCode(ctx context.Context, client *http.Client, metadata Metadata, clientID, code, verifier, redirectURI string, scopes []string) (TokenResponse, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	return TokenRequest(ctx, client, metadata.TokenEndpoint, values, time.Now(), scopes, slices.Contains(scopes, "offline_access"))
}

func Refresh(ctx context.Context, client *http.Client, metadata Metadata, clientID, refreshToken string, scopes []string) (TokenResponse, error) {
	values := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}}
	return TokenRequest(ctx, client, metadata.TokenEndpoint, values, time.Now(), scopes, false)
}
