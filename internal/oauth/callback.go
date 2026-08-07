package oauth

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type CallbackResult struct {
	Code             string
	OAuthError       string
	ErrorDescription string
}

type CallbackServer struct {
	listener net.Listener
	server   *http.Server
	result   chan callbackOutcome
	once     sync.Once
}

type callbackOutcome struct {
	result CallbackResult
	err    error
}

var callbackPage = template.Must(template.New("callback").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Mockingo CLI</title></head><body><main><h1>{{.Title}}</h1><p>You may close this browser window and return to the terminal.</p></main></body></html>`))

func StartCallback(host string, port int, path, expectedState string) (*CallbackServer, string, error) {
	if expectedState == "" {
		return nil, "", errors.New("callback state must not be empty")
	}
	redirect, err := RedirectURI(host, port, path)
	if err != nil {
		return nil, "", err
	}
	network := "tcp6"
	if net.ParseIP(host).To4() != nil {
		network = "tcp4"
	}
	listener, err := net.Listen(network, net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return nil, "", fmt.Errorf("callback port %d is unavailable: %w", port, err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	redirect, err = RedirectURI(host, actualPort, path)
	if err != nil {
		listener.Close()
		return nil, "", err
	}
	callback := &CallbackServer{listener: listener, result: make(chan callbackOutcome, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(r.RequestURI) > 8192 {
			http.Error(w, "Callback URL is too large", http.StatusRequestURITooLong)
			callback.complete(callbackOutcome{err: errors.New("OAuth callback URL is too large")})
			return
		}
		query := r.URL.Query()
		state := query.Get("state")
		if state == "" || state != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			_ = callbackPage.Execute(w, map[string]string{"Title": "Mockingo CLI authorization failed."})
			callback.complete(callbackOutcome{err: errors.New("OAuth callback state is missing or invalid")})
			return
		}
		if oauthError := query.Get("error"); oauthError != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = callbackPage.Execute(w, map[string]string{"Title": "Authorization was denied."})
			callback.complete(callbackOutcome{result: CallbackResult{OAuthError: oauthError, ErrorDescription: query.Get("error_description")}})
			return
		}
		code := query.Get("code")
		if code == "" || strings.ContainsAny(code, "\r\n") {
			w.WriteHeader(http.StatusBadRequest)
			_ = callbackPage.Execute(w, map[string]string{"Title": "Mockingo CLI authorization failed."})
			callback.complete(callbackOutcome{err: errors.New("OAuth callback is missing an authorization code")})
			return
		}
		_ = callbackPage.Execute(w, map[string]string{"Title": "Mockingo CLI authorization complete."})
		callback.complete(callbackOutcome{result: CallbackResult{Code: code}})
	})
	callback.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		err := callback.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			callback.complete(callbackOutcome{err: fmt.Errorf("OAuth callback listener failed: %w", err)})
		}
	}()
	return callback, redirect, nil
}

func (c *CallbackServer) complete(outcome callbackOutcome) {
	c.once.Do(func() { c.result <- outcome })
}

func (c *CallbackServer) Wait(ctx context.Context) (CallbackResult, error) {
	select {
	case outcome := <-c.result:
		return outcome.result, outcome.err
	case <-ctx.Done():
		return CallbackResult{}, ctx.Err()
	}
}

func (c *CallbackServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
