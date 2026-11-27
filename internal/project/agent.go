package project

// The project side of the agent sandbox (zdev claude, PLAN_2_AGENT_SANDBOX.md):
// the sandbox container is built from a service's config through
// buildContainerConfig, so it gets the same image, environment, resolved
// mounts (the Mutagen sync volume included), network and CA trust as the
// service, and never drifts from it. The caller adds the sandbox's own parts.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// AgentSource is where a service mounts the project directory.
type AgentSource struct {
	ContainerPath string // e.g. /app
	HostPath      string // the project directory (or the bind's source)
	SyncVolume    string // the Mutagen sync volume, "" for a plain bind
}

// AgentSource finds the service's bind mount of the project directory: the
// bind whose source is the project root, else one that contains it.
func (p *Project) AgentSource(service string) (AgentSource, error) {
	svc, ok := p.Config.Services[service]
	if !ok {
		return AgentSource{}, fmt.Errorf("unknown service: %s", service)
	}
	var found *AgentSource
	for _, vol := range svc.Volumes {
		if !isBindMount(vol) {
			continue
		}
		source, target, _ := parseVolumeMount(vol)
		abs := source
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Dir, abs)
		}
		abs = filepath.Clean(abs)
		if abs == filepath.Clean(p.Dir) {
			found = &AgentSource{ContainerPath: target, HostPath: abs}
			break
		}
		if found == nil && strings.HasPrefix(filepath.Clean(p.Dir)+string(os.PathSeparator), abs+string(os.PathSeparator)) {
			found = &AgentSource{ContainerPath: target, HostPath: abs}
		}
	}
	if found == nil {
		return AgentSource{}, fmt.Errorf("service %s doesn't mount the project directory, so an agent in its image couldn't see the source; choose another service (zdev claude <service>)", service)
	}
	if p.IsMutagenEnabled() {
		for _, m := range p.serviceSyncMounts(service, svc) {
			if m.ContainerPath == found.ContainerPath {
				found.SyncVolume = m.VolumeName
			}
		}
	}
	return *found, nil
}

// AgentDefaultService picks the service an agent runs in when none is named:
// the configured one, else "app", else the only service mounting the project.
func (p *Project) AgentDefaultService(configured string) (string, error) {
	if configured != "" {
		if _, ok := p.Config.Services[configured]; !ok {
			return "", fmt.Errorf("agent.projects.%s.service names %q, which is not a service of the project", p.Config.Name, configured)
		}
		return configured, nil
	}
	if _, ok := p.Config.Services["app"]; ok {
		return "app", nil
	}
	var candidates []string
	for name := range p.Config.Services {
		if _, err := p.AgentSource(name); err == nil {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", fmt.Errorf("can't tell which service's image the agent should run in (no service named app, services mounting the project: %s); name one: zdev claude <service>", strings.Join(candidates, ", "))
}

// AgentBaseConfig is the sandbox container's starting point: the service's
// container config with what makes it that service removed (name, network
// alias, published ports, labels, command), so the sandbox doesn't receive the
// service's traffic, routes or identity. Mutagen volumes are prepared first,
// like on every container-creation path.
func (p *Project) AgentBaseConfig(ctx context.Context, service string) (runtime.ContainerConfig, error) {
	svc, ok := p.Config.Services[service]
	if !ok {
		return runtime.ContainerConfig{}, fmt.Errorf("unknown service: %s", service)
	}
	_, _, mounts, err := p.prepareMutagen(ctx)
	if err != nil {
		return runtime.ContainerConfig{}, err
	}
	p.resolveImageGate(ctx, service, svc, p.IsMutagenEnabled(), mounts)
	cfg := p.buildContainerConfig(service, svc, p.IsMutagenEnabled(), mounts)
	// The same 1Password values the service gets (the agent can read the
	// app's environment anyway); resolved here, never on a compare path.
	if err := p.resolveSecretEnv(ctx, service, svc, &cfg); err != nil {
		return runtime.ContainerConfig{}, err
	}
	cfg.Name = ""
	cfg.Aliases = nil
	cfg.Ports = nil
	cfg.Labels = map[string]string{}
	cfg.Command = nil
	cfg.Entrypoint = "" // the sync gate's wrapper; the sandbox sets its own
	cfg.RestartPolicy = ""
	return cfg, nil
}

// ServiceImage is the image a service runs (its build tag for dockerfile:).
func (p *Project) ServiceImage(service string) string {
	return p.serviceImage(service, p.Config.Services[service])
}

// URL is the project's main routed URL, "" when nothing is routed.
func (p *Project) URL(scheme string) string {
	if p.Config.Domain == "" {
		return ""
	}
	for _, svc := range p.Config.Services {
		if p.routesService(svc) && (svc.Routing.Protocol == "" || svc.Routing.Protocol == "http" || svc.Routing.Protocol == "https") {
			return scheme + "://" + p.routingHost(svc.Routing)
		}
	}
	return ""
}

// dockerSocketPath matches bind sources that are (or point at) a Docker
// socket: /var/run/docker.sock, ~/.docker/run/docker.sock, OrbStack's and
// Colima's sockets all end in docker.sock.
var dockerSocketPath = regexp.MustCompile(`(^|/)docker\.sock$`)

// DockerSocketServices lists services that mount the Docker socket. Whoever
// reaches such a service is root on the host.
func (p *Project) DockerSocketServices() []string {
	var out []string
	for name, svc := range p.Config.Services {
		for _, vol := range svc.Volumes {
			source, _, isNamed := parseVolumeMount(vol)
			if isNamed {
				continue
			}
			if dockerSocketPath.MatchString(source) {
				out = append(out, name)
				break
			}
			if resolved, err := filepath.EvalSymlinks(source); err == nil && dockerSocketPath.MatchString(resolved) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

var numericID = regexp.MustCompile(`^[0-9]+$`)

// AgentUser resolves the uid:gid the agent runs as: the owner of the files in
// the source the service sees. With Mutagen that is the session's default
// owner (services.<name>.mutagen.user/group, else the image's user), resolved
// to numbers inside the running service container, since names don't exist
// in every image. Without Mutagen (a host bind) it is the host user.
func (p *Project) AgentUser(ctx context.Context, service, imageUser string) (string, error) {
	if !p.IsMutagenEnabled() {
		return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), nil
	}
	svc := p.Config.Services[service]
	user, group := svc.Mutagen.User, svc.Mutagen.Group
	if user == "" {
		user, group, _ = strings.Cut(imageUser, ":")
	}
	if user == "" {
		user = "0"
	}
	container := p.ContainerName(service)
	resolve := func(cmd string, name string) (string, error) {
		if numericID.MatchString(name) {
			return name, nil
		}
		out, err := p.Runtime.ExecOutput(ctx, container, []string{"sh", "-c", cmd, "zdev", name})
		out = strings.TrimSpace(out)
		if err != nil || !numericID.MatchString(out) {
			return "", fmt.Errorf("resolving %q in %s: %v", name, container, err)
		}
		return out, nil
	}
	uid, err := resolve(`id -u "$1"`, user)
	if err != nil {
		return "", err
	}
	if group == "" {
		gid, err := resolve(`id -g "$1"`, user)
		if err != nil {
			return "", err
		}
		return uid + ":" + gid, nil
	}
	gid, err := resolve(`awk -F: -v g="$1" '$1==g{print $3}' /etc/group`, group)
	if err != nil {
		return "", err
	}
	return uid + ":" + gid, nil
}

// agentSyncMarker records which service's project mount syncs .zdev and .git
// for the agent (PLAN_2 section 6). Host-side state, never project config.
func agentSyncMarker(projectName string) string {
	return filepath.Join(config.GetZdevHome(), "agent", "sync", projectName)
}

// AgentSyncService returns the service recorded by SetAgentSyncService.
func AgentSyncService(projectName string) string {
	b, err := os.ReadFile(agentSyncMarker(projectName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetAgentSyncService records the agent's service; the next start of the
// project's sync sessions then un-ignores .zdev and .git for its mount of the
// project directory (the session is recreated once). Reports whether it changed.
func SetAgentSyncService(projectName, service string) (bool, error) {
	if AgentSyncService(projectName) == service {
		return false, nil
	}
	path := agentSyncMarker(projectName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(service+"\n"), 0o600)
}

// agentSyncNegations are appended at the END of the agent mount's ignore
// list: a later negation overrides an earlier ignore, including the built-in
// .git and a project's own .zdev entry (spikes 7 and 8).
var agentSyncNegations = []string{"!.zdev", "!.git"}

// agentGitGuards come last in every sync session of a project in agent mode:
// the Mac runs hooks and what .git/config names (core.fsmonitor, hooksPath)
// without anyone deciding to, and every container that mounts the sync
// volume can write them (the app container through `zdev exec`). Ignored,
// nothing written there in a container reaches the Mac; the agent sees the
// Mac's own copies through read-only binds. Last, so no negation in the
// project's mutagen.ignore can lift them.
var agentGitGuards = []string{".git/hooks", ".git/config"}

// AgentGitGuards are the .git paths the sync never carries in agent mode.
func AgentGitGuards() []string { return append([]string(nil), agentGitGuards...) }

// gitGuardMounts are read-only binds of the host's .git/hooks and .git/config
// over every writable host bind of a service that contains them, in a project
// in agent mode. Without Mutagen (Linux, or mutagen.no_sync) such a bind is
// the host directory itself, so any container could write a hook the host
// runs; the sync guards (agentGitGuards) only cover synced mounts. Part of
// buildContainerConfig, so the config hash sees them.
func (p *Project) gitGuardMounts(vols []runtime.VolumeMount) []runtime.VolumeMount {
	if p.skipGitGuards || AgentSyncService(p.Config.Name) == "" {
		return nil
	}
	gitDir := filepath.Join(p.Dir, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil
	}
	_ = os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755)
	root := resolveHostPath(p.Dir, ".")
	have := map[string]bool{}
	for _, v := range vols {
		have[v.Target] = true
	}
	var out []runtime.VolumeMount
	for _, v := range vols {
		if v.ReadOnly || !(filepath.IsAbs(v.Source) || strings.HasPrefix(v.Source, ".")) {
			continue // read-only, or a named (sync) volume
		}
		src := resolveHostPath(p.Dir, v.Source)
		for _, g := range agentGitGuards {
			guard := filepath.Join(root, g)
			if _, err := os.Stat(guard); err != nil || !within(guard, src) || guard == src {
				continue
			}
			rel, err := filepath.Rel(src, guard)
			if err != nil {
				continue
			}
			dst := path.Join(v.Target, filepath.ToSlash(rel))
			if have[dst] {
				continue
			}
			have[dst] = true
			out = append(out, runtime.VolumeMount{Source: guard, Target: dst, ReadOnly: true})
		}
	}
	return out
}

// ApplyAgentGitGuards recreates the running services whose container lacks
// only the git guard mounts (a project entering agent mode), and nothing
// else: other drift stays for the developer's `zdev update`.
func (p *Project) ApplyAgentGitGuards(ctx context.Context) error {
	var names []string
	for name, svc := range p.Config.Services {
		labels, err := p.Runtime.GetContainerLabels(ctx, p.ContainerName(name))
		if err != nil {
			continue
		}
		enabled := p.IsMutagenEnabled()
		var mm MutagenMounts
		if enabled {
			mm = NewMutagenMounts(p.GetMutagenSyncMounts())
		}
		p.resolveImageGate(ctx, name, svc, enabled, mm)
		with := p.buildContainerConfig(name, svc, enabled, mm).Labels[runtime.ConfigHashLabel]
		p.skipGitGuards = true
		without := p.buildContainerConfig(name, svc, enabled, mm).Labels[runtime.ConfigHashLabel]
		p.skipGitGuards = false
		if with != without && labels[runtime.ConfigHashLabel] == without {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	daemon, mounts, mountMap, err := p.prepareMutagen(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		fmt.Printf("Recreating service %s (read-only .git/hooks and .git/config for agent mode)...\n", name)
		svc := p.Config.Services[name]
		if err := p.prefetchSecrets(ctx, name, svc); err != nil {
			return err
		}
		if err := p.removeServiceContainer(ctx, name); err != nil {
			return err
		}
		if err := p.startServiceWithMutagen(ctx, name, svc, daemon != nil, mountMap); err != nil {
			return fmt.Errorf("failed to start service %s: %w", name, err)
		}
	}
	if daemon != nil {
		return p.finalizeMutagen(ctx, daemon, mounts)
	}
	return nil
}

// sessionIgnores is the ignore list of one sync session.
func (p *Project) sessionIgnores(mount MutagenSyncMount, base []string) []string {
	agentService := AgentSyncService(p.Config.Name)
	if agentService == "" {
		return base
	}
	out := append([]string(nil), base...)
	if mount.ServiceName == agentService {
		if src, err := p.AgentSource(mount.ServiceName); err == nil && src.ContainerPath == mount.ContainerPath {
			out = append(out, agentSyncNegations...)
		}
	}
	return append(out, agentGitGuards...)
}

// agentSyncMount is the sync session of the agent's mount of the project
// directory, nil without Mutagen or agent mode.
func (p *Project) agentSyncMount() *MutagenSyncMount {
	service := AgentSyncService(p.Config.Name)
	if service == "" || !p.IsMutagenEnabled() {
		return nil
	}
	src, err := p.AgentSource(service)
	if err != nil {
		return nil
	}
	for _, m := range p.serviceSyncMounts(service, p.Config.Services[service]) {
		if m.ContainerPath == src.ContainerPath {
			m := m
			return &m
		}
	}
	return nil
}

// FlushAgentSync waits until what the agent wrote reached this machine: a
// command the relay runs on the host reads .zdev/ here, right after the agent
// edited it in its container. A no-op without Mutagen.
func (p *Project) FlushAgentSync(ctx context.Context) error {
	mount := p.agentSyncMount()
	if mount == nil {
		return nil
	}
	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return err
	}
	return m.FlushSession(ctx, mount.SessionName)
}

// AgentGitConflicts lists the agent mount's sync conflicts below .git: both
// sides changed the same file (typically .git/index when git on the
// developer's machine writes it while the agent commits), and Mutagen's
// two-way-safe mode stops syncing it without an error (spike 7).
func (p *Project) AgentGitConflicts(ctx context.Context) (session string, paths []string, err error) {
	mount := p.agentSyncMount()
	if mount == nil {
		return "", nil, nil
	}
	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return "", nil, err
	}
	all, err := m.Conflicts(ctx, mount.SessionName)
	if err != nil {
		return "", nil, err
	}
	for _, path := range all {
		if path == ".git" || strings.HasPrefix(path, ".git/") {
			paths = append(paths, path)
		}
	}
	return mount.SessionName, paths, nil
}

// RepairAgentGit resolves the agent mount's .git conflicts in favor of this
// machine: each conflicting file is copied from here into the service
// container, so both sides are equal again and the session syncs. When the
// index was among them, `git reset -q` then brings this machine's index to
// HEAD (which may include the agent's commits); staged changes become
// unstaged, nothing in the working tree changes.
func (p *Project) RepairAgentGit(ctx context.Context) ([]string, bool, error) {
	session, paths, err := p.AgentGitConflicts(ctx)
	if err != nil || len(paths) == 0 {
		return nil, false, err
	}
	service := AgentSyncService(p.Config.Name)
	src, err := p.AgentSource(service)
	if err != nil {
		return nil, false, err
	}
	container := p.ContainerName(service)
	resetIndex := false
	for _, rel := range paths {
		host := filepath.Join(src.HostPath, rel)
		if _, err := os.Stat(host); err != nil {
			continue
		}
		if out, err := exec.CommandContext(ctx, "docker", "cp", host, container+":"+src.ContainerPath+"/"+rel).CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("copying %s into %s: %v: %s", rel, container, err, strings.TrimSpace(string(out)))
		}
		if rel == ".git/index" {
			resetIndex = true
		}
	}
	m, err := p.EnsureMutagen(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := m.FlushSession(ctx, session); err != nil {
		return nil, false, err
	}
	if resetIndex {
		c := exec.CommandContext(ctx, "git", "reset", "-q")
		c.Dir = src.HostPath
		if out, err := c.CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("git reset: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if err := m.FlushSession(ctx, session); err != nil {
			return nil, false, err
		}
	}
	return paths, resetIndex, nil
}
