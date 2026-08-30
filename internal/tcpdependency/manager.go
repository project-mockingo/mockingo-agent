package tcpdependency

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type Config struct {
	Emit             func(tunnelprotocol.TCPConnectionEvent) bool
	OnError          func(string, error)
	ProgressInterval time.Duration
}

type Manager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	config    Config
	mu        sync.Mutex
	listeners map[string]*listenerState
	wg        sync.WaitGroup
}

type listenerState struct {
	definition tunnelprotocol.TCPDependency
	listener   net.Listener
}

func New(parent context.Context, config Config) *Manager {
	ctx, cancel := context.WithCancel(parent)
	return &Manager{ctx: ctx, cancel: cancel, config: config, listeners: make(map[string]*listenerState)}
}

func (m *Manager) Replace(snapshot tunnelprotocol.TCPDependencySnapshot) error {
	message := tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPDependencyConfig, TCPDependencies: &snapshot}
	if err := tunnelprotocol.Validate(message); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	wanted := make(map[string]tunnelprotocol.TCPDependency, len(snapshot.Dependencies))
	for _, definition := range snapshot.Dependencies {
		wanted[definition.ID] = definition
	}
	for id, current := range m.listeners {
		definition, keep := wanted[id]
		if keep && definition == current.definition {
			delete(wanted, id)
			continue
		}
		_ = current.listener.Close()
		delete(m.listeners, id)
	}
	for id, definition := range wanted {
		listener, err := net.Listen("tcp", net.JoinHostPort(definition.ListenHost, strconv.Itoa(definition.ListenPort)))
		if err != nil {
			if m.config.OnError != nil {
				m.config.OnError(definition.Name, err)
			}
			continue
		}
		state := &listenerState{definition: definition, listener: listener}
		m.listeners[id] = state
		m.wg.Add(1)
		go m.accept(state)
	}
	return nil
}

func (m *Manager) accept(state *listenerState) {
	defer m.wg.Done()
	for {
		client, err := state.listener.Accept()
		if err != nil {
			return
		}
		m.wg.Add(1)
		go func() { defer m.wg.Done(); m.proxy(state.definition, client) }()
	}
}

func (m *Manager) proxy(definition tunnelprotocol.TCPDependency, client net.Conn) {
	defer client.Close()
	id := uuid.NewString()
	started := time.Now().UTC()
	sourceHost, sourcePort := split(client.RemoteAddr())
	event := tunnelprotocol.TCPConnectionEvent{
		ID: id, DependencyID: definition.ID, TrafficType: "DEPENDENCY", State: "OPEN", StartedAt: started,
		SourceHost: sourceHost, SourcePort: sourcePort,
		ListenHost: definition.ListenHost, ListenPort: definition.ListenPort,
		TargetHost: definition.TargetHost, TargetPort: definition.TargetPort,
	}
	m.emit(event)
	state, closeReason, errorText := "CLOSED", "normal", ""
	var clientToServer atomic.Int64
	var serverToClient atomic.Int64
	defer func() {
		ended := time.Now().UTC()
		event.State, event.EndedAt, event.LastActivityAt = state, ended, ended
		event.DurationMS = ended.Sub(started).Milliseconds()
		event.BytesClientToServer, event.BytesServerToClient = clientToServer.Load(), serverToClient.Load()
		event.CloseReason, event.Error = closeReason, errorText
		m.emit(event)
	}()
	backend, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(m.ctx, "tcp", net.JoinHostPort(definition.TargetHost, strconv.Itoa(definition.TargetPort)))
	if err != nil {
		state, closeReason, errorText = "FAILED", "target_unavailable", err.Error()
		return
	}
	defer backend.Close()
	progressInterval := m.config.ProgressInterval
	if progressInterval <= 0 {
		progressInterval = 5 * time.Second
	}
	progressStop := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		var lastClientToServer, lastServerToClient int64
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-progressStop:
				return
			case observedAt := <-ticker.C:
				toServer, fromServer := clientToServer.Load(), serverToClient.Load()
				if toServer == lastClientToServer && fromServer == lastServerToClient {
					continue
				}
				lastClientToServer, lastServerToClient = toServer, fromServer
				progress := event
				progress.DurationMS = observedAt.Sub(started).Milliseconds()
				progress.LastActivityAt = observedAt.UTC()
				progress.BytesClientToServer = toServer
				progress.BytesServerToClient = fromServer
				m.emit(progress)
			}
		}
	}()
	defer func() {
		close(progressStop)
		<-progressDone
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-m.ctx.Done():
			_ = client.Close()
			_ = backend.Close()
		case <-done:
		}
	}()
	errorsChannel := make(chan error, 2)
	go func() {
		copyErr := copyStream(backend, client, &clientToServer)
		if closer, ok := backend.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		errorsChannel <- copyErr
	}()
	go func() {
		copyErr := copyStream(client, backend, &serverToClient)
		if closer, ok := client.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		errorsChannel <- copyErr
	}()
	for range 2 {
		if copyErr := <-errorsChannel; copyErr != nil && !errors.Is(copyErr, net.ErrClosed) {
			closeReason, errorText = "connection_error", copyErr.Error()
		}
	}
}

func copyStream(destination io.Writer, source io.Reader, count *atomic.Int64) error {
	buffer := make([]byte, tunnelprotocol.MaxTCPFramePayload)
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			if writeErr := writeAll(destination, buffer[:n]); writeErr != nil {
				return writeErr
			}
			count.Add(int64(n))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func writeAll(destination io.Writer, value []byte) error {
	for len(value) > 0 {
		n, err := destination.Write(value)
		if err != nil {
			return err
		}
		value = value[n:]
	}
	return nil
}

func split(address net.Addr) (string, int) {
	host, rawPort, err := net.SplitHostPort(address.String())
	if err != nil {
		return address.String(), 0
	}
	port, _ := strconv.Atoi(rawPort)
	return host, port
}

func (m *Manager) emit(event tunnelprotocol.TCPConnectionEvent) {
	if m.config.Emit != nil {
		m.config.Emit(event)
	}
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	for id, state := range m.listeners {
		delete(m.listeners, id)
		_ = state.listener.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
