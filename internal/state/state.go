package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"gopkg.in/yaml.v3"
)

// RoutingState tracks the routing ports used by a project
type RoutingState struct {
	TCPPorts []int `yaml:"tcp_ports,omitempty"`
	UDPPorts []int `yaml:"udp_ports,omitempty"`
}

// ProjectEntry represents a registered project in the state file
type ProjectEntry struct {
	Path        string       `yaml:"path"`
	LastStarted time.Time    `yaml:"last_started,omitempty"`
	Routing     RoutingState `yaml:"routing,omitempty"`
}

// LinkMember represents a member of a link network - either a whole project or a specific service
type LinkMember struct {
	Project string `yaml:"project"`
	Service string `yaml:"service,omitempty"` // Empty means all services
}

// LinkEntry represents a named link network and its members
type LinkEntry struct {
	Network string       `yaml:"network"`
	Members []LinkMember `yaml:"members"`
}

// State represents the global zdev state
type State struct {
	Projects map[string]ProjectEntry `yaml:"projects"`
	Links    map[string]LinkEntry    `yaml:"links,omitempty"`
}

// Manager handles reading and writing the state file
type Manager struct {
	path string
	mu   sync.Mutex
}

// DefaultManager returns a manager using the default state file location
func DefaultManager() (*Manager, error) {
	zdevDir := config.GetZdevHome()
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create .zdev directory: %w", err)
	}

	return &Manager{
		path: filepath.Join(zdevDir, "state.yaml"),
	}, nil
}

// NewManager creates a state manager with a custom path (useful for testing)
func NewManager(path string) *Manager {
	return &Manager{path: path}
}

// Load reads the state file. Safe for read-only callers.
// Mutating callers must use Mutate to avoid lost updates under concurrent access.
func (m *Manager) Load() (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

// Mutate atomically loads the state, applies fn, and saves the result.
// The entire load-modify-save cycle holds the manager's lock, so concurrent
// goroutines in the same process cannot clobber each other's changes.
// (Cross-process concurrency is not protected - callers running multiple
// zdev invocations simultaneously on the same state file can still race.)
func (m *Manager) Mutate(fn func(*State) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, err := m.loadLocked()
	if err != nil {
		return err
	}

	if err := fn(state); err != nil {
		return err
	}

	return m.saveLocked(state)
}

// loadLocked reads and parses the state file. Caller must hold m.mu.
func (m *Manager) loadLocked() (*State, error) {
	state := &State{
		Projects: make(map[string]ProjectEntry),
	}

	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil // Return empty state if file doesn't exist
		}
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	if err := yaml.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}

	// Ensure maps are initialized
	if state.Projects == nil {
		state.Projects = make(map[string]ProjectEntry)
	}
	if state.Links == nil {
		state.Links = make(map[string]LinkEntry)
	}

	return state, nil
}

// saveLocked writes the state file. Caller must hold m.mu.
func (m *Manager) saveLocked(state *State) error {
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	if err := os.WriteFile(m.path, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	return nil
}

// RegisterProject adds or updates a project in the state
func (m *Manager) RegisterProject(name, path string) error {
	return m.RegisterProjectWithRouting(name, path, nil, nil)
}

// RegisterProjectWithRouting adds or updates a project with routing info
func (m *Manager) RegisterProjectWithRouting(name, path string, tcpPorts, udpPorts []int) error {
	return m.Mutate(func(state *State) error {
		state.Projects[name] = ProjectEntry{
			Path:        path,
			LastStarted: time.Now(),
			Routing: RoutingState{
				TCPPorts: tcpPorts,
				UDPPorts: udpPorts,
			},
		}
		return nil
	})
}

// UnregisterProject removes a project from the state
func (m *Manager) UnregisterProject(name string) error {
	return m.Mutate(func(state *State) error {
		delete(state.Projects, name)
		return nil
	})
}

// GetProject returns a single project entry
func (m *Manager) GetProject(name string) (*ProjectEntry, error) {
	state, err := m.Load()
	if err != nil {
		return nil, err
	}

	entry, ok := state.Projects[name]
	if !ok {
		return nil, nil
	}

	return &entry, nil
}

// ListProjects returns all registered projects
func (m *Manager) ListProjects() (map[string]ProjectEntry, error) {
	state, err := m.Load()
	if err != nil {
		return nil, err
	}

	return state.Projects, nil
}

// GetAllRoutingPorts aggregates all TCP and UDP ports from all projects
func (m *Manager) GetAllRoutingPorts() (tcpPorts, udpPorts []int, err error) {
	state, err := m.Load()
	if err != nil {
		return nil, nil, err
	}

	tcpSet := make(map[int]bool)
	udpSet := make(map[int]bool)

	for _, entry := range state.Projects {
		for _, port := range entry.Routing.TCPPorts {
			tcpSet[port] = true
		}
		for _, port := range entry.Routing.UDPPorts {
			udpSet[port] = true
		}
	}

	for port := range tcpSet {
		tcpPorts = append(tcpPorts, port)
	}
	for port := range udpSet {
		udpPorts = append(udpPorts, port)
	}

	return tcpPorts, udpPorts, nil
}

// GetTCPPortOwner returns the project name that owns the given TCP port, or empty string
func (m *Manager) GetTCPPortOwner(port int) (string, error) {
	state, err := m.Load()
	if err != nil {
		return "", err
	}

	for name, entry := range state.Projects {
		for _, p := range entry.Routing.TCPPorts {
			if p == port {
				return name, nil
			}
		}
	}

	return "", nil
}

// GetUDPPortOwner returns the project name that owns the given UDP port, or empty string
func (m *Manager) GetUDPPortOwner(port int) (string, error) {
	state, err := m.Load()
	if err != nil {
		return "", err
	}

	for name, entry := range state.Projects {
		for _, p := range entry.Routing.UDPPorts {
			if p == port {
				return name, nil
			}
		}
	}

	return "", nil
}

// RenameProject renames a project entry in the state and updates all link memberships
func (m *Manager) RenameProject(oldName, newName string) error {
	return m.Mutate(func(st *State) error {
		entry, ok := st.Projects[oldName]
		if !ok {
			return fmt.Errorf("project %q not found in state", oldName)
		}

		if _, exists := st.Projects[newName]; exists {
			return fmt.Errorf("project %q already exists in state", newName)
		}

		// Move project entry
		st.Projects[newName] = entry
		delete(st.Projects, oldName)

		// Update link memberships
		for linkName, link := range st.Links {
			changed := false
			for i, member := range link.Members {
				if member.Project == oldName {
					link.Members[i].Project = newName
					changed = true
				}
			}
			if changed {
				st.Links[linkName] = link
			}
		}
		return nil
	})
}

// ValidateLinkName checks that a link name is valid for use as a Docker network suffix
func ValidateLinkName(name string) error {
	if name == "" {
		return fmt.Errorf("link name cannot be empty")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("link name %q contains invalid character %q (allowed: alphanumeric, hyphens, underscores)", name, string(c))
		}
	}
	return nil
}

// LinkNetworkName returns the Docker network name for a link
func LinkNetworkName(name string) string {
	return fmt.Sprintf("zdev_link_%s", name)
}

// ParseMember parses a member string like "myproject" or "myproject.app"
// into a LinkMember. If the string contains a dot, everything before the
// last dot is the project name and everything after is the service name.
func ParseMember(s string) LinkMember {
	idx := strings.LastIndex(s, ".")
	if idx == -1 {
		return LinkMember{Project: s}
	}
	return LinkMember{Project: s[:idx], Service: s[idx+1:]}
}

// String returns the member as a human-readable string
func (lm LinkMember) String() string {
	if lm.Service == "" {
		return lm.Project
	}
	return fmt.Sprintf("%s.%s", lm.Project, lm.Service)
}

// CreateLink creates a new named link
func (m *Manager) CreateLink(name string) error {
	return m.Mutate(func(st *State) error {
		if st.Links == nil {
			st.Links = make(map[string]LinkEntry)
		}

		if _, exists := st.Links[name]; exists {
			return fmt.Errorf("link %q already exists", name)
		}

		st.Links[name] = LinkEntry{
			Network: LinkNetworkName(name),
		}
		return nil
	})
}

// DeleteLink removes a named link
func (m *Manager) DeleteLink(name string) error {
	return m.Mutate(func(st *State) error {
		if st.Links == nil {
			return fmt.Errorf("link %q does not exist", name)
		}

		if _, exists := st.Links[name]; !exists {
			return fmt.Errorf("link %q does not exist", name)
		}

		delete(st.Links, name)
		return nil
	})
}

// GetLink returns a link entry by name
func (m *Manager) GetLink(name string) (*LinkEntry, error) {
	st, err := m.Load()
	if err != nil {
		return nil, err
	}

	if st.Links == nil {
		return nil, nil
	}

	entry, ok := st.Links[name]
	if !ok {
		return nil, nil
	}

	return &entry, nil
}

// ListLinks returns all links
func (m *Manager) ListLinks() (map[string]LinkEntry, error) {
	st, err := m.Load()
	if err != nil {
		return nil, err
	}

	if st.Links == nil {
		return make(map[string]LinkEntry), nil
	}

	return st.Links, nil
}

// AddLinkMembers adds members to a link
func (m *Manager) AddLinkMembers(name string, members []LinkMember) error {
	return m.Mutate(func(st *State) error {
		if st.Links == nil {
			return fmt.Errorf("link %q does not exist", name)
		}

		entry, exists := st.Links[name]
		if !exists {
			return fmt.Errorf("link %q does not exist", name)
		}

		for _, newMember := range members {
			found := false
			for _, existing := range entry.Members {
				if existing.Project == newMember.Project && existing.Service == newMember.Service {
					found = true
					break
				}
			}
			if !found {
				entry.Members = append(entry.Members, newMember)
			}
		}

		st.Links[name] = entry
		return nil
	})
}

// RemoveLinkMembers removes members from a link
func (m *Manager) RemoveLinkMembers(name string, members []LinkMember) error {
	return m.Mutate(func(st *State) error {
		if st.Links == nil {
			return fmt.Errorf("link %q does not exist", name)
		}

		entry, exists := st.Links[name]
		if !exists {
			return fmt.Errorf("link %q does not exist", name)
		}

		filtered := make([]LinkMember, 0, len(entry.Members))
		for _, existing := range entry.Members {
			remove := false
			for _, toRemove := range members {
				if existing.Project == toRemove.Project && existing.Service == toRemove.Service {
					remove = true
					break
				}
			}
			if !remove {
				filtered = append(filtered, existing)
			}
		}

		entry.Members = filtered
		st.Links[name] = entry
		return nil
	})
}

// GetLinksForProject returns all links that include the given project
func (m *Manager) GetLinksForProject(projectName string) (map[string]LinkEntry, error) {
	st, err := m.Load()
	if err != nil {
		return nil, err
	}

	result := make(map[string]LinkEntry)
	for linkName, entry := range st.Links {
		for _, member := range entry.Members {
			if member.Project == projectName {
				result[linkName] = entry
				break
			}
		}
	}

	return result, nil
}
