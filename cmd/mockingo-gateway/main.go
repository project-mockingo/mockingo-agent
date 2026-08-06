package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mockingo/mockingo-cli/internal/database"
	"github.com/mockingo/mockingo-cli/internal/endpoint"
	"github.com/mockingo/mockingo-cli/internal/gateway"
	"github.com/mockingo/mockingo-cli/internal/gatewayconfig"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		address := os.Getenv("MOCKINGO_HEALTHCHECK_URL")
		if address == "" {
			address = "http://127.0.0.1:9090/health/ready"
		}
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get(address)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return errors.New("gateway is not ready")
		}
		return nil
	}
	if len(os.Args) > 1 && os.Args[1] != "serve" && os.Args[1] != "migrate" {
		return errors.New("usage: mockingo-gateway [serve|migrate|healthcheck]")
	}
	config, err := gatewayconfig.Load()
	if err != nil {
		return err
	}
	level := new(slog.LevelVar)
	switch config.LogLevel {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var repository endpoint.Repository = endpoint.NewMemoryRepository()
	var readyCheck func(context.Context) error
	if config.DatabaseURL != "" {
		databaseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		pool, openErr := database.Open(databaseCtx, config.DatabaseURL)
		cancel()
		if openErr != nil {
			return openErr
		}
		if len(os.Args) > 1 && os.Args[1] == "migrate" {
			defer pool.Close()
			migrationCtx, migrationCancel := context.WithTimeout(ctx, 30*time.Second)
			defer migrationCancel()
			if err := database.Migrate(migrationCtx, pool); err != nil {
				return err
			}
			logger.Info("database migrations applied", "version", database.RequiredVersion)
			return nil
		}
		readyCtx, readyCancel := context.WithTimeout(ctx, 5*time.Second)
		err = database.Ready(readyCtx, pool)
		readyCancel()
		if err != nil {
			pool.Close()
			return err
		}
		repository = endpoint.NewPostgresRepository(pool)
		readyCheck = func(checkCtx context.Context) error { return database.Ready(checkCtx, pool) }
	} else if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return errors.New("DATABASE_URL is required for migrations")
	}
	trusted, err := gateway.ParseTrustedProxyCIDRs(config.TrustedProxyCIDRs)
	if err != nil {
		repository.Close()
		return err
	}
	defer repository.Close()
	handler := gateway.NewServer(gateway.Config{
		BaseDomain: config.BaseDomain, PublicScheme: config.PublicScheme, APIPublicURL: config.APIPublicURL,
		APIToken: config.APIToken, Repository: repository, TrustedProxyCIDRs: trusted, ReadyCheck: readyCheck,
		MetricsEnabled: config.MetricsEnabled, Logger: logger,
	})
	server := &http.Server{Addr: config.Address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	serveError := make(chan error, 1)
	go func() {
		logger.Info("gateway listening", "address", server.Addr, "base_domain", config.BaseDomain)
		serveError <- server.ListenAndServe()
	}()
	select {
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	handler.BeginShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	_ = handler.Shutdown(shutdownCtx)
	logger.Info("gateway stopped")
	return nil
}
