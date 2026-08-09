package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/mockingo/mockingo-cli/internal/endpoint"
)

var (
	ErrNameConnected     = errors.New("endpoint is already connected")
	ErrNotFound          = errors.New("tunnel session not found")
	ErrUnauthorized      = errors.New("invalid tunnel session")
	ErrConnected         = errors.New("tunnel session is already connected")
	ErrEndpointConnected = errors.New("endpoint is already connected")
	ErrSessionConnected  = errors.New("session is already connected")
)

type TunnelAuthMethod string

const (
	TunnelAuthTicket TunnelAuthMethod = "ticket"
	TunnelAuthLegacy TunnelAuthMethod = "legacy"
)

type ActiveTunnel struct {
	EndpointID      string           `json:"endpointId"`
	SessionID       string           `json:"sessionId"`
	OwnerUserID     string           `json:"-"`
	EndpointName    string           `json:"endpointName"`
	Hostname        string           `json:"hostname"`
	Protocol        string           `json:"protocol"`
	LocalPort       int              `json:"localPort"`
	ProtocolVersion int              `json:"protocolVersion"`
	ConnectedAt     time.Time        `json:"connectedAt"`
	AuthMethod      TunnelAuthMethod `json:"authMethod"`
}

type Tunnel struct {
	ActiveTunnel
	SessionTokenHash [32]byte
	CreatedAt        time.Time
	LastSeenAt       time.Time
	DisconnectedAt   time.Time
	ResumeUntil      time.Time
	ExpiresAt        time.Time
	Active           bool
	disconnectReason string
	connection       *connection
}

type Store struct {
	mu           sync.RWMutex
	byID         map[string]*Tunnel
	byEndpointID map[string]*Tunnel
	byName       map[string]*Tunnel
	byHostname   map[string]*Tunnel
	now          func() time.Time
}

func NewStore() *Store {
	return &Store{
		byID: make(map[string]*Tunnel), byEndpointID: make(map[string]*Tunnel),
		byName: make(map[string]*Tunnel), byHostname: make(map[string]*Tunnel), now: time.Now,
	}
}

func randomHex(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func randomUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	return hex.EncodeToString(buffer[0:4]) + "-" + hex.EncodeToString(buffer[4:6]) + "-" + hex.EncodeToString(buffer[6:8]) + "-" + hex.EncodeToString(buffer[8:10]) + "-" + hex.EncodeToString(buffer[10:16]), nil
}

func (s *Store) Create(value endpoint.Endpoint, port int) (*Tunnel, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.byEndpointID[value.ID]; current != nil {
		if current.connection != nil || current.AuthMethod == TunnelAuthTicket {
			return nil, "", ErrNameConnected
		}
		s.deleteLocked(current)
	}
	if current := s.byName[value.Name]; current != nil {
		if current.connection != nil || current.AuthMethod == TunnelAuthTicket {
			return nil, "", ErrNameConnected
		}
		s.deleteLocked(current)
	}
	id, err := randomUUID()
	if err != nil {
		return nil, "", err
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	tunnel := &Tunnel{
		ActiveTunnel: ActiveTunnel{
			EndpointID: value.ID, SessionID: id, EndpointName: value.Name, Hostname: value.Hostname,
			Protocol: "http", LocalPort: port, ProtocolVersion: 1, AuthMethod: TunnelAuthLegacy,
		},
		SessionTokenHash: sha256.Sum256([]byte(token)), CreatedAt: now, ResumeUntil: now.Add(5 * time.Minute),
	}
	s.addLocked(tunnel)
	return tunnel, token, nil
}

func (s *Store) ReserveTicket(active ActiveTunnel, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.byID[active.SessionID]; current != nil {
		return ErrSessionConnected
	}
	if current := s.byEndpointID[active.EndpointID]; current != nil {
		if current.connection != nil || current.AuthMethod == TunnelAuthTicket {
			return ErrEndpointConnected
		}
		s.deleteLocked(current)
	}
	if current := s.byName[active.EndpointName]; current != nil {
		if current.connection != nil || current.AuthMethod == TunnelAuthTicket {
			return ErrEndpointConnected
		}
		s.deleteLocked(current)
	}
	tunnel := &Tunnel{ActiveTunnel: active, CreatedAt: s.now().UTC(), ExpiresAt: expiresAt}
	s.addLocked(tunnel)
	return nil
}

func (s *Store) AttachReserved(sessionID string, conn *connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[sessionID]
	if tunnel == nil || tunnel.AuthMethod != TunnelAuthTicket {
		return ErrNotFound
	}
	if tunnel.connection != nil {
		return ErrConnected
	}
	now := s.now().UTC()
	tunnel.connection = conn
	tunnel.ConnectedAt = now
	tunnel.LastSeenAt = now
	tunnel.Active = true
	return nil
}

func (s *Store) CancelReserved(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tunnel := s.byID[sessionID]; tunnel != nil && tunnel.AuthMethod == TunnelAuthTicket && tunnel.connection == nil {
		s.deleteLocked(tunnel)
	}
}

func (s *Store) addLocked(tunnel *Tunnel) {
	s.byID[tunnel.SessionID] = tunnel
	s.byEndpointID[tunnel.EndpointID] = tunnel
	s.byName[tunnel.EndpointName] = tunnel
	s.byHostname[tunnel.Hostname] = tunnel
}

func (s *Store) deleteLocked(tunnel *Tunnel) {
	if s.byID[tunnel.SessionID] == tunnel {
		delete(s.byID, tunnel.SessionID)
	}
	if s.byEndpointID[tunnel.EndpointID] == tunnel {
		delete(s.byEndpointID, tunnel.EndpointID)
	}
	if s.byName[tunnel.EndpointName] == tunnel {
		delete(s.byName, tunnel.EndpointName)
	}
	if s.byHostname[tunnel.Hostname] == tunnel {
		delete(s.byHostname, tunnel.Hostname)
	}
}

func (s *Store) DeleteSession(id string) {
	var conn *connection
	s.mu.Lock()
	if tunnel := s.byID[id]; tunnel != nil {
		conn = tunnel.connection
		s.deleteLocked(tunnel)
	}
	s.mu.Unlock()
	if conn != nil {
		conn.close()
	}
}

func (s *Store) DeleteEndpoint(endpointID string) {
	var conn *connection
	s.mu.Lock()
	if tunnel := s.byEndpointID[endpointID]; tunnel != nil {
		conn = tunnel.connection
		s.deleteLocked(tunnel)
	}
	s.mu.Unlock()
	if conn != nil {
		conn.close()
	}
}

func (s *Store) AuthenticateSession(id, authorization string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tunnel := s.byID[id]
	if tunnel == nil || tunnel.AuthMethod != TunnelAuthLegacy {
		return ErrNotFound
	}
	const prefix = "Bearer "
	if len(authorization) <= len(prefix) || authorization[:len(prefix)] != prefix {
		return ErrUnauthorized
	}
	actual := sha256.Sum256([]byte(authorization[len(prefix):]))
	if subtle.ConstantTimeCompare(actual[:], tunnel.SessionTokenHash[:]) != 1 {
		return ErrUnauthorized
	}
	if !tunnel.ResumeUntil.IsZero() && s.now().After(tunnel.ResumeUntil) {
		return ErrUnauthorized
	}
	return nil
}

func (s *Store) Attach(id string, conn *connection) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[id]
	if tunnel == nil || tunnel.AuthMethod != TunnelAuthLegacy {
		return false, ErrNotFound
	}
	if tunnel.connection != nil {
		return false, ErrConnected
	}
	reconnected := !tunnel.LastSeenAt.IsZero()
	tunnel.connection = conn
	now := s.now().UTC()
	tunnel.ConnectedAt = now
	tunnel.LastSeenAt = now
	tunnel.DisconnectedAt = time.Time{}
	tunnel.ResumeUntil = time.Time{}
	tunnel.Active = true
	return reconnected, nil
}

func (s *Store) Disconnect(id string, conn *connection) {
	s.detach(id, conn, "client_closed")
}

func (s *Store) detach(id string, conn *connection, fallbackReason string) (ActiveTunnel, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[id]
	if tunnel == nil || tunnel.connection != conn {
		return ActiveTunnel{}, "", false
	}
	now := s.now().UTC()
	tunnel.connection = nil
	tunnel.LastSeenAt = now
	tunnel.DisconnectedAt = now
	tunnel.Active = false
	reason := tunnel.disconnectReason
	if reason == "" {
		reason = fallbackReason
	}
	active := tunnel.ActiveTunnel
	if tunnel.AuthMethod == TunnelAuthTicket {
		s.deleteLocked(tunnel)
	} else {
		tunnel.ResumeUntil = now.Add(5 * time.Minute)
	}
	return active, reason, true
}

func (s *Store) RequestDisconnect(endpointID, reason string) (*connection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byEndpointID[endpointID]
	if tunnel == nil || tunnel.connection == nil {
		return nil, false
	}
	if tunnel.disconnectReason == "" {
		tunnel.disconnectReason = reason
	}
	return tunnel.connection, true
}

func (s *Store) ConnectionByEndpoint(endpointID string) *connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tunnel := s.byEndpointID[endpointID]; tunnel != nil {
		return tunnel.connection
	}
	return nil
}

func (s *Store) ConnectionByHostname(hostname string) *connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tunnel := s.byHostname[hostname]; tunnel != nil {
		return tunnel.connection
	}
	return nil
}

func (s *Store) Connected(endpointID string) bool { return s.ConnectionByEndpoint(endpointID) != nil }

func (s *Store) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, tunnel := range s.byEndpointID {
		if tunnel.connection != nil {
			count++
		}
	}
	return count
}

func (s *Store) Statuses(endpointIDs []string) map[string]ActiveTunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]ActiveTunnel, len(endpointIDs))
	for _, endpointID := range endpointIDs {
		if tunnel := s.byEndpointID[endpointID]; tunnel != nil && tunnel.connection != nil {
			result[endpointID] = tunnel.ActiveTunnel
		}
	}
	return result
}

func (s *Store) ActiveBySession(sessionID string) (ActiveTunnel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tunnel := s.byID[sessionID]
	if tunnel == nil || tunnel.connection == nil {
		return ActiveTunnel{}, false
	}
	return tunnel.ActiveTunnel, true
}

func (s *Store) CloseAll(reason string) []*connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	connections := make([]*connection, 0, len(s.byEndpointID))
	for _, tunnel := range s.byEndpointID {
		if tunnel.connection != nil {
			if tunnel.disconnectReason == "" {
				tunnel.disconnectReason = reason
			}
			connections = append(connections, tunnel.connection)
		}
	}
	return connections
}
