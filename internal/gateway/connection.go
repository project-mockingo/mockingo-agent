package gateway

import (
	"context"
	"errors"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/mockingo/mockingo-cli/pkg/tunnelprotocol"
)

var errConnectionClosed = errors.New("tunnel connection closed")

// connection owns exactly one reader. Writes are serialized because gorilla
// permits concurrent readers and writers, but not multiple concurrent writers.
type connection struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan tunnelprotocol.Message
	done      chan struct{}
	closeOnce sync.Once
}

func newConnection(ws *websocket.Conn) *connection {
	ws.SetReadLimit(tunnelprotocol.MaxMessageSize)
	return &connection{ws: ws, pending: make(map[string]chan tunnelprotocol.Message), done: make(chan struct{})}
}

func (c *connection) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close()
	})
}

func (c *connection) readLoop() error {
	for {
		var message tunnelprotocol.Message
		if err := c.ws.ReadJSON(&message); err != nil {
			return err
		}
		if message.Version != tunnelprotocol.Version {
			continue
		}
		switch message.Type {
		case tunnelprotocol.TypeResponse, tunnelprotocol.TypeError:
			c.pendingMu.Lock()
			response := c.pending[message.RequestID]
			c.pendingMu.Unlock()
			if response != nil {
				select {
				case response <- message:
				default:
				}
			}
		case tunnelprotocol.TypePing:
			_ = c.write(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypePong})
		}
	}
}

func (c *connection) write(message tunnelprotocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteJSON(message)
}

func (c *connection) roundTrip(ctx context.Context, request tunnelprotocol.Message) (tunnelprotocol.Message, error) {
	response := make(chan tunnelprotocol.Message, 1)
	c.pendingMu.Lock()
	c.pending[request.RequestID] = response
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, request.RequestID)
		c.pendingMu.Unlock()
	}()
	if err := c.write(request); err != nil {
		return tunnelprotocol.Message{}, err
	}
	select {
	case message := <-response:
		return message, nil
	case <-c.done:
		return tunnelprotocol.Message{}, errConnectionClosed
	case <-ctx.Done():
		return tunnelprotocol.Message{}, ctx.Err()
	}
}
