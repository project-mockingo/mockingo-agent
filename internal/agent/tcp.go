package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

const tcpQueueDepth = 8

type tcpCommand struct {
	kind string
	data []byte
}

type localTCPConnection struct {
	conn   net.Conn
	cancel context.CancelFunc
	input  chan tcpCommand
}

type tcpRuntime struct {
	ctx       context.Context
	localPort int
	writer    *socketWriter
	verbose   func(string, ...any)
	mu        sync.Mutex
	conns     map[string]*localTCPConnection
	wg        sync.WaitGroup
}

func newTCPRuntime(ctx context.Context, localPort int, writer *socketWriter, verbose func(string, ...any)) *tcpRuntime {
	return &tcpRuntime{ctx: ctx, localPort: localPort, writer: writer, verbose: verbose, conns: make(map[string]*localTCPConnection)}
}

func (r *tcpRuntime) openAsync(id string) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.open(id)
	}()
}

func (r *tcpRuntime) open(id string) {
	r.mu.Lock()
	if _, exists := r.conns[id]; exists {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.localPort)))
	if err != nil {
		_ = r.writer.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPError, ConnectionID: id, ErrorCode: "local_unreachable", Error: "local TCP service is unavailable"})
		return
	}
	ctx, cancel := context.WithCancel(r.ctx)
	state := &localTCPConnection{conn: conn, cancel: cancel, input: make(chan tcpCommand, tcpQueueDepth)}
	r.mu.Lock()
	if _, exists := r.conns[id]; exists {
		r.mu.Unlock()
		cancel()
		_ = conn.Close()
		return
	}
	r.conns[id] = state
	r.mu.Unlock()
	if err := r.writer.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPOpened, ConnectionID: id}); err != nil {
		r.remove(id, state)
		return
	}
	r.wg.Add(2)
	go r.writeLocal(ctx, id, state)
	go r.readLocal(ctx, id, state)
}

func (r *tcpRuntime) handle(message tunnelprotocol.Message) {
	r.mu.Lock()
	state := r.conns[message.ConnectionID]
	r.mu.Unlock()
	if state == nil {
		return
	}
	command := tcpCommand{kind: message.Type}
	if message.Type == tunnelprotocol.TypeTCPData {
		decoded, err := base64.StdEncoding.DecodeString(message.DataBase64)
		if err != nil || len(decoded) == 0 || len(decoded) > tunnelprotocol.MaxTCPFramePayload {
			r.remove(message.ConnectionID, state)
			return
		}
		command.data = decoded
	}
	select {
	case state.input <- command:
	case <-r.ctx.Done():
	}
}

func (r *tcpRuntime) writeLocal(ctx context.Context, id string, state *localTCPConnection) {
	defer r.wg.Done()
	for {
		select {
		case command := <-state.input:
			switch command.kind {
			case tunnelprotocol.TypeTCPData:
				if err := writeAll(state.conn, command.data); err != nil {
					_ = r.writer.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPError, ConnectionID: id, ErrorCode: "local_write_failed", Error: "local TCP connection failed"})
					r.remove(id, state)
					return
				}
			case tunnelprotocol.TypeTCPHalfClose:
				if closer, ok := state.conn.(interface{ CloseWrite() error }); ok {
					_ = closer.CloseWrite()
				}
			case tunnelprotocol.TypeTCPClose, tunnelprotocol.TypeTCPError:
				r.remove(id, state)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *tcpRuntime) readLocal(ctx context.Context, id string, state *localTCPConnection) {
	defer r.wg.Done()
	buffer := make([]byte, tunnelprotocol.MaxTCPFramePayload)
	for {
		n, err := state.conn.Read(buffer)
		if n > 0 {
			message := tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPData, ConnectionID: id, DataBase64: base64.StdEncoding.EncodeToString(buffer[:n])}
			if writeErr := r.writer.write(message); writeErr != nil {
				r.remove(id, state)
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = r.writer.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPHalfClose, ConnectionID: id})
			} else if ctx.Err() == nil {
				_ = r.writer.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPError, ConnectionID: id, ErrorCode: "local_read_failed", Error: "local TCP connection failed"})
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		n, err := writer.Write(value)
		if err != nil {
			return err
		}
		value = value[n:]
	}
	return nil
}

func (r *tcpRuntime) remove(id string, expected *localTCPConnection) {
	r.mu.Lock()
	if r.conns[id] == expected {
		delete(r.conns, id)
		expected.cancel()
		_ = expected.conn.Close()
	}
	r.mu.Unlock()
}

func (r *tcpRuntime) closeAll() {
	r.mu.Lock()
	for id, state := range r.conns {
		delete(r.conns, id)
		state.cancel()
		_ = state.conn.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
