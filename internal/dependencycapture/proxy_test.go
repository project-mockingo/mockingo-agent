package dependencycapture

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

func TestHTTPProxyStreamsFullBodiesAndRedactsOnlyCapture(t *testing.T) {
	requestBody := bytes.Repeat([]byte("r"), tunnelprotocol.MaxDependencyRequestPreview+4096)
	responseBody := bytes.Repeat([]byte("s"), tunnelprotocol.MaxDependencyResponsePreview+4096)
	backendRequest := make(chan *http.Request, 1)
	backendBody := make(chan []byte, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		backendRequest <- r.Clone(context.Background())
		backendBody <- body
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Set-Cookie", "backend=SECRET")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(responseBody)
	}))
	defer backend.Close()
	events := make(chan tunnelprotocol.DependencyInteraction, 1)
	client, stop := testProxyClient(t, ProxyConfig{Emit: func(event tunnelprotocol.DependencyInteraction) bool { events <- event; return true }}, nil)
	defer stop()
	req, _ := http.NewRequest(http.MethodPost, backend.URL+"/check?access_token=SECRET&keep=yes", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer SECRET")
	req.Header.Set("Cookie", "session=SECRET")
	req.Header.Set("Proxy-Authorization", "Basic SECRET")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	actualResponse, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict || !bytes.Equal(actualResponse, responseBody) {
		t.Fatal("client did not receive the complete backend response")
	}
	forwarded := <-backendRequest
	if body := <-backendBody; !bytes.Equal(body, requestBody) {
		t.Fatal("backend did not receive the complete request body")
	}
	if forwarded.Header.Get("Authorization") != "Bearer SECRET" || forwarded.Header.Get("Cookie") != "session=SECRET" {
		t.Fatal("application credentials were changed before forwarding")
	}
	if forwarded.Header.Get("Proxy-Authorization") != "" {
		t.Fatal("proxy authorization reached the dependency")
	}
	event := <-events
	if event.Request.Headers["Authorization"][0] != "[REDACTED]" || event.Request.Headers["Cookie"][0] != "[REDACTED]" || event.Request.Headers["Proxy-Authorization"][0] != "[REDACTED]" {
		t.Fatal("sensitive request headers were not redacted")
	}
	if event.Response.Headers["Set-Cookie"][0] != "[REDACTED]" || strings.Contains(event.Request.RawQuery, "SECRET") {
		t.Fatal("response headers or query were not redacted")
	}
	if !event.Request.Body.Truncated || event.Request.Body.CapturedBytes != tunnelprotocol.MaxDependencyRequestPreview {
		t.Fatalf("request preview was not bounded: %+v", event.Request.Body)
	}
	if !event.Response.Body.Truncated || event.Response.Body.CapturedBytes != tunnelprotocol.MaxDependencyResponsePreview {
		t.Fatalf("response preview was not bounded: %+v", event.Response.Body)
	}
}

func TestHTTPProxyCapturesDecodedGzipWithoutChangingApplicationResponse(t *testing.T) {
	payload := []byte(`{"code":"LIMIT_EXCEEDED","message":"captured decoded body"}`)
	var encoded bytes.Buffer
	gzipWriter := gzip.NewWriter(&encoded)
	_, _ = gzipWriter.Write(payload)
	_ = gzipWriter.Close()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(encoded.Len()))
		_, _ = w.Write(encoded.Bytes())
	}))
	defer backend.Close()

	events := make(chan tunnelprotocol.DependencyInteraction, 1)
	client, stop := testProxyClient(t, ProxyConfig{Emit: func(event tunnelprotocol.DependencyInteraction) bool { events <- event; return true }}, nil)
	defer stop()
	req, _ := http.NewRequest(http.MethodGet, backend.URL+"/compressed", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	applicationBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(applicationBody, encoded.Bytes()) {
		t.Fatal("proxy changed the encoded response delivered to the application")
	}
	event := <-events
	if !event.Response.Body.Captured || event.Response.Body.Truncated || event.Response.Body.Content != string(payload) {
		t.Fatalf("decoded capture = %+v", event.Response.Body)
	}
	if event.Response.Body.SizeBytes != int64(len(payload)) || event.Response.SizeBytes != int64(encoded.Len()) {
		t.Fatalf("decoded/wire sizes = %d/%d", event.Response.Body.SizeBytes, event.Response.SizeBytes)
	}
}

func TestProxyForwardingSucceedsWhenTelemetryIsDropped(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("real response")) }))
	defer backend.Close()
	client, stop := testProxyClient(t, ProxyConfig{Emit: func(tunnelprotocol.DependencyInteraction) bool { return false }}, nil)
	defer stop()
	response, err := client.Get(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "real response" {
		t.Fatalf("body = %q", body)
	}
}

func TestDependencyBehaviorMatcherNormalizesAndIgnoresQuery(t *testing.T) {
	store := NewBehaviorStore()
	snapshot := tunnelprotocol.DependencyBehaviorSnapshot{
		EndpointID: "e9949642-8b35-4247-ac5d-c076a463058d",
		Behaviors: []tunnelprotocol.DependencyBehavior{{
			ID: "5220c66a-3411-48d6-9756-aa94a2fbe4ad", Scheme: "https", Host: "billing.internal",
			Port: 443, Method: "POST", Path: "/check", Status: 409, Headers: map[string][]string{},
		}},
	}
	if err := store.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, found := store.Match("HTTPS", "BILLING.INTERNAL.", 443, "post", "/check"); !found {
		t.Fatal("normalized matcher did not match")
	}
	for _, candidate := range []struct {
		scheme, host, method, path string
		port                       int
	}{
		{"http", "billing.internal", "POST", "/check", 443},
		{"https", "other.internal", "POST", "/check", 443},
		{"https", "billing.internal", "POST", "/check", 8443},
		{"https", "billing.internal", "GET", "/check", 443},
		{"https", "billing.internal", "POST", "/check/", 443},
		{"https", "billing.internal", "POST", "/check/123", 443},
	} {
		if _, found := store.Match(candidate.scheme, candidate.host, candidate.port, candidate.method, candidate.path); found {
			t.Fatalf("unexpected match for %+v", candidate)
		}
	}
}

func TestDependencyTargetsUseEffectiveDefaultPorts(t *testing.T) {
	for _, candidate := range []struct {
		authority, scheme, host string
		port                    int
	}{
		{"billing.internal", "http", "billing.internal", 80},
		{"billing.internal", "https", "billing.internal", 443},
		{"BILLING.INTERNAL.:8443", "https", "billing.internal", 8443},
	} {
		host, port, err := targetHostPort(candidate.authority, candidate.scheme)
		if err != nil || host != candidate.host || port != candidate.port {
			t.Fatalf("target %s %s = %s:%d err=%v", candidate.scheme, candidate.authority, host, port, err)
		}
	}
}

func TestOfflineHTTPReplayConsumesBodyAndNeverDialsOrigin(t *testing.T) {
	store := NewBehaviorStore()
	if err := store.Replace(testBehaviorSnapshot("http", "unavailable-target.test", 12345, "POST", "/hello")); err != nil {
		t.Fatal(err)
	}
	events := make(chan tunnelprotocol.DependencyInteraction, 1)
	client, stop := testProxyClient(t, ProxyConfig{Behaviors: store, Emit: func(event tunnelprotocol.DependencyInteraction) bool { events <- event; return true }}, nil)
	defer stop()
	requestBody := bytes.Repeat([]byte("request-body"), 1024)
	request, _ := http.NewRequest(http.MethodPost, "http://unavailable-target.test:12345/hello?ignored=yes", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "text/plain")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict || string(body) != `{"code":"OFFLINE"}` {
		t.Fatalf("response status=%d body=%q", response.StatusCode, body)
	}
	if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Set-Cookie") != "" {
		t.Fatalf("unsafe replay headers leaked: %v", response.Header)
	}
	event := <-events
	if event.HandledBy != "REPLAY" || event.Request.SizeBytes != int64(len(requestBody)) || event.Request.RawQuery != "ignored=yes" {
		t.Fatalf("replay event = %+v", event)
	}
}

func TestOfflineHTTPSReplayDoesNotContactOrigin(t *testing.T) {
	store := NewBehaviorStore()
	if err := store.Replace(testBehaviorSnapshot("https", "unavailable-target.test", 443, "GET", "/secure")); err != nil {
		t.Fatal(err)
	}
	client, stop := testProxyClient(t, ProxyConfig{Behaviors: store}, nil)
	defer stop()
	response, err := client.Get("https://unavailable-target.test/secure")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict || string(body) != `{"code":"OFFLINE"}` {
		t.Fatalf("response status=%d body=%q", response.StatusCode, body)
	}
}

func TestHTTPSKeepAliveMixesReplayAndOriginPerRequest(t *testing.T) {
	var originRequests atomic.Int32
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		_, _ = w.Write([]byte("origin:" + r.URL.Path))
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	host, port, err := targetHostPort(target.Host, "https")
	if err != nil {
		t.Fatal(err)
	}
	store := NewBehaviorStore()
	if err := store.Replace(testBehaviorSnapshot("https", host, port, "GET", "/mocked")); err != nil {
		t.Fatal(err)
	}
	upstreamRoots := x509.NewCertPool()
	upstreamRoots.AddCert(backend.Certificate())
	client, stop := testProxyClient(t, ProxyConfig{Behaviors: store, UpstreamRootCAs: upstreamRoots}, nil)
	defer stop()
	for _, path := range []string{"/mocked", "/real", "/mocked"} {
		response, err := client.Get(backend.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if path == "/real" && string(body) != "origin:/real" {
			t.Fatalf("origin body = %q", body)
		}
		if path == "/mocked" && string(body) != `{"code":"OFFLINE"}` {
			t.Fatalf("replay body = %q", body)
		}
	}
	if originRequests.Load() != 1 {
		t.Fatalf("origin requests = %d, want 1", originRequests.Load())
	}
}

func TestHTTPSInterceptionUsesCaptureCAAndValidatesUpstream(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()
	upstreamRoots := x509.NewCertPool()
	upstreamRoots.AddCert(backend.Certificate())
	events := make(chan tunnelprotocol.DependencyInteraction, 1)
	client, stop := testProxyClient(t, ProxyConfig{UpstreamRootCAs: upstreamRoots, Emit: func(event tunnelprotocol.DependencyInteraction) bool { events <- event; return true }}, nil)
	defer stop()
	response, err := client.Get(backend.URL + "/secure")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q", body)
	}
	event := <-events
	if event.Scheme != "https" || event.Host != "127.0.0.1" || event.Request.Path != "/secure" || event.Response.Status != 200 {
		t.Fatalf("unexpected HTTPS event: %+v", event)
	}
}

func TestHTTPSPassthroughPreservesBackendIdentityAndEmitsNoHTTPEvent(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("passthrough")) }))
	defer backend.Close()
	backendRoots := x509.NewCertPool()
	backendRoots.AddCert(backend.Certificate())
	target, _ := url.Parse(backend.URL)
	host, port, _ := targetHostPort(target.Host, "https")
	store := NewBehaviorStore()
	if err := store.Replace(testBehaviorSnapshot("https", host, port, "GET", "/")); err != nil {
		t.Fatal(err)
	}
	var emitted atomic.Int32
	client, stop := testProxyClient(t, ProxyConfig{PassthroughHosts: []string{"127.0.0.1"}, Behaviors: store, Emit: func(tunnelprotocol.DependencyInteraction) bool { emitted.Add(1); return true }}, backendRoots)
	defer stop()
	response, err := client.Get(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.TLS == nil || !bytes.Equal(response.TLS.PeerCertificates[0].Raw, backend.Certificate().Raw) {
		t.Fatal("passthrough replaced the real backend certificate")
	}
	if emitted.Load() != 0 {
		t.Fatal("passthrough emitted a decrypted HTTP event")
	}
}

func TestUpstreamCertificateFailureIsNotBypassed(t *testing.T) {
	var handled atomic.Bool
	backend := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handled.Store(true) }))
	defer backend.Close()
	client, stop := testProxyClient(t, ProxyConfig{}, nil)
	defer stop()
	response, err := client.Get(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || handled.Load() {
		t.Fatal("untrusted upstream TLS certificate was accepted")
	}
}

func TestCAPersistsAndLeafCertificatesAreCached(t *testing.T) {
	directory := t.TempDir()
	ca, path, err := LoadOrCreateCA(directory)
	if err != nil {
		t.Fatal(err)
	}
	firstFile, _ := os.ReadFile(path)
	first, err := ca.CertificateFor("service.internal")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ca.CertificateFor("service.internal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("leaf certificate was not cached")
	}
	_, secondPath, err := LoadOrCreateCA(directory)
	if err != nil {
		t.Fatal(err)
	}
	secondFile, _ := os.ReadFile(secondPath)
	if !bytes.Equal(firstFile, secondFile) {
		t.Fatal("capture CA was regenerated")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(directory + string(os.PathSeparator) + "ca.key")
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("CA key permissions are too broad: %v", info.Mode().Perm())
		}
	}
}

func TestUploaderQueueIsBounded(t *testing.T) {
	var drops atomic.Int32
	uploader := NewUploader(UploaderConfig{QueueSize: 1, OnDrop: func() { drops.Add(1) }})
	event := validTestEvent()
	if !uploader.Enqueue(event) || uploader.Enqueue(event) {
		t.Fatal("queue did not enforce its configured bound")
	}
	if drops.Load() != 1 {
		t.Fatalf("drops = %d", drops.Load())
	}
}

func TestUploaderReconnectsWithoutBlockingProxyTelemetryProducer(t *testing.T) {
	connections := make(chan int, 2)
	received := make(chan tunnelprotocol.Message, 1)
	var count atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ordinal := int(count.Add(1))
		connections <- ordinal
		if ordinal == 1 {
			_ = connection.Close()
			return
		}
		defer connection.Close()
		var message tunnelprotocol.Message
		if err := connection.ReadJSON(&message); err == nil {
			received <- message
		}
	}))
	defer server.Close()
	connectURL := strings.Replace(server.URL, "http://", "ws://", 1)
	acquire := func(context.Context) (CaptureSession, error) {
		return CaptureSession{ConnectURL: connectURL, Ticket: "short-lived-ticket"}, nil
	}
	uploader := NewUploader(UploaderConfig{
		InitialSession:        &CaptureSession{ConnectURL: connectURL, Ticket: "short-lived-ticket"},
		AcquireSession:        acquire,
		Retryable:             func(error) bool { return true },
		ReconnectInitialDelay: time.Millisecond,
		ReconnectMaxDelay:     2 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("uploader stopped: %v", err)
		}
	}()
	for expected := 1; expected <= 2; expected++ {
		select {
		case actual := <-connections:
			if actual != expected {
				t.Fatalf("connection ordinal = %d, want %d", actual, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for connection %d", expected)
		}
	}
	if !uploader.Enqueue(validTestEvent()) {
		t.Fatal("producer was blocked or queue was unexpectedly full")
	}
	select {
	case message := <-received:
		if message.Type != tunnelprotocol.TypeDependencyInteraction || message.Dependency == nil {
			t.Fatalf("message = %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnected uploader did not send the queued event")
	}
}

func TestLoadedBehaviorSurvivesCaptureConnectionLoss(t *testing.T) {
	snapshot := testBehaviorSnapshot("http", "offline.internal", 80, "GET", "/ready")
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = connection.WriteJSON(tunnelprotocol.Message{
			Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeDependencyConfig,
			DependencyConfig: &snapshot,
		})
		_ = connection.Close()
	}))
	defer server.Close()
	store := NewBehaviorStore()
	loaded := make(chan struct{})
	var loadedOnce sync.Once
	connectURL := strings.Replace(server.URL, "http://", "ws://", 1)
	session := CaptureSession{EndpointID: snapshot.EndpointID, ConnectURL: connectURL, Ticket: "ticket"}
	uploader := NewUploader(UploaderConfig{
		InitialSession: &session,
		AcquireSession: func(context.Context) (CaptureSession, error) { return session, nil },
		Retryable:      func(error) bool { return true }, ReconnectInitialDelay: time.Second,
		OnConfig: func(value tunnelprotocol.DependencyBehaviorSnapshot) error {
			if err := store.Replace(value); err != nil {
				return err
			}
			loadedOnce.Do(func() { close(loaded) })
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- uploader.Run(ctx) }()
	select {
	case <-loaded:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("snapshot was not loaded")
	}
	// The server has already closed the capture socket. Disconnect does not
	// clear the atomic last-known configuration.
	if _, found := store.Match("http", "offline.internal", 80, "GET", "/ready"); !found {
		cancel()
		t.Fatal("last-known dependency replay was cleared on disconnect")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyReportsPortConflict(t *testing.T) {
	ca, _, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewProxy(ProxyConfig{Port: 0, CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := first.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProxy(ProxyConfig{Port: port, CA: ca})
	if err != nil {
		t.Fatal(err)
	}
	if conflicting, err := second.Listen(); err == nil {
		_ = conflicting.Close()
		t.Fatal("second proxy unexpectedly acquired the occupied port")
	}
}

func TestBinaryUnsupportedEncodedAndCompressedBodies(t *testing.T) {
	binaryHeaders := http.Header{"Content-Type": []string{"application/octet-stream"}}
	binary := capturedBody(binaryHeaders, []byte{0, 1, 2}, 3, 16)
	if binary.Captured || binary.Reason != "binary_content" || binary.SizeBytes != 3 {
		t.Fatalf("binary body = %+v", binary)
	}

	encodedHeaders := http.Header{
		"Content-Type":     []string{"text/plain"},
		"Content-Encoding": []string{"compress"},
	}
	encoded := capturedBody(encodedHeaders, []byte("compressed"), 10, 16)
	if encoded.Captured || encoded.Reason != "encoded_content" || encoded.SizeBytes != 10 {
		t.Fatalf("encoded body = %+v", encoded)
	}

	payload := []byte(`{"message":"decoded"}`)
	var deflated bytes.Buffer
	deflateWriter := zlib.NewWriter(&deflated)
	_, _ = deflateWriter.Write(payload)
	_ = deflateWriter.Close()
	var brotliEncoded bytes.Buffer
	brotliWriter := brotli.NewWriter(&brotliEncoded)
	_, _ = brotliWriter.Write(payload)
	_ = brotliWriter.Close()
	for name, value := range map[string][]byte{"deflate": deflated.Bytes(), "br": brotliEncoded.Bytes()} {
		captured := capturedBody(
			http.Header{"Content-Type": []string{"application/json"}, "Content-Encoding": []string{name}},
			value,
			int64(len(value)),
			64,
		)
		if !captured.Captured || captured.Truncated || captured.Content != string(payload) || captured.SizeBytes != int64(len(payload)) {
			t.Fatalf("%s body = %+v", name, captured)
		}
	}
}

func testProxyClient(t *testing.T, config ProxyConfig, clientRoots *x509.CertPool) (*http.Client, func()) {
	t.Helper()
	ca, certPath, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config.Port = 0
	config.CA = ca
	proxy, err := NewProxy(config)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := proxy.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proxy.Serve(ctx, listener) }()
	proxyURL, _ := url.Parse("http://" + listener.Addr().String())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.ForceAttemptHTTP2 = false
	if clientRoots == nil {
		clientRoots = x509.NewCertPool()
		certPEM, _ := os.ReadFile(certPath)
		clientRoots.AppendCertsFromPEM(certPEM)
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: clientRoots, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	return client, func() {
		transport.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("proxy stopped: %v", err)
			}
		case <-time.After(6 * time.Second):
			t.Error("proxy did not stop")
		}
	}
}

func validTestEvent() tunnelprotocol.DependencyInteraction {
	now := time.Now().UTC()
	return tunnelprotocol.DependencyInteraction{ID: "d0ab19d5-093a-49f5-b508-c656adc9b76f", Scheme: "http", Host: "example.test", Port: 80, StartedAt: now, CompletedAt: now, Request: tunnelprotocol.CapturedRequest{Method: "GET", Path: "/", Headers: map[string][]string{}, Body: tunnelprotocol.CapturedBody{}, SizeBytes: 0}, Response: tunnelprotocol.CapturedResponse{Status: 200, Headers: map[string][]string{}, Body: tunnelprotocol.CapturedBody{}, SizeBytes: 0}}
}

func testBehaviorSnapshot(scheme, host string, port int, method, path string) tunnelprotocol.DependencyBehaviorSnapshot {
	return tunnelprotocol.DependencyBehaviorSnapshot{
		EndpointID: "e9949642-8b35-4247-ac5d-c076a463058d",
		Behaviors: []tunnelprotocol.DependencyBehavior{{
			ID: "5220c66a-3411-48d6-9756-aa94a2fbe4ad", Scheme: scheme, Host: host, Port: port,
			Method: method, Path: path, Status: http.StatusConflict,
			Headers: map[string][]string{
				"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"},
				"Set-Cookie": {"must-not-pass=1"},
			},
			Body: `{"code":"OFFLINE"}`,
		}},
	}
}
