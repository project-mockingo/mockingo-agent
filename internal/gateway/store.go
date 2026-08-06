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
	ErrNameConnected = errors.New("endpoint is already connected")
	ErrNotFound      = errors.New("tunnel session not found")
	ErrUnauthorized  = errors.New("invalid tunnel session")
	ErrConnected     = errors.New("tunnel session is already connected")
)

type Tunnel struct {
	ID               string
	EndpointID       string
	Name             string
	LocalPort        int
	SessionTokenHash [32]byte
	CreatedAt        time.Time
	ConnectedAt      time.Time
	LastSeenAt       time.Time
	DisconnectedAt   time.Time
	ResumeUntil      time.Time
	Active           bool
	connection       *connection
}

type Store struct {
	mu           sync.RWMutex
	byID         map[string]*Tunnel
	byEndpointID map[string]*Tunnel
	now          func() time.Time
}

func NewStore() *Store {
	return &Store{byID: make(map[string]*Tunnel), byEndpointID: make(map[string]*Tunnel), now: time.Now}
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
		if current.connection != nil {
			return nil, "", ErrNameConnected
		}
		delete(s.byID, current.ID)
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
		ID: id, EndpointID: value.ID, Name: value.Name, LocalPort: port,
		SessionTokenHash: sha256.Sum256([]byte(token)), CreatedAt: now,
		ResumeUntil: now.Add(5 * time.Minute),
	}
	s.byID[id] = tunnel
	s.byEndpointID[value.ID] = tunnel
	return tunnel, token, nil
}

func (s *Store) DeleteSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteSessionLocked(id)
}

func (s *Store) deleteSessionLocked(id string) {
	tunnel := s.byID[id]
	if tunnel == nil {
		return
	}
	if tunnel.connection != nil {
		tunnel.connection.close()
	}
	delete(s.byID, id)
	if s.byEndpointID[tunnel.EndpointID] == tunnel {
		delete(s.byEndpointID, tunnel.EndpointID)
	}
}

func (s *Store) DeleteEndpoint(endpointID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tunnel := s.byEndpointID[endpointID]; tunnel != nil {
		s.deleteSessionLocked(tunnel.ID)
	}
}

func (s *Store) AuthenticateSession(id, authorization string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tunnel := s.byID[id]
	if tunnel == nil {
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
	if tunnel == nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if tunnel := s.byID[id]; tunnel != nil && tunnel.connection == conn {
		tunnel.connection = nil
		now := s.now().UTC()
		tunnel.LastSeenAt = now
		tunnel.DisconnectedAt = now
		tunnel.ResumeUntil = now.Add(5 * time.Minute)
		tunnel.Active = false
	}
}

func (s *Store) ConnectionByEndpoint(endpointID string) *connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tunnel := s.byEndpointID[endpointID]; tunnel != nil {
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

func (s *Store) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byID {
		s.deleteSessionLocked(id)
	}
}
