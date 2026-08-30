package tcpdependency

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

func TestProxyPreservesBinaryDataWhenTelemetryIsOffline(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, acceptErr := backend.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request, _ := io.ReadAll(conn)
		_, _ = conn.Write(request)
		if closer, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan tunnelprotocol.TCPConnectionEvent, 4)
	var telemetryOnline atomic.Bool
	telemetryOnline.Store(true)
	manager := New(ctx, Config{Emit: func(event tunnelprotocol.TCPConnectionEvent) bool { events <- event; return telemetryOnline.Load() }})
	defer manager.Close()
	backendPort := backend.Addr().(*net.TCPAddr).Port
	dependencyID := uuid.NewString()
	err = manager.Replace(tunnelprotocol.TCPDependencySnapshot{
		EndpointID:   uuid.NewString(),
		Dependencies: []tunnelprotocol.TCPDependency{{ID: dependencyID, Name: "binary", ListenHost: "127.0.0.1", ListenPort: proxyPort, TargetHost: "127.0.0.1", TargetPort: backendPort}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)))
	if err != nil {
		t.Fatal(err)
	}
	open := <-events
	if open.State != "OPEN" || open.DependencyID != dependencyID {
		t.Fatalf("open event = %#v", open)
	}
	// Simulate losing the cloud telemetry path after the local connection is
	// established. Forwarding must remain entirely local and lossless.
	telemetryOnline.Store(false)
	payload := append([]byte{0x00, 0xff, 0xfe}, bytes.Repeat([]byte{0x80, 0x41, 0x00}, 40000)...)
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = client.(*net.TCPConn).CloseWrite()
	response, err := io.ReadAll(client)
	_ = client.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, payload) {
		t.Fatalf("binary response mismatch: got %d bytes, want %d", len(response), len(payload))
	}
	select {
	case closed := <-events:
		if closed.State != "CLOSED" || closed.BytesClientToServer != int64(len(payload)) || closed.BytesServerToClient != int64(len(payload)) {
			t.Fatalf("closed event = %#v", closed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close telemetry was not emitted")
	}
}

func TestLongLivedConnectionEmitsProgressWithoutBlockingForwarding(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backendAccepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := backend.Accept()
		if acceptErr == nil {
			backendAccepted <- conn
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan tunnelprotocol.TCPConnectionEvent, 8)
	manager := New(ctx, Config{
		Emit:             func(event tunnelprotocol.TCPConnectionEvent) bool { events <- event; return true },
		ProgressInterval: 10 * time.Millisecond,
	})
	defer manager.Close()
	dependencyID := uuid.NewString()
	if err := manager.Replace(tunnelprotocol.TCPDependencySnapshot{
		EndpointID: uuid.NewString(),
		Dependencies: []tunnelprotocol.TCPDependency{{
			ID: dependencyID, Name: "stream", ListenHost: "127.0.0.1", ListenPort: proxyPort,
			TargetHost: "127.0.0.1", TargetPort: backend.Addr().(*net.TCPAddr).Port,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-backendAccepted
	defer server.Close()
	if open := <-events; open.State != "OPEN" {
		t.Fatalf("open event = %#v", open)
	}
	payload := []byte("activity")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(payload))
	if _, err := io.ReadFull(server, buffer); err != nil {
		t.Fatal(err)
	}
	select {
	case progress := <-events:
		if progress.State != "OPEN" || progress.DependencyID != dependencyID || progress.BytesClientToServer != int64(len(payload)) || progress.LastActivityAt.IsZero() {
			t.Fatalf("progress event = %#v", progress)
		}
	case <-time.After(time.Second):
		t.Fatal("progress telemetry was not emitted")
	}
}
