package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ssl"
	"github.com/0ploy/zdev/internal/state"
	"github.com/spf13/cobra"
)

var (
	cleanupForce        bool
	cleanupPurgeMissing bool
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove unused containers, volumes and stale project registrations",
	Long: `Prune resources no longer associated with any live project:

  - Orphaned Docker containers (not present in a live project's current config)
  - Orphaned Docker volumes (not referenced by a live project's current config)
  - Orphaned Docker networks (no live project or link owns them)
  - Stale state entries whose project directory no longer exists on disk

A project whose directory is missing keeps its volumes (its databases) and its
registration: the directory may only be absent for now (an unmounted drive, a
moved checkout). Use --purge-missing to remove those as well.

Resources used by the current configuration of registered projects are retained.
Stale resources left behind by removed services are included in the confirmation
list before deletion.

Use --force to skip the confirmation prompt.`,
	RunE: runCleanup,
}

func init() {
	cleanupCmd.Flags().BoolVarP(&cleanupForce, "force", "f", false, "skip confirmation prompt")
	cleanupCmd.Flags().BoolVar(&cleanupPurgeMissing, "purge-missing", false, "also remove the volumes and registrations of projects whose directory is missing")
	rootCmd.AddCommand(cleanupCmd)
}

func runCleanup(cmd *cobra.Command, args []string) error {
	return withDocker(2*time.Minute, func(ctx context.Context) error {
		stateMgr, err := state.DefaultManager()
		if err != nil {
			return fmt.Errorf("failed to load state: %w", err)
		}

		projects, err := stateMgr.ListProjects()
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}
		links, err := stateMgr.ListLinks()
		if err != nil {
			links = nil // without them link networks look orphaned only if nothing uses them
		}

		docker := runtime.NewDockerCLI()
		plan, err := planCleanup(ctx, projects, links, docker, cleanupPurgeMissing)
		if err != nil {
			return err
		}

		if !plan.print(os.Stdout) {
			return nil
		}
		if !cleanupForce {
			if !confirm("Delete the items above? [y/N]: ") {
				fmt.Println("Aborted.")
				return nil
			}
		}
		plan.apply(ctx, os.Stdout, stateMgr, docker)
		return nil
	})
}

// cleanupProject is a registered project cleanup reports on.
type cleanupProject struct {
	name, path string
	err        error // why its config couldn't be read (unreadable only)
}

// cleanupPlan is what a cleanup removes, and what it keeps on purpose. Built
// by planCleanup without touching anything, shown by print, carried out by
// apply.
type cleanupPlan struct {
	// unreadable projects are skipped with everything carrying their name.
	unreadable []cleanupProject
	// keptMissing projects have a missing directory but volumes: both their
	// registration and the volumes (keptVolumes) stay.
	keptMissing []cleanupProject
	keptVolumes map[string][]string
	// unregister: missing directory and nothing worth keeping.
	unregister []cleanupProject

	containers, volumes, networks []string
}

// cleanupLister is the slice of the runtime planCleanup reads.
type cleanupLister interface {
	containerLister
	ListVolumes(ctx context.Context, filter string) ([]runtime.Volume, error)
	ListNetworks(ctx context.Context, filter string) ([]string, error)
}

// planCleanup collects what a cleanup would remove: containers and volumes
// no registered project's current config references, networks no project or
// link owns, and registrations whose directory is missing. It reads state,
// configs and Docker, and changes nothing.
func planCleanup(ctx context.Context, projects map[string]state.ProjectEntry, links map[string]state.LinkEntry, docker cleanupLister, purgeMissing bool) (cleanupPlan, error) {
	var plan cleanupPlan
	var stale []cleanupProject
	knownContainers := make(map[string]bool)
	knownVolumes := make(map[string]bool)
	knownNetworks := make(map[string]bool)
	unreadable := make(map[string]bool)

	for name, entry := range projects {
		if _, err := os.Stat(entry.Path); err != nil {
			if os.IsNotExist(err) {
				stale = append(stale, cleanupProject{name: name, path: entry.Path})
				continue
			}
			return plan, fmt.Errorf("failed to inspect project %s at %s: %w", name, entry.Path, err)
		}

		cfg, err := config.LoadProject(entry.Path)
		if err != nil {
			// One unreadable config - an unreleased field, a syntax error,
			// a half-finished edit - must not take the whole cleanup down
			// with it. But its resources cannot be judged either, so every
			// resource carrying its name is preserved.
			plan.unreadable = append(plan.unreadable, cleanupProject{name: name, path: entry.Path, err: err})
			unreadable[name] = true
			continue
		}
		loaded := &project.Project{Config: cfg, Dir: entry.Path}
		for serviceName := range cfg.Services {
			knownContainers[project.ContainerNameFor(serviceName, cfg.Name)] = true
			// Kept for services whose sync volume predates per-mount
			// naming, or whose bind source is temporarily absent: an
			// in-use sync volume must never be offered for pruning.
			knownVolumes[project.MutagenVolumeNameFor(serviceName, cfg.Name)] = true
		}
		// Services with several directory binds have one sync volume per
		// mount, none of which carries the bare per-service name.
		for _, mount := range loaded.GetMutagenSyncMounts() {
			knownVolumes[mount.VolumeName] = true
		}
		for _, volumeName := range loaded.NamedVolumes() {
			knownVolumes[project.VolumeNameFor(volumeName, cfg.Name)] = true
		}
		knownNetworks[loaded.NetworkName()] = true
	}

	// Link networks belong to the links in state, not to any one project.
	for _, link := range links {
		knownNetworks[link.Network] = true
	}

	containers, err := docker.ListContainers(ctx, "label=zdev.project")
	if err != nil {
		return plan, fmt.Errorf("failed to list Docker containers: %w", err)
	}
	for _, name := range containers {
		if !knownContainers[name] && !belongsToAnyProject(name, unreadable) {
			plan.containers = append(plan.containers, name)
		}
	}

	dockerVolumes, err := docker.ListVolumes(ctx, "name=.zdev")
	if err != nil {
		return plan, fmt.Errorf("failed to list Docker volumes: %w", err)
	}
	for _, vol := range dockerVolumes {
		if !knownVolumes[vol.Name] && !belongsToAnyProject(vol.Name, unreadable) {
			plan.volumes = append(plan.volumes, vol.Name)
		}
	}
	// A missing directory may be back tomorrow: its volumes stay, and so
	// does its registration, or the next run would no longer know whose
	// they are and delete them as orphans.
	staleNames := make([]string, 0, len(stale))
	for _, p := range stale {
		staleNames = append(staleNames, p.name)
	}
	if !purgeMissing {
		plan.volumes, plan.keptVolumes = splitStaleVolumes(plan.volumes, staleNames)
	}
	for _, p := range stale {
		if len(plan.keptVolumes[p.name]) > 0 {
			plan.keptMissing = append(plan.keptMissing, p)
		} else {
			plan.unregister = append(plan.unregister, p)
		}
	}

	// Networks are the scarcest resource zdev consumes - Docker's default
	// address pool is exhausted after ~31 of them - and until now the only
	// one nothing reclaimed. `zdev down` removes a project's network, but a
	// project directory that is simply deleted leaves its network behind
	// forever.
	dockerNetworks, err := docker.ListNetworks(ctx, "")
	if err != nil {
		return plan, fmt.Errorf("failed to list Docker networks: %w", err)
	}
	removingContainer := make(map[string]bool, len(plan.containers))
	for _, name := range plan.containers {
		removingContainer[name] = true
	}
	for name := range unreadable {
		knownNetworks[name+".zdev"] = true
	}
	if plan.networks, err = selectOrphanNetworks(ctx, docker, dockerNetworks, knownNetworks, removingContainer); err != nil {
		return plan, err
	}

	byName := func(ps []cleanupProject) {
		sort.Slice(ps, func(i, j int) bool { return ps[i].name < ps[j].name })
	}
	byName(plan.unreadable)
	byName(plan.unregister)
	byName(plan.keptMissing)
	sort.Strings(plan.containers)
	sort.Strings(plan.volumes)
	sort.Strings(plan.networks)
	return plan, nil
}

// empty reports whether the plan removes nothing.
func (p cleanupPlan) empty() bool {
	return len(p.unregister) == 0 && len(p.containers) == 0 && len(p.volumes) == 0 && len(p.networks) == 0
}

// print shows the plan: what is kept and why, then what would be removed.
// Reports whether there is anything to remove.
func (p cleanupPlan) print(w io.Writer) bool {
	for _, u := range p.unreadable {
		fmt.Fprintf(w, "Warning: not inspecting %s (%s): %v\n", u.name, u.path, u.err)
		fmt.Fprintf(w, "         its containers, volumes and network are kept.\n\n")
	}

	if len(p.keptMissing) > 0 {
		fmt.Fprintf(w, "Projects whose directory is missing (%d) - kept with their volumes, the directory may be back later:\n", len(p.keptMissing))
		for _, m := range p.keptMissing {
			fmt.Fprintf(w, "  - %s (%s): %s\n", m.name, m.path, strings.Join(p.keptVolumes[m.name], ", "))
		}
		fmt.Fprintln(w, "  Gone for good? `zdev cleanup --purge-missing` removes them.")
		fmt.Fprintln(w)
	}

	if p.empty() {
		fmt.Fprintln(w, "Nothing to clean up.")
		return false
	}

	if len(p.unregister) > 0 {
		fmt.Fprintf(w, "Stale project registrations (%d) - directory missing on disk:\n", len(p.unregister))
		for _, s := range p.unregister {
			fmt.Fprintf(w, "  - %s (%s)\n", s.name, s.path)
		}
		fmt.Fprintln(w)
	}
	list := func(title string, names []string) {
		if len(names) == 0 {
			return
		}
		fmt.Fprintf(w, title+":\n", len(names))
		for _, name := range names {
			fmt.Fprintf(w, "  - %s\n", name)
		}
		fmt.Fprintln(w)
	}
	list("Orphaned containers (%d) - absent from live project configs", p.containers)
	list("Orphaned volumes (%d) - absent from live project configs", p.volumes)
	list("Orphaned networks (%d) - no live project or link owns them", p.networks)
	return true
}

// cleanupRegistry is the slice of the state manager apply changes.
type cleanupRegistry interface {
	UnregisterProject(name string) error
	RemoveProjectFromLinks(name string) error
}

// cleanupRemover is the slice of the runtime apply removes with.
type cleanupRemover interface {
	ForceRemoveContainer(ctx context.Context, nameOrID string) error
	RemoveNetwork(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
}

// apply carries out the plan, reporting each step; a failed step doesn't
// stop the others.
func (p cleanupPlan) apply(ctx context.Context, w io.Writer, reg cleanupRegistry, docker cleanupRemover) {
	for _, s := range p.unregister {
		fmt.Fprintf(w, "Unregistering %s... ", s.name)
		if err := reg.UnregisterProject(s.name); err != nil {
			fmt.Fprintf(w, "failed: %v\n", err)
			continue
		}
		fmt.Fprintln(w, "done")
		if err := reg.RemoveProjectFromLinks(s.name); err != nil {
			fmt.Fprintf(w, "Warning: failed to remove %s from its links: %v\n", s.name, err)
		}
		if err := ssl.RemoveProjectCert(s.name); err != nil {
			fmt.Fprintf(w, "Warning: failed to remove certificate for %s: %v\n", s.name, err)
		}
	}

	step := func(kind, name string, remove func(context.Context, string) error) bool {
		fmt.Fprintf(w, "Removing %s %s... ", kind, name)
		if err := remove(ctx, name); err != nil {
			fmt.Fprintf(w, "failed: %v\n", err)
			return false
		}
		fmt.Fprintln(w, "done")
		return true
	}
	for _, name := range p.containers {
		step("container", name, docker.ForceRemoveContainer)
	}
	// After the containers, so a network whose only attachments were just
	// removed can go too.
	for _, name := range p.networks {
		step("network", name, docker.RemoveNetwork)
	}
	var deleted, failed int
	for _, name := range p.volumes {
		if step("volume", name, docker.RemoveVolume) {
			deleted++
		} else {
			failed++
		}
	}
	if len(p.volumes) > 0 {
		fmt.Fprintf(w, "\nRemoved %d volume(s)", deleted)
		if failed > 0 {
			fmt.Fprintf(w, ", %d failed", failed)
		}
		fmt.Fprintln(w)
	}
}

// containerLister is the slice of the runtime selectOrphanNetworks needs.
type containerLister interface {
	ListContainers(ctx context.Context, filter string) ([]string, error)
}

// selectOrphanNetworks picks the zdev networks that no live project or link
// owns and that nothing is still attached to.
//
// Attachment is checked with `docker ps -a`, not the network's own endpoint
// list: the latter reports only RUNNING containers, and a stopped container
// pins its network by ID just as firmly. Removing a network out from under one
// leaves it unstartable until it is recreated, so anything still referenced is
// kept - except containers this same run is about to remove, which would
// otherwise keep their network alive one cleanup longer than necessary.
func selectOrphanNetworks(ctx context.Context, docker containerLister, networks []string, known, removingContainer map[string]bool) ([]string, error) {
	var orphans []string

	for _, name := range networks {
		if !isZdevNetwork(name) || known[name] {
			continue
		}

		attached, err := docker.ListContainers(ctx, "network="+name)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect network %s: %w", name, err)
		}

		inUse := false
		for _, containerName := range attached {
			if !removingContainer[containerName] {
				inUse = true
				break
			}
		}
		if !inUse {
			orphans = append(orphans, name)
		}
	}

	return orphans, nil
}

// isZdevNetwork reports whether a Docker network is one zdev creates: a
// project network (<project>.zdev) or a link network (zdev_link_<name>).
// Everything else on the host belongs to someone else.
func isZdevNetwork(name string) bool {
	return strings.HasSuffix(name, ".zdev") || strings.HasPrefix(name, "zdev_link_")
}

// belongsToAnyProject reports whether a container or volume name carries the
// name of one of the given projects, using zdev's <name>.<project>.zdev
// convention. Used to preserve everything belonging to a project whose config
// could not be read.
// splitStaleVolumes takes the volumes of projects whose directory is missing
// out of the orphans, per project.
func splitStaleVolumes(orphans, staleProjects []string) (remove []string, kept map[string][]string) {
	kept = map[string][]string{}
	for _, vol := range orphans {
		owner := ""
		for _, p := range staleProjects {
			if strings.HasSuffix(vol, "."+p+".zdev") {
				owner = p
				break
			}
		}
		if owner == "" {
			remove = append(remove, vol)
			continue
		}
		kept[owner] = append(kept[owner], vol)
	}
	return remove, kept
}

func belongsToAnyProject(resourceName string, projects map[string]bool) bool {
	for projectName := range projects {
		if strings.HasSuffix(resourceName, "."+projectName+".zdev") {
			return true
		}
	}
	return false
}
