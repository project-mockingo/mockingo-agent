package dependencycapture

import (
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/project-mockingo/mockingo-agent/tunnelprotocol"
)

type behaviorConfig struct {
	endpointID string
	byMatcher  map[string]tunnelprotocol.DependencyBehavior
}

// BehaviorStore provides lock-free request lookup and atomic full-snapshot replacement.
// It intentionally has no disk persistence; the last received snapshot remains in memory
// across cloud disconnects for the lifetime of the capture process.
type BehaviorStore struct {
	current atomic.Pointer[behaviorConfig]
}

func NewBehaviorStore() *BehaviorStore {
	store := &BehaviorStore{}
	store.current.Store(&behaviorConfig{byMatcher: map[string]tunnelprotocol.DependencyBehavior{}})
	return store
}

func (s *BehaviorStore) Replace(snapshot tunnelprotocol.DependencyBehaviorSnapshot) error {
	message := tunnelprotocol.Message{
		Version: tunnelprotocol.Version, Type: tunnelprotocol.TypeDependencyConfig,
		DependencyConfig: &snapshot,
	}
	if err := tunnelprotocol.Validate(message); err != nil {
		return err
	}
	next := &behaviorConfig{
		endpointID: snapshot.EndpointID,
		byMatcher:  make(map[string]tunnelprotocol.DependencyBehavior, len(snapshot.Behaviors)),
	}
	for _, behavior := range snapshot.Behaviors {
		next.byMatcher[matcherKey(behavior.Scheme, behavior.Host, behavior.Port, behavior.Method, behavior.Path)] = behavior
	}
	s.current.Store(next)
	return nil
}

func (s *BehaviorStore) Match(scheme, host string, port int, method, path string) (tunnelprotocol.DependencyBehavior, bool) {
	if s == nil {
		return tunnelprotocol.DependencyBehavior{}, false
	}
	config := s.current.Load()
	if config == nil {
		return tunnelprotocol.DependencyBehavior{}, false
	}
	behavior, found := config.byMatcher[matcherKey(
		strings.ToLower(scheme), normalizeHostname(host), port, strings.ToUpper(method), path)]
	return behavior, found
}

func (s *BehaviorStore) Count() int {
	if s == nil || s.current.Load() == nil {
		return 0
	}
	return len(s.current.Load().byMatcher)
}

func matcherKey(scheme, host string, port int, method, path string) string {
	return scheme + "\x00" + host + "\x00" + strconv.Itoa(port) + "\x00" + method + "\x00" + path
}
