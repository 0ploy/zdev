package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

// StaleFileMount is a single-file bind mount that no longer points at the file
// living on the host.
type StaleFileMount struct {
	ServiceName   string
	ContainerPath string
}

// StaleFileMounts reports single-file bind mounts whose host file was replaced
// after the container started.
//
// A bind mount of a single FILE resolves to an inode, not to a name. Most
// editors - and any tool that writes a temp file and renames it over the
// original - replace the inode rather than writing through it, and the
// container then keeps the old, unlinked one. On the host the file is present
// and correct; inside the container it is stale or gone, and nothing says why.
// zdev cannot repair it (the mount is fixed for the container's lifetime) but
// it can stop the user from spending half an hour looking for the bug in their
// own code.
//
// The kernel marks such a mount by suffixing its root with "//deleted" in
// /proc/self/mountinfo, but that marker alone is NOT the failure: on Docker
// Desktop's virtiofs the mount keeps resolving by path, so the container reads
// the new content and the marker is harmless. Measured, not assumed - warning
// on the marker alone would fire on every editor save. So each marked mount is
// confirmed against the host: gone inside the container, or a different size
// or mtime than the file the host has.
//
// Diagnostic only: any container that cannot be inspected is skipped silently.
func (p *Project) StaleFileMounts(ctx context.Context) []StaleFileMount {
	serviceNames := make([]string, 0, len(p.Config.Services))
	for serviceName := range p.Config.Services {
		serviceNames = append(serviceNames, serviceName)
	}
	sort.Strings(serviceNames)

	var stale []StaleFileMount
	for _, serviceName := range serviceNames {
		targets := p.fileBindTargets(p.Config.Services[serviceName])
		if len(targets) == 0 {
			continue
		}

		containerName := p.ContainerName(serviceName)
		if running, err := p.Runtime.IsContainerRunning(ctx, containerName); err != nil || !running {
			continue
		}

		mountinfo, err := p.Runtime.ExecOutput(ctx, containerName, []string{"cat", "/proc/self/mountinfo"})
		if err != nil {
			// No shell utilities in the image, or the container went away
			// between the check and the exec.
			continue
		}

		for _, mountPoint := range deletedMountPoints(mountinfo) {
			// Only report mounts zdev itself declared as single files. Other
			// deleted mounts in the table are the image's business.
			hostPath, declared := targets[mountPoint]
			if !declared {
				continue
			}
			if p.fileMountDiverged(ctx, containerName, mountPoint, hostPath) {
				stale = append(stale, StaleFileMount{ServiceName: serviceName, ContainerPath: mountPoint})
			}
		}
	}

	return stale
}

// fileBindTargets maps the container path of each single-file bind mount of a
// service to its source on the host. Directory binds are resolved by name and
// never go stale this way.
func (p *Project) fileBindTargets(svc config.ServiceConfig) map[string]string {
	targets := make(map[string]string)

	for _, vol := range svc.Volumes {
		if !isBindMount(vol) {
			continue
		}
		source, target, _ := parseVolumeMount(vol)

		absSource := source
		if !filepath.IsAbs(source) {
			absSource = filepath.Join(p.Dir, source)
		}
		if info, err := os.Stat(absSource); err == nil && !info.IsDir() {
			targets[target] = absSource
		}
	}

	return targets
}

// fileMountDiverged reports whether the container's view of a file bind has
// actually parted ways with the host's. Missing inside the container is the
// clearest case; otherwise size and mtime are compared, which catches a mount
// still serving the old inode's contents. When the container has no `stat` the
// answer is "not diverged" - a diagnostic must not guess.
func (p *Project) fileMountDiverged(ctx context.Context, containerName, containerPath, hostPath string) bool {
	hostInfo, err := os.Stat(hostPath)
	if err != nil {
		return false
	}

	out, err := p.Runtime.ExecOutput(ctx, containerName, []string{"stat", "-c", "%s:%Y", containerPath})
	if err != nil {
		// Either the path is gone inside the container - which is the failure
		// itself - or the image has no stat. Tell them apart.
		_, existsErr := p.Runtime.ExecOutput(ctx, containerName, []string{"test", "-e", containerPath})
		return existsErr != nil
	}

	return strings.TrimSpace(out) != fmt.Sprintf("%d:%d", hostInfo.Size(), hostInfo.ModTime().Unix())
}

// deletedMountPoints parses /proc/self/mountinfo and returns the mount points
// whose backing file has been unlinked. Per proc(5) field 4 is the mount's
// root within its filesystem and field 5 is the mount point; the kernel
// appends "//deleted" to the former once the inode is gone.
func deletedMountPoints(mountinfo string) []string {
	var points []string

	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if strings.HasSuffix(fields[3], "//deleted") {
			points = append(points, fields[4])
		}
	}

	return points
}
