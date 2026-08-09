package backendcallback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

const maxResponseBytes = 64 << 10

type ConnectedEvent struct {
	SessionID         string    `json:"-"`
	EndpointID        string    `json:"endpointId"`
	GatewayInstanceID string    `json:"gatewayInstanceId"`
	ConnectedAt       time.Time `json:"connectedAt"`
}

type DisconnectedEvent struct {
	SessionID      string    `json:"-"`
	EndpointID     string    `json:"endpointId"`
	DisconnectedAt time.Time `json:"disconnectedAt"`
	Reason         string    `json:"reason,omitempty"`
}

type RejectedEvent struct {
	SessionID  string    `json:"-"`
	EndpointID string    `json:"endpointId"`
	RejectedAt time.Time `json:"rejectedAt"`
	Reason     string    `json:"reason"`
}

type BackendCallbackClient interface {
	Connected(context.Context, ConnectedEvent) error
	Disconnected(context.Context, DisconnectedEvent) error
	Rejected(context.Context, RejectedEvent) error
}

type NoOpClient struct{}

func (NoOpClient) Connected(context.Context, ConnectedEvent) error       { return nil }
func (NoOpClient) Disconnected(context.Context, DisconnectedEvent) error { return nil }
func (NoOpClient) Rejected(context.Context, RejectedEvent) error         { return nil }

type CallbackError struct {
	StatusCode int
	Definitive bool
}

func (e *CallbackError) Error() string { return "backend callback was rejected" }

type Config struct {
	BackendURL     string
	Token          string
	HTTPClient     *http.Client
	Attempts       int
	InitialBackoff time.Duration
	OnResult       func(callbackType string, success bool)
}

type HTTPClient struct{ config Config }

func NewHTTPClient(config Config) *HTTPClient {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if config.Attempts <= 0 {
		config.Attempts = 3
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 100 * time.Millisecond
	}
	config.BackendURL = strings.TrimRight(config.BackendURL, "/")
	return &HTTPClient{config: config}
}

func (c *HTTPClient) Connected(ctx context.Context, event ConnectedEvent) error {
	return c.send(ctx, event.SessionID, "connected", event)
}

func (c *HTTPClient) Disconnected(ctx context.Context, event DisconnectedEvent) error {
	return c.send(ctx, event.SessionID, "disconnected", event)
}

func (c *HTTPClient) Rejected(ctx context.Context, event RejectedEvent) error {
	return c.send(ctx, event.SessionID, "rejected", event)
}

func (c *HTTPClient) send(ctx context.Context, sessionID, callbackType string, event any) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("%s/internal/v1/gateway/tunnel-sessions/%s/%s", c.config.BackendURL, sessionID, callbackType)
	var last error
	for attempt := 0; attempt < c.config.Attempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(payload))
		if requestErr != nil {
			return requestErr
		}
		request.Header.Set("Authorization", "Bearer "+c.config.Token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		if requestID, ok := RequestIDFromContext(ctx); ok {
			request.Header.Set("X-Request-ID", requestID)
		}
		response, requestErr := c.config.HTTPClient.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				c.result(callbackType, true)
				return nil
			}
			definitive := response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests
			last = &CallbackError{StatusCode: response.StatusCode, Definitive: definitive}
			if definitive {
				c.result(callbackType, false)
				return last
			}
		} else {
			last = requestErr
		}
		if attempt+1 < c.config.Attempts {
			delay := c.config.InitialBackoff << attempt
			jitter := time.Duration(rand.Int63n(int64(delay/2) + 1))
			timer := time.NewTimer(delay + jitter)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				c.result(callbackType, false)
				return ctx.Err()
			}
		}
	}
	c.result(callbackType, false)
	if last == nil {
		last = errors.New("backend callback failed")
	}
	return last
}

func (c *HTTPClient) result(callbackType string, success bool) {
	if c.config.OnResult != nil {
		c.config.OnResult(callbackType, success)
	}
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(requestIDKey{}).(string)
	return value, ok && value != ""
}
