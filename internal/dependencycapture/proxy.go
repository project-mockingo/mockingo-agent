package dependencycapture

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type ProxyConfig struct {
	BindAddress      string
	Port             int
	CA               *CertificateAuthority
	PassthroughHosts []string
	Behaviors        *BehaviorStore
	Emit             func(tunnelprotocol.DependencyInteraction) bool
	OnCompleted      func(tunnelprotocol.DependencyInteraction)
	Verbose          func(string, ...any)
	UpstreamRootCAs  *x509.CertPool
}

type Proxy struct {
	config        ProxyConfig
	server        *http.Server
	transport     *http.Transport
	dialer        net.Dialer
	passthrough   map[string]struct{}
	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}
}

func NewProxy(config ProxyConfig) (*Proxy, error) {
	if config.BindAddress == "" {
		config.BindAddress = "127.0.0.1"
	}
	bindIP := net.ParseIP(config.BindAddress)
	if bindIP == nil {
		return nil, fmt.Errorf("invalid proxy bind address %q", config.BindAddress)
	}
	config.BindAddress = bindIP.String()
	if config.Port < 0 || config.Port > 65535 {
		return nil, errors.New("proxy port must be between 1 and 65535")
	}
	if config.CA == nil {
		return nil, errors.New("capture certificate authority is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: config.UpstreamRootCAs}
	proxy := &Proxy{config: config, transport: transport, passthrough: make(map[string]struct{}), connections: make(map[net.Conn]struct{})}
	for _, host := range config.PassthroughHosts {
		proxy.passthrough[normalizeHostname(host)] = struct{}{}
	}
	proxy.server = &http.Server{Handler: proxy, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second}
	return proxy, nil
}

func (p *Proxy) Listen() (net.Listener, error) {
	network := "tcp4"
	if net.ParseIP(p.config.BindAddress).To4() == nil {
		network = "tcp6"
	}
	return net.Listen(network, net.JoinHostPort(p.config.BindAddress, strconv.Itoa(p.config.Port)))
}

func (p *Proxy) Serve(ctx context.Context, listener net.Listener) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			p.closeHijackedConnections()
			p.transport.CloseIdleConnections()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = p.server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := p.server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

func (p *Proxy) CloseIdleConnections() { p.transport.CloseIdleConnections() }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.forward(w, r, "", "")
}

func (p *Proxy) forward(w http.ResponseWriter, request *http.Request, forcedScheme, forcedAuthority string) {
	if request.URL == nil {
		http.Error(w, "Bad proxy request", http.StatusBadRequest)
		return
	}
	scheme := strings.ToLower(request.URL.Scheme)
	authority := request.URL.Host
	if forcedScheme != "" {
		scheme = forcedScheme
		authority = forcedAuthority
	}
	if scheme != "http" && scheme != "https" {
		http.Error(w, "Only HTTP and HTTPS proxy requests are supported", http.StatusBadRequest)
		return
	}
	host, port, err := targetHostPort(authority, scheme)
	if err != nil {
		http.Error(w, "Invalid dependency target", http.StatusBadRequest)
		return
	}
	if behavior, matched := p.config.Behaviors.Match(scheme, host, port, request.Method, request.URL.Path); matched {
		p.replay(w, request, scheme, host, port, behavior)
		return
	}
	if p.config.Verbose != nil {
		p.config.Verbose("dependency_replay_miss scheme=%s host=%s port=%d method=%s path=%s", scheme, host, port, request.Method, request.URL.Path)
	}

	started := time.Now()
	startedAt := started.UTC()
	body := request.Body
	if body == nil {
		body = http.NoBody
	}
	requestCapture := newPreviewReader(body, request.Header, tunnelprotocol.MaxDependencyRequestPreview)
	outgoing := request.Clone(request.Context())
	outgoing.RequestURI = ""
	outgoing.URL = cloneURL(request.URL)
	outgoing.URL.Scheme = scheme
	outgoing.URL.Host = authority
	outgoing.Body = requestCapture
	outgoing.Header = filterHopHeaders(request.Header)
	outgoing.Header.Del("Proxy-Authorization")
	outgoing.Close = false

	response, err := p.transport.RoundTrip(outgoing)
	if err != nil {
		if p.config.Verbose != nil {
			p.config.Verbose("dependency upstream request failed for %s://%s: %v", scheme, authority, err)
		}
		http.Error(w, "Dependency request failed: "+safeNetworkError(err), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for name, values := range filterHopHeaders(response.Header) {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	responseCapture := newPreviewWriter(response.Header, tunnelprotocol.MaxDependencyResponsePreview)
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			written, writeErr := w.Write(chunk)
			if written > 0 {
				responseCapture.observe(chunk[:written])
			}
			if writeErr != nil || written != n {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return
			}
			break
		}
	}
	duration := time.Since(started)
	requestSize := requestCapture.total
	if request.ContentLength >= 0 {
		requestSize = request.ContentLength
	}
	event := tunnelprotocol.DependencyInteraction{
		ID: uuid.NewString(), Scheme: scheme, Host: host, Port: port,
		HandledBy: "ORIGIN",
		StartedAt: startedAt, CompletedAt: startedAt.Add(duration), DurationMS: duration.Milliseconds(),
		Request:  tunnelprotocol.CapturedRequest{Method: request.Method, Path: request.URL.Path, RawQuery: redactRawQuery(request.URL.RawQuery), Headers: redactHeaders(request.Header), Body: capturedBody(request.Header, requestCapture.preview, requestSize, tunnelprotocol.MaxDependencyRequestPreview), SizeBytes: requestSize},
		Response: tunnelprotocol.CapturedResponse{Status: response.StatusCode, Headers: redactHeaders(response.Header), Body: capturedBody(response.Header, responseCapture.preview, responseCapture.total, tunnelprotocol.MaxDependencyResponsePreview), SizeBytes: responseCapture.total},
	}
	if p.config.Emit != nil {
		_ = p.config.Emit(event)
	}
	if p.config.OnCompleted != nil {
		p.config.OnCompleted(event)
	}
}

func (p *Proxy) replay(
	w http.ResponseWriter,
	request *http.Request,
	scheme, host string,
	port int,
	behavior tunnelprotocol.DependencyBehavior,
) {
	started := time.Now()
	startedAt := started.UTC()
	body := request.Body
	if body == nil {
		body = http.NoBody
	}
	requestCapture := newPreviewReader(body, request.Header, tunnelprotocol.MaxDependencyRequestPreview)
	_, readErr := io.Copy(io.Discard, requestCapture)
	if readErr != nil {
		http.Error(w, "Could not read dependency request", http.StatusBadRequest)
		return
	}
	responseHeaders := http.Header(behavior.Headers).Clone()
	responseHeaders = filterHopHeaders(responseHeaders)
	for _, name := range []string{"Authentication-Info", "Authorization", "Content-Encoding", "Content-Length", "Cookie", "Date", "Proxy-Authentication-Info", "Server", "Set-Cookie", "Via"} {
		responseHeaders.Del(name)
	}
	responseBytes := []byte(behavior.Body)
	responseHeaders.Set("Content-Length", strconv.Itoa(len(responseBytes)))
	for name, values := range responseHeaders {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(behavior.Status)
	_, _ = w.Write(responseBytes)
	duration := time.Since(started)
	requestSize := requestCapture.total
	if request.ContentLength >= 0 {
		requestSize = request.ContentLength
	}
	responsePreview := responseBytes
	if len(responsePreview) > tunnelprotocol.MaxDependencyResponsePreview {
		responsePreview = responsePreview[:tunnelprotocol.MaxDependencyResponsePreview]
	}
	event := tunnelprotocol.DependencyInteraction{
		ID: uuid.NewString(), Scheme: scheme, Host: host, Port: port, HandledBy: "REPLAY",
		StartedAt: startedAt, CompletedAt: startedAt.Add(duration), DurationMS: duration.Milliseconds(),
		Request: tunnelprotocol.CapturedRequest{
			Method: request.Method, Path: request.URL.Path,
			RawQuery: redactRawQuery(request.URL.RawQuery), Headers: redactHeaders(request.Header),
			Body:      capturedBody(request.Header, requestCapture.preview, requestSize, tunnelprotocol.MaxDependencyRequestPreview),
			SizeBytes: requestSize,
		},
		Response: tunnelprotocol.CapturedResponse{
			Status: behavior.Status, Headers: redactHeaders(responseHeaders),
			Body:      capturedBody(responseHeaders, responsePreview, int64(len(responseBytes)), tunnelprotocol.MaxDependencyResponsePreview),
			SizeBytes: int64(len(responseBytes)),
		},
	}
	if p.config.Emit != nil {
		_ = p.config.Emit(event)
	}
	if p.config.OnCompleted != nil {
		p.config.OnCompleted(event)
	}
	if p.config.Verbose != nil {
		p.config.Verbose("dependency_replay_matched behaviorId=%s scheme=%s host=%s port=%d method=%s path=%s status=%d", behavior.ID, scheme, host, port, request.Method, request.URL.Path, behavior.Status)
	}
}

func (p *Proxy) handleConnect(w http.ResponseWriter, request *http.Request) {
	authority := request.Host
	host, _, err := targetHostPort(authority, "https")
	if err != nil {
		http.Error(w, "Invalid CONNECT target", http.StatusBadRequest)
		return
	}
	if _, ok := p.passthrough[normalizeHostname(host)]; ok {
		p.tunnelPassthrough(w, request, authority)
		return
	}
	p.interceptTLS(w, request, authority, host)
}

func (p *Proxy) tunnelPassthrough(w http.ResponseWriter, request *http.Request, authority string) {
	upstream, err := p.dialer.DialContext(request.Context(), "tcp", authority)
	if err != nil {
		http.Error(w, "Dependency connection failed", http.StatusBadGateway)
		return
	}
	client, buffered, err := hijack(w)
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	p.trackConnection(client)
	p.trackConnection(upstream)
	defer p.untrackConnection(client)
	defer p.untrackConnection(upstream)
	if p.config.Verbose != nil {
		p.config.Verbose("dependency HTTPS passthrough: %s", authority)
	}
	copyTunnel(client, upstream)
}

func (p *Proxy) interceptTLS(w http.ResponseWriter, request *http.Request, authority, host string) {
	certificate, err := p.config.CA.CertificateFor(host)
	if err != nil {
		http.Error(w, "Cannot create inspection certificate", http.StatusBadGateway)
		return
	}
	client, buffered, err := hijack(w)
	if err != nil {
		return
	}
	p.trackConnection(client)
	defer p.untrackConnection(client)
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = client.Close()
		return
	}
	tlsClient := tls.Server(client, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if err := tlsClient.HandshakeContext(request.Context()); err != nil {
		_ = tlsClient.Close()
		return
	}
	if p.config.Verbose != nil {
		p.config.Verbose("dependency HTTPS intercepted: %s", authority)
	}
	listener := newSingleConnListener(tlsClient)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, inner *http.Request) { p.forward(writer, inner, "https", authority) }), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second}
	err = server.Serve(listener)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) && p.config.Verbose != nil {
		p.config.Verbose("dependency HTTPS connection closed: %v", err)
	}
}

func targetHostPort(authority, scheme string) (string, int, error) {
	if authority == "" || strings.ContainsAny(authority, "/@ \t\r\n") {
		return "", 0, errors.New("invalid authority")
	}
	host := authority
	portText := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(authority); err == nil {
		host, portText = parsedHost, parsedPort
	} else if strings.Contains(authority, ":") {
		return "", 0, errors.New("invalid authority")
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if portText != "" {
		parsed, err := strconv.Atoi(portText)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", 0, errors.New("invalid port")
		}
		port = parsed
	}
	host = normalizeHostname(host)
	if host == "" {
		return "", 0, errors.New("invalid host")
	}
	return host, port, nil
}

func normalizeHostname(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}
func cloneURL(source *url.URL) *url.URL { copy := *source; return &copy }

func filterHopHeaders(source http.Header) http.Header {
	result := source.Clone()
	for _, connection := range source.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			result.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		result.Del(name)
	}
	return result
}

func hijack(w http.ResponseWriter) (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("proxy connection cannot be hijacked")
	}
	return hijacker.Hijack()
}
func copyTunnel(a, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
func safeNetworkError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "connection timed out"
	}
	return "upstream connection or TLS validation failed"
}

func (p *Proxy) trackConnection(connection net.Conn) {
	p.connectionsMu.Lock()
	p.connections[connection] = struct{}{}
	p.connectionsMu.Unlock()
}

func (p *Proxy) untrackConnection(connection net.Conn) {
	p.connectionsMu.Lock()
	delete(p.connections, connection)
	p.connectionsMu.Unlock()
}

func (p *Proxy) closeHijackedConnections() {
	p.connectionsMu.Lock()
	connections := make([]net.Conn, 0, len(p.connections))
	for connection := range p.connections {
		connections = append(connections, connection)
	}
	p.connectionsMu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

type singleConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}
type trackedConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, done: make(chan struct{})}
}
func (l *singleConnListener) Accept() (net.Conn, error) {
	accepted := false
	l.once.Do(func() { accepted = true })
	if accepted {
		return &trackedConn{Conn: l.conn, done: l.done}, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return l.conn.Close()
}
func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
func (c *trackedConn) Close() error {
	c.once.Do(func() {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	})
	return c.Conn.Close()
}

var _ http.Handler = (*Proxy)(nil)
var _ net.Listener = (*singleConnListener)(nil)
