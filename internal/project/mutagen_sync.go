package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/mutagen"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/tools"
)

const (
	// syncReadyFlag is touched by zdev once a service's sync mounts are
	// verified; the command wrapper waits for it before exec'ing the service.
	//
	// It lives in /tmp because the wrapper runs as the image's default user,
	// which on plenty of images (percona, postgres, node) cannot write to /.
	// Such a container could neither be signalled nor clear a stale flag of
	// its own, so the gate would either deadlock or be skipped outright. /tmp
	// is 1777 in every image that has a shell to run the wrapper in.
	syncReadyFlag = "/tmp/.zdev-sync-ready"

	// syncWaitingFlag is raised by the wrapper itself to announce that it has
	// cleared the ready flag and is now waiting. It exists so zdev can order
	// itself against the wrapper instead of racing it.
	syncWaitingFlag = "/tmp/.zdev-sync-waiting"

	// legacySyncReadyFlag is the pre-/tmp location. zdev still touches it, as
	// root and best-effort, because template entrypoints were documented to
	// wait on it themselves. Nothing in zdev reads it.
	legacySyncReadyFlag = "/.zdev-sync-ready"

	// syncGateWaitTimeout bounds how long zdev waits for the wrapper to reach
	// the gate before signalling anyway.
	syncGateWaitTimeout = 10 * time.Second

	// syncGatePollInterval is how often the wrapper's readiness is polled.
	syncGatePollInterval = 200 * time.Millisecond
)

// SyncGateCommand wraps a service command in the sync-ready gate.
//
// The gate is re-armed on every container start: the flag lives in the
// container's writable layer, so a container that was let through once would
// skip the gate on every subsequent `docker start` - which is how a service
// that died on an empty directory could never recover without `docker rm`.
// Clearing it here (as PID 1, before anything else) makes the gate effective
// for plain `docker start` too, not only for containers zdev just created.
func SyncGateCommand(command string) string {
	return "rm -f " + syncReadyFlag + " " + legacySyncReadyFlag + " 2>/dev/null" +
		"; touch " + syncWaitingFlag +
		"; while [ ! -f " + syncReadyFlag + " ]; do sleep 0.2; done" +
		"; rm -f " + syncWaitingFlag +
		"; exec sh -c " + shellQuote(command)
}

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

// MutagenSessionName returns the Mutagen session name for a service with a
// single directory bind. Services with several name their sessions per mount
// (see SyncNames): sessions are addressed by name everywhere - SessionExists,
// GetSessionStatus, terminate, resume, the config-hash file - so two mounts
// sharing one name means the second is never created, because the existence
// check finds its sibling and skips it.
// Pattern: zdev-<project>-<service> (hyphens - Mutagen only allows alphanumeric and hyphens)
func (p *Project) MutagenSessionName(serviceName string) string {
	return fmt.Sprintf("zdev-%s-%s", p.Config.Name, serviceName)
}

// MutagenVolumeName returns the Docker volume name for Mutagen sync of a
// service with a single directory bind.
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
		mounts = append(mounts, p.serviceSyncMounts(serviceName, svc)...)
	}

	return mounts
}

// serviceSyncMounts returns one sync mount per directory bind of a service, in
// declaration order.
//
// Binds listed in the service's mutagen.no_sync are skipped entirely: they
// stay native Docker binds, which is the only way to guarantee content is
// there before the container's entrypoint runs.
//
// Naming rule: a service with exactly ONE directory bind keeps the historic
// per-service volume and session names, so the common case needs no migration
// on upgrade - its volume (and anything living at a Mutagen-ignored path
// inside it, like node_modules or vendor/) survives untouched. As soon as a
// service has more than one, every mount is named after its container path.
// Sharing one name across mounts is what made the second bind of a service
// silently lose both its session and its volume.
func (p *Project) serviceSyncMounts(serviceName string, svc config.ServiceConfig) []MutagenSyncMount {
	var mounts []MutagenSyncMount

	noSync := make(map[string]bool, len(svc.Mutagen.NoSync))
	for _, path := range svc.Mutagen.NoSync {
		noSync[path] = true
	}

	for _, vol := range svc.Volumes {
		if !isBindMount(vol) {
			continue
		}

		// Declared as a plain bind: the container needs it present the moment
		// it starts, before any sync session exists.
		if noSync[config.VolumeMountTarget(vol)] {
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
			Owner:         svc.Mutagen.User,
			Group:         svc.Mutagen.Group,
			FileMode:      svc.Mutagen.FileMode,
			DirectoryMode: svc.Mutagen.DirectoryMode,
		})
	}

	for i := range mounts {
		mounts[i].VolumeName, mounts[i].SessionName = SyncNames(
			serviceName, mounts[i].ContainerPath, p.Config.Name, len(mounts))
	}

	return mounts
}

// SyncNames returns the volume and session name for one sync mount. mountCount
// is how many sync mounts the service has in total, which is what decides
// between the historic per-service names and per-mount ones. Single source of
// truth for the rule - `zdev rename` has to reproduce it for the new project
// name, and a second copy of it would be a naming drift waiting to happen.
func SyncNames(serviceName, containerPath, projectName string, mountCount int) (volume, session string) {
	if mountCount <= 1 {
		return runtime.MutagenSyncVolumeName(serviceName, projectName),
			fmt.Sprintf("zdev-%s-%s", projectName, serviceName)
	}
	slug := runtime.MountSlug(containerPath)
	return runtime.MutagenSyncVolumeNameForMount(serviceName, slug, projectName),
		fmt.Sprintf("zdev-%s-%s-%s", projectName, serviceName, slug)
}

// MutagenMounts indexes a project's sync mounts by service name. A service can
// have several - one per directory bind - so consumers must look mounts up by
// (service, container path) and never assume a service has exactly one.
type MutagenMounts map[string][]MutagenSyncMount

// NewMutagenMounts indexes a flat mount list by service, preserving order.
func NewMutagenMounts(mounts []MutagenSyncMount) MutagenMounts {
	indexed := make(MutagenMounts, len(mounts))
	for _, mount := range mounts {
		indexed[mount.ServiceName] = append(indexed[mount.ServiceName], mount)
	}
	return indexed
}

// For returns the sync mounts of one service (nil when it has none).
func (m MutagenMounts) For(serviceName string) []MutagenSyncMount {
	return m[serviceName]
}

// Has reports whether a service has any sync mount at all.
func (m MutagenMounts) Has(serviceName string) bool {
	return len(m[serviceName]) > 0
}

// Lookup returns the sync mount a service declares at the given container
// path, if any.
func (m MutagenMounts) Lookup(serviceName, containerPath string) (MutagenSyncMount, bool) {
	for _, mount := range m[serviceName] {
		if mount.ContainerPath == containerPath {
			return mount, true
		}
	}
	return MutagenSyncMount{}, false
}

// validateMountNames guards the derived names against collisions. Two distinct
// container paths can slug to the same token (/app/x and /app-x), and a slug
// appended to a service name can collide with another service's plain name.
// Either would silently reintroduce exactly the shared-name bug this naming
// scheme exists to prevent, so refuse to start instead.
func validateMountNames(mounts []MutagenSyncMount) error {
	type owner struct{ service, path string }
	sessions := make(map[string]owner, len(mounts))
	volumes := make(map[string]owner, len(mounts))

	for _, mount := range mounts {
		this := owner{mount.ServiceName, mount.ContainerPath}
		if prev, taken := sessions[mount.SessionName]; taken {
			return fmt.Errorf("Mutagen session name %s is claimed by both %s:%s and %s:%s - rename one of the services or mount paths",
				mount.SessionName, prev.service, prev.path, this.service, this.path)
		}
		if prev, taken := volumes[mount.VolumeName]; taken {
			return fmt.Errorf("sync volume name %s is claimed by both %s:%s and %s:%s - rename one of the services or mount paths",
				mount.VolumeName, prev.service, prev.path, this.service, this.path)
		}
		sessions[mount.SessionName] = this
		volumes[mount.VolumeName] = this
	}

	return nil
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

// SessionConfigFor builds the Mutagen session configuration for one mount.
// Single source of truth: `zdev start` and `zdev mutagen reset` both go
// through it, so a session created by either carries the same ignores and the
// same per-service ownership defaults.
func (p *Project) SessionConfigFor(mount MutagenSyncMount) mutagen.SessionConfig {
	containerName := p.ContainerName(mount.ServiceName)
	return mutagen.SessionConfig{
		Name:                     mount.SessionName,
		Alpha:                    mount.HostPath,
		Beta:                     fmt.Sprintf("docker://%s%s", containerName, mount.ContainerPath),
		Ignores:                  mutagen.MergeIgnores(p.Config.Mutagen.Ignore),
		DefaultOwnerBeta:         mount.Owner,
		DefaultGroupBeta:         mount.Group,
		DefaultFileModeBeta:      mount.FileMode,
		DefaultDirectoryModeBeta: mount.DirectoryMode,
	}
}

// startMutagenSessions creates or resumes Mutagen sync sessions. It returns
// the names of sessions that were freshly (re)created during this call, so
// the caller can perform post-creation work (e.g., chown pre-existing files
// in the container after the initial flush).
//
// Every mount is attempted even when an earlier one fails, and the failures
// come back joined. Returning on the first error let a single dead container
// strip the sessions off every service behind it in the list - and since the
// list is built from a map iteration, which services those were changed from
// run to run.
func (p *Project) startMutagenSessions(ctx context.Context, m *mutagen.Mutagen, mounts []MutagenSyncMount) (map[string]bool, error) {
	p.migrateLegacySessions(ctx, m, mounts)

	recreated := make(map[string]bool)
	var errs []error

	for _, mount := range mounts {
		created, err := p.ensureMutagenSession(ctx, m, mount)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if created {
			recreated[mount.SessionName] = true
		}
	}

	return recreated, errors.Join(errs...)
}

// ensureMutagenSession resumes the session for one mount when its config is
// unchanged, and creates (or recreates) it otherwise. Reports whether the
// session was freshly created.
func (p *Project) ensureMutagenSession(ctx context.Context, m *mutagen.Mutagen, mount MutagenSyncMount) (bool, error) {
	exists, err := m.SessionExists(ctx, mount.SessionName)
	if err != nil {
		return false, fmt.Errorf("failed to check session %s: %w", mount.SessionName, err)
	}

	desired := p.SessionConfigFor(mount)
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
			return false, nil
		}

		// Drift detected (or no stored hash from a pre-upgrade install) -
		// terminate so we can recreate with the new defaults.
		fmt.Printf("Mutagen config changed for %s, recreating sync session...\n", mount.SessionName)
		if err := m.TerminateSession(ctx, mount.SessionName); err != nil {
			return false, fmt.Errorf("failed to terminate stale session %s: %w", mount.SessionName, err)
		}
	} else {
		fmt.Printf("Creating sync session %s...\n", mount.SessionName)
	}

	if err := m.CreateSession(ctx, desired); err != nil {
		return false, fmt.Errorf("failed to create session %s: %w", mount.SessionName, err)
	}
	if err := writeSessionHash(mount.SessionName, desiredHash); err != nil {
		fmt.Printf("Warning: could not record sync session hash for %s: %v\n", mount.SessionName, err)
	}
	return true, nil
}

// RecreateSyncSession terminates a mount's session if present and creates it
// fresh, recording the config hash so a following `zdev start` resumes it
// instead of rebuilding it. `zdev mutagen reset` uses this rather than
// assembling its own SessionConfig, which is how reset came to create
// sessions without the per-service ownership defaults.
func (p *Project) RecreateSyncSession(ctx context.Context, m *mutagen.Mutagen, mount MutagenSyncMount) error {
	exists, err := m.SessionExists(ctx, mount.SessionName)
	if err != nil {
		return fmt.Errorf("failed to check session %s: %w", mount.SessionName, err)
	}
	if exists {
		if err := m.TerminateSession(ctx, mount.SessionName); err != nil {
			return fmt.Errorf("failed to terminate session %s: %w", mount.SessionName, err)
		}
		_ = removeSessionHash(mount.SessionName)
	}

	desired := p.SessionConfigFor(mount)
	if err := m.CreateSession(ctx, desired); err != nil {
		return fmt.Errorf("failed to create session %s: %w", mount.SessionName, err)
	}
	if err := writeSessionHash(mount.SessionName, desired.Hash()); err != nil {
		fmt.Printf("Warning: could not record sync session hash for %s: %v\n", mount.SessionName, err)
	}
	return nil
}

// migrateLegacySessions terminates the pre-per-mount session of any service
// that now names its sessions after the mount. Such a session was created
// under the bare zdev-<project>-<service> name, is no longer referenced by
// anything, and would otherwise keep syncing that service's first directory
// bind forever - invisible to every command that now looks up per-mount names.
//
// Safe to do unconditionally: the per-mount sessions are created immediately
// afterwards from the same host directories.
func (p *Project) migrateLegacySessions(ctx context.Context, m *mutagen.Mutagen, mounts []MutagenSyncMount) {
	perService := make(map[string]int)
	for _, mount := range mounts {
		perService[mount.ServiceName]++
	}

	for _, serviceName := range sortedServiceNames(perService) {
		if perService[serviceName] < 2 {
			// Single-mount services kept the legacy name - nothing to migrate.
			continue
		}
		legacyName := p.MutagenSessionName(serviceName)
		exists, err := m.SessionExists(ctx, legacyName)
		if err != nil || !exists {
			continue
		}
		fmt.Printf("Migrating %s to per-mount sync sessions (removing %s)...\n", serviceName, legacyName)
		if err := m.TerminateSession(ctx, legacyName); err != nil {
			fmt.Printf("Warning: could not terminate legacy session %s: %v\n", legacyName, err)
			continue
		}
		_ = removeSessionHash(legacyName)
	}
}

// sortedServiceNames keeps output and migration order deterministic - the
// mount list itself comes from a map iteration.
func sortedServiceNames(m map[string]int) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
// and reports, per session name, which ones actually got there.
//
// A flush that returns without error proves nothing on its own: the beta
// endpoint is the service container, so the session can be unable to reach it,
// or reach it and fail to write a single file, and still flush cleanly. Both
// are checked, and the returned map holds a reason for every mount that is NOT
// usable - an empty map means all of them are.
func (p *Project) waitForInitialSync(ctx context.Context, m *mutagen.Mutagen, mounts []MutagenSyncMount, timeout time.Duration) map[string]string {
	failed := make(map[string]string)
	if len(mounts) == 0 {
		return failed
	}

	fmt.Println("Waiting for initial file sync...")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, mount := range mounts {
		if err := m.FlushSession(ctx, mount.SessionName); err != nil {
			if ctx.Err() != nil {
				for _, remaining := range mounts {
					if _, done := failed[remaining.SessionName]; !done {
						failed[remaining.SessionName] = "timed out waiting for the initial sync"
					}
				}
				fmt.Printf("Warning: sync timeout - files may still be syncing in the background\n")
				return failed
			}
			failed[mount.SessionName] = err.Error()
			continue
		}

		healthy, detail, known := m.SessionHealthy(ctx, mount.SessionName)
		if known && !healthy {
			if detail == "" {
				detail = "session is not synchronizing"
			}
			failed[mount.SessionName] = detail
		}
	}

	for _, mount := range mounts {
		if reason, bad := failed[mount.SessionName]; bad {
			fmt.Printf("Warning: sync for %s at %s did not complete: %s\n",
				mount.ServiceName, mount.ContainerPath, reason)
		}
	}
	if len(failed) == 0 {
		fmt.Println("Initial sync complete")
	}

	return failed
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
func (p *Project) prepareMutagen(ctx context.Context) (*mutagen.Mutagen, []MutagenSyncMount, MutagenMounts, error) {
	if !p.IsMutagenEnabled() {
		return nil, nil, nil, nil
	}

	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize Mutagen: %w", err)
	}

	mounts := p.GetMutagenSyncMounts()
	if err := validateMountNames(mounts); err != nil {
		return nil, nil, nil, err
	}
	if err := p.createMutagenVolumes(ctx, mounts); err != nil {
		return nil, nil, nil, err
	}

	return m, mounts, NewMutagenMounts(mounts), nil
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
	// A session failure is reported, not fatal: the services whose sessions
	// did come up still deserve their gate opened.
	recreated, sessionErr := p.startMutagenSessions(ctx, m, mounts)
	failed := p.waitForInitialSync(ctx, m, mounts, 60*time.Second)
	signalErr := p.signalSyncReady(ctx, mounts, failed)
	p.applyPostSyncOwnership(ctx, mounts, recreated, failed)
	if err := errors.Join(sessionErr, signalErr); err != nil {
		return fmt.Errorf("Mutagen sync did not complete: %w", err)
	}
	return nil
}

// applyPostSyncOwnership runs `chown -R` inside each container whose Mutagen
// session was just (re)created and whose service config sets a Mutagen owner
// or group. Best-effort: failures are logged but don't block startup.
func (p *Project) applyPostSyncOwnership(ctx context.Context, mounts []MutagenSyncMount, recreated map[string]bool, failed map[string]string) {
	for _, mount := range mounts {
		if _, bad := failed[mount.SessionName]; bad || !recreated[mount.SessionName] {
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
		// As root: chown is a root operation, and the images that need this
		// setting at all are exactly the ones whose default user is not root.
		err := p.Runtime.Exec(ctx, containerName,
			[]string{"chown", "-R", spec, mount.ContainerPath}, false, runtime.ExecOptions{User: "root"})
		if err != nil {
			fmt.Printf("Warning: could not chown %s in %s: %v\n", mount.ContainerPath, containerName, err)
		}
	}
}

// transformVolumesForMutagen transforms bind mounts to Mutagen sync volumes
// Returns the modified volumes list for container creation
func (p *Project) transformVolumesForMutagen(serviceName string, volumes []string, mutagenMounts MutagenMounts) []runtime.VolumeMount {
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
			// Bind mount - use the sync volume declared for THIS mount. Every
			// directory bind of the service has its own; a lookup by service
			// alone would hand the same volume to all of them and leave the
			// rest as raw binds.
			mount, ok := mutagenMounts.Lookup(serviceName, target)
			if ok {
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

// signalSyncReady writes the marker file that unblocks the sync-ready gate in
// a container's command wrapper - but only for services whose sync mounts ALL
// reached a usable state.
//
// Opening the gate on a half-synced service is what turned a sync failure into
// a dead container: the command starts against an empty or stale directory,
// exits in milliseconds, and takes down the very beta endpoint the session
// needs in order to recover. Leaving the gate shut keeps the container alive
// and waiting, so a later `zdev start` can still fix it.
func (p *Project) signalSyncReady(ctx context.Context, mounts []MutagenSyncMount, failed map[string]string) error {
	byService := NewMutagenMounts(mounts)

	names := make([]string, 0, len(byService))
	for serviceName := range byService {
		names = append(names, serviceName)
	}
	sort.Strings(names)

	var errs []error
	for _, serviceName := range names {
		var blocked []string
		for _, mount := range byService[serviceName] {
			if reason, bad := failed[mount.SessionName]; bad {
				blocked = append(blocked, fmt.Sprintf("%s (%s)", mount.ContainerPath, reason))
			}
		}
		if len(blocked) > 0 {
			errs = append(errs, fmt.Errorf(
				"sync for %s never completed at %s - the container is holding at the sync-ready gate instead of starting against an empty directory; check 'zdev mutagen status'",
				serviceName, strings.Join(blocked, ", ")))
			continue
		}

		containerName := p.ContainerName(serviceName)
		p.awaitSyncGate(ctx, serviceName, containerName)
		if err := p.openSyncGate(ctx, containerName); err != nil {
			errs = append(errs, fmt.Errorf("could not signal sync-ready for %s: %w", serviceName, err))
		}
	}

	return errors.Join(errs...)
}

// openSyncGate raises the ready flag inside a container. The flag lives in
// /tmp so the image's own user can raise and clear it; the pre-/tmp path is
// touched too, as root, for entrypoints that were documented to wait on it
// themselves. Only the /tmp one is required to succeed.
func (p *Project) openSyncGate(ctx context.Context, containerName string) error {
	err := p.Runtime.Exec(ctx, containerName,
		[]string{"sh", "-c", "touch " + syncReadyFlag}, false, runtime.ExecOptions{})
	if err != nil {
		return err
	}

	_ = p.Runtime.Exec(ctx, containerName,
		[]string{"sh", "-c", "touch " + legacySyncReadyFlag}, false, runtime.ExecOptions{User: "root"})
	return nil
}

// awaitSyncGate waits for the container's wrapper to report that it is sitting
// at the gate. The wrapper clears the ready flag on every start (the flag
// lives in the writable layer and would otherwise survive), so signalling
// before the wrapper has run would set a flag it is about to delete - and the
// service would then wait forever. Best-effort: a service whose command isn't
// wrapped never raises the marker, and services that are slow to start are
// signalled anyway rather than held up.
func (p *Project) awaitSyncGate(ctx context.Context, serviceName, containerName string) {
	if p.Config.Services[serviceName].Command == "" {
		return // no wrapper, no gate
	}
	// Nothing to wait for if the container isn't up - don't spend the whole
	// timeout on a service that is already down.
	if running, err := p.Runtime.IsContainerRunning(ctx, containerName); err != nil || !running {
		return
	}

	deadline := time.Now().Add(syncGateWaitTimeout)
	for time.Now().Before(deadline) {
		err := p.Runtime.Exec(ctx, containerName,
			[]string{"sh", "-c", "test -f " + syncWaitingFlag}, false, runtime.ExecOptions{})
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		time.Sleep(syncGatePollInterval)
	}
}
