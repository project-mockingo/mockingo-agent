package ticketauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const (
	testSessionID  = "8db7aeef-a927-48d1-9190-99076fbe3c71"
	testEndpointID = "e9949642-8b35-4247-ac5d-c076a463058d"
)

type rotatingJWKS struct {
	server *httptest.Server
	mu     sync.RWMutex
	body   []byte
	calls  atomic.Int32
}

func newRotatingJWKS(t *testing.T, body []byte) *rotatingJWKS {
	t.Helper()
	value := &rotatingJWKS{body: body}
	value.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		value.calls.Add(1)
		value.mu.RLock()
		defer value.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(value.body)
	}))
	t.Cleanup(value.server.Close)
	return value
}

func (s *rotatingJWKS) set(body []byte) {
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

func testKey(t *testing.T, kid string) (*rsa.PrivateKey, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := jwk.Import(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = publicKey.Set(jwk.KeyIDKey, kid)
	_ = publicKey.Set(jwk.AlgorithmKey, jwa.RS256())
	_ = publicKey.Set(jwk.KeyUsageKey, string(jwk.ForSignature))
	set := jwk.NewSet()
	if err := set.AddKey(publicKey); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, body
}

type ticketOptions struct {
	issuer, audience, subject, sessionID, endpointID, endpointName, protocol string
	localPort, protocolVersion                                               int
	issuedAt, notBefore, expiresAt                                           time.Time
	kid                                                                      string
	algorithm                                                                jwa.SignatureAlgorithm
}

func validOptions() ticketOptions {
	now := time.Now().UTC().Truncate(time.Second)
	return ticketOptions{
		issuer: "https://api.mockingo.com", audience: "mockingo-gateway", subject: "user_123",
		sessionID: testSessionID, endpointID: testEndpointID, endpointName: "spring-demo",
		protocol: "http", localPort: 8080, protocolVersion: 1,
		issuedAt: now, notBefore: now, expiresAt: now.Add(time.Minute), kid: "key-1", algorithm: jwa.RS256(),
	}
}

func signTicket(t *testing.T, key any, options ticketOptions) string {
	t.Helper()
	token, err := jwt.NewBuilder().
		Issuer(options.issuer).Audience([]string{options.audience}).Subject(options.subject).
		JwtID(options.sessionID).IssuedAt(options.issuedAt).NotBefore(options.notBefore).Expiration(options.expiresAt).
		Claim("endpointId", options.endpointID).Claim("endpointName", options.endpointName).
		Claim("protocol", options.protocol).Claim("localPort", options.localPort).
		Claim("protocolVersion", options.protocolVersion).Build()
	if err != nil {
		t.Fatal(err)
	}
	headers := jws.NewHeaders()
	if options.kid != "" {
		_ = headers.Set(jws.KeyIDKey, options.kid)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(options.algorithm, key, jws.WithProtectedHeaders(headers)))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func newTestVerifier(t *testing.T, jwksURL string) (*Verifier, *JWKSCache) {
	t.Helper()
	cache := NewJWKSCache(JWKSConfig{URL: jwksURL, HTTPClient: &http.Client{Timeout: time.Second}, RefreshInterval: time.Hour})
	if err := cache.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewVerifier(Config{Issuer: "https://api.mockingo.com", Audience: "mockingo-gateway", ProtocolVersion: 1, ClockSkew: time.Second, Keys: cache, ReplayMax: 100}), cache
}

func TestVerifierValidRS256AndClaimValidation(t *testing.T) {
	privateKey, jwks := testKey(t, "key-1")
	server := newRotatingJWKS(t, jwks)
	verifier, _ := newTestVerifier(t, server.server.URL)
	raw := signTicket(t, privateKey, validOptions())
	claims, err := verifier.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.EndpointID != testEndpointID || claims.SessionID != testSessionID || claims.LocalPort != 8080 || claims.ProtocolVersion != 1 {
		t.Fatalf("unexpected claims: %#v", claims)
	}

	for name, test := range map[string]struct {
		mutate   func(*ticketOptions)
		expected error
	}{
		"issuer":           {func(o *ticketOptions) { o.issuer = "https://wrong.example" }, ErrInvalidTicket},
		"audience":         {func(o *ticketOptions) { o.audience = "wrong" }, ErrInvalidTicket},
		"expired":          {func(o *ticketOptions) { o.expiresAt = time.Now().Add(-time.Minute) }, ErrExpiredTicket},
		"future nbf":       {func(o *ticketOptions) { o.notBefore = time.Now().Add(time.Minute) }, ErrInvalidTicket},
		"subject":          {func(o *ticketOptions) { o.subject = "" }, ErrInvalidTicket},
		"missing jti":      {func(o *ticketOptions) { o.sessionID = "" }, ErrInvalidTicket},
		"session UUID":     {func(o *ticketOptions) { o.sessionID = "bad" }, ErrInvalidTicket},
		"endpoint UUID":    {func(o *ticketOptions) { o.endpointID = "bad" }, ErrInvalidTicket},
		"endpoint name":    {func(o *ticketOptions) { o.endpointName = "Bad Name" }, ErrInvalidTicket},
		"local port":       {func(o *ticketOptions) { o.localPort = 0 }, ErrInvalidTicket},
		"protocol":         {func(o *ticketOptions) { o.protocol = "tcp" }, ErrUnsupportedProtocol},
		"protocol version": {func(o *ticketOptions) { o.protocolVersion = 2 }, ErrUnsupportedProtocolVersion},
	} {
		t.Run(name, func(t *testing.T) {
			options := validOptions()
			test.mutate(&options)
			_, err := verifier.Verify(context.Background(), signTicket(t, privateKey, options))
			if !errors.Is(err, test.expected) {
				t.Fatalf("error = %v, want %v", err, test.expected)
			}
		})
	}
}

func TestVerifierRejectsSignatureAlgorithmAndMissingKID(t *testing.T) {
	privateKey, jwks := testKey(t, "key-1")
	server := newRotatingJWKS(t, jwks)
	verifier, _ := newTestVerifier(t, server.server.URL)
	wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if _, err := verifier.Verify(context.Background(), signTicket(t, wrongKey, validOptions())); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("invalid signature error = %v", err)
	}
	options := validOptions()
	options.kid = ""
	if _, err := verifier.Verify(context.Background(), signTicket(t, privateKey, options)); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("missing kid error = %v", err)
	}
	options = validOptions()
	options.algorithm = jwa.HS256()
	if _, err := verifier.Verify(context.Background(), signTicket(t, []byte("01234567890123456789012345678901"), options)); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("unexpected algorithm error = %v", err)
	}
}

func TestUnknownKIDRefreshAndCachedKeysSurviveFailure(t *testing.T) {
	key1, jwks1 := testKey(t, "key-1")
	key2, jwks2 := testKey(t, "key-2")
	server := newRotatingJWKS(t, jwks1)
	verifier, cache := newTestVerifier(t, server.server.URL)
	server.set(jwks2)
	options := validOptions()
	options.kid = "key-2"
	rotatedTicket := signTicket(t, key2, options)
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := verifier.Verify(context.Background(), rotatedTicket); err != nil {
				t.Errorf("rotated ticket: %v", err)
			}
		}()
	}
	wait.Wait()
	if server.calls.Load() != 2 {
		t.Fatalf("JWKS calls = %d, want startup plus one refresh", server.calls.Load())
	}
	server.set([]byte("not-json"))
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("invalid refresh unexpectedly succeeded")
	}
	if _, err := verifier.Verify(context.Background(), rotatedTicket); err != nil {
		t.Fatalf("cached rotated key stopped working: %v", err)
	}
	_ = key1
}

func TestReplayCacheConcurrentUseAndCleanup(t *testing.T) {
	cache := NewReplayCache(10, time.Second)
	expires := time.Now().Add(time.Minute)
	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if cache.Consume(testSessionID, expires) == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful consumes = %d, want 1", successes.Load())
	}
	if removed := cache.Cleanup(expires.Add(2 * time.Second)); removed != 1 {
		t.Fatalf("removed entries = %d, want 1", removed)
	}
}

func TestParseBearerIsExact(t *testing.T) {
	if token, err := ParseBearer([]string{"Bearer abc.def.ghi"}); err != nil || token != "abc.def.ghi" {
		t.Fatalf("valid bearer = %q, %v", token, err)
	}
	for _, values := range [][]string{nil, {"bearer token"}, {"Bearer token extra"}, {"Bearer a", "Bearer b"}} {
		if _, err := ParseBearer(values); err == nil {
			t.Fatalf("invalid values accepted: %#v", values)
		}
	}
}
