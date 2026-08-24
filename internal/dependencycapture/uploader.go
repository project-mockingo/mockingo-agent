package dependencycapture

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type CaptureSession struct {
	EndpointID   string
	EndpointName string
	SessionID    string
	ConnectURL   string
	Ticket       string
}

func (s CaptureSession) String() string {
	return fmt.Sprintf("{EndpointID:%s EndpointName:%s SessionID:%s ConnectURL:%s Ticket:<redacted>}", s.EndpointID, s.EndpointName, s.SessionID, s.ConnectURL)
}

type UploaderConfig struct {
	InitialSession        *CaptureSession
	AcquireSession        func(context.Context) (CaptureSession, error)
	Retryable             func(error) bool
	QueueSize             int
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
	OnState               func(string)
	OnDrop                func()
	OnSent                func()
	Verbose               func(string, ...any)
}

type Uploader struct {
	config          UploaderConfig
	queue           chan tunnelprotocol.DependencyInteraction
	dropMu          sync.Mutex
	lastDropWarning time.Time
}

func NewUploader(config UploaderConfig) *Uploader {
	if config.QueueSize <= 0 {
		config.QueueSize = 100
	}
	return &Uploader{config: config, queue: make(chan tunnelprotocol.DependencyInteraction, config.QueueSize)}
}

func (u *Uploader) Enqueue(event tunnelprotocol.DependencyInteraction) bool {
	message := tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeDependencyInteraction, Dependency: &event}
	if err := tunnelprotocol.Validate(message); err != nil {
		u.dropped()
		return false
	}
	select {
	case u.queue <- event:
		return true
	default:
		u.dropped()
		return false
	}
}

func (u *Uploader) dropped() {
	u.dropMu.Lock()
	defer u.dropMu.Unlock()
	if time.Since(u.lastDropWarning) < 10*time.Second {
		return
	}
	u.lastDropWarning = time.Now()
	if u.config.OnDrop != nil {
		u.config.OnDrop()
	}
}

func (u *Uploader) Run(ctx context.Context) error {
	if u.config.AcquireSession == nil {
		return errors.New("dependency capture session provider is required")
	}
	initialDelay := u.config.ReconnectInitialDelay
	if initialDelay <= 0 {
		initialDelay = time.Second
	}
	maxDelay := u.config.ReconnectMaxDelay
	if maxDelay < initialDelay {
		maxDelay = 30 * time.Second
	}
	delay := initialDelay
	connectedOnce := false
	var next *CaptureSession
	if u.config.InitialSession != nil {
		copy := *u.config.InitialSession
		u.config.InitialSession.Ticket = ""
		next = &copy
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		var session CaptureSession
		if next != nil {
			session = *next
			next = nil
		} else {
			var err error
			session, err = u.config.AcquireSession(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if u.config.Retryable == nil || !u.config.Retryable(err) {
					return err
				}
				if u.config.Verbose != nil {
					u.config.Verbose("dependency capture session request failed: %v", err)
				}
				if err := waitCaptureBackoff(ctx, delay); err != nil {
					return nil
				}
				delay = nextCaptureDelay(delay, maxDelay)
				continue
			}
		}
		headers := http.Header{"Authorization": []string{"Bearer " + session.Ticket}}
		session.Ticket = ""
		ws, response, err := dialCaptureWebSocket(ctx, session.ConnectURL, headers)
		headers.Del("Authorization")
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if response != nil && response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusConflict {
				return fmt.Errorf("gateway rejected dependency capture (HTTP %d)", response.StatusCode)
			}
			if u.config.Verbose != nil {
				u.config.Verbose("dependency capture connection failed: %v", err)
			}
			if err := waitCaptureBackoff(ctx, delay); err != nil {
				return nil
			}
			delay = nextCaptureDelay(delay, maxDelay)
			continue
		}
		delay = initialDelay
		if u.config.OnState != nil {
			if connectedOnce {
				u.config.OnState("Dependency capture reconnected.")
			} else {
				u.config.OnState("Dependency capture connected.")
			}
		}
		connectedOnce = true
		if err := u.serveConnection(ctx, ws); ctx.Err() != nil {
			return nil
		} else if u.config.Verbose != nil {
			u.config.Verbose("dependency capture connection lost: %v", err)
		}
		if u.config.OnState != nil {
			u.config.OnState("Dependency capture connection lost. Proxy forwarding continues; reconnecting...")
		}
		if err := waitCaptureBackoff(ctx, delay); err != nil {
			return nil
		}
		delay = nextCaptureDelay(delay, maxDelay)
	}
}

func (u *Uploader) serveConnection(ctx context.Context, ws *websocket.Conn) error {
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
	readErrors := make(chan error, 1)
	go func() {
		for {
			var message tunnelprotocol.Message
			if err := ws.ReadJSON(&message); err != nil {
				readErrors <- err
				return
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		var message tunnelprotocol.Message
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErrors:
			return err
		case event := <-u.queue:
			message = tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeDependencyInteraction, Dependency: &event}
		case <-ticker.C:
			message = tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypePing}
		}
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := ws.WriteJSON(message); err != nil {
			if message.Dependency != nil {
				u.dropped()
			}
			return err
		}
		if message.Dependency != nil && u.config.OnSent != nil {
			u.config.OnSent()
		}
	}
}

func dialCaptureWebSocket(ctx context.Context, connectURL string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	dialer := *websocket.DefaultDialer
	dialer.Proxy = nil
	networkDialer := &net.Dialer{Timeout: dialer.HandshakeTimeout}
	dialer.NetDialContext = networkDialer.DialContext
	return dialer.DialContext(ctx, connectURL, headers)
}
func waitCaptureBackoff(ctx context.Context, base time.Duration) error {
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
func nextCaptureDelay(current, maximum time.Duration) time.Duration {
	next := current * 2
	if next > maximum {
		return maximum
	}
	return next
}
