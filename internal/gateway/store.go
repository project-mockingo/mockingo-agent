package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/mockingo/mockingo-cli/internal/auth"
)

var (
	ErrNameConnected = errors.New("tunnel name is already connected")
	ErrNotFound      = errors.New("tunnel not found")
	ErrUnauthorized  = errors.New("invalid tunnel session")
	ErrConnected     = errors.New("tunnel is already connected")
)

type Tunnel struct {
	ID           string
	Name         string
	LocalPort    int
	SessionToken string
	CreatedAt    time.Time
	ResumeUntil  time.Time
	connection   *connection
}

type Store struct {
	mu     sync.RWMutex
	byID   map[string]*Tunnel
	byName map[string]*Tunnel
	now    func() time.Time
}

func NewStore() *Store {
	return &Store{byID: make(map[string]*Tunnel), byName: make(map[string]*Tunnel), now: time.Now}
}

func randomHex(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func (s *Store) Create(name string, port int) (*Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.byName[name]; current != nil {
		if current.connection != nil {
			return nil, ErrNameConnected
		}
		delete(s.byID, current.ID)
		delete(s.byName, name)
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	tunnel := &Tunnel{ID: id, Name: name, LocalPort: port, SessionToken: token, CreatedAt: s.now()}
	s.byID[id] = tunnel
	s.byName[name] = tunnel
	return tunnel, nil
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[id]
	if tunnel == nil {
		return
	}
	if tunnel.connection != nil {
		tunnel.connection.close()
	}
	delete(s.byID, id)
	if s.byName[tunnel.Name] == tunnel {
		delete(s.byName, tunnel.Name)
	}
}

func (s *Store) AuthenticateSession(id, authorization string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tunnel := s.byID[id]
	if tunnel == nil {
		return ErrNotFound
	}
	if !auth.BearerMatches(authorization, tunnel.SessionToken) {
		return ErrUnauthorized
	}
	if !tunnel.ResumeUntil.IsZero() && s.now().After(tunnel.ResumeUntil) {
		return ErrUnauthorized
	}
	return nil
}

func (s *Store) Attach(id string, conn *connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tunnel := s.byID[id]
	if tunnel == nil {
		return ErrNotFound
	}
	if tunnel.connection != nil {
		return ErrConnected
	}
	tunnel.connection = conn
	tunnel.ResumeUntil = time.Time{}
	return nil
}

func (s *Store) Disconnect(id string, conn *connection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tunnel := s.byID[id]; tunnel != nil && tunnel.connection == conn {
		tunnel.connection = nil
		tunnel.ResumeUntil = s.now().Add(5 * time.Minute)
	}
}

func (s *Store) ConnectionByName(name string) *connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if tunnel := s.byName[name]; tunnel != nil {
		return tunnel.connection
	}
	return nil
}
