package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var (
	ErrNotFound          = errors.New("tunnel session not found")
	ErrConnected         = errors.New("tunnel session is already connected")
	ErrEndpointConnected = errors.New("endpoint is already connected")
	ErrSessionConnected  = errors.New("session is already connected")
)

type ActiveTunnel struct {
	EndpointID      string    `json:"endpointId"`
	SessionID       string    `json:"sessionId"`
	OwnerUserID     string    `json:"-"`
	EndpointName    string    `json:"endpointName"`
	Hostname        string    `json:"hostname"`
	Protocol        string    `json:"protocol"`
	LocalPort       int       `json:"localPort"`
	ProtocolVersion int       `json:"protocolVersion"`
	ConnectedAt     time.Time `json:"connectedAt"`
}

type Tunnel struct {
	ActiveTunnel
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

func (s *Store) ReserveTicket(active ActiveTunnel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[active.SessionID] != nil {
		return ErrSessionConnected
	}
	if s.byEndpointID[active.EndpointID] != nil || s.byName[active.EndpointName] != nil {
		return ErrEndpointConnected
	}
	tunnel := &Tunnel{ActiveTunnel: active}
	s.byID[tunnel.SessionID] = tunnel
	s.byEndpointID[tunnel.EndpointID] = tunnel
	s.byName[tunnel.EndpointName] = tunnel
	s.byHostname[tunnel.Hostname] = tunnel
	return nil
}

func (s *Store) AttachReserved(sessionID string, conn *connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[sessionID]
	if tunnel == nil {
		return ErrNotFound
	}
	if tunnel.connection != nil {
		return ErrConnected
	}
	now := s.now().UTC()
	tunnel.connection = conn
	tunnel.ConnectedAt = now
	return nil
}

func (s *Store) CancelReserved(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tunnel := s.byID[sessionID]; tunnel != nil && tunnel.connection == nil {
		s.deleteLocked(tunnel)
	}
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

func (s *Store) detach(id string, conn *connection, fallbackReason string) (ActiveTunnel, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[id]
	if tunnel == nil || tunnel.connection != conn {
		return ActiveTunnel{}, "", false
	}
	reason := tunnel.disconnectReason
	if reason == "" {
		reason = fallbackReason
	}
	active := tunnel.ActiveTunnel
	s.deleteLocked(tunnel)
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

func (s *Store) ConnectionByHostname(hostname string) *connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tunnel := s.byHostname[hostname]; tunnel != nil {
		return tunnel.connection
	}
	return nil
}

func (s *Store) Connected(endpointID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tunnel := s.byEndpointID[endpointID]
	return tunnel != nil && tunnel.connection != nil
}

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
