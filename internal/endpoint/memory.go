package endpoint

import (
	"context"
	"sort"
	"sync"
)

type MemoryRepository struct {
	mu     sync.RWMutex
	byName map[string]Endpoint
	byHost map[string]string
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{byName: make(map[string]Endpoint), byHost: make(map[string]string)}
}

func (r *MemoryRepository) CreateEndpoint(_ context.Context, value Endpoint) (Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[value.Name]; exists {
		return Endpoint{}, ErrConflict
	}
	if _, exists := r.byHost[value.Hostname]; exists {
		return Endpoint{}, ErrConflict
	}
	r.byName[value.Name] = value
	r.byHost[value.Hostname] = value.Name
	return value, nil
}

func (r *MemoryRepository) GetEndpointByName(_ context.Context, name string) (Endpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.byName[name]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	return value, nil
}

func (r *MemoryRepository) GetEndpointByHostname(_ context.Context, hostname string) (Endpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, ok := r.byHost[hostname]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	return r.byName[name], nil
}

func (r *MemoryRepository) ListEndpoints(_ context.Context) ([]Endpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]Endpoint, 0, len(r.byName))
	for _, value := range r.byName {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values, nil
}

func (r *MemoryRepository) DeleteEndpoint(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.byName[name]
	if !ok {
		return nil
	}
	delete(r.byName, name)
	delete(r.byHost, value.Hostname)
	return nil
}

func (r *MemoryRepository) Ping(context.Context) error { return nil }
func (r *MemoryRepository) Close()                     {}
