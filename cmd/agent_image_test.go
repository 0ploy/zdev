package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
)

func imageTestProject(t *testing.T, svc config.ServiceConfig, configYAML string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755)
	if configYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"), []byte(configYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &project.Project{Dir: dir, Config: &config.ProjectConfig{Name: "shop", Services: map[string]config.ServiceConfig{"app": svc}}}
}

var alpineInfo = &agent.ImageInfo{OS: "Alpine Linux v3.24", PackageManager: "apk", UID: "0"}

func TestImageAdviceForImage(t *testing.T) {
	p := imageTestProject(t, config.ServiceConfig{Image: "php:8.4-fpm-alpine"}, "")
	text, fix := sandboxImageAdvice(p, "app", alpineInfo, []string{"bash", "git"})
	for _, want := range []string{"php:8.4-fpm-alpine, Alpine Linux v3.24) lacks bash and git", "Create .zdev/app.Dockerfile",
		"FROM php:8.4-fpm-alpine", "RUN apk add --no-cache bash git", "dockerfile: .zdev/app.Dockerfile", "zdev update"} {
		if !strings.Contains(text, want) {
			t.Errorf("advice lacks %q:\n%s", want, text)
		}
	}
	if fix == nil || !fix.Create || fix.Dockerfile != ".zdev/app.Dockerfile" || fix.FromImage != "php:8.4-fpm-alpine" {
		t.Errorf("fix = %+v", fix)
	}
}

func TestImageAdviceForDockerfile(t *testing.T) {
	p := imageTestProject(t, config.ServiceConfig{Dockerfile: ".zdev/Dockerfile"}, "")
	info := &agent.ImageInfo{PackageManager: "apt-get", UID: "33", User: "www-data"}
	text, fix := sandboxImageAdvice(p, "app", info, []string{"git"})
	for _, want := range []string{"built from .zdev/Dockerfile", "Add to .zdev/Dockerfile", "USER root", "USER www-data", "zdev update"} {
		if !strings.Contains(text, want) {
			t.Errorf("advice lacks %q:\n%s", want, text)
		}
	}
	if fix == nil || fix.Create {
		t.Errorf("fix = %+v, want an append to the existing Dockerfile", fix)
	}
}

func TestImageAdviceDistroless(t *testing.T) {
	p := imageTestProject(t, config.ServiceConfig{Image: "gcr.io/distroless/base"}, "")
	text, fix := sandboxImageAdvice(p, "app", &agent.ImageInfo{}, []string{"bash", "git"})
	if fix != nil || !strings.Contains(text, "no package manager") {
		t.Errorf("fix %+v, text:\n%s", fix, text)
	}
}

func TestImageFixCreatesDockerfileAndEditsConfig(t *testing.T) {
	cfg := "version: 1\nname: shop\nservices:\n  app:\n    # the PHP app\n    image: php:8.4-fpm-alpine\n    volumes:\n      - ${PROJECTPATH}:/app\n"
	p := imageTestProject(t, config.ServiceConfig{Image: "php:8.4-fpm-alpine"}, cfg)
	_, fix := sandboxImageAdvice(p, "app", alpineInfo, []string{"bash", "git"})
	if err := offerImageFix(bufio.NewReader(strings.NewReader("\n")), p, "app", alpineInfo); err != nil {
		t.Fatal(err)
	}
	df, err := os.ReadFile(filepath.Join(p.Dir, ".zdev", "app.Dockerfile"))
	if err != nil || !strings.Contains(string(df), "FROM php:8.4-fpm-alpine\nRUN apk add --no-cache bash git\n") {
		t.Errorf("Dockerfile %q, %v", df, err)
	}
	got, _ := os.ReadFile(filepath.Join(p.Dir, ".zdev", "config.yaml"))
	if !strings.Contains(string(got), "# the PHP app") || !strings.Contains(string(got), "dockerfile: .zdev/app.Dockerfile") || strings.Contains(string(got), "image:") {
		t.Errorf("config.yaml:\n%s", got)
	}
	loaded, err := config.LoadProject(p.Dir)
	if err != nil || loaded.Services["app"].Dockerfile != ".zdev/app.Dockerfile" {
		t.Errorf("edited config doesn't load as expected: %v", err)
	}
	if err := fix.apply(p.Dir); err == nil {
		t.Error("an existing Dockerfile was overwritten")
	}
}

func TestImageFixAppendsAndDeclines(t *testing.T) {
	p := imageTestProject(t, config.ServiceConfig{Dockerfile: ".zdev/Dockerfile"}, "")
	path := filepath.Join(p.Dir, ".zdev", "Dockerfile")
	_ = os.WriteFile(path, []byte("FROM node:24-alpine\nCMD [\"node\"]"), 0o644)
	if err := offerImageFix(bufio.NewReader(strings.NewReader("n\n")), p, "app", alpineInfo); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "apk add") {
		t.Error("changed although declined")
	}
	if err := offerImageFix(bufio.NewReader(strings.NewReader("y\n")), p, "app", alpineInfo); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), "CMD [\"node\"]\n\n# For zdev's agent sandbox:\nRUN apk add --no-cache bash git\n") {
		t.Errorf("Dockerfile:\n%s", b)
	}
}
