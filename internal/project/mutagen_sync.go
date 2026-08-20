package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/mutagen"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/tools"
)

// MutagenSyncMount describes a bind mount to be synced via Mutagen
type MutagenSyncMount struct {
	ServiceName   string // Service this mount belongs to
	HostPath      string // Absolute path on host
	ContainerPath string // Path inside container
	VolumeName    string // Docker volume name for sync
	SessionName   string // Mutagen session name
	// Beta-side defaults stamped on new files/dirs inside the container.
	// Empty strings mean "don't pass the flag" (Mutagen falls back to its own defaults).
	Owner         string
	Group         string
	FileMode      string
	DirectoryMode string
}

// MutagenSessionName returns the Mutagen session name for a service
// Pattern: zdev-<project>-<service> (hyphens - Mutagen only allows alphanumeric and hyphens)
func (p *Project) MutagenSessionName(serviceName string) string {
	return fmt.Sprintf("zdev-%s-%s", p.Config.Name, serviceName)
}

// MutagenVolumeName returns the Docker volume name for Mutagen sync
// Same as session name for clarity
func (p *Project) MutagenVolumeName(serviceName string) string {
	return runtime.MutagenSyncVolumeName(serviceName, p.Config.Name)
}

// isBindMount checks if a volume string represents a bind mount (vs named volume)
func isBindMount(volume string) bool {
	source, _, isNamed := parseVolumeMount(volume)
	if isNamed {
		return false
	}
	// It's a bind mount if it starts with / or . or contains path separators
	return strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.Contains(source, string(os.PathSeparator))
}

// GetMutagenSyncMounts returns all bind mounts that should be synced via Mutagen
// Only directories are synced - file mounts are kept as regular bind mounts
func (p *Project) GetMutagenSyncMounts() []MutagenSyncMount {
	var mounts []MutagenSyncMount

	for serviceName, svc := range p.Config.Services {
		for _, vol := range svc.Volumes {
			if !isBindMount(vol) {
				continue
			}

			source, target, _ := parseVolumeMount(vol)

			// Resolve source to absolute path
			absSource := source
			if !filepath.IsAbs(source) {
				absSource = filepath.Join(p.Dir, source)
			}

			// Only sync directories - Mutagen doesn't support single file sync
			info, err := os.Stat(absSource)
			if err != nil || !info.IsDir() {
				continue
			}

			mounts = append(mounts, MutagenSyncMount{
				ServiceName:   serviceName,
				HostPath:      absSource,
				ContainerPath: target,
				VolumeName:    p.MutagenVolumeName(serviceName),
				SessionName:   p.MutagenSessionName(serviceName),
				Owner:         svc.Mutagen.User,
				Group:         svc.Mutagen.Group,
				FileMode:      svc.Mutagen.FileMode,
				DirectoryMode: svc.Mutagen.DirectoryMode,
			})
		}
	}

	return mounts
}

// EnsureMutagen ensures the Mutagen binary is available and daemon is running
func (p *Project) EnsureMutagen(ctx context.Context) (*mutagen.Mutagen, error) {
	toolMgr, err := tools.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tool manager: %w", err)
	}

	mutagenPath, err := toolMgr.EnsureTool(ctx, tools.MutagenTool())
	if err != nil {
		return nil, fmt.Errorf("failed to ensure mutagen: %w", err)
	}

	m := mutagen.New(mutagenPath)

	if err := m.EnsureDaemon(ctx); err != nil {
		return nil, fmt.Errorf("failed to start mutagen daemon: %w", err)
	}

	return m, nil
}

// createMutagenVolumes creates Docker volumes for Mutagen sync
func (p *Project) createMutagenVolumes(ctx context.Context, mounts []MutagenSyncMount) error {
	for _, mount := range mounts {
		exists, err := p.Runtime.VolumeExists(ctx, mount.VolumeName)
		if err != nil {
			return fmt.Errorf("failed to check volume %s: %w", mount.VolumeName, err)
		}
		if !exists {
			fmt.Printf("Creating sync volume %s...\n", mount.VolumeName)
			if err := p.Runtime.CreateVolume(ctx, mount.VolumeName); err != nil {
				return fmt.Errorf("failed to create volume %s: %w", mount.VolumeName, err)
			}
		}
	}
	return nil
}

// startMutagenSessions creates or resumes Mutagen sync sessions. It returns
// the names of sessions that were freshly (re)created during this call, so
// the caller can perform post-creation work (e.g., chown pre-existing files
// in the container after the initial flush).
func (p *Project) startMutagenSessions(ctx context.Context, m *mutagen.Mutagen, mounts []MutagenSyncMount) (map[string]bool, error) {
	recreated := make(map[string]bool)

	for _, mount := range mounts {
		exists, err := m.SessionExists(ctx, mount.SessionName)
		if err != nil {
			return nil, fmt.Errorf("failed to check session %s: %w", mount.SessionName, err)
		}

		containerName := p.ContainerName(mount.ServiceName)
		beta := fmt.Sprintf("docker://%s%s", containerName, mount.ContainerPath)

		// Build the desired session config once - reused for hashing AND
		// for any actual create call below.
		ignores := mutagen.MergeIgnores(p.Config.Mutagen.Ignore)
		desired := mutagen.SessionConfig{
			Name:                     mount.SessionName,
			Alpha:                    mount.HostPath,
			Beta:                     beta,
			Ignores:                  ignores,
			DefaultOwnerBeta:         mount.Owner,
			DefaultGroupBeta:         mount.Group,
			DefaultFileModeBeta:      mount.FileMode,
			DefaultDirectoryModeBeta: mount.DirectoryMode,
		}
		desiredHash := desired.Hash()

		if exists {
			storedHash, _ := readSessionHash(mount.SessionName)
			if storedHash == desiredHash {
				// Resume existing session unchanged.
				fmt.Printf("Resuming sync session %s...\n", mount.SessionName)
				if err := m.ResumeSession(ctx, mount.SessionName); err != nil {
					// Ignore resume errors - session might already be running
					fmt.Printf("Note: could not resume session (may already be running): %v\n", err)
				}
				continue
			}

			// Drift detected (or no stored hash from a pre-upgrade install) -
			// terminate so we can recreate with the new defaults.
			fmt.Printf("Mutagen config changed for %s, recreating sync session...\n", mount.SessionName)
			if err := m.TerminateSession(ctx, mount.SessionName); err != nil {
				return nil, fmt.Errorf("failed to terminate stale session %s: %w", mount.SessionName, err)
			}
		} else {
			fmt.Printf("Creating sync session %s...\n", mount.SessionName)
		}

		if err := m.CreateSession(ctx, desired); err != nil {
			return nil, fmt.Errorf("failed to create session %s: %w", mount.SessionName, err)
		}
		if err := writeSessionHash(mount.SessionName, desiredHash); err != nil {
			fmt.Printf("Warning: could not record sync session hash for %s: %v\n", mount.SessionName, err)
		}
		recreated[mount.SessionName] = true
	}

	return recreated, nil
}

// mutagenSessionHashDir returns the directory where Mutagen session config
// hashes are persisted. Each session gets one file at <dir>/<session-name>.
func mutagenSessionHashDir() string {
	return filepath.Join(config.GetZdevHome(), "mutagen", "sessions")
}

// readSessionHash returns the previously-stamped config hash for the given
// session, or "" if none is recorded (treated as "drift" so the caller will
// recreate the session and stamp a fresh hash).
func readSessionHash(sessionName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(mutagenSessionHashDir(), sessionName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeSessionHash records the config hash for a session so future starts
// can detect drift without re-querying Mutagen for every flag.
func writeSessionHash(sessionName, hash string) error {
	dir := mutagenSessionHashDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sessionName), []byte(hash+"\n"), 0o644)
}

// removeSessionHash deletes the stored hash file for a session. Best-effort:
// missing file is fine, other errors are returned for callers that care.
func removeSessionHash(sessionName string) error {
	err := os.Remove(filepath.Join(mutagenSessionHashDir(), sessionName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// pauseMutagenSessions pauses all Mutagen sync sessions for this project
func (p *Project) pauseMutagenSessions(ctx context.Context) {
	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return // Silently ignore - Mutagen might not be set up
	}

	mounts := p.GetMutagenSyncMounts()
	for _, mount := range mounts {
		exists, _ := m.SessionExists(ctx, mount.SessionName)
		if exists {
			fmt.Printf("Pausing sync session %s...\n", mount.SessionName)
			if err := m.PauseSession(ctx, mount.SessionName); err != nil {
				fmt.Printf("Warning: could not pause session %s: %v\n", mount.SessionName, err)
			}
		}
	}
}

// terminateMutagenSessions terminates all Mutagen sync sessions for this project
func (p *Project) terminateMutagenSessions(ctx context.Context) {
	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return // Silently ignore
	}

	mounts := p.GetMutagenSyncMounts()
	for _, mount := range mounts {
		exists, _ := m.SessionExists(ctx, mount.SessionName)
		if exists {
			fmt.Printf("Terminating sync session %s...\n", mount.SessionName)
			if err := m.TerminateSession(ctx, mount.SessionName); err != nil {
				fmt.Printf("Warning: could not terminate session %s: %v\n", mount.SessionName, err)
			}
		}
		_ = removeSessionHash(mount.SessionName)
	}
}

// waitForInitialSync waits for Mutagen sync sessions to complete initial sync
func (p *Project) waitForInitialSync(ctx context.Context, m *mutagen.Mutagen, mounts []MutagenSyncMount, timeout time.Duration) {
	if len(mounts) == 0 {
		return
	}

	fmt.Println("Waiting for initial file sync...")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, mount := range mounts {
		if err := m.FlushSession(ctx, mount.SessionName); err != nil {
			if ctx.Err() != nil {
				fmt.Printf("Warning: sync timeout - files may still be syncing in the background\n")
				return
			}
			fmt.Printf("Warning: could not wait for sync %s: %v\n", mount.SessionName, err)
		}
	}

	fmt.Println("Initial sync complete")
}

// IsMutagenEnabled checks if Mutagen is enabled for this project
func (p *Project) IsMutagenEnabled() bool {
	globalCfg, err := config.LoadGlobalConfig()
	if err != nil {
		return false
	}
	return globalCfg.IsMutagenEnabled()
}

// prepareMutagen ensures the Mutagen daemon is up and the sync volumes exist
// before any container that references them is created. Returns the daemon
// handle, the discovered mounts, and a service-keyed lookup map suitable for
// buildContainerConfig. When Mutagen is disabled the returned daemon is nil
// and the slices/map are empty - callers should treat that as "no Mutagen".
func (p *Project) prepareMutagen(ctx context.Context) (*mutagen.Mutagen, []MutagenSyncMount, map[string]MutagenSyncMount, error) {
	if !p.IsMutagenEnabled() {
		return nil, nil, nil, nil
	}

	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize Mutagen: %w", err)
	}

	mounts := p.GetMutagenSyncMounts()
	if err := p.createMutagenVolumes(ctx, mounts); err != nil {
		return nil, nil, nil, err
	}

	mountMap := make(map[string]MutagenSyncMount, len(mounts))
	for _, mount := range mounts {
		mountMap[mount.ServiceName] = mount
	}
	return m, mounts, mountMap, nil
}

// finalizeMutagen starts/resumes sync sessions for the given mounts, waits for
// the initial sync, and signals containers that they may proceed past the
// sync-ready gate. Safe to call with a nil daemon or empty mounts (no-op).
//
// For sessions freshly (re)created in this call, after the initial flush we
// chown the synced tree inside the container to the configured owner/group.
// Mutagen's --default-*-beta flags only stamp NEW files, so without this step
// pre-existing container-side files (or files synced before a config change)
// would keep their previous ownership and the in-container process (e.g.
// www-data) would still be unable to read them.
func (p *Project) finalizeMutagen(ctx context.Context, m *mutagen.Mutagen, mounts []MutagenSyncMount) error {
	if m == nil || len(mounts) == 0 {
		return nil
	}
	recreated, err := p.startMutagenSessions(ctx, m, mounts)
	if err != nil {
		return fmt.Errorf("failed to start Mutagen sync: %w", err)
	}
	p.waitForInitialSync(ctx, m, mounts, 60*time.Second)
	p.signalSyncReady(ctx, mounts)
	p.applyPostSyncOwnership(ctx, mounts, recreated)
	return nil
}

// applyPostSyncOwnership runs `chown -R` inside each container whose Mutagen
// session was just (re)created and whose service config sets a Mutagen owner
// or group. Best-effort: failures are logged but don't block startup.
func (p *Project) applyPostSyncOwnership(ctx context.Context, mounts []MutagenSyncMount, recreated map[string]bool) {
	for _, mount := range mounts {
		if !recreated[mount.SessionName] {
			continue
		}
		if mount.Owner == "" && mount.Group == "" {
			continue
		}
		spec := mount.Owner
		if mount.Group != "" {
			spec = spec + ":" + mount.Group
		}
		containerName := p.ContainerName(mount.ServiceName)
		fmt.Printf("Applying ownership %s to %s in %s...\n", spec, mount.ContainerPath, containerName)
		err := p.Runtime.Exec(ctx, containerName,
			[]string{"chown", "-R", spec, mount.ContainerPath}, false, runtime.ExecOptions{})
		if err != nil {
			fmt.Printf("Warning: could not chown %s in %s: %v\n", mount.ContainerPath, containerName, err)
		}
	}
}

// transformVolumesForMutagen transforms bind mounts to Mutagen sync volumes
// Returns the modified volumes list for container creation
func (p *Project) transformVolumesForMutagen(serviceName string, volumes []string, mutagenMounts map[string]MutagenSyncMount) []runtime.VolumeMount {
	var result []runtime.VolumeMount

	for _, vol := range volumes {
		source, target, isNamedVolume := parseVolumeMount(vol)

		if isNamedVolume {
			// Named volume - prefix with project name
			result = append(result, runtime.VolumeMount{
				Source: p.VolumeName(source),
				Target: target,
			})
		} else if isBindMount(vol) {
			// Bind mount - use Mutagen sync volume instead
			mount, ok := mutagenMounts[serviceName]
			if ok && mount.ContainerPath == target {
				result = append(result, runtime.VolumeMount{
					Source: mount.VolumeName,
					Target: target,
				})
			} else {
				// Fallback to bind mount if not in Mutagen mounts
				result = append(result, runtime.VolumeMount{
					Source: source,
					Target: target,
				})
			}
		} else {
			// Regular bind mount
			result = append(result, runtime.VolumeMount{
				Source: source,
				Target: target,
			})
		}
	}

	return result
}

// signalSyncReady writes a marker file into each container that has a Mutagen sync mount,
// unblocking the sync-ready gate in the container's entrypoint wrapper.
func (p *Project) signalSyncReady(ctx context.Context, mounts []MutagenSyncMount) {
	for _, mount := range mounts {
		containerName := p.ContainerName(mount.ServiceName)
		err := p.Runtime.Exec(ctx, containerName,
			[]string{"sh", "-c", "touch /.zdev-sync-ready"}, false, runtime.ExecOptions{})
		if err != nil {
			fmt.Printf("Warning: could not signal sync-ready for %s: %v\n", mount.ServiceName, err)
		}
	}
}
