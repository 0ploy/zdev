package secrets

import (
	"context"
	"fmt"
	"sync"
)

// MockCall records one Resolve invocation.
type MockCall struct {
	EnvID string
	Keys  []string
}

// Mock implements Resolver for testing. Tests pre-populate Values and
// assert against recorded Calls, mirroring runtime.MockRuntime.
type Mock struct {
	mu sync.Mutex

	// Values maps variable names to values, simulating the content of
	// the 1Password Environment. A key missing from Values causes
	// Resolve to fail unless Default is set.
	Values map[string]string

	// Default is returned for keys not present in Values when non-empty.
	Default string

	// Err, when set, is returned by every Resolve call.
	Err error

	// Calls records each Resolve invocation in order.
	Calls []MockCall
}

// Resolve implements Resolver.
func (m *Mock) Resolve(ctx context.Context, envID string, keys []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = append(m.Calls, MockCall{EnvID: envID, Keys: append([]string(nil), keys...)})

	if m.Err != nil {
		return nil, m.Err
	}

	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if val, ok := m.Values[key]; ok {
			result[key] = val
			continue
		}
		if m.Default != "" {
			result[key] = m.Default
			continue
		}
		return nil, fmt.Errorf("mock resolver: no value for %s in environment %s", key, envID)
	}
	return result, nil
}

// CallCount returns the number of Resolve invocations.
func (m *Mock) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}
