package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

var nameLineRegex = regexp.MustCompile(`(?m)^name:\s*.*$`)

// Rename changes the project name, migrating all Docker resources (containers, volumes, network)
// and updating the state file and config on disk. The project is stopped and restarted.
func (p *Project) Rename(ctx context.Context, newName string) error {
	oldName := p.Config.Name

	// Pick a project service image for volume migration (guaranteed local since project was running)
	copyImage := p.firstServiceImage()
	if copyImage == "" {
		return fmt.Errorf("no service images found for volume migration")
	}

	// Phase 1: Tear down with old name
	fmt.Printf("Stopping project %s...\n", oldName)

	if err := p.teardownContainers(ctx); err != nil {
		return err
	}

	// Phase 2: Migrate volumes (copy all first, then remove old)
	fmt.Println()
	fmt.Printf("Renaming %s -> %s...\n", oldName, newName)

	type volumePair struct{ oldName, newName string }
	var migrated []volumePair

	// Copy named volumes
	for _, volumeName := range p.NamedVolumes() {
		oldFullName := p.VolumeName(volumeName)
		newFullName := VolumeNameFor(volumeName, newName)

		copied, err := p.copyVolumeData(ctx, oldFullName, newFullName, copyImage)
		if err != nil {
			return fmt.Errorf("failed to migrate volume %s: %w", volumeName, err)
		}
		if copied {
			migrated = append(migrated, volumePair{oldFullName, newFullName})
		}
	}

	// Copy Mutagen sync volumes
	if p.IsMutagenEnabled() {
		for serviceName := range p.Config.Services {
			oldVolName := p.MutagenVolumeName(serviceName)
			newVolName := MutagenVolumeNameFor(serviceName, newName)

			copied, err := p.copyVolumeData(ctx, oldVolName, newVolName, copyImage)
			if err != nil {
				return fmt.Errorf("failed to migrate sync volume for %s: %w", serviceName, err)
			}
			if copied {
				migrated = append(migrated, volumePair{oldVolName, newVolName})
			}
		}
	}

	// Remove old volumes (safe - data is already copied to new volumes)
	for _, v := range migrated {
		if err := p.Runtime.RemoveVolume(ctx, v.oldName); err != nil {
			fmt.Printf("Warning: failed to remove old volume %s: %v\n", v.oldName, err)
		}
	}

	// Remove old network
	networkName := p.NetworkName()
	networkExists, _ := p.Runtime.NetworkExists(ctx, networkName)
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

	if err := stateMgr.RenameProject(oldName, newName); err != nil {
		// If project wasn't in state (e.g., never started), just register the new one
		if strings.Contains(err.Error(), "not found") {
			_ = stateMgr.RegisterProject(newName, p.Dir)
		} else {
			return fmt.Errorf("failed to update state: %w", err)
		}
	}

	// Phase 4: Update config file on disk
	if err := updateConfigName(p.Dir, newName); err != nil {
		return fmt.Errorf("failed to update config file: %w", err)
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

// copyVolumeData creates a new volume and copies data from the old one.
// Returns (true, nil) if data was copied, (false, nil) if old volume doesn't exist.
// On copy failure, the new volume is cleaned up.
func (p *Project) copyVolumeData(ctx context.Context, oldName, newName, image string) (bool, error) {
	exists, _ := p.Runtime.VolumeExists(ctx, oldName)
	if !exists {
		return false, nil
	}

	fmt.Printf("Migrating volume %s -> %s...\n", oldName, newName)

	if err := p.Runtime.CreateVolume(ctx, newName); err != nil {
		return false, fmt.Errorf("failed to create volume %s: %w", newName, err)
	}

	if err := p.Runtime.CopyVolume(ctx, oldName, newName, image); err != nil {
		_ = p.Runtime.RemoveVolume(ctx, newName)
		return false, fmt.Errorf("failed to copy volume data: %w", err)
	}

	return true, nil
}

// firstServiceImage returns the image of the first service in the config.
// Used for volume migration - project images are guaranteed local since the project was running.
func (p *Project) firstServiceImage() string {
	for _, svc := range p.Config.Services {
		if svc.Image != "" {
			return svc.Image
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
