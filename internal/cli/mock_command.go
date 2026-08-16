package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	mockserver "github.com/project-mockingo/mockingo-agent/internal/mock/server"
	"github.com/project-mockingo/mockingo-agent/internal/naming"
)

func (a *App) mock(ctx context.Context, args []string) (int, error) {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			a.mockUsage()
			return 0, nil
		}
	}
	options, err := ParseMock(args)
	if err != nil {
		return 2, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := naming.Validate(options.Name); err != nil {
		return 2, fmt.Errorf("invalid arguments: %w", err)
	}

	loaded, err := loadMockSource(ctx, options.WireMock, options.OpenAPI, func(message string) {
		fmt.Fprintf(a.Stderr, "Warning: %s\n", message)
	})
	if err != nil {
		return 1, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var traffic func(string, string, int, bool)
	if options.Verbose {
		traffic = func(method, path string, status int, matched bool) {
			label := "MOCK"
			if !matched {
				label = "NO MATCH"
			}
			fmt.Fprintf(a.Stdout, "→ %s %s\n← %d %s\n", method, path, status, label)
		}
	}
	server, err := mockserver.Start(runCtx, loaded.Engine, traffic)
	if err != nil {
		return 1, fmt.Errorf("start embedded mock server: %w", err)
	}
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintln(a.Stdout, "Mockingo Mock")
	fmt.Fprintf(a.Stdout, "\nSource:     %s\n%s:%s%d\nEndpoint:   %s\n\n", loaded.Source, loaded.CountLabel, strings.Repeat(" ", max(1, 11-len(loaded.CountLabel))), loaded.Count, options.Name)
	if loaded.Source == "OpenAPI" {
		fmt.Fprintf(a.Stdout, "✓ OpenAPI loaded\n✓ %d mock routes generated\n", loaded.Count)
	} else {
		fmt.Fprintln(a.Stdout, "✓ Mock engine ready")
	}

	exposeOptions := ExposeOptions{
		Name: options.Name, HTTPPort: server.Port(), StartupTimeout: time.Second,
		RequestTimeout: options.RequestTimeout, Verbose: options.Verbose,
		APIURL: options.APIURL, ExpectedGatewayHost: options.ExpectedGatewayHost,
		ProtocolVersion: options.ProtocolVersion, ReconnectEnabled: options.ReconnectEnabled,
		ReconnectInitialDelay: options.ReconnectInitialDelay, ReconnectMaxDelay: options.ReconnectMaxDelay,
		AllowInsecureGateway: options.AllowInsecureGateway, AllowFileCredentials: options.AllowFileCredentials,
	}
	return a.exposeOptions(runCtx, exposeOptions, exposeTarget{embeddedMock: true})
}

func (a *App) mockUsage() {
	fmt.Fprintln(a.Stdout, "Usage: mockingo mock --name NAME (--wiremock PATH | --openapi FILE) [options]")
	fmt.Fprintln(a.Stdout, "")
	fmt.Fprintln(a.Stdout, "Loads static mocks before creating a normal authenticated Mockingo HTTP tunnel.")
	fmt.Fprintln(a.Stdout, "Options: --request-timeout, --verbose, --api-url, --expected-gateway-host, --reconnect, --reconnect-initial-delay, --reconnect-max-delay, --allow-insecure-gateway, --allow-insecure-storage")
}
