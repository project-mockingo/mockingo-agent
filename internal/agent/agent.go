package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/project-mockingo/mockingo-agent/internal/tunnelhttp"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type Config struct {
	LocalPort             int
	RequestTimeout        time.Duration
	OnState               func(string)
	Verbose               func(string, ...any)
	PublicURL             string
	InitialSession        *Session
	AcquireSession        func(context.Context) (Session, error)
	Retryable             func(error) bool
	TemporaryConflict     func(error) bool
	ReconnectEnabled      bool
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
}

// Session contains an ephemeral backend-issued credential. Ticket is consumed
// by exactly one DialContext call and must never be persisted or logged.
type Session struct {
	EndpointID   string
	EndpointName string
	SessionID    string
	ConnectURL   string
	Ticket       string
	PublicURL    string
}

func (s Session) String() string {
	return fmt.Sprintf("{EndpointID:%s EndpointName:%s SessionID:%s ConnectURL:%s Ticket:<redacted> PublicURL:%s}", s.EndpointID, s.EndpointName, s.SessionID, s.ConnectURL, s.PublicURL)
}

type Agent struct {
	config Config
	client *http.Client
}

func New(config Config) *Agent {
	client := &http.Client{
		Timeout: config.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Agent{config: config, client: client}
}

// Run owns the connection state machine until its parent context is cancelled.
// It acquires a fresh backend session before every reconnect attempt.
func (a *Agent) Run(ctx context.Context) error {
	if a.config.AcquireSession == nil {
		return errors.New("backend tunnel-session provider is required")
	}
	return a.runTicket(ctx)
}

type GatewayError struct {
	Status int
	Code   string
}

func (e *GatewayError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("gateway rejected tunnel connection (HTTP %d, code %s)", e.Status, e.Code)
	}
	return fmt.Sprintf("gateway rejected tunnel connection (HTTP %d)", e.Status)
}

func (a *Agent) runTicket(ctx context.Context) error {
	initialDelay := a.config.ReconnectInitialDelay
	if initialDelay <= 0 {
		initialDelay = time.Second
	}
	maxDelay := a.config.ReconnectMaxDelay
	if maxDelay < initialDelay {
		maxDelay = 30 * time.Second
	}
	reconnectEnabled := a.config.ReconnectEnabled
	delay := initialDelay
	connectedOnce := false
	first := true
	pendingMessageShown := false
	var next *Session
	if a.config.InitialSession != nil {
		copy := *a.config.InitialSession
		a.config.InitialSession.Ticket = ""
		next = &copy
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		var session Session
		if next != nil {
			session = *next
			next = nil
		} else {
			var err error
			session, err = a.config.AcquireSession(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if a.config.Retryable == nil || !a.config.Retryable(err) {
					return err
				}
				if !reconnectEnabled && !first {
					return err
				}
				if !pendingMessageShown && a.config.OnState != nil {
					if a.config.TemporaryConflict != nil && a.config.TemporaryConflict(err) {
						a.config.OnState("Waiting for the previous tunnel session to close...")
					} else {
						a.config.OnState("Mockingo control plane unavailable. Retrying...")
					}
					pendingMessageShown = true
				}
				if a.config.Verbose != nil {
					a.config.Verbose("tunnel session request failed: %v", err)
				}
				if err := waitBackoff(ctx, delay); err != nil {
					return nil
				}
				delay = nextDelayMax(delay, maxDelay)
				first = false
				continue
			}
		}
		first = false
		pendingMessageShown = false
		headers := http.Header{"Authorization": []string{"Bearer " + session.Ticket}}
		session.Ticket = ""
		ws, response, dialErr := dialWebSocket(ctx, session.ConnectURL, headers)
		headers.Del("Authorization")
		gatewayErr := decodeGatewayError(response)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if dialErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			if gatewayErr != nil && !retryableGatewayError(gatewayErr, dialErr) {
				return gatewayErr
			}
			if !reconnectEnabled {
				if gatewayErr != nil {
					return gatewayErr
				}
				return fmt.Errorf("connect to gateway: %w", dialErr)
			}
			if a.config.Verbose != nil {
				a.config.Verbose("gateway connection attempt failed (status %d)", gatewayStatus(gatewayErr))
			}
			if err := waitBackoff(ctx, delay); err != nil {
				return nil
			}
			delay = nextDelayMax(delay, maxDelay)
			continue
		}
		delay = initialDelay
		if connectedOnce && a.config.OnState != nil {
			a.config.OnState("Tunnel reconnected.")
		} else if a.config.OnState != nil {
			a.config.OnState("Tunnel connected.")
		}
		connectedOnce = true
		err := a.serveConnection(ctx, ws)
		if ctx.Err() != nil {
			return nil
		}
		if !reconnectEnabled {
			return fmt.Errorf("tunnel disconnected: %w", err)
		}
		if a.config.OnState != nil {
			a.config.OnState("Connection lost. Reconnecting...")
		}
		if a.config.Verbose != nil && err != nil {
			a.config.Verbose("tunnel disconnected: %v", err)
		}
		if err := waitBackoff(ctx, delay); err != nil {
			return nil
		}
		delay = nextDelayMax(delay, maxDelay)
	}
}

// dialWebSocket closes the underlying socket when the owning context is
// cancelled, including while a peer stalls after TCP connect but before the
// WebSocket upgrade completes. Copying DefaultDialer preserves proxy and TLS
// behavior without mutating global state.
func dialWebSocket(ctx context.Context, connectURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	dialer := *websocket.DefaultDialer
	baseDial := dialer.NetDialContext
	if baseDial == nil {
		networkDialer := &net.Dialer{Timeout: dialer.HandshakeTimeout}
		baseDial = networkDialer.DialContext
	}
	dialDone := make(chan struct{})
	dialer.NetDialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		conn, err := baseDial(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-dialDone:
			}
		}()
		return conn, nil
	}
	ws, response, err := dialer.DialContext(ctx, connectURL, headers)
	close(dialDone)
	return ws, response, err
}

func decodeGatewayError(response *http.Response) *GatewayError {
	if response == nil {
		return nil
	}
	value := struct {
		Code string `json:"code"`
	}{}
	if response.Body != nil {
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&value)
	}
	for _, r := range value.Code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			value.Code = ""
			break
		}
	}
	return &GatewayError{Status: response.StatusCode, Code: value.Code}
}

func gatewayStatus(err *GatewayError) int {
	if err == nil {
		return 0
	}
	return err.Status
}

func retryableGatewayError(gatewayErr *GatewayError, dialErr error) bool {
	if gatewayErr == nil {
		var netErr interface{ Temporary() bool }
		return errors.As(dialErr, &netErr) || dialErr != nil
	}
	if gatewayErr.Status >= 500 || gatewayErr.Status == http.StatusUnauthorized {
		return true
	}
	if gatewayErr.Status == http.StatusConflict {
		return gatewayErr.Code == "endpoint_already_connected" || gatewayErr.Code == "tunnel_session_replayed"
	}
	return false
}

func waitBackoff(ctx context.Context, base time.Duration) error {
	jitter := time.Duration(rand.Int63n(int64(base/2) + 1))
	timer := time.NewTimer(base + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextDelayMax(current, maximum time.Duration) time.Duration {
	next := current * 2
	if next > maximum {
		return maximum
	}
	return next
}

type socketWriter struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (w *socketWriter) write(message tunnelprotocol.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteJSON(message)
}

func (a *Agent) serveConnection(parent context.Context, ws *websocket.Conn) error {
	ws.SetReadLimit(tunnelprotocol.MaxMessageSize)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer ws.Close()
	writer := &socketWriter{ws: ws}
	var requests sync.WaitGroup
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-closed:
		}
	}()
	defer close(closed)
	for {
		var message tunnelprotocol.Message
		if err := ws.ReadJSON(&message); err != nil {
			cancel()
			requests.Wait()
			return err
		}
		if message.Version != tunnelprotocol.Version {
			continue
		}
		switch message.Type {
		case tunnelprotocol.TypeRequest:
			requests.Add(1)
			go func() {
				defer requests.Done()
				a.handleRequest(ctx, writer, message)
			}()
		case tunnelprotocol.TypePing:
			_ = writer.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypePong})
		}
	}
}

func (a *Agent) handleRequest(parent context.Context, writer *socketWriter, message tunnelprotocol.Message) {
	respondError := func(code string) {
		_ = writer.write(tunnelprotocol.Message{
			Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeError,
			RequestID: message.RequestID, ErrorCode: code, Error: "local forwarding failed",
		})
	}
	body, err := base64.StdEncoding.DecodeString(message.BodyBase64)
	if err != nil || len(body) > tunnelprotocol.MaxBodySize {
		respondError(tunnelprotocol.ErrorCodeInvalidRequest)
		return
	}
	path, err := url.ParseRequestURI(message.Path)
	if err != nil || path.Scheme != "" || path.Host != "" || len(path.Path) == 0 || path.Path[0] != '/' {
		respondError(tunnelprotocol.ErrorCodeInvalidRequest)
		return
	}
	localURL := "http://127.0.0.1:" + strconv.Itoa(a.config.LocalPort) + path.RequestURI()
	ctx, cancel := context.WithTimeout(parent, a.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, message.Method, localURL, bytes.NewReader(body))
	if err != nil {
		respondError(tunnelprotocol.ErrorCodeInvalidRequest)
		return
	}
	request.Header = tunnelhttp.FilterHeaders(http.Header(message.Headers))
	request.Host = "" // derive the local Host from the fixed URL above
	response, err := a.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			respondError(tunnelprotocol.ErrorCodeTimeout)
		} else {
			respondError(tunnelprotocol.ErrorCodeLocalUnreachable)
		}
		return
	}
	defer response.Body.Close()
	responseBody, err := tunnelprotocol.ReadBody(response.Body)
	if err != nil {
		if errors.Is(err, tunnelprotocol.ErrBodyTooLarge) {
			respondError(tunnelprotocol.ErrorCodeResponseTooLarge)
		} else {
			respondError(tunnelprotocol.ErrorCodeLocalResponseError)
		}
		return
	}
	result := tunnelprotocol.Message{
		Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeResponse,
		RequestID: message.RequestID, Status: response.StatusCode,
		Headers:    tunnelhttp.FilterHeaders(response.Header),
		BodyBase64: base64.StdEncoding.EncodeToString(responseBody),
	}
	_ = writer.write(result)
}

func (a *Agent) String() string {
	return fmt.Sprintf("agent forwarding to 127.0.0.1:%d", a.config.LocalPort)
}
