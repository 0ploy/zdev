package runtime

import "strings"

import "io"

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
	Target   string // Container path, without mode options
	ReadOnly bool
	// Options holds further comma-separated mount options from the config
	// (e.g. "z", "cached"), passed through to docker as-is. "ro" and "rw"
	// are never in here - ReadOnly carries that.
	Options string
}

// Arg renders the mount as a `docker -v` value: source:target[:options].
func (v VolumeMount) Arg() string {
	mount := v.Source + ":" + v.Target
	var opts []string
	if v.ReadOnly {
		opts = append(opts, "ro")
	}
	if v.Options != "" {
		opts = append(opts, v.Options)
	}
	if len(opts) > 0 {
		mount += ":" + strings.Join(opts, ",")
	}
	return mount
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
	// Interactive attaches a TTY (-it) when the caller's stdin is a terminal, so
	// the command can prompt the user (e.g. a scaffolder's module picker). When
	// stdin isn't a terminal (CI/piped), no TTY is attached and the command must
	// be able to run non-interactively.
	Interactive bool
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

// ExecStreamOptions configures ExecStream: unlike Exec, the caller owns the
// streams (so output can be captured) and can pass env vars.
type ExecStreamOptions struct {
	User    string
	Workdir string
	// Env is passed as `-e KEY` with the value in the docker CLI's own
	// environment, so values never show up in the process list.
	Env map[string]string
	// Interactive keeps stdin attached (-i). TTY allocates a terminal (-t).
	Interactive bool
	TTY         bool
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}
