package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ui"
)

// ScaffoldHookName is the create-time scaffold hook, relative to .zdev/. A
// template ships it to generate a fresh project (framework scaffolders like
// `nuxi init` or `composer create-project`). It runs once, at `zdev create`
// time, inside a throwaway container; it is not a persistent `zdev` command and
// nothing re-runs it on `zdev start`.
const ScaffoldHookName = "scaffold.sh"

// scaffoldHookRelPath is the hook path inside the (bind-mounted) project.
const scaffoldHookRelPath = ".zdev/" + ScaffoldHookName

// ScaffoldHookPath returns the absolute path to an ACTIVE scaffold hook, or ""
// if there is none. A hook renamed to scaffold.sh.disabled is intentionally
// ignored, so a scaffolded project used as a template again won't re-scaffold.
func (p *Project) ScaffoldHookPath() string {
	path := filepath.Join(p.Dir, scaffoldHookRelPath)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

// DisableScaffoldHook renames an active scaffold hook to <name>.disabled, so it
// is retained for reference but never runs again — a scaffolded project reused
// as a template won't re-scaffold and clobber the work. No-op when there is no
// active hook. Returns the new path, or "" when nothing was renamed.
func (p *Project) DisableScaffoldHook() (string, error) {
	active := p.ScaffoldHookPath()
	if active == "" {
		return "", nil
	}
	disabled := active + ".disabled"
	if err := os.Rename(active, disabled); err != nil {
		return "", err
	}
	return disabled, nil
}

// scaffoldService picks the service whose image carries the scaffolding
// toolchain. With a single service that's unambiguous; otherwise the service
// named "app" wins; otherwise it's an error the template author must resolve.
func (p *Project) scaffoldService() (string, config.ServiceConfig, error) {
	svcs := p.Config.Services
	if len(svcs) == 1 {
		for name, svc := range svcs {
			return name, svc, nil
		}
	}
	if svc, ok := svcs["app"]; ok {
		return "app", svc, nil
	}
	return "", config.ServiceConfig{}, fmt.Errorf(
		"scaffold: cannot decide which service to scaffold in (%d services, none named \"app\")", len(svcs))
}

// RunScaffold runs the template's create-time scaffold hook inside a throwaway
// container built from the scaffold service's image. The container's ENTRYPOINT
// is overridden to a plain shell, so whatever init the image normally runs
// (zpinit, a PHP entrypoint, …) is bypassed — the hook is process-manager
// agnostic. The project directory is bind-mounted at /app so scaffolded files
// land on the host synchronously; no Mutagen session is involved.
//
// Dependency install is deliberately NOT the hook's job — that belongs in the
// container's normal boot (so clones and dependency changes are covered). The
// hook should scaffold source only (e.g. `nuxi init --no-install`).
func (p *Project) RunScaffold(ctx context.Context) error {
	hook := p.ScaffoldHookPath()
	if hook == "" {
		return fmt.Errorf("no active %s to run", scaffoldHookRelPath)
	}

	// The bind-mount source must be absolute: docker treats a relative -v source
	// as a named volume, which would silently discard the scaffolded files.
	projectDir, err := filepath.Abs(p.Dir)
	if err != nil {
		return fmt.Errorf("scaffold: cannot resolve project dir %q: %w", p.Dir, err)
	}

	name, svc, err := p.scaffoldService()
	if err != nil {
		return err
	}

	image := p.serviceImage(name, svc)
	if image == "" {
		return fmt.Errorf("scaffold: service %q has neither image nor dockerfile", name)
	}

	// Make the image available: build a `dockerfile:` image, else pull a
	// referenced image when it's missing locally.
	if svc.Dockerfile != "" {
		if _, err := p.ensureBuiltImage(ctx, name, svc); err != nil {
			return err
		}
	} else {
		exists, err := p.Runtime.ImageExists(ctx, image)
		if err != nil {
			return err
		}
		if !exists {
			if err := p.Runtime.PullImage(ctx, image); err != nil {
				return err
			}
		}
	}

	plainMode := false
	if gcfg, err := config.LoadGlobalConfig(); err == nil {
		plainMode = ui.PlainMode(gcfg.Terminal.Plain)
	}
	ui.StatusStep("Scaffolding project", plainMode)

	// Run `sh /app/.zdev/scaffold.sh` in the throwaway container. Invoking via
	// the shell means the hook needs no executable bit to survive template
	// copy/download.
	return p.Runtime.RunContainer(ctx, runtime.RunConfig{
		Image:      image,
		Entrypoint: "sh",
		Command:    []string{"/app/" + scaffoldHookRelPath},
		Volumes:    []runtime.VolumeMount{{Source: projectDir, Target: "/app"}},
		WorkingDir: "/app",
	})
}
