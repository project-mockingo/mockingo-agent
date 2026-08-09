package ticketauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/mockingo/mockingo-cli/internal/naming"
)

const maxJWKSBytes = 1 << 20

var (
	ErrInvalidTicket              = errors.New("invalid tunnel ticket")
	ErrExpiredTicket              = errors.New("expired tunnel ticket")
	ErrUnsupportedProtocol        = errors.New("unsupported protocol")
	ErrUnsupportedProtocolVersion = errors.New("unsupported protocol version")
	ErrReplay                     = errors.New("tunnel session replayed")
	ErrReplayCacheFull            = errors.New("tunnel replay cache capacity reached")
)

// TunnelTicketClaims mirrors the claim names issued by mockingo-backend.
type TunnelTicketClaims struct {
	Issuer          string
	Audience        []string
	Subject         string
	SessionID       string
	EndpointID      string
	EndpointName    string
	Protocol        string
	LocalPort       int
	ProtocolVersion int
	IssuedAt        time.Time
	NotBefore       time.Time
	ExpiresAt       time.Time
}

type JWKSConfig struct {
	URL             string
	HTTPClient      *http.Client
	RefreshInterval time.Duration
	OnRefresh       func(success bool)
}

// JWKSCache stores only usable public RSA signing keys. A failed refresh never
// replaces a previously usable set.
type JWKSCache struct {
	config    JWKSConfig
	mu        sync.RWMutex
	keys      jwk.Set
	refreshMu sync.Mutex
	ready     atomic.Bool
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewJWKSCache(config JWKSConfig) *JWKSCache {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = 15 * time.Minute
	}
	return &JWKSCache{config: config, done: make(chan struct{})}
}

func (c *JWKSCache) Load(ctx context.Context) error { return c.Refresh(ctx) }

func (c *JWKSCache) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(c.config.RefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refreshCtx, refreshCancel := context.WithTimeout(ctx, c.config.HTTPClient.Timeout)
				_ = c.Refresh(refreshCtx)
				refreshCancel()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (c *JWKSCache) Close() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
}

func (c *JWKSCache) Ready() bool { return c.ready.Load() }

func (c *JWKSCache) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refreshLocked(ctx)
}

func (c *JWKSCache) refreshLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.URL, nil)
	if err != nil {
		return c.refreshResult(err)
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return c.refreshResult(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return c.refreshResult(fmt.Errorf("JWKS endpoint returned status %d", response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxJWKSBytes+1))
	if err != nil || len(body) > maxJWKSBytes {
		return c.refreshResult(errors.New("JWKS response is invalid or too large"))
	}
	parsed, err := jwk.Parse(body)
	if err != nil {
		return c.refreshResult(errors.New("JWKS response is invalid"))
	}
	usable := jwk.NewSet()
	seen := make(map[string]struct{})
	for i := 0; i < parsed.Len(); i++ {
		key, ok := parsed.Key(i)
		if !ok || key.KeyType() != jwa.RSA() {
			continue
		}
		if asymmetric, ok := key.(jwk.AsymmetricKey); ok && asymmetric.IsPrivate() {
			continue
		}
		kid, ok := key.KeyID()
		if !ok || strings.TrimSpace(kid) == "" {
			continue
		}
		if _, duplicate := seen[kid]; duplicate {
			continue
		}
		if algorithm, exists := key.Algorithm(); exists && algorithm.String() != jwa.RS256().String() {
			continue
		}
		if usage, exists := key.KeyUsage(); exists && usage != string(jwk.ForSignature) {
			continue
		}
		if err := key.Validate(); err != nil {
			continue
		}
		if err := usable.AddKey(key); err == nil {
			seen[kid] = struct{}{}
		}
	}
	if usable.Len() == 0 {
		return c.refreshResult(errors.New("JWKS contains no usable RSA signing keys"))
	}
	c.mu.Lock()
	c.keys = usable
	c.mu.Unlock()
	c.ready.Store(true)
	if c.config.OnRefresh != nil {
		c.config.OnRefresh(true)
	}
	return nil
}

func (c *JWKSCache) refreshResult(err error) error {
	if c.config.OnRefresh != nil {
		c.config.OnRefresh(false)
	}
	return err
}

func (c *JWKSCache) lookup(kid string) (jwk.Key, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.keys == nil {
		return nil, false
	}
	return c.keys.LookupKeyID(kid)
}

func (c *JWKSCache) Key(ctx context.Context, kid string) (jwk.Key, error) {
	if key, ok := c.lookup(kid); ok {
		return key, nil
	}
	// Refresh is serialized, so simultaneous unknown-kid requests cannot stampede.
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if key, ok := c.lookup(kid); ok {
		return key, nil
	}
	if err := c.refreshLocked(ctx); err != nil {
		return nil, ErrInvalidTicket
	}
	key, ok := c.lookup(kid)
	if !ok {
		return nil, ErrInvalidTicket
	}
	return key, nil
}

type Config struct {
	Issuer          string
	Audience        string
	ProtocolVersion int
	ClockSkew       time.Duration
	Keys            *JWKSCache
	ReplayMax       int
	OnValidation    func(result string)
	OnReplay        func()
}

type Verifier struct {
	config        Config
	replay        *ReplayCache
	cleanupCancel context.CancelFunc
	cleanupDone   chan struct{}
}

func NewVerifier(config Config) *Verifier {
	if config.ClockSkew <= 0 {
		config.ClockSkew = 5 * time.Second
	}
	if config.ReplayMax <= 0 {
		config.ReplayMax = 100_000
	}
	return &Verifier{config: config, replay: NewReplayCache(config.ReplayMax, config.ClockSkew)}
}

func (v *Verifier) Ready() bool { return v.config.Keys != nil && v.config.Keys.Ready() }

func (v *Verifier) EstablishmentExpired(claims TunnelTicketClaims, now time.Time) bool {
	return !now.Before(claims.ExpiresAt.Add(v.config.ClockSkew))
}

func (v *Verifier) StartReplayCleanup(parent context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ctx, cancel := context.WithCancel(parent)
	v.cleanupCancel = cancel
	v.cleanupDone = make(chan struct{})
	go func() {
		defer close(v.cleanupDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				v.replay.Cleanup(now)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (v *Verifier) Close() {
	if v.cleanupCancel != nil {
		v.cleanupCancel()
		<-v.cleanupDone
	}
}

func ParseBearer(values []string) (string, error) {
	if len(values) != 1 {
		return "", ErrInvalidTicket
	}
	const prefix = "Bearer "
	header := values[0]
	if !strings.HasPrefix(header, prefix) {
		return "", ErrInvalidTicket
	}
	token := strings.TrimPrefix(header, prefix)
	if token == "" || strings.ContainsAny(token, " \t\r\n,") {
		return "", ErrInvalidTicket
	}
	return token, nil
}

func (v *Verifier) Verify(ctx context.Context, raw string) (TunnelTicketClaims, error) {
	claims, err := v.verify(ctx, raw)
	if v.config.OnValidation != nil {
		result := "success"
		if err != nil {
			result = validationResult(err)
		}
		v.config.OnValidation(result)
	}
	return claims, err
}

func validationResult(err error) string {
	switch {
	case errors.Is(err, ErrExpiredTicket):
		return "expired"
	case errors.Is(err, ErrUnsupportedProtocol):
		return "unsupported_protocol"
	case errors.Is(err, ErrUnsupportedProtocolVersion):
		return "unsupported_protocol_version"
	default:
		return "invalid"
	}
}

func (v *Verifier) verify(ctx context.Context, raw string) (TunnelTicketClaims, error) {
	message, err := jws.Parse([]byte(raw))
	if err != nil || len(message.Signatures()) != 1 {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	headers := message.Signatures()[0].ProtectedHeaders()
	if headers == nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	algorithm, ok := headers.Algorithm()
	if !ok || algorithm != jwa.RS256() {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	kid, ok := headers.KeyID()
	if !ok || strings.TrimSpace(kid) == "" {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	key, err := v.config.Keys.Key(ctx, kid)
	if err != nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	token, err := jwt.Parse([]byte(raw), jwt.WithKey(jwa.RS256(), key), jwt.WithValidate(false))
	if err != nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	validationOptions := []jwt.ValidateOption{
		jwt.WithIssuer(v.config.Issuer), jwt.WithAudience(v.config.Audience),
		jwt.WithAcceptableSkew(v.config.ClockSkew),
		jwt.WithRequiredClaim(jwt.IssuerKey), jwt.WithRequiredClaim(jwt.AudienceKey),
		jwt.WithRequiredClaim(jwt.SubjectKey), jwt.WithRequiredClaim(jwt.JwtIDKey),
		jwt.WithRequiredClaim(jwt.ExpirationKey), jwt.WithRequiredClaim(jwt.NotBeforeKey),
	}
	if err := jwt.Validate(token, validationOptions...); err != nil {
		if expiration, ok := token.Expiration(); ok && time.Now().After(expiration.Add(v.config.ClockSkew)) {
			return TunnelTicketClaims{}, ErrExpiredTicket
		}
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	claims, err := v.claims(token)
	if err != nil {
		return TunnelTicketClaims{}, err
	}
	return claims, nil
}

func (v *Verifier) claims(token jwt.Token) (TunnelTicketClaims, error) {
	var claims TunnelTicketClaims
	claims.Issuer, _ = token.Issuer()
	claims.Audience, _ = token.Audience()
	claims.Subject, _ = token.Subject()
	claims.SessionID, _ = token.JwtID()
	claims.IssuedAt, _ = token.IssuedAt()
	claims.NotBefore, _ = token.NotBefore()
	claims.ExpiresAt, _ = token.Expiration()
	if claims.Subject == "" || uuid.Validate(claims.SessionID) != nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	if err := token.Get("endpointId", &claims.EndpointID); err != nil || uuid.Validate(claims.EndpointID) != nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	if err := token.Get("endpointName", &claims.EndpointName); err != nil || naming.Validate(claims.EndpointName) != nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	if err := token.Get("protocol", &claims.Protocol); err != nil {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	if claims.Protocol != "http" {
		return claims, ErrUnsupportedProtocol
	}
	var localPort float64
	if err := token.Get("localPort", &localPort); err != nil || math.Trunc(localPort) != localPort || localPort < 1 || localPort > 65535 {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	claims.LocalPort = int(localPort)
	var protocolVersion float64
	if err := token.Get("protocolVersion", &protocolVersion); err != nil || math.Trunc(protocolVersion) != protocolVersion {
		return TunnelTicketClaims{}, ErrInvalidTicket
	}
	claims.ProtocolVersion = int(protocolVersion)
	if claims.ProtocolVersion != v.config.ProtocolVersion {
		return claims, ErrUnsupportedProtocolVersion
	}
	return claims, nil
}

func (v *Verifier) Consume(sessionID string, expiresAt time.Time) error {
	err := v.replay.Consume(sessionID, expiresAt)
	if errors.Is(err, ErrReplay) && v.config.OnReplay != nil {
		v.config.OnReplay()
	}
	return err
}

func (v *Verifier) CleanupReplay(now time.Time) int { return v.replay.Cleanup(now) }

func ValidateURL(raw string, production bool) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if production && parsed.Scheme != "https" {
		return "", errors.New("URL must use HTTPS in production")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
