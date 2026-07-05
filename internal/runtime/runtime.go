package runtime

import "context"

// Runtime abstracts container operations.
// This interface allows swapping Docker for Podman or other runtimes.
type Runtime interface {
	// Container operations
	CreateContainer(ctx context.Context, cfg ContainerConfig) (string, error)
	StartContainer(ctx context.Context, nameOrID string) error
	StopContainer(ctx context.Context, nameOrID string) error
	RemoveContainer(ctx context.Context, nameOrID string) error

	// Container inspection
	ContainerExists(ctx context.Context, name string) (bool, error)
	IsContainerRunning(ctx context.Context, name string) (bool, error)
	GetContainer(ctx context.Context, name string) (*Container, error)
	GetContainerLabels(ctx context.Context, name string) (map[string]string, error)

	// Exec runs a command in a running container
	// If interactive is true, stdin/stdout/stderr are attached to the terminal
	Exec(ctx context.Context, container string, cmd []string, interactive bool, opts ExecOptions) error

	// Logs streams container logs to stdout/stderr
	Logs(ctx context.Context, container string, opts LogsOptions) error

	// Network operations
	CreateNetwork(ctx context.Context, name string) error
	RemoveNetwork(ctx context.Context, name string) error
	NetworkExists(ctx context.Context, name string) (bool, error)
	NetworkConnect(ctx context.Context, networkName, containerName string, aliases ...string) error
	NetworkDisconnect(ctx context.Context, networkName, containerName string) error

	// Volume operations
	CreateVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeExists(ctx context.Context, name string) (bool, error)
	ListVolumes(ctx context.Context, filter string) ([]Volume, error)
	CopyVolume(ctx context.Context, srcVolume, dstVolume, image string) error

	// Image operations
	PullImage(ctx context.Context, image string) error
	ImageExists(ctx context.Context, image string) (bool, error)
	GetImageLabels(ctx context.Context, image string) (map[string]string, error)
	BuildImage(ctx context.Context, cfg ImageBuildConfig) error
}
