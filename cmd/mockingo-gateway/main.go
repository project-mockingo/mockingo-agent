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

	"github.com/mockingo/mockingo-cli/internal/gateway"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	server := &http.Server{
		Addr: env("MOCKINGO_GATEWAY_ADDR", ":9090"),
		Handler: gateway.NewServer(gateway.Config{
			BaseDomain:   env("MOCKINGO_BASE_DOMAIN", "mockingo.click"),
			PublicScheme: env("MOCKINGO_PUBLIC_SCHEME", "https"),
			DevToken:     env("MOCKINGO_DEV_TOKEN", "development-token"),
			Logger:       logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("gateway listening", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}
