package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mockingo/mockingo-cli/internal/agent"
	"github.com/mockingo/mockingo-cli/internal/apiclient"
	"github.com/mockingo/mockingo-cli/internal/config"
	"github.com/mockingo/mockingo-cli/internal/naming"
	"github.com/mockingo/mockingo-cli/internal/oauth"
	"github.com/mockingo/mockingo-cli/internal/process"
	"github.com/mockingo/mockingo-cli/internal/readiness"
)

type App struct {
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	ConfigPath  string
	HTTPClient  *http.Client
	Credentials oauth.CredentialStore
	OpenBrowser func(string) error
}

func New() *App { return &App{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr} }

func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}
	var err error
	var code int
	switch args[0] {
	case "login":
		err = a.login(ctx, args[1:])
	case "whoami":
		err = a.whoami(ctx, args[1:])
	case "logout":
		err = a.logout(ctx, args[1:])
	case "expose":
		code, err = a.expose(ctx, args[1:])
	case "endpoints":
		code, err = a.endpoints(ctx, args[1:])
	case "help", "--help", "-h":
		a.usage()
		return 0
	default:
		err = fmt.Errorf("invalid arguments: unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "Error: %v\n", err)
		if code != 0 {
			return code
		}
		return 1
	}
	return code
}

func (a *App) usage() {
	fmt.Fprintln(a.Stdout, "Usage:")
	fmt.Fprintln(a.Stdout, "  mockingo login [--api-url URL] [--issuer URL] [--callback-port PORT]")
	fmt.Fprintln(a.Stdout, "  mockingo login --api-url URL --token TOKEN  # deprecated legacy gateway login")
	fmt.Fprintln(a.Stdout, "  mockingo whoami [--json]")
	fmt.Fprintln(a.Stdout, "  mockingo logout")
	fmt.Fprintln(a.Stdout, "  mockingo expose --name NAME --http PORT [options] [-- command args...]")
	fmt.Fprintln(a.Stdout, "  mockingo expose --legacy --name NAME --http PORT  # deprecated compatibility mode")
	fmt.Fprintln(a.Stdout, "  mockingo endpoints list [--json]")
	fmt.Fprintln(a.Stdout, "  mockingo endpoints delete NAME [--force]")
	fmt.Fprintln(a.Stdout, "\nLogin uses Clerk OAuth Authorization Code Flow with PKCE.")
}

func (a *App) path() (string, error) {
	if a.ConfigPath != "" {
		return a.ConfigPath, nil
	}
	return config.Path()
}

func (a *App) expose(ctx context.Context, args []string) (int, error) {
	options, err := ParseExpose(args)
	if err != nil {
		return 2, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := naming.Validate(options.Name); err != nil {
		return 2, fmt.Errorf("invalid arguments: %w", err)
	}
	if options.CWD != "" {
		absolute, err := filepath.Abs(options.CWD)
		if err != nil {
			return 2, fmt.Errorf("invalid arguments: resolve --cwd: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			return 2, fmt.Errorf("invalid arguments: --cwd is not a directory")
		}
		options.CWD = absolute
	}
	path, err := a.path()
	if err != nil {
		return 1, fmt.Errorf("configuration error: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		if !options.Legacy && errors.Is(err, config.ErrNotConfigured) {
			return 1, apiclient.ErrNotSignedIn
		}
		return 1, fmt.Errorf("configuration error: %w", err)
	}
	legacyAPIURL, legacyToken, hasLegacy := cfg.Legacy()
	var controlClient *apiclient.Client
	var identity apiclient.Me
	if options.Legacy {
		if !hasLegacy {
			return 1, errors.New("legacy gateway credentials are not configured; run 'mockingo login --api-url URL --token TOKEN'")
		}
		fmt.Fprintln(a.Stderr, "Warning: legacy tunnel authentication is deprecated and will be removed.")
	} else {
		if cfg.OAuthIssuer == "" || cfg.OAuthClientID == "" || cfg.APIURL == "" {
			return 1, apiclient.ErrNotSignedIn
		}
		metadata, discoverErr := oauth.Discover(ctx, a.httpClient(), cfg.OAuthIssuer)
		if discoverErr != nil {
			return 1, discoverErr
		}
		apiURL := cfg.APIURL
		if options.APIURL != "" {
			apiURL, err = validateAPIURL(options.APIURL)
			if err != nil {
				return 2, fmt.Errorf("invalid arguments: %w", err)
			}
		}
		controlClient = &apiclient.Client{
			HTTP: a.httpClient(), APIURL: apiURL, Issuer: cfg.OAuthIssuer,
			ClientID: cfg.OAuthClientID, Scopes: strings.Fields(cfg.OAuthScopes), Metadata: metadata,
			Store: a.credentialStore(path, options.AllowFileCredentials),
		}
		identity, err = controlClient.Me(ctx)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(a.Stdout, "✓ Signed in as %s\n", identity.UserID)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var child *process.Process
	if len(options.Command) > 0 {
		fmt.Fprintf(a.Stdout, "Starting: %s\n", formatCommand(options.Command))
		child, err = process.Start(process.Options{
			Command: options.Command, CWD: options.CWD, Env: options.Environment,
			Stdout: a.Stdout, Stderr: a.Stderr,
		})
		if err != nil {
			return 1, fmt.Errorf("process startup failure: %w", err)
		}
	}
	cleanupChild := func() {
		if child == nil {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = child.Stop(stopCtx)
	}
	defer cleanupChild()

	fmt.Fprintf(a.Stdout, "Waiting for 127.0.0.1:%d...\n", options.HTTPPort)
	startupCtx, startupCancel := context.WithTimeout(runCtx, options.StartupTimeout)
	ready := make(chan error, 1)
	go func() { ready <- readiness.Wait(startupCtx, options.HTTPPort) }()
	if child == nil {
		err = <-ready
	} else {
		select {
		case err = <-ready:
		case result := <-child.Done():
			startupCancel()
			return exitCode(result), fmt.Errorf("process startup failure: process exited before port became ready")
		case <-runCtx.Done():
			startupCancel()
			return 0, nil
		}
	}
	startupCancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 1, fmt.Errorf("startup timeout: port %d did not become ready within %s", options.HTTPPort, options.StartupTimeout)
		}
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return 0, nil
		}
		return 1, fmt.Errorf("process startup failure: wait for local port: %w", err)
	}
	fmt.Fprintln(a.Stdout, "Application is ready.")

	initialConnected := make(chan struct{}, 1)
	state := func(message string) {
		fmt.Fprintln(a.Stdout, message)
		if message == "Tunnel connected." || message == "Connected to Mockingo Gateway." {
			select {
			case initialConnected <- struct{}{}:
			default:
			}
		}
	}
	var verbose func(string, ...any)
	if options.Verbose {
		verbose = func(format string, values ...any) { fmt.Fprintf(a.Stderr, "debug: "+format+"\n", values...) }
	}
	var tunnelAgent *agent.Agent
	var publicURL string
	if options.Legacy {
		httpClient := &http.Client{Timeout: 30 * time.Second}
		registration, registerErr := agent.Register(runCtx, httpClient, legacyAPIURL, legacyToken, options.Name, options.HTTPPort)
		if registerErr != nil {
			return 1, fmt.Errorf("gateway registration failure: %w", registerErr)
		}
		publicURL = registration.PublicURL
		fmt.Fprintf(a.Stdout, "Registering endpoint %s...\n", registration.Hostname)
		tunnelAgent = agent.New(agent.Config{
			ConnectURL: registration.ConnectURL, SessionToken: registration.SessionToken,
			LocalPort: options.HTTPPort, RequestTimeout: options.RequestTimeout,
			OnState: state, Verbose: verbose, PublicURL: registration.PublicURL,
			Reregister: func(registerCtx context.Context) (agent.Registration, error) {
				return agent.Register(registerCtx, httpClient, legacyAPIURL, legacyToken, options.Name, options.HTTPPort)
			},
		})
	} else {
		request := apiclient.TunnelSessionRequest{EndpointName: options.Name, Protocol: "http", LocalPort: options.HTTPPort, ProtocolVersion: options.ProtocolVersion}
		validation := apiclient.TunnelSessionValidation{ExpectedGatewayHosts: strings.Split(options.ExpectedGatewayHost, ","), AllowInsecureLocal: options.AllowInsecureGateway}
		createSession := func(sessionCtx context.Context) (agent.Session, error) {
			response, createErr := controlClient.CreateTunnelSession(sessionCtx, request, validation)
			if createErr != nil {
				return agent.Session{}, mapTunnelSessionError(options.Name, createErr)
			}
			return agent.Session{
				EndpointID: response.Endpoint.ID, EndpointName: response.Endpoint.Name,
				SessionID: response.Tunnel.SessionID, ConnectURL: response.Tunnel.ConnectURL,
				Ticket: response.Tunnel.Ticket, PublicURL: response.Endpoint.PublicURL,
			}, nil
		}
		initial, createErr := createSession(runCtx)
		if createErr != nil {
			return 1, createErr
		}
		publicURL = initial.PublicURL
		fmt.Fprintf(a.Stdout, "✓ Endpoint reserved: %s\n", initial.EndpointName)
		tunnelAgent = agent.New(agent.Config{
			InitialSession: &initial, AcquireSession: createSession, Retryable: apiclient.IsRetryable, TemporaryConflict: apiclient.IsTemporarySessionConflict,
			LocalPort: options.HTTPPort, RequestTimeout: options.RequestTimeout,
			OnState: state, Verbose: verbose, PublicURL: initial.PublicURL,
			ReconnectEnabled: options.ReconnectEnabled, ReconnectInitialDelay: options.ReconnectInitialDelay,
			ReconnectMaxDelay: options.ReconnectMaxDelay,
		})
	}
	agentDone := make(chan error, 1)
	go func() { agentDone <- tunnelAgent.Run(runCtx) }()

	if child == nil {
		select {
		case <-initialConnected:
		case err := <-agentDone:
			if err == nil {
				return 0, nil
			}
			return 1, fmt.Errorf("tunnel connection failure: %w", err)
		case <-runCtx.Done():
			return 0, nil
		}
	} else {
		select {
		case <-initialConnected:
		case result := <-child.Done():
			cancel()
			if result.Err != nil {
				return exitCode(result), fmt.Errorf("process exited: %w", result.Err)
			}
			return result.Code, nil
		case err := <-agentDone:
			if err == nil {
				return 0, nil
			}
			return 1, fmt.Errorf("tunnel connection failure: %w", err)
		case <-runCtx.Done():
			return 0, nil
		}
	}
	fmt.Fprintf(a.Stdout, "\nPublic URL:\n%s\n\nForwarding:\n%s → http://127.0.0.1:%d\n\nPress Ctrl+C to stop.\n", publicURL, publicURL, options.HTTPPort)
	if child == nil {
		select {
		case err := <-agentDone:
			if err == nil {
				return 0, nil
			}
			return 1, fmt.Errorf("tunnel connection failure: %w", err)
		case <-runCtx.Done():
			return 0, nil
		}
	}
	select {
	case result := <-child.Done():
		cancel()
		if result.Err != nil {
			return exitCode(result), fmt.Errorf("process exited: %w", result.Err)
		}
		return result.Code, nil
	case err := <-agentDone:
		if err == nil {
			return 0, nil
		}
		return 1, fmt.Errorf("tunnel connection failure: %w", err)
	case <-runCtx.Done():
		return 0, nil
	}
}

func mapTunnelSessionError(name string, err error) error {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Problem.Code {
		case "endpoint_name_unavailable":
			return fmt.Errorf("The endpoint name %q is unavailable.\nChoose another name.", name)
		case "invalid_endpoint_name":
			return fmt.Errorf("invalid endpoint name %q", name)
		}
	}
	return err
}

func (a *App) endpoints(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("invalid arguments: expected 'list' or 'delete'")
	}
	path, err := a.path()
	if err != nil {
		return 1, fmt.Errorf("configuration error: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return 1, fmt.Errorf("configuration error: %w", err)
	}
	legacyAPIURL, legacyToken, ok := cfg.Legacy()
	if !ok {
		return 1, errors.New("legacy gateway credentials are not configured; endpoint ticket integration is deferred")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	switch args[0] {
	case "list":
		set := flag.NewFlagSet("endpoints list", flag.ContinueOnError)
		set.SetOutput(a.Stderr)
		asJSON := set.Bool("json", false, "print JSON")
		if err := set.Parse(args[1:]); err != nil || set.NArg() != 0 {
			return 2, errors.New("invalid arguments for endpoints list")
		}
		values, err := agent.ListEndpoints(ctx, client, legacyAPIURL, legacyToken)
		if err != nil {
			return 1, err
		}
		if *asJSON {
			encoder := json.NewEncoder(a.Stdout)
			encoder.SetIndent("", "  ")
			return 0, encoder.Encode(map[string]any{"endpoints": values})
		}
		writer := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "NAME\tHOSTNAME\tSTATUS")
		for _, value := range values {
			fmt.Fprintf(writer, "%s\t%s\t%s\n", value.Name, value.Hostname, value.Status)
		}
		if err := writer.Flush(); err != nil {
			return 1, err
		}
		return 0, nil
	case "delete":
		force := false
		name := ""
		for _, value := range args[1:] {
			switch {
			case value == "--force":
				force = true
			case strings.HasPrefix(value, "-") || name != "":
				return 2, errors.New("invalid arguments: endpoints delete requires NAME and optional --force")
			default:
				name = strings.ToLower(value)
			}
		}
		if name == "" {
			return 2, errors.New("invalid arguments: endpoints delete requires NAME")
		}
		if err := naming.Validate(name); err != nil {
			return 2, fmt.Errorf("invalid arguments: %w", err)
		}
		if !force {
			fmt.Fprintf(a.Stdout, "Delete endpoint %s? Type its name to confirm: ", name)
			input, readErr := bufio.NewReader(a.Stdin).ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return 1, fmt.Errorf("read confirmation: %w", readErr)
			}
			if strings.TrimSpace(input) != name {
				return 1, errors.New("deletion cancelled")
			}
		}
		if err := agent.DeleteEndpoint(ctx, client, legacyAPIURL, legacyToken, name); err != nil {
			return 1, err
		}
		fmt.Fprintf(a.Stdout, "Endpoint %s deleted.\n", name)
		return 0, nil
	default:
		return 2, fmt.Errorf("invalid arguments: unknown endpoints command %q", args[0])
	}
}

func exitCode(result process.Result) int {
	if result.Code > 0 {
		return result.Code
	}
	return 1
}
