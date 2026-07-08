package secrets

import (
	"context"
	"fmt"
	"sync"
)

// Mock implements Resolver for testing. Tests pre-populate Environments
// and assert against recorded Calls, mirroring runtime.MockRuntime.
// Unlike OnePasswordCLI it does not cache, so Calls counts every read
// the project layer performs.
type Mock struct {
	mu sync.Mutex

	// Environments maps Environment IDs to their variables.
	Environments map[string]map[string]string

	// Err, when set, is returned by every ReadEnvironment call.
	Err error

	// Calls records the Environment ID of each ReadEnvironment invocation.
	Calls []string
}

// ReadEnvironment implements Resolver.
func (m *Mock) ReadEnvironment(ctx context.Context, envID string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Calls = append(m.Calls, envID)

	if m.Err != nil {
		return nil, m.Err
	}

	vars, ok := m.Environments[envID]
	if !ok {
		return nil, fmt.Errorf("mock resolver: unknown environment %s", envID)
	}

	result := make(map[string]string, len(vars))
	for k, v := range vars {
		result[k] = v
	}
	return result, nil
}

// CallCount returns the number of ReadEnvironment invocations.
func (m *Mock) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}
