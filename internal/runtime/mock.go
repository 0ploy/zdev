package runtime

import (
	"context"
	"fmt"
	"sync"
)

// MockCall records a single method call to MockRuntime
type MockCall struct {
	Method string
	Args   []interface{}
}

// MockRuntime implements Runtime for testing purposes.
// It tracks all calls, maintains state maps, and allows error injection.
type MockRuntime struct {
	mu sync.Mutex

	// Calls records every method invocation in order
	Calls []MockCall

	// State maps - tests can pre-populate these and assert against them after calls
	ContainersExist   map[string]bool
	ContainersRunning map[string]bool
	NetworksExist     map[string]bool
	VolumesExist      map[string]bool
	ImagesExist       map[string]bool

	// Containers stores container configs that were created (keyed by name)
	Containers map[string]ContainerConfig

	// ContainerLabels stores labels per container name (for GetContainerLabels)
	ContainerLabels map[string]map[string]string

	// ImageLabels stores labels per image name (for GetImageLabels)
	ImageLabels map[string]map[string]string

	// BuiltImages stores build configs by tag (set by BuildImage)
	BuiltImages map[string]ImageBuildConfig

	// Volumes stores created volume names
	Volumes map[string]bool

	// Networks stores created network names
	Networks map[string]bool

	// Errors allows injecting errors for specific method names.
	// Key is the method name (e.g., "CreateContainer", "StartContainer").
	Errors map[string]error
}

// NewMockRuntime creates a MockRuntime with all maps initialized
func NewMockRuntime() *MockRuntime {
	return &MockRuntime{
		ContainersExist:   make(map[string]bool),
		ContainersRunning: make(map[string]bool),
		NetworksExist:     make(map[string]bool),
		VolumesExist:      make(map[string]bool),
		ImagesExist:       make(map[string]bool),
		Containers:        make(map[string]ContainerConfig),
		ContainerLabels:   make(map[string]map[string]string),
		ImageLabels:       make(map[string]map[string]string),
		BuiltImages:       make(map[string]ImageBuildConfig),
		Volumes:           make(map[string]bool),
		Networks:          make(map[string]bool),
		Errors:            make(map[string]error),
	}
}

func (m *MockRuntime) record(method string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, MockCall{Method: method, Args: args})
}

func (m *MockRuntime) err(method string) error {
	if e, ok := m.Errors[method]; ok {
		return e
	}
	return nil
}

// CallCount returns the number of times the given method was called
func (m *MockRuntime) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, c := range m.Calls {
		if c.Method == method {
			count++
		}
	}
	return count
}

// CalledWith returns true if the method was called with the given first string argument
func (m *MockRuntime) CalledWith(method, arg string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Calls {
		if c.Method == method && len(c.Args) > 0 {
			if s, ok := c.Args[0].(string); ok && s == arg {
				return true
			}
		}
	}
	return false
}

// CreateContainer creates a container and tracks it in state
func (m *MockRuntime) CreateContainer(_ context.Context, cfg ContainerConfig) (string, error) {
	m.record("CreateContainer", cfg.Name, cfg)
	if err := m.err("CreateContainer"); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Containers[cfg.Name] = cfg
	m.ContainersExist[cfg.Name] = true
	return fmt.Sprintf("mock-%s", cfg.Name), nil
}

// RunContainer records a one-shot foreground run
func (m *MockRuntime) RunContainer(_ context.Context, cfg RunConfig) error {
	m.record("RunContainer", cfg.Image, cfg)
	return m.err("RunContainer")
}

// StartContainer starts a container
func (m *MockRuntime) StartContainer(_ context.Context, nameOrID string) error {
	m.record("StartContainer", nameOrID)
	if err := m.err("StartContainer"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ContainersRunning[nameOrID] = true
	return nil
}

// StopContainer stops a container
func (m *MockRuntime) StopContainer(_ context.Context, nameOrID string) error {
	m.record("StopContainer", nameOrID)
	if err := m.err("StopContainer"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ContainersRunning[nameOrID] = false
	return nil
}

// RemoveContainer removes a container
func (m *MockRuntime) RemoveContainer(_ context.Context, nameOrID string) error {
	m.record("RemoveContainer", nameOrID)
	if err := m.err("RemoveContainer"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Containers, nameOrID)
	m.ContainersExist[nameOrID] = false
	m.ContainersRunning[nameOrID] = false
	return nil
}

// ContainerExists checks if a container exists in the mock state
func (m *MockRuntime) ContainerExists(_ context.Context, name string) (bool, error) {
	m.record("ContainerExists", name)
	if err := m.err("ContainerExists"); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ContainersExist[name], nil
}

// IsContainerRunning checks if a container is running in the mock state
func (m *MockRuntime) IsContainerRunning(_ context.Context, name string) (bool, error) {
	m.record("IsContainerRunning", name)
	if err := m.err("IsContainerRunning"); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ContainersRunning[name], nil
}

// GetContainer returns a mock Container
func (m *MockRuntime) GetContainer(_ context.Context, name string) (*Container, error) {
	m.record("GetContainer", name)
	if err := m.err("GetContainer"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ContainersExist[name] {
		return nil, nil
	}
	running := m.ContainersRunning[name]
	status := "exited"
	if running {
		status = "running"
	}
	return &Container{
		ID:      fmt.Sprintf("mock-%s", name),
		Name:    name,
		Status:  status,
		Running: running,
	}, nil
}

// GetContainerLabels returns labels for a mock container
func (m *MockRuntime) GetContainerLabels(_ context.Context, name string) (map[string]string, error) {
	m.record("GetContainerLabels", name)
	if err := m.err("GetContainerLabels"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if labels, ok := m.ContainerLabels[name]; ok {
		return labels, nil
	}
	// Return labels from container config if available
	if cfg, ok := m.Containers[name]; ok {
		return cfg.Labels, nil
	}
	return map[string]string{}, nil
}

// Exec records the call and returns any configured error
func (m *MockRuntime) Exec(_ context.Context, container string, cmd []string, interactive bool, opts ExecOptions) error {
	m.record("Exec", container, cmd, interactive, opts)
	return m.err("Exec")
}

// Logs records the call and returns any configured error
func (m *MockRuntime) Logs(_ context.Context, container string, opts LogsOptions) error {
	m.record("Logs", container, opts)
	return m.err("Logs")
}

// CreateNetwork creates a network in the mock state
func (m *MockRuntime) CreateNetwork(_ context.Context, name string) error {
	m.record("CreateNetwork", name)
	if err := m.err("CreateNetwork"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Networks[name] = true
	m.NetworksExist[name] = true
	return nil
}

// RemoveNetwork removes a network from the mock state
func (m *MockRuntime) RemoveNetwork(_ context.Context, name string) error {
	m.record("RemoveNetwork", name)
	if err := m.err("RemoveNetwork"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Networks, name)
	m.NetworksExist[name] = false
	return nil
}

// NetworkExists checks if a network exists in the mock state
func (m *MockRuntime) NetworkExists(_ context.Context, name string) (bool, error) {
	m.record("NetworkExists", name)
	if err := m.err("NetworkExists"); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.NetworksExist[name], nil
}

// NetworkConnect records the call
func (m *MockRuntime) NetworkConnect(_ context.Context, networkName, containerName string, aliases ...string) error {
	m.record("NetworkConnect", networkName, containerName)
	return m.err("NetworkConnect")
}

// NetworkDisconnect records the call
func (m *MockRuntime) NetworkDisconnect(_ context.Context, networkName, containerName string) error {
	m.record("NetworkDisconnect", networkName, containerName)
	return m.err("NetworkDisconnect")
}

// CreateVolume creates a volume in the mock state
func (m *MockRuntime) CreateVolume(_ context.Context, name string) error {
	m.record("CreateVolume", name)
	if err := m.err("CreateVolume"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Volumes[name] = true
	m.VolumesExist[name] = true
	return nil
}

// RemoveVolume removes a volume from the mock state
func (m *MockRuntime) RemoveVolume(_ context.Context, name string) error {
	m.record("RemoveVolume", name)
	if err := m.err("RemoveVolume"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Volumes, name)
	m.VolumesExist[name] = false
	return nil
}

// VolumeExists checks if a volume exists in the mock state
func (m *MockRuntime) VolumeExists(_ context.Context, name string) (bool, error) {
	m.record("VolumeExists", name)
	if err := m.err("VolumeExists"); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.VolumesExist[name], nil
}

// CopyVolume records the call
func (m *MockRuntime) CopyVolume(_ context.Context, srcVolume, dstVolume, image string) error {
	m.record("CopyVolume", srcVolume, dstVolume, image)
	if err := m.err("CopyVolume"); err != nil {
		return err
	}
	// Simulate the copy: if src exists, mark dst as existing too
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.VolumesExist[srcVolume] {
		m.Volumes[dstVolume] = true
		m.VolumesExist[dstVolume] = true
	}
	return nil
}

// ListVolumes returns an empty list (override by setting a custom error or extending mock)
func (m *MockRuntime) ListVolumes(_ context.Context, filter string) ([]Volume, error) {
	m.record("ListVolumes", filter)
	if err := m.err("ListVolumes"); err != nil {
		return nil, err
	}
	return []Volume{}, nil
}

// PullImage records the call
func (m *MockRuntime) PullImage(_ context.Context, image string) error {
	m.record("PullImage", image)
	return m.err("PullImage")
}

// ImageExists checks if an image exists in the mock state.
// Defaults to true if the image is not explicitly set in ImagesExist.
func (m *MockRuntime) ImageExists(_ context.Context, image string) (bool, error) {
	m.record("ImageExists", image)
	if err := m.err("ImageExists"); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Default to true so tests don't need to pre-populate every image
	if exists, ok := m.ImagesExist[image]; ok {
		return exists, nil
	}
	return true, nil
}

// GetImageLabels returns labels for a mock image
func (m *MockRuntime) GetImageLabels(_ context.Context, image string) (map[string]string, error) {
	m.record("GetImageLabels", image)
	if err := m.err("GetImageLabels"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if labels, ok := m.ImageLabels[image]; ok {
		return labels, nil
	}
	return map[string]string{}, nil
}

// BuildImage records the build and marks the tag as existing with its labels
func (m *MockRuntime) BuildImage(_ context.Context, cfg ImageBuildConfig) error {
	m.record("BuildImage", cfg.Tag, cfg)
	if err := m.err("BuildImage"); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BuiltImages[cfg.Tag] = cfg
	m.ImagesExist[cfg.Tag] = true
	m.ImageLabels[cfg.Tag] = cfg.Labels
	return nil
}
