package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	mockengine "github.com/project-mockingo/mockingo-agent/internal/mock"
)

type Server struct {
	listener net.Listener
	server   *http.Server
	done     chan error
}

func Start(ctx context.Context, engine mockengine.Engine, log func(string, string, int, bool)) (*Server, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	httpServer := &http.Server{
		Handler:           mockengine.Handler{Engine: engine, Done: ctx.Done(), Log: log},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	result := &Server{listener: listener, server: httpServer, done: make(chan error, 1)}
	go func() { result.done <- httpServer.Serve(listener) }()
	return result, nil
}

func (s *Server) Port() int {
	_, value, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := strconv.Atoi(value)
	return port
}

func (s *Server) Shutdown(ctx context.Context) error {
	err := s.server.Shutdown(ctx)
	if err != nil {
		_ = s.server.Close()
	}
	select {
	case serveErr := <-s.done:
		if serveErr != nil && serveErr != http.ErrServerClosed && err == nil {
			err = serveErr
		}
	default:
	}
	return err
}
