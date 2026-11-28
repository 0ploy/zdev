package cmd

// What to change when a service's image lacks what agent mode needs (bash,
// git): the exact Dockerfile lines for the image's package manager, in the
// service's own Dockerfile, or in a new dev Dockerfile built FROM its image.
// zdev claude prints it when it refuses; zdev agent setup offers to apply it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
)

// imageFix is the change that adds the missing packages, when there is one.
type imageFix struct {
	Service    string
	Dockerfile string // project-relative
	Create     bool   // a new Dockerfile FROM the service's image, else append to the existing one
	Lines      []string
	FromImage  string // Create: the image it builds on, which config's image: no longer names
}

// sandboxImageAdvice explains what the service's image lacks and how to add
// it. fix is nil when nothing can be added (no package manager).
func sandboxImageAdvice(proj *project.Project, service string, info *agent.ImageInfo, missing []string) (string, *imageFix) {
	svc := proj.Config.Services[service]
	var b strings.Builder
	what := svc.Image
	if svc.Dockerfile != "" {
		what = "built from " + svc.Dockerfile
	}
	if info.OS != "" {
		what += ", " + info.OS
	}
	fmt.Fprintf(&b, "Service %s's image (%s) lacks %s, which the agent sandbox needs.\n", service, what, strings.Join(missing, " and "))

	lines, ok := info.InstallAdvice(missing)
	if !ok {
		b.WriteString("It has no package manager (a distroless image?), so nothing can be added to it.\n")
		b.WriteString("Run the agent in a service with the project's toolchain instead: zdev claude <service>, or let\n")
		b.WriteString("zdev agent setup store one; or add a dev service built with dockerfile: from a toolchain image.\n")
		return b.String(), nil
	}
	fix := &imageFix{Service: service, Lines: lines}
	if svc.Dockerfile != "" {
		fix.Dockerfile = svc.Dockerfile
		fmt.Fprintf(&b, "\nAdd to %s:\n\n", svc.Dockerfile)
		writeIndented(&b, lines)
		b.WriteString("\nthen run: zdev update\n")
		return b.String(), fix
	}
	fix.Create, fix.FromImage = true, svc.Image
	fix.Dockerfile = filepath.ToSlash(filepath.Join(".zdev", service+".Dockerfile"))
	fmt.Fprintf(&b, "\nBuild a dev image from it:\n\n1. Create %s:\n\n", fix.Dockerfile)
	writeIndented(&b, append([]string{"FROM " + svc.Image}, lines...))
	fmt.Fprintf(&b, "\n2. In .zdev/config.yaml, under services.%s, replace image: with\n\n    dockerfile: %s\n", service, fix.Dockerfile)
	b.WriteString("\n3. Run: zdev update\n")
	return b.String(), fix
}

func writeIndented(b *strings.Builder, lines []string) {
	for _, l := range lines {
		fmt.Fprintf(b, "    %s\n", l)
	}
}

// apply makes the change: writes or extends the Dockerfile and, for a new
// one, points the service at it in .zdev/config.yaml.
func (f *imageFix) apply(projectDir string) error {
	path := filepath.Join(projectDir, filepath.FromSlash(f.Dockerfile))
	if f.Create {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists already; add the lines yourself", f.Dockerfile)
		}
		content := "# Dev image for zdev's agent sandbox: " + f.FromImage + " plus what agent mode needs.\n" +
			"FROM " + f.FromImage + "\n" + strings.Join(f.Lines, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
		err := config.EditProjectConfig(projectDir, func(root *yaml.Node) error {
			svc, err := config.YAMLMapping(root, "services", f.Service)
			if err != nil {
				return err
			}
			// Replace image: in place (its position and comments stay).
			for i := 0; i+1 < len(svc.Content); i += 2 {
				if svc.Content[i].Value == "image" {
					svc.Content[i].Value = "dockerfile"
					svc.Content[i+1].Value, svc.Content[i+1].Tag, svc.Content[i+1].Style = f.Dockerfile, "!!str", 0
					return nil
				}
			}
			return config.SetYAML(svc, "dockerfile", f.Dockerfile)
		})
		if err != nil {
			_ = os.Remove(path)
			return err
		}
		return nil
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(old)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n# For zdev's agent sandbox:\n" + strings.Join(f.Lines, "\n") + "\n"
	return os.WriteFile(path, []byte(text), 0o644)
}
