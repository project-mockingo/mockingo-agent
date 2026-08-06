package endpoint

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryRepositoryLifecycleAndConcurrency(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository()
	now := time.Now().UTC()
	value := Endpoint{ID: "one", Name: "spring-demo", Hostname: "spring-demo.mockingo.click", CreatedAt: now, UpdatedAt: now}
	if _, err := repository.CreateEndpoint(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if got, err := repository.GetEndpointByHostname(context.Background(), value.Hostname); err != nil || got != value {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if _, err := repository.CreateEndpoint(context.Background(), value); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate error = %v", err)
	}
	if err := repository.DeleteEndpoint(context.Background(), value.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetEndpointByName(context.Background(), value.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted get error = %v", err)
	}

	var successes atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func(id int) {
			defer wait.Done()
			candidate := value
			candidate.ID = fmt.Sprintf("id-%d", id)
			if _, err := repository.CreateEndpoint(context.Background(), candidate); err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful creates = %d, want 1", successes.Load())
	}
}
