package endpoint

import (
	"context"
	"sync"
	"testing"
)

func TestMemoryCatalogLookupAndConcurrency(t *testing.T) {
	t.Parallel()
	catalog := NewMemoryCatalog("spring-demo.mockingo.click")
	exists, err := catalog.ExistsByHostname(context.Background(), "spring-demo.mockingo.click")
	if err != nil || !exists {
		t.Fatalf("existing hostname = %v, %v", exists, err)
	}
	exists, err = catalog.ExistsByHostname(context.Background(), "missing.mockingo.click")
	if err != nil || exists {
		t.Fatalf("missing hostname = %v, %v", exists, err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			catalog.Add("concurrent.mockingo.click")
			_, _ = catalog.ExistsByHostname(context.Background(), "concurrent.mockingo.click")
		}()
	}
	wait.Wait()
}
