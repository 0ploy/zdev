package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ssl"
	"github.com/0ploy/zdev/internal/state"
)

var nameLineRegex = regexp.MustCompile(`(?m)^name:\s*.*$`)

// Rename changes the project name, migrating all Docker resources (containers, volumes, network)
// and updating the state file and config on disk. The project is stopped and restarted.
//
// Order matters because Docker cannot rename a volume: data is copied to
// volumes under the new name, and the old ones are removed only once state
// and config both say the new name. Any failure before that point rolls the
// copies back, so the old volumes - still what config and state point at -
// are never the casualty, and a retry is not blocked by half-made targets.
func (p *Project) Rename(ctx context.Context, newName string) error {
	oldName := p.Config.Name

	// Pick a project service image for volume migration (guaranteed local since project was running)
	copyImage := p.firstServiceImage()
	if copyImage == "" {
		return fmt.Errorf("no service images found for volume migration")
	}

	// Phase 0: Plan the migration and refuse before touching anything if a
	// target volume already exists (e.g. left behind by a `zdev down`ed
	// project of that name) - copying into it would overlay two datasets.
	pairs, err := p.renameVolumePairs(ctx, newName)
	if err != nil {
		return err
	}

	// Phase 1: Tear down with old name
	fmt.Printf("Stopping project %s...\n", oldName)

	if err := p.teardownContainers(ctx); err != nil {
		return err
	}

	// Phase 2: Copy volumes. Old volumes stay until state and config are updated.
	fmt.Println()
	fmt.Printf("Renaming %s -> %s...\n", oldName, newName)

	var copied []volumePair
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, v := range copied {
			if err := p.Runtime.RemoveVolume(ctx, v.newName); err != nil {
				fmt.Printf("Warning: failed to roll back volume %s: %v\n", v.newName, err)
			}
		}
	}()

	for _, v := range pairs {
		fmt.Printf("Migrating volume %s -> %s...\n", v.oldName, v.newName)
		if err := p.copyVolumeData(ctx, v.oldName, v.newName, copyImage); err != nil {
			return fmt.Errorf("failed to migrate volume %s: %w", v.oldName, err)
		}
		copied = append(copied, v)
	}

	// Remove old network
	networkName := p.NetworkName()
	networkExists, err := p.Runtime.NetworkExists(ctx, networkName)
	if err != nil {
		return fmt.Errorf("failed to check network %s: %w", networkName, err)
	}
	if networkExists {
		fmt.Printf("Removing network %s...\n", networkName)
		if err := p.Runtime.RemoveNetwork(ctx, networkName); err != nil {
			return fmt.Errorf("failed to remove network: %w", err)
		}
	}

	// Phase 3: Update state
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	stateRenamed := false
	if err := stateMgr.RenameProject(oldName, newName); err != nil {
		// If project wasn't in state (e.g., never started), just register the new one
		if strings.Contains(err.Error(), "not found") {
			_ = stateMgr.RegisterProject(newName, p.Dir)
		} else {
			return fmt.Errorf("failed to update state: %w", err)
		}
	} else {
		stateRenamed = true
	}

	// Phase 4: Update config file on disk
	if err := updateConfigName(p.Dir, newName); err != nil {
		if stateRenamed {
			if rbErr := stateMgr.RenameProject(newName, oldName); rbErr != nil {
				fmt.Printf("Warning: failed to roll back state rename: %v\n", rbErr)
			}
		}
		return fmt.Errorf("failed to update config file: %w", err)
	}
	committed = true

	// The new name's cert is issued by the Start below
	if err := ssl.RemoveProjectCert(oldName); err != nil {
		fmt.Printf("Warning: failed to remove certificate for %s: %v\n", oldName, err)
	}

	// Only now that config and state point at the new volumes are the old
	// ones expendable.
	for _, v := range copied {
		if err := p.Runtime.RemoveVolume(ctx, v.oldName); err != nil {
			fmt.Printf("Warning: failed to remove old volume %s: %v\n", v.oldName, err)
		}
	}

	// Phase 5: Reload and start with new name
	fmt.Println()
	reloaded, err := LoadFromDir(p.Dir)
	if err != nil {
		return fmt.Errorf("failed to reload project: %w", err)
	}

	fmt.Printf("Starting project %s...\n", newName)
	return reloaded.Start(ctx)
}

type volumePair struct{ oldName, newName string }

// renameVolumePairs lists the existing volumes to migrate (named volumes and
// Mutagen sync volumes) with their names under newName. It fails if a
// lookup errors - a transient Docker error must not read as "no such volume"
// and silently leave data behind - or if a target name is already taken.
func (p *Project) renameVolumePairs(ctx context.Context, newName string) ([]volumePair, error) {
	var candidates []volumePair
	for _, volumeName := range p.NamedVolumes() {
		candidates = append(candidates, volumePair{p.VolumeName(volumeName), VolumeNameFor(volumeName, newName)})
	}
	// A service has one sync volume per directory bind, so enumerate the
	// mounts rather than the services - the volume name depends on the
	// mount's container path once a service has more than one.
	if p.IsMutagenEnabled() {
		for serviceName, mounts := range NewMutagenMounts(p.GetMutagenSyncMounts()) {
			for _, mount := range mounts {
				newVolName, _ := SyncNames(serviceName, mount.ContainerPath, newName, len(mounts))
				candidates = append(candidates, volumePair{mount.VolumeName, newVolName})
			}
		}
	}

	var pairs []volumePair
	for _, v := range candidates {
		exists, err := p.Runtime.VolumeExists(ctx, v.oldName)
		if err != nil {
			return nil, fmt.Errorf("failed to check volume %s: %w", v.oldName, err)
		}
		if !exists {
			continue
		}
		taken, err := p.Runtime.VolumeExists(ctx, v.newName)
		if err != nil {
			return nil, fmt.Errorf("failed to check volume %s: %w", v.newName, err)
		}
		if taken {
			return nil, fmt.Errorf("volume %s already exists (left over from an earlier project named like this?) - remove it with 'docker volume rm %s' if its data is not needed, then retry", v.newName, v.newName)
		}
		pairs = append(pairs, v)
	}
	return pairs, nil
}

// copyVolumeData creates newName and copies oldName's data into it. On copy
// failure the new volume is cleaned up.
func (p *Project) copyVolumeData(ctx context.Context, oldName, newName, image string) error {
	if err := p.Runtime.CreateVolume(ctx, newName); err != nil {
		return fmt.Errorf("failed to create volume %s: %w", newName, err)
	}

	if err := p.Runtime.CopyVolume(ctx, oldName, newName, image); err != nil {
		_ = p.Runtime.RemoveVolume(ctx, newName)
		return fmt.Errorf("failed to copy volume data: %w", err)
	}

	return nil
}

// firstServiceImage returns a runnable image from the project config. Generated
// tags for dockerfile-only services are local because rename requires the
// project to have been started.
//
// Services are tried in sorted order (map order would make a project with one
// shell-less image fail only sometimes, and only after teardown).
func (p *Project) firstServiceImage() string {
	names := make([]string, 0, len(p.Config.Services))
	for name := range p.Config.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if image := p.serviceImage(name, p.Config.Services[name]); image != "" {
			return image
		}
	}
	return ""
}

// VolumeNameFor returns the full volume name for a given volume and project name.
// Standalone version of Project.VolumeName() for use without a loaded Project.
func VolumeNameFor(volume, projectName string) string {
	return runtime.ScopedName(volume, projectName)
}

// MutagenVolumeNameFor returns the Mutagen sync volume name for a given service and project.
// Standalone version of Project.MutagenVolumeName() for use without a loaded Project.
func MutagenVolumeNameFor(service, projectName string) string {
	return runtime.MutagenSyncVolumeName(service, projectName)
}

// updateConfigName reads the config file and sets the name field.
// Uses targeted replacement to preserve YAML formatting and comments.
func updateConfigName(projectDir, newName string) error {
	configPath := filepath.Join(projectDir, ".zdev", "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	content := string(data)

	if nameLineRegex.MatchString(content) {
		content = nameLineRegex.ReplaceAllString(content, "name: "+newName)
	} else {
		content = "name: " + newName + "\n" + content
	}

	return os.WriteFile(configPath, []byte(content), 0644)
}
