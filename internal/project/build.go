package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ui"
)

// BuildHashLabel carries the Dockerfile-contents hash on images built from a
// service `dockerfile:` config. Staleness is decided by comparing this label
// against a fresh hash of the Dockerfile - not by hashing the build context;
// Docker's own layer cache handles file-level changes, and source is
// bind-mounted live anyway. The build context is always the project root.
const BuildHashLabel = "zdev.build-hash"

// BuildMode controls when images with a `dockerfile:` config are (re)built.
type BuildMode int

const (
	// BuildIfStale builds when the image is missing or the Dockerfile
	// contents changed. The default.
	BuildIfStale BuildMode = iota
	// BuildAlways forces a rebuild (--build).
	BuildAlways
	// BuildNever skips building and errors if the image is missing (--no-build).
	BuildNever
)

// BuildModeFromFlags maps the --build / --no-build CLI flags to a BuildMode.
func BuildModeFromFlags(force, skip bool) BuildMode {
	switch {
	case force:
		return BuildAlways
	case skip:
		return BuildNever
	default:
		return BuildIfStale
	}
}

// buildImageTag returns the tag a service's `dockerfile:` config builds into:
// the service's `image:` if set, otherwise a deterministic generated tag.
func (p *Project) buildImageTag(name string, svc config.ServiceConfig) string {
	if svc.Image != "" {
		return svc.Image
	}
	return fmt.Sprintf("zdev-%s-%s:latest", p.Config.Name, name)
}

// serviceImage returns the image a service's container runs: the build tag
// when a `dockerfile:` config is present, otherwise the configured image.
func (p *Project) serviceImage(name string, svc config.ServiceConfig) string {
	if svc.Dockerfile != "" {
		return p.buildImageTag(name, svc)
	}
	return svc.Image
}

// dockerfilePath resolves the service's Dockerfile against the project root.
func (p *Project) dockerfilePath(svc config.ServiceConfig) string {
	if filepath.IsAbs(svc.Dockerfile) {
		return svc.Dockerfile
	}
	return filepath.Join(p.Dir, svc.Dockerfile)
}

// serviceBuildHash hashes the Dockerfile contents - the input that triggers a
// rebuild. Editing source files does not change the hash; source is
// bind-mounted live into dev containers.
func (p *Project) serviceBuildHash(name string, svc config.ServiceConfig) (string, error) {
	contents, err := os.ReadFile(p.dockerfilePath(svc))
	if err != nil {
		return "", fmt.Errorf("service %s: cannot read Dockerfile %s: %w", name, svc.Dockerfile, err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
}

// imageBuildStale reports whether the image is missing or was built from a
// different Dockerfile than the given hash.
func (p *Project) imageBuildStale(ctx context.Context, tag, hash string) (bool, error) {
	exists, err := p.Runtime.ImageExists(ctx, tag)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	labels, err := p.Runtime.GetImageLabels(ctx, tag)
	if err != nil {
		return false, err
	}
	return labels[BuildHashLabel] != hash, nil
}

// serviceBuildStale reports whether a service's `dockerfile:` image needs
// rebuilding under the current BuildMode. Update folds this into its
// recreate decision; the rebuild itself happens on the recreate path.
func (p *Project) serviceBuildStale(ctx context.Context, name string, svc config.ServiceConfig) (bool, error) {
	if svc.Dockerfile == "" || p.BuildMode == BuildNever {
		return false, nil
	}
	if p.BuildMode == BuildAlways {
		return true, nil
	}
	hash, err := p.serviceBuildHash(name, svc)
	if err != nil {
		return false, err
	}
	return p.imageBuildStale(ctx, p.buildImageTag(name, svc), hash)
}

// ensureBuiltImage makes sure the image for a service with a `dockerfile:`
// config exists and is current, building it when needed. Returns whether a
// build ran so callers can recreate an existing container to pick up the new
// image.
func (p *Project) ensureBuiltImage(ctx context.Context, name string, svc config.ServiceConfig) (bool, error) {
	tag := p.buildImageTag(name, svc)

	if p.BuildMode == BuildNever {
		exists, err := p.Runtime.ImageExists(ctx, tag)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, fmt.Errorf("service %s: image %s does not exist and --no-build was given - drop --no-build to build it", name, tag)
		}
		return false, nil
	}

	hash, err := p.serviceBuildHash(name, svc)
	if err != nil {
		return false, err
	}

	if p.BuildMode != BuildAlways {
		stale, err := p.imageBuildStale(ctx, tag, hash)
		if err != nil {
			return false, err
		}
		if !stale {
			return false, nil
		}
	}

	plainMode := false
	if gcfg, err := config.LoadGlobalConfig(); err == nil {
		plainMode = ui.PlainMode(gcfg.Terminal.Plain)
	}
	ui.StatusStep(fmt.Sprintf("Building image %s", tag), plainMode)

	if err := p.Runtime.BuildImage(ctx, runtime.ImageBuildConfig{
		Tag:        tag,
		Context:    p.Dir,
		Dockerfile: p.dockerfilePath(svc),
		Labels:     map[string]string{BuildHashLabel: hash},
	}); err != nil {
		return false, fmt.Errorf("failed to build image %s: %w", tag, err)
	}
	return true, nil
}
