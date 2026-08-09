package endpoint

import (
	"context"
	"sync"
)

type MemoryCatalog struct {
	mu        sync.RWMutex
	hostnames map[string]struct{}
}

func NewMemoryCatalog(hostnames ...string) *MemoryCatalog {
	catalog := &MemoryCatalog{hostnames: make(map[string]struct{}, len(hostnames))}
	for _, hostname := range hostnames {
		catalog.hostnames[hostname] = struct{}{}
	}
	return catalog
}

func (r *MemoryCatalog) Add(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostnames[hostname] = struct{}{}
}

func (r *MemoryCatalog) ExistsByHostname(_ context.Context, hostname string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.hostnames[hostname]
	return exists, nil
}

func (r *MemoryCatalog) Ping(context.Context) error { return nil }
func (r *MemoryCatalog) Close()                     {}
