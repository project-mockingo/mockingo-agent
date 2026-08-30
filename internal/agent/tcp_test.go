package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

func TestTCPFramesStreamThroughLocalServiceWithHalfClose(t *testing.T) {
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	request := append([]byte{0x00, 0xff, 0xfe}, bytes.Repeat([]byte{0x80, 0x41, 0x00}, 30000)...)
	response := append([]byte{0xff, 0x00}, bytes.Repeat([]byte{0x7f, 0x81}, 40000)...)
	localResult := make(chan error, 1)
	go func() {
		conn, acceptErr := local.Accept()
		if acceptErr != nil {
			localResult <- acceptErr
			return
		}
		defer conn.Close()
		got, readErr := io.ReadAll(conn)
		if readErr != nil {
			localResult <- readErr
			return
		}
		if !bytes.Equal(got, request) {
			localResult <- fmt.Errorf("local request mismatch: got %d, want %d", len(got), len(request))
			return
		}
		_, writeErr := conn.Write(response)
		localResult <- writeErr
	}()

	connected := make(chan struct{}, 1)
	gatewayResult := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, upgradeErr := upgrader.Upgrade(w, r, nil)
		if upgradeErr != nil {
			gatewayResult <- upgradeErr
			return
		}
		defer ws.Close()
		connected <- struct{}{}
		id := "11111111-1111-1111-1111-111111111111"
		if writeErr := ws.WriteJSON(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPOpen, ConnectionID: id}); writeErr != nil {
			gatewayResult <- writeErr
			return
		}
		var opened tunnelprotocol.Message
		if readErr := ws.ReadJSON(&opened); readErr != nil || opened.Type != tunnelprotocol.TypeTCPOpened {
			gatewayResult <- fmt.Errorf("open response: %#v, %v", opened, readErr)
			return
		}
		for offset := 0; offset < len(request); offset += tunnelprotocol.MaxTCPFramePayload {
			end := min(offset+tunnelprotocol.MaxTCPFramePayload, len(request))
			if writeErr := ws.WriteJSON(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPData, ConnectionID: id, DataBase64: base64.StdEncoding.EncodeToString(request[offset:end])}); writeErr != nil {
				gatewayResult <- writeErr
				return
			}
		}
		if writeErr := ws.WriteJSON(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPHalfClose, ConnectionID: id}); writeErr != nil {
			gatewayResult <- writeErr
			return
		}
		var got []byte
		for {
			var message tunnelprotocol.Message
			if readErr := ws.ReadJSON(&message); readErr != nil {
				gatewayResult <- readErr
				return
			}
			switch message.Type {
			case tunnelprotocol.TypeTCPData:
				value, decodeErr := base64.StdEncoding.DecodeString(message.DataBase64)
				if decodeErr != nil {
					gatewayResult <- decodeErr
					return
				}
				got = append(got, value...)
			case tunnelprotocol.TypeTCPHalfClose:
				if !bytes.Equal(got, response) {
					gatewayResult <- fmt.Errorf("gateway response mismatch: got %d, want %d", len(got), len(response))
					return
				}
				_ = ws.WriteJSON(tunnelprotocol.Message{Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeTCPClose, ConnectionID: id})
				gatewayResult <- nil
				return
			}
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connectURL := strings.Replace(server.URL, "http://", "ws://", 1)
	session := Session{ConnectURL: connectURL, Ticket: "ticket"}
	runtime := New(Config{
		InitialSession:   &session,
		AcquireSession:   func(context.Context) (Session, error) { return Session{}, fmt.Errorf("unexpected reconnect") },
		LocalPort:        local.Addr().(*net.TCPAddr).Port,
		Transport:        "tcp",
		RequestTimeout:   time.Second,
		ReconnectEnabled: false,
	})
	runtimeDone := make(chan error, 1)
	go func() { runtimeDone <- runtime.Run(ctx) }()
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not connect")
	}
	select {
	case result := <-gatewayResult:
		if result != nil {
			t.Fatal(result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TCP frame exchange timed out")
	}
	if result := <-localResult; result != nil {
		t.Fatal(result)
	}
	cancel()
	select {
	case <-runtimeDone:
	case <-time.After(time.Second):
		t.Fatal("Agent did not stop")
	}
}
