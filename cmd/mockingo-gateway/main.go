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
	"github.com/mockingo/mockingo-cli/internal/gateway/backendcallback"
	"github.com/mockingo/mockingo-cli/internal/gateway/ticketauth"
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
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		return errors.New("usage: mockingo-gateway [serve|healthcheck]")
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

	var catalog endpoint.Catalog = endpoint.NewMemoryCatalog()
	if config.DatabaseURL != "" {
		databaseCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		pool, openErr := database.Open(databaseCtx, config.DatabaseURL)
		cancel()
		if openErr != nil {
			return openErr
		}
		catalog = endpoint.NewPostgresCatalog(pool)
	}
	trusted, err := gateway.ParseTrustedProxyCIDRs(config.TrustedProxyCIDRs)
	if err != nil {
		catalog.Close()
		return err
	}
	defer catalog.Close()
	metrics := gateway.NewMetrics()
	var verifier *ticketauth.Verifier
	var jwksCache *ticketauth.JWKSCache
	var callbackClient backendcallback.BackendCallbackClient
	{
		jwksHTTPClient := &http.Client{
			Timeout:       config.JWKSHTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		jwksCache = ticketauth.NewJWKSCache(ticketauth.JWKSConfig{
			URL: config.BackendJWKSURL, HTTPClient: jwksHTTPClient,
			RefreshInterval: config.JWKSRefreshInterval, OnRefresh: metrics.ObserveJWKSRefresh,
		})
		loadCtx, loadCancel := context.WithTimeout(ctx, config.JWKSHTTPTimeout)
		err = jwksCache.Load(loadCtx)
		loadCancel()
		if err != nil {
			return errors.New("load tunnel ticket JWKS: " + err.Error())
		}
		jwksCache.Start(ctx)
		defer jwksCache.Close()
		verifier = ticketauth.NewVerifier(ticketauth.Config{
			Issuer: config.TicketIssuer, Audience: config.TicketAudience,
			ProtocolVersion: config.TunnelProtocolVersion, ClockSkew: config.TicketClockSkew,
			Keys: jwksCache, ReplayMax: config.ReplayCacheMaxEntries,
			OnValidation: metrics.ObserveTicketValidation, OnReplay: metrics.ObserveTicketReplay,
		})
		verifier.StartReplayCleanup(ctx, time.Minute)
		defer verifier.Close()
		callbackHTTPClient := &http.Client{
			Timeout:       config.BackendCallbackTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		callbackClient = backendcallback.NewHTTPClient(backendcallback.Config{
			BackendURL: config.BackendURL, Token: config.BackendCallbackToken,
			HTTPClient: callbackHTTPClient, Attempts: config.BackendCallbackAttempts,
			InitialBackoff: config.BackendCallbackBackoff, OnResult: metrics.ObserveCallback,
		})
	}
	handler := gateway.NewServer(gateway.Config{
		BaseDomain: config.BaseDomain, GatewayHost: config.GatewayHost, PublicScheme: config.PublicScheme,
		Catalog: catalog, TrustedProxyCIDRs: trusted,
		MetricsEnabled: config.MetricsEnabled, Metrics: metrics, Logger: logger,
		TicketVerifier: verifier,
		CallbackClient: callbackClient, GatewayInternalToken: config.GatewayInternalToken,
		GatewayInstanceID: config.GatewayInstanceID, InternalStatusMaxBatch: config.InternalStatusMaxBatch,
		BackendCallbackBudget: callbackBudget(config.BackendCallbackTimeout, config.BackendCallbackBackoff, config.BackendCallbackAttempts),
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

func callbackBudget(timeout, backoff time.Duration, attempts int) time.Duration {
	budget := timeout * time.Duration(attempts)
	for attempt := 0; attempt+1 < attempts; attempt++ {
		budget += (backoff << attempt) * 2
	}
	return budget
}
