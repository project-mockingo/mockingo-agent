package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

type Config struct {
	ConnectURL     string
	SessionToken   string
	LocalPort      int
	RequestTimeout time.Duration
	OnState        func(string)
	Verbose        func(string, ...any)
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

// Run reconnects until its parent context is cancelled. A tunnel registration
// and session token stay unchanged across those connections.
func (a *Agent) Run(ctx context.Context) error {
	delay := time.Second
	connectedOnce := false
	for {
		if ctx.Err() != nil {
			return nil
		}
		headers := http.Header{"Authorization": []string{"Bearer " + a.config.SessionToken}}
		ws, response, err := websocket.DefaultDialer.DialContext(ctx, a.config.ConnectURL, headers)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if a.config.Verbose != nil {
				a.config.Verbose("tunnel connection failed: %v", err)
			}
			if err := waitBackoff(ctx, delay); err != nil {
				return nil
			}
			delay = nextDelay(delay)
			continue
		}
		if connectedOnce && a.config.OnState != nil {
			a.config.OnState("Reconnected to Mockingo gateway.")
		} else if a.config.OnState != nil {
			a.config.OnState("Connected to Mockingo gateway.")
		}
		connectedOnce = true
		delay = time.Second
		err = a.serveConnection(ctx, ws)
		if ctx.Err() != nil {
			return nil
		}
		if a.config.OnState != nil {
			a.config.OnState("Connection lost; reconnecting...")
		}
		if a.config.Verbose != nil && err != nil {
			a.config.Verbose("tunnel disconnected: %v", err)
		}
	}
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

func nextDelay(current time.Duration) time.Duration {
	next := current * 2
	if next > 15*time.Second {
		return 15 * time.Second
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
		respondError("invalid_request")
		return
	}
	path, err := url.ParseRequestURI(message.Path)
	if err != nil || path.Scheme != "" || path.Host != "" || len(path.Path) == 0 || path.Path[0] != '/' {
		respondError("invalid_request")
		return
	}
	localURL := "http://127.0.0.1:" + strconv.Itoa(a.config.LocalPort) + path.RequestURI()
	ctx, cancel := context.WithTimeout(parent, a.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, message.Method, localURL, bytes.NewReader(body))
	if err != nil {
		respondError("invalid_request")
		return
	}
	request.Header = tunnelprotocol.FilterHeaders(http.Header(message.Headers))
	request.Host = "" // derive the local Host from the fixed URL above
	response, err := a.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			respondError("timeout")
		} else {
			respondError("local_unreachable")
		}
		return
	}
	defer response.Body.Close()
	responseBody, err := tunnelprotocol.ReadBody(response.Body)
	if err != nil {
		if errors.Is(err, tunnelprotocol.ErrBodyTooLarge) {
			respondError("response_too_large")
		} else {
			respondError("local_response_error")
		}
		return
	}
	result := tunnelprotocol.Message{
		Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeResponse,
		RequestID: message.RequestID, Status: response.StatusCode,
		Headers:    tunnelprotocol.FilterHeaders(response.Header),
		BodyBase64: base64.StdEncoding.EncodeToString(responseBody),
	}
	_ = writer.write(result)
}

func (a *Agent) String() string {
	return fmt.Sprintf("agent forwarding to 127.0.0.1:%d", a.config.LocalPort)
}
