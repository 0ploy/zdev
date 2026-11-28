package project

// The policy on lifecycle commands an agent sandbox sends (PLAN_2 section
// 10). The agent may edit .zdev/ freely; what is checked is APPLYING the
// config from the sandbox. Everything that stays inside containers is
// allowed (images, env, command, Dockerfile builds). What reaches outside a
// container must already be in effect: the baseline records it from the
// containers that run the current config whenever the developer starts or
// updates the project, so the developer's own `zdev update` is the approval.
//
// The check runs in the same process that applies the config (the host's zdev
// child the relay starts, marked by AgentRequestEnv), on the config it loaded,
// so nothing can change between check and apply.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/state"
)

// AgentRequestEnv marks a zdev process the relay runs for an agent sandbox;
// its value is the session's project. Set by the host only: a request's
// environment never reaches the host process.
const AgentRequestEnv = config.AgentRequestEnv

// An agent request substitutes only the host environment variables the
// developer's own runs read (config.SetAgentHostEnv).
func init() {
	config.SetAgentHostEnv(func(project string) map[string]bool {
		allowed := map[string]bool{}
		for _, name := range loadAgentBaseline(project).EnvNames {
			allowed[name] = true
		}
		return allowed
	})
}

// AgentRequest returns the session's project when this process runs a
// request from an agent sandbox.
func AgentRequest() (string, bool) {
	v := os.Getenv(AgentRequestEnv)
	return v, v != ""
}

// checkAgentProject refuses a config whose name isn't the session's project:
// the agent can edit `name:`, and zdev would then act on another project's
// containers.
func checkAgentProject(cfg *config.ProjectConfig) error {
	if want, ok := AgentRequest(); ok && cfg.Name != want {
		return &agent.Refusal{Code: "project-changed",
			Message: fmt.Sprintf(".zdev/config.yaml now names project %q, the sandbox belongs to %q", cfg.Name, want),
			Next:    "ask the developer: a project's name can't change from its sandbox"}
	}
	return nil
}

// BindGrant is a bind mount source outside the project (or one that exposes
// .git), resolved through symlinks the way Docker resolves it on the host.
type BindGrant struct {
	Source   string `json:"source"`
	ReadOnly bool   `json:"read_only,omitempty"`
	// Raw: a native bind, not a Mutagen sync (whose .git guards apply).
	Raw bool `json:"raw,omitempty"`
}

// covers reports whether an approved grant allows g: same source, and no
// more access (writable or raw where the approval wasn't).
func (a BindGrant) covers(g BindGrant) bool {
	return a.Source == g.Source && (!a.ReadOnly || g.ReadOnly) && (a.Raw || !g.Raw)
}

// serviceFootprint is what one service's config reaches outside containers.
type serviceFootprint struct {
	Binds   []BindGrant `json:"binds,omitempty"`
	Traefik []string    `json:"traefik_labels,omitempty"` // key=value of user labels under traefik.
	OpEnvs  []string    `json:"op_envs,omitempty"`        // 1Password Environment IDs
}

// AgentBaseline is what the developer applied, per service plus the
// project-wide routing. Host-side state in ~/.zdev/agent/approved.
type AgentBaseline struct {
	Services  map[string]serviceFootprint `json:"services"`
	Hosts     []string                    `json:"hosts,omitempty"`
	HostPorts []string                    `json:"host_ports,omitempty"` // "tcp/5432"
	// EnvNames are the host environment variables the config reads through
	// ${VAR}; an agent request substitutes only these.
	EnvNames []string `json:"env_names,omitempty"`
}

// PolicyViolation is one thing an agent-initiated apply would add.
type PolicyViolation struct {
	Service string // empty for project-wide items
	What    string
}

func (v PolicyViolation) String() string {
	if v.Service == "" {
		return v.What
	}
	return "service " + v.Service + ": " + v.What
}

func agentBaselinePath(projectName string) string {
	return filepath.Join(config.GetZdevHome(), "agent", "approved", projectName+".json")
}

func loadAgentBaseline(projectName string) AgentBaseline {
	b := AgentBaseline{Services: map[string]serviceFootprint{}}
	if data, err := os.ReadFile(agentBaselinePath(projectName)); err == nil {
		_ = json.Unmarshal(data, &b)
	}
	if b.Services == nil {
		b.Services = map[string]serviceFootprint{}
	}
	return b
}

// resolveHostPath resolves a host path like Docker does: relative to the
// project, through symlinks; a missing tail is kept as written below its
// deepest existing parent (Docker creates it).
func resolveHostPath(dir, src string) string {
	p := src
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func within(path, root string) bool {
	if root == string(os.PathSeparator) {
		return strings.HasPrefix(path, root)
	}
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// writesGitGuard reports whether a bind makes .git/hooks or .git/config
// writable from inside: a writable bind at or below .git/hooks, or of
// .git/config itself. A bind that merely contains them (the project root,
// .git) gets read-only guard mounts over them (gitGuardMounts), and a synced
// one has them ignored; neither helps a bind rooted at those paths.
func writesGitGuard(g BindGrant, root string) bool {
	if g.ReadOnly {
		return false
	}
	for _, guard := range agentGitGuards {
		if within(g.Source, filepath.Join(root, guard)) {
			return true
		}
	}
	return false
}

// footprint computes what a service's config reaches outside containers.
// Binds inside the project are left out (always allowed) unless they make
// the host's git hooks or config writable (writesGitGuard).
func (p *Project) footprint(name string, svc config.ServiceConfig) serviceFootprint {
	var fp serviceFootprint
	root := resolveHostPath(p.Dir, ".")
	synced := map[string]bool{}
	if p.IsMutagenEnabled() {
		for _, m := range p.serviceSyncMounts(name, svc) {
			synced[m.HostPath+"\x00"+m.ContainerPath] = true
		}
	}
	for _, vol := range svc.Volumes {
		if !isBindMount(vol) {
			continue
		}
		source, target, _ := parseVolumeMount(vol)
		abs := source
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.Dir, abs)
		}
		readOnly, _ := volumeMountOptions(vol)
		g := BindGrant{Source: resolveHostPath(p.Dir, source), ReadOnly: readOnly, Raw: !synced[abs+"\x00"+target]}
		if within(g.Source, root) && !(writesGitGuard(g, root)) {
			continue
		}
		fp.Binds = append(fp.Binds, g)
	}
	for k, v := range svc.Labels {
		if strings.HasPrefix(k, "traefik.") {
			fp.Traefik = append(fp.Traefik, k+"="+v)
		}
	}
	ids := map[string]bool{}
	if svc.OpEnv != "" {
		ids[svc.OpEnv] = true
	}
	for _, v := range p.containerEnv(svc) {
		if secrets.IsRef(v) {
			if id, _, err := secrets.ParseRef(v); err == nil {
				ids[id] = true
			}
		}
	}
	for id := range ids {
		fp.OpEnvs = append(fp.OpEnvs, id)
	}
	sort.Slice(fp.Binds, func(i, j int) bool { return fp.Binds[i].Source < fp.Binds[j].Source })
	sort.Strings(fp.Traefik)
	sort.Strings(fp.OpEnvs)
	return fp
}

// runningFootprint reads what an existing container applies: its binds
// (all of them: inside the project or not, they were applied) and router
// labels, and its attached 1Password Environment.
func (p *Project) runningFootprint(ctx context.Context, name string) (serviceFootprint, error) {
	var fp serviceFootprint
	container := p.ContainerName(name)
	binds, err := p.Runtime.ContainerBinds(ctx, container)
	if err != nil {
		return fp, err
	}
	for _, b := range binds {
		fp.Binds = append(fp.Binds, BindGrant{Source: resolveHostPath(p.Dir, b.Source), ReadOnly: b.ReadOnly, Raw: true})
	}
	labels, err := p.Runtime.GetContainerLabels(ctx, container)
	if err != nil {
		return fp, err
	}
	for k, v := range labels {
		if strings.HasPrefix(k, "traefik.") {
			fp.Traefik = append(fp.Traefik, k+"="+v)
		}
	}
	if id := labels[runtime.OpEnvLabel]; id != "" {
		fp.OpEnvs = append(fp.OpEnvs, id)
	}
	sort.Slice(fp.Binds, func(i, j int) bool { return fp.Binds[i].Source < fp.Binds[j].Source })
	sort.Strings(fp.Traefik)
	return fp, nil
}

// hostPorts lists the project's router-published ports as "proto/port".
func (p *Project) hostPorts() []string {
	tcp, udp := p.GetRequiredPorts()
	var out []string
	for _, port := range tcp {
		out = append(out, fmt.Sprintf("tcp/%d", port))
	}
	for _, port := range udp {
		out = append(out, fmt.Sprintf("udp/%d", port))
	}
	sort.Strings(out)
	return out
}

// RecordAgentBaseline records what the running project applies, for later
// agent-initiated applies to be checked against: the config's footprint for
// containers that run the current config, the container's own binds and
// labels for an outdated one (its config isn't known). Called after the developer's own start/update/restart and
// when `zdev claude` starts; never for a request from a sandbox.
func (p *Project) RecordAgentBaseline(ctx context.Context) error {
	if _, ok := AgentRequest(); ok {
		return nil
	}
	b := loadAgentBaseline(p.Config.Name)
	for name := range b.Services {
		if _, ok := p.Config.Services[name]; !ok {
			delete(b.Services, name)
		}
	}
	for name, svc := range p.Config.Services {
		exists, err := p.Runtime.ContainerExists(ctx, p.ContainerName(name))
		if err != nil || !exists {
			continue
		}
		stale, err := p.serviceNeedsRecreate(ctx, name, svc)
		if err != nil {
			continue
		}
		if stale {
			// Runs an older config: record what the container itself applies,
			// plus the config's synced directories, which the start just
			// applied (sync sessions follow the config without a recreate).
			if fp, err := p.runningFootprint(ctx, name); err == nil {
				for _, g := range p.footprint(name, svc).Binds {
					if !g.Raw {
						fp.Binds = append(fp.Binds, g)
					}
				}
				b.Services[name] = fp
			}
			continue
		}
		b.Services[name] = p.footprint(name, svc)
	}
	b.Hosts = p.resolvableHosts()
	b.HostPorts = p.hostPorts()
	b.EnvNames = p.Config.HostEnv
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	path := agentBaselinePath(p.Config.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// CheckAgentPolicy lists what applying the loaded config would add beyond
// the baseline, plus the static rules (Dockerfile inside the project).
func (p *Project) CheckAgentPolicy(domain string) []PolicyViolation {
	b := loadAgentBaseline(p.Config.Name)
	var out []PolicyViolation
	root := resolveHostPath(p.Dir, ".")

	names := make([]string, 0, len(p.Config.Services))
	for name := range p.Config.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := p.Config.Services[name]
		if svc.Dockerfile != "" && !within(resolveHostPath(p.Dir, svc.Dockerfile), root) {
			out = append(out, PolicyViolation{name, "dockerfile " + svc.Dockerfile + " is outside the project"})
		}
		have := b.Services[name]
		fp := p.footprint(name, svc)
		for _, g := range fp.Binds {
			ok := false
			for _, a := range have.Binds {
				if a.covers(g) {
					ok = true
					break
				}
			}
			if !ok {
				what := "bind mount of " + g.Source
				switch {
				case within(g.Source, root):
					what += " (the repository's git hooks or config)"
				default:
					what += " (outside the project)"
				}
				if !g.ReadOnly {
					what += ", writable"
				}
				out = append(out, PolicyViolation{name, what})
			}
		}
		for _, l := range fp.Traefik {
			if !contains(have.Traefik, l) {
				out = append(out, PolicyViolation{name, "router label " + l})
			}
		}
		for _, id := range fp.OpEnvs {
			if !contains(have.OpEnvs, id) {
				out = append(out, PolicyViolation{name, "1Password Environment " + id})
			}
		}
	}

	for _, name := range p.Config.HostEnv {
		if !contains(b.EnvNames, name) {
			out = append(out, PolicyViolation{"", "${" + name + "} reads this machine's environment"})
		}
	}
	for _, port := range p.hostPorts() {
		if !contains(b.HostPorts, port) {
			out = append(out, PolicyViolation{"", "host port " + port + " (published on every interface of this machine)"})
		}
	}
	others := map[string]string{}
	if mgr, err := state.DefaultManager(); err == nil {
		if projects, err := mgr.ListProjects(); err == nil {
			for name, entry := range projects {
				if name == p.Config.Name {
					continue
				}
				for _, h := range entry.Hosts {
					others[h] = name
				}
			}
		}
	}
	for _, h := range p.resolvableHosts() {
		if owner, ok := others[h]; ok {
			out = append(out, PolicyViolation{"", "hostname " + h + " belongs to project " + owner})
			continue
		}
		if contains(b.Hosts, h) {
			continue
		}
		if domain == "" || !(h == domain || strings.HasSuffix(h, "."+domain)) {
			out = append(out, PolicyViolation{"", "hostname " + h + " (outside " + domain + ")"})
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// EscapingBinds lists, in a project in agent mode, bind mounts written inside
// the project whose source leads outside it through a symlink. Containers
// write the project directory (directly on Linux and with mutagen.no_sync,
// through the sync otherwise), so such a link may have been placed by one:
// replacing ./storage with a link to the home directory would mount the home
// directory at the next start, by whoever runs it, and that start would
// record it as approved (review 2026-11-28, H1). Synced binds count too:
// the sync would then copy the link's target both ways.
func (p *Project) EscapingBinds() []PolicyViolation {
	if AgentSyncService(p.Config.Name) == "" {
		return nil
	}
	dir := filepath.Clean(p.Dir)
	root := resolveHostPath(p.Dir, ".")
	names := make([]string, 0, len(p.Config.Services))
	for name := range p.Config.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []PolicyViolation
	for _, name := range names {
		for _, vol := range p.Config.Services[name].Volumes {
			source, _, isNamed := parseVolumeMount(vol)
			if isNamed {
				continue
			}
			written := source
			if !filepath.IsAbs(written) {
				written = filepath.Join(dir, written)
			}
			written = filepath.Clean(written)
			if !within(written, dir) && !within(written, root) {
				continue // written outside the project: the policy's grants cover it
			}
			if resolved := resolveHostPath(p.Dir, source); !within(resolved, root) {
				out = append(out, PolicyViolation{name, source + " leads to " + resolved + " through a symlink"})
			}
		}
	}
	return out
}

// HasAgentBaseline reports whether a baseline was recorded for the project.
func (p *Project) HasAgentBaseline() bool {
	_, err := os.Stat(agentBaselinePath(p.Config.Name))
	return err == nil
}

// ForgetAgentState removes the project's host-side agent state: the
// baseline and the agent sync marker (the next `zdev claude` sets it again).
func ForgetAgentState(projectName string) error {
	for _, p := range []string{agentBaselinePath(projectName), agentSyncMarker(projectName)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
