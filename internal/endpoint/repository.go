package endpoint

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("endpoint not found")
	ErrConflict = errors.New("endpoint already exists")
)

type Endpoint struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Repository interface {
	CreateEndpoint(context.Context, Endpoint) (Endpoint, error)
	GetEndpointByName(context.Context, string) (Endpoint, error)
	GetEndpointByHostname(context.Context, string) (Endpoint, error)
	ListEndpoints(context.Context) ([]Endpoint, error)
	DeleteEndpoint(context.Context, string) error
	Ping(context.Context) error
	Close()
}
