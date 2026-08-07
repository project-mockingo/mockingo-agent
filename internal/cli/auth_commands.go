package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mockingo/mockingo-cli/internal/apiclient"
	"github.com/mockingo/mockingo-cli/internal/config"
	"github.com/mockingo/mockingo-cli/internal/oauth"
)

const (
	defaultAPIURL       = "https://api.mockingo.com"
	defaultCallbackHost = "127.0.0.1"
	defaultCallbackPort = 53682
	defaultCallbackPath = "/oauth/callback"
	defaultScopes       = "openid profile email offline_access"
)

type loginOptions struct {
	APIURL, Issuer, ClientID, Scopes, CallbackHost, CallbackPath string
	CallbackPort                                                 int
	Timeout                                                      time.Duration
	NoBrowser, Force, AllowFile                                  bool
	APIURLExplicit                                               bool
	LegacyToken                                                  string
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func (a *App) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (a *App) credentialStore(configPath string, allowFile bool) oauth.CredentialStore {
	if a.Credentials != nil {
		return a.Credentials
	}
	return oauth.FallbackStore{
		Primary:   oauth.KeyringStore{},
		File:      oauth.FileStore{Directory: filepath.Join(filepath.Dir(configPath), "credentials")},
		AllowFile: allowFile,
		Warn:      func(message string) { fmt.Fprintln(a.Stderr, message) },
	}
}

func parseLoginOptions(args []string, existing config.Config, stderr io.Writer) (loginOptions, error) {
	port, err := envInt("MOCKINGO_OAUTH_CALLBACK_PORT", defaultCallbackPort)
	if err != nil {
		return loginOptions{}, err
	}
	timeout, err := envDuration("MOCKINGO_LOGIN_TIMEOUT", 5*time.Minute)
	if err != nil {
		return loginOptions{}, err
	}
	apiDefault := existing.APIURL
	if existing.OAuthIssuer == "" || apiDefault == "" {
		apiDefault = defaultAPIURL
	}
	options := loginOptions{
		APIURL:       envString("MOCKINGO_API_URL", apiDefault),
		Issuer:       envString("MOCKINGO_OAUTH_ISSUER", existing.OAuthIssuer),
		ClientID:     envString("MOCKINGO_OAUTH_CLIENT_ID", existing.OAuthClientID),
		Scopes:       envString("MOCKINGO_OAUTH_SCOPES", existing.OAuthScopes),
		CallbackHost: envString("MOCKINGO_OAUTH_CALLBACK_HOST", defaultCallbackHost),
		CallbackPath: envString("MOCKINGO_OAUTH_CALLBACK_PATH", defaultCallbackPath),
		CallbackPort: port,
		Timeout:      timeout,
		AllowFile:    strings.EqualFold(os.Getenv("MOCKINGO_ALLOW_FILE_CREDENTIALS"), "true"),
	}
	if options.Scopes == "" {
		options.Scopes = defaultScopes
	}
	set := flag.NewFlagSet("login", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&options.APIURL, "api-url", options.APIURL, "Mockingo control-plane API URL")
	set.StringVar(&options.Issuer, "issuer", options.Issuer, "Clerk Frontend API issuer URL")
	set.StringVar(&options.ClientID, "client-id", options.ClientID, "public OAuth client ID")
	set.IntVar(&options.CallbackPort, "callback-port", options.CallbackPort, "loopback callback port (0 requests an ephemeral port)")
	set.BoolVar(&options.NoBrowser, "no-browser", false, "print the authorization URL without opening a browser")
	set.BoolVar(&options.Force, "force", false, "authenticate again even when already signed in")
	set.BoolVar(&options.AllowFile, "allow-insecure-storage", options.AllowFile, "allow owner-only file storage when the OS credential store is unavailable")
	set.StringVar(&options.LegacyToken, "token", "", "deprecated static gateway token")
	if err := set.Parse(args); err != nil {
		return loginOptions{}, fmt.Errorf("invalid arguments: %w", err)
	}
	set.Visit(func(value *flag.Flag) {
		if value.Name == "api-url" {
			options.APIURLExplicit = true
		}
	})
	if set.NArg() != 0 {
		return loginOptions{}, errors.New("invalid arguments: login does not accept positional arguments")
	}
	return options, nil
}

func validateAPIURL(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("API URL must be an http or https URL without credentials, query, or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

func (a *App) login(ctx context.Context, args []string) error {
	path, err := a.path()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	existing, err := config.LoadOptional(path)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	options, err := parseLoginOptions(args, existing, a.Stderr)
	if err != nil {
		return err
	}
	if options.LegacyToken != "" {
		return a.legacyLogin(path, existing, options)
	}
	apiURL, err := validateAPIURL(options.APIURL)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	if options.Issuer == "" || options.ClientID == "" {
		return errors.New("configuration error: OAuth issuer and public client ID are required; set MOCKINGO_OAUTH_ISSUER and MOCKINGO_OAUTH_CLIENT_ID or use --issuer and --client-id")
	}
	scopes := strings.Fields(options.Scopes)
	if len(scopes) == 0 {
		return errors.New("configuration error: at least one OAuth scope is required")
	}
	store := a.credentialStore(path, options.AllowFile)
	account := oauth.Account(options.Issuer, options.ClientID)
	if !options.Force {
		if current, err := store.Get(account); err == nil && time.Now().Add(time.Minute).Before(current.ExpiresAt) {
			fmt.Fprintln(a.Stdout, "Already signed in to Mockingo. Use --force to authenticate again.")
			return nil
		}
	}
	loginCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	metadata, err := oauth.Discover(loginCtx, a.httpClient(), options.Issuer)
	if err != nil {
		return err
	}
	verifier, err := oauth.GenerateVerifier()
	if err != nil {
		return err
	}
	defer func() { verifier = "" }()
	state, err := oauth.GenerateState()
	if err != nil {
		return err
	}
	nonce := ""
	for _, scope := range scopes {
		if scope == "openid" {
			nonce, err = oauth.GenerateNonce()
			if err != nil {
				return err
			}
			break
		}
	}
	callback, redirectURI, err := oauth.StartCallback(options.CallbackHost, options.CallbackPort, options.CallbackPath, state)
	if err != nil {
		return err
	}
	defer callback.Close()
	authorizeURL, err := oauth.AuthorizationURL(oauth.AuthorizationRequest{Endpoint: metadata.AuthorizationEndpoint, ClientID: options.ClientID, RedirectURI: redirectURI, State: state, Challenge: oauth.Challenge(verifier), Nonce: nonce, Scopes: scopes})
	if err != nil {
		return err
	}
	if options.NoBrowser {
		fmt.Fprintln(a.Stdout, "Open this URL in your browser to sign in to Mockingo:")
		fmt.Fprintln(a.Stdout, authorizeURL)
	} else {
		fmt.Fprintln(a.Stdout, "Opening your browser to sign in to Mockingo...")
		opener := a.OpenBrowser
		if opener == nil {
			opener = openBrowser
		}
		if err := opener(authorizeURL); err != nil {
			fmt.Fprintf(a.Stderr, "The browser could not be opened: %v\n", err)
		}
		fmt.Fprintln(a.Stdout, "\nIf the browser does not open, visit:")
		fmt.Fprintln(a.Stdout, authorizeURL)
	}
	if options.CallbackPort == 0 {
		fmt.Fprintf(a.Stdout, "\nUsing callback %s. This exact URI must be registered for the OAuth application.\n", redirectURI)
	}
	if !strings.Contains(authorizeURL, url.QueryEscape(redirectURI)) {
		return errors.New("internal error: authorization URL does not contain the callback URI")
	}
	fmt.Fprintln(a.Stdout, "\nWaiting for authorization...")
	result, err := callback.Wait(loginCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("login timed out after %s", options.Timeout)
	}
	if err != nil {
		return err
	}
	if result.OAuthError != "" {
		if result.ErrorDescription != "" {
			return fmt.Errorf("authorization was denied: %s", result.ErrorDescription)
		}
		return fmt.Errorf("authorization was denied: %s", result.OAuthError)
	}
	tokens, err := oauth.ExchangeCode(loginCtx, a.httpClient(), metadata, options.ClientID, result.Code, verifier, redirectURI, scopes)
	if err != nil {
		return err
	}
	me, err := apiclient.VerifyLogin(loginCtx, a.httpClient(), apiURL, tokens.AccessToken)
	if err != nil {
		return err
	}
	credentials := oauth.OAuthCredentials{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, TokenType: tokens.TokenType, Scope: tokens.Scope, ExpiresAt: tokens.ExpiresAt, UserID: me.UserID}
	if err := store.Set(account, credentials); err != nil {
		return fmt.Errorf("save OAuth credentials: %w", err)
	}
	existing.SetOAuth(apiURL, options.Issuer, options.ClientID, tokens.Scope, me.UserID, tokens.ExpiresAt)
	if err := config.Save(path, existing); err != nil {
		_ = store.Delete(account)
		return fmt.Errorf("save OAuth configuration: %w", err)
	}
	fmt.Fprintln(a.Stdout, "✓ Signed in to Mockingo")
	fmt.Fprintf(a.Stdout, "User ID: %s\nAPI: %s\n", me.UserID, apiURL)
	return nil
}

func (a *App) legacyLogin(path string, existing config.Config, options loginOptions) error {
	if !options.APIURLExplicit {
		return errors.New("invalid arguments: deprecated --token login also requires an explicit --api-url for the legacy gateway")
	}
	apiURL, err := validateAPIURL(options.APIURL)
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	if options.Issuer != "" || options.ClientID != "" {
		// Existing OAuth configuration is fine; the two credential types stay separate.
	}
	existing.LegacyAPIURL = apiURL
	existing.LegacyToken = options.LegacyToken
	if existing.OAuthIssuer == "" {
		existing.APIURL = ""
	}
	if err := config.Save(path, existing); err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	fmt.Fprintln(a.Stderr, "Warning: --token login is deprecated and will be removed after tunnel-ticket integration.")
	fmt.Fprintf(a.Stdout, "Legacy gateway configuration saved to %s\n", path)
	return nil
}

func (a *App) loadOAuth(ctx context.Context, path string, allowFile bool) (config.Config, oauth.Metadata, oauth.CredentialStore, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, oauth.Metadata{}, nil, err
	}
	if cfg.OAuthIssuer == "" || cfg.OAuthClientID == "" || cfg.APIURL == "" {
		return config.Config{}, oauth.Metadata{}, nil, errors.New("not signed in with OAuth; run 'mockingo login'")
	}
	metadata, err := oauth.Discover(ctx, a.httpClient(), cfg.OAuthIssuer)
	if err != nil {
		return config.Config{}, oauth.Metadata{}, nil, err
	}
	return cfg, metadata, a.credentialStore(path, allowFile), nil
}

func (a *App) whoami(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("whoami", flag.ContinueOnError)
	set.SetOutput(a.Stderr)
	asJSON := set.Bool("json", false, "print JSON")
	allowFile := set.Bool("allow-insecure-storage", strings.EqualFold(os.Getenv("MOCKINGO_ALLOW_FILE_CREDENTIALS"), "true"), "allow configured owner-only fallback credential storage")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return errors.New("invalid arguments for whoami")
	}
	path, err := a.path()
	if err != nil {
		return err
	}
	cfg, metadata, store, err := a.loadOAuth(ctx, path, *allowFile)
	if err != nil {
		return err
	}
	client := &apiclient.Client{HTTP: a.httpClient(), APIURL: cfg.APIURL, Issuer: cfg.OAuthIssuer, ClientID: cfg.OAuthClientID, Scopes: strings.Fields(cfg.OAuthScopes), Metadata: metadata, Store: store}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(a.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]string{"api": cfg.APIURL, "authenticationMethod": me.AuthenticationMethod, "userId": me.UserID})
	}
	fmt.Fprintln(a.Stdout, "Signed in to Mockingo")
	fmt.Fprintf(a.Stdout, "User ID: %s\nAuthentication: Clerk OAuth\nAPI: %s\n", me.UserID, cfg.APIURL)
	return nil
}

func (a *App) logout(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("logout", flag.ContinueOnError)
	set.SetOutput(a.Stderr)
	allowFile := set.Bool("allow-insecure-storage", strings.EqualFold(os.Getenv("MOCKINGO_ALLOW_FILE_CREDENTIALS"), "true"), "remove configured owner-only fallback credentials")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return errors.New("invalid arguments for logout")
	}
	path, err := a.path()
	if err != nil {
		return err
	}
	cfg, err := config.LoadOptional(path)
	if err != nil {
		return err
	}
	if cfg.OAuthIssuer == "" || cfg.OAuthClientID == "" {
		fmt.Fprintln(a.Stdout, "✓ Signed out of Mockingo")
		return nil
	}
	store := a.credentialStore(path, *allowFile)
	account := oauth.Account(cfg.OAuthIssuer, cfg.OAuthClientID)
	credentials, _ := store.Get(account)
	if err := store.Delete(account); err != nil {
		return fmt.Errorf("remove local OAuth credentials: %w", err)
	}
	issuer, clientID := cfg.OAuthIssuer, cfg.OAuthClientID
	cfg.ClearOAuth()
	if cfg.Empty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove OAuth configuration: %w", err)
		}
	} else if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("clear OAuth configuration: %w", err)
	}
	// Local logout is already complete. Revocation is best-effort and only used
	// when advertised by discovery.
	if credentials.RefreshToken != "" {
		revokeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if metadata, discoverErr := oauth.Discover(revokeCtx, a.httpClient(), issuer); discoverErr == nil && metadata.RevocationEndpoint != "" {
			values := url.Values{"token": {credentials.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID}}
			request, _ := http.NewRequestWithContext(revokeCtx, http.MethodPost, metadata.RevocationEndpoint, strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			httpClient := *a.httpClient()
			httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			if response, requestErr := httpClient.Do(request); requestErr == nil {
				io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
			}
		}
	}
	fmt.Fprintln(a.Stdout, "✓ Signed out of Mockingo")
	return nil
}
