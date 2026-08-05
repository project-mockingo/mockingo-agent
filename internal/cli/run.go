package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mockingo/mockingo-cli/internal/agent"
	"github.com/mockingo/mockingo-cli/internal/config"
	"github.com/mockingo/mockingo-cli/internal/naming"
	"github.com/mockingo/mockingo-cli/internal/process"
	"github.com/mockingo/mockingo-cli/internal/readiness"
)

type App struct {
	Stdout     io.Writer
	Stderr     io.Writer
	ConfigPath string
}

func New() *App { return &App{Stdout: os.Stdout, Stderr: os.Stderr} }

func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}
	var err error
	var code int
	switch args[0] {
	case "login":
		err = a.login(args[1:])
	case "expose":
		code, err = a.expose(ctx, args[1:])
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
	fmt.Fprintln(a.Stdout, "  mockingo login --api-url URL --token TOKEN")
	fmt.Fprintln(a.Stdout, "  mockingo expose --name NAME --http PORT [options] [-- command args...]")
}

func (a *App) path() (string, error) {
	if a.ConfigPath != "" {
		return a.ConfigPath, nil
	}
	return config.Path()
}

func (a *App) login(args []string) error {
	set := flag.NewFlagSet("login", flag.ContinueOnError)
	set.SetOutput(a.Stderr)
	apiURL := set.String("api-url", "", "gateway API URL")
	token := set.String("token", "", "gateway token")
	if err := set.Parse(args); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if set.NArg() != 0 || *apiURL == "" || *token == "" {
		return errors.New("invalid arguments: --api-url and --token are required")
	}
	parsed, err := url.ParseRequestURI(*apiURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("configuration error: --api-url must be an http or https URL without credentials")
	}
	path, err := a.path()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	if err := config.Save(path, config.Config{APIURL: strings.TrimRight(*apiURL, "/"), Token: *token}); err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	fmt.Fprintf(a.Stdout, "Configuration saved to %s\n", path)
	return nil
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
		return 1, fmt.Errorf("configuration error: %w", err)
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

	httpClient := &http.Client{Timeout: 30 * time.Second}
	registration, err := agent.Register(runCtx, httpClient, cfg.APIURL, cfg.Token, options.Name, options.HTTPPort)
	if err != nil {
		return 1, fmt.Errorf("gateway registration failure: %w", err)
	}
	defer func() {
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer deleteCancel()
		_ = agent.Delete(deleteCtx, httpClient, cfg.APIURL, cfg.Token, registration.ID)
	}()

	state := func(message string) { fmt.Fprintln(a.Stdout, message) }
	var verbose func(string, ...any)
	if options.Verbose {
		verbose = func(format string, values ...any) { fmt.Fprintf(a.Stderr, "debug: "+format+"\n", values...) }
	}
	tunnelAgent := agent.New(agent.Config{
		ConnectURL: registration.ConnectURL, SessionToken: registration.SessionToken,
		LocalPort: options.HTTPPort, RequestTimeout: options.RequestTimeout,
		OnState: state, Verbose: verbose,
	})
	agentDone := make(chan error, 1)
	go func() { agentDone <- tunnelAgent.Run(runCtx) }()

	fmt.Fprintf(a.Stdout, "\nPublic endpoint:\n%s\n\nPress Ctrl+C to stop.\n", registration.PublicURL)
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

func exitCode(result process.Result) int {
	if result.Code > 0 {
		return result.Code
	}
	return 1
}
