package runtime

// ContainerConfig defines how to create a container
type ContainerConfig struct {
	Name        string
	Image       string
	Command     []string          // Optional command override
	Env         map[string]string // Environment variables
	Labels      map[string]string // Container labels
	WorkingDir  string            // Working directory inside container
	Volumes     []VolumeMount     // Volume mounts
	Ports       []string          // Port mappings (e.g., "5432:5432", "127.0.0.1:3306:3306")
	NetworkName string            // Network to attach to
	Aliases     []string          // Network aliases for DNS
	// RestartPolicy maps to `docker create --restart` (e.g. "unless-stopped").
	// Deliberately NOT part of ComputeConfigHash (the hash payload is
	// explicit, so this field is invisible to it) - it's set once at
	// creation and never a drift signal. Empty means no policy (Docker
	// default "no").
	RestartPolicy string
}

// VolumeMount defines a volume or bind mount
type VolumeMount struct {
	Source   string // Host path or volume name
	Target   string // Container path
	ReadOnly bool
}

// ImageBuildConfig defines how to build a local image from a Dockerfile
type ImageBuildConfig struct {
	Tag        string            // Image tag to build into
	Context    string            // Build context directory (absolute path)
	Dockerfile string            // Dockerfile path (absolute path)
	Labels     map[string]string // Labels stamped on the image (--label)
}

// RunConfig defines a one-shot foreground container (docker run --rm). Used for
// short-lived jobs like the create-time scaffold hook. Entrypoint overrides the
// image's ENTRYPOINT, which lets a caller bypass a process manager baked into
// the image (e.g. run a plain shell instead of the image's init).
type RunConfig struct {
	Image      string
	Entrypoint string            // Overrides the image ENTRYPOINT; empty keeps it
	Command    []string          // Args passed after the image
	Env        map[string]string // Environment variables
	Volumes    []VolumeMount     // Volume/bind mounts
	WorkingDir string            // Working directory inside the container
}

// Container represents a running or stopped container
type Container struct {
	ID      string
	Name    string
	Image   string
	Status  string // "running", "exited", "created", etc.
	Running bool
}

// Network represents a Docker network
type Network struct {
	ID   string
	Name string
}

// Volume represents a Docker volume
type Volume struct {
	Name       string
	Mountpoint string
	Size       string // Human-readable size (e.g., "1.2GB")
}

// ExecOptions contains options for executing a command in a container
type ExecOptions struct {
	User    string // Username or UID to run command as
	Workdir string // Working directory inside the container
}
