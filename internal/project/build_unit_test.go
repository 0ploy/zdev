package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// buildTestTag is the generated tag for the "app" service in newBuildTestProject
const buildTestTag = "zdev-buildproj-app:latest"

// newBuildTestProject creates a project whose "app" service has a dockerfile:
// config pointing at a real Dockerfile in a temp dir. svcMod can adjust the
// service config before it is stored.
func newBuildTestProject(t *testing.T, mock *runtime.MockRuntime, svcMod func(*config.ServiceConfig)) *Project {
	t.Helper()
	cleanup := setupTestEnv(t)
	t.Cleanup(cleanup)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeTestDockerfile(t, dir, "FROM alpine:latest\n")

	svc := config.ServiceConfig{
		Command:    "sleep infinity",
		Dockerfile: ".zdev/Dockerfile",
	}
	if svcMod != nil {
		svcMod(&svc)
	}

	return &Project{
		Dir: dir,
		Config: &config.ProjectConfig{
			Name:     "buildproj",
			Services: map[string]config.ServiceConfig{"app": svc},
		},
		Runtime: mock,
	}
}

func writeTestDockerfile(t *testing.T, projectDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, ".zdev", "Dockerfile"), []byte(contents), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
}

// seedFreshImage marks the build image as existing with the current build hash,
// as if a previous start had just built it.
func seedFreshImage(t *testing.T, p *Project, mock *runtime.MockRuntime) {
	t.Helper()
	hash, err := p.serviceBuildHash("app", p.Config.Services["app"])
	if err != nil {
		t.Fatalf("serviceBuildHash: %v", err)
	}
	mock.ImagesExist[buildTestTag] = true
	mock.ImageLabels[buildTestTag] = map[string]string{BuildHashLabel: hash}
}

func TestBuildImageTag(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)

	if got := p.buildImageTag("app", p.Config.Services["app"]); got != buildTestTag {
		t.Errorf("default tag = %q, want %q", got, buildTestTag)
	}

	svc := p.Config.Services["app"]
	svc.Image = "myapp-dev:latest"
	if got := p.buildImageTag("app", svc); got != "myapp-dev:latest" {
		t.Errorf("tag with image set = %q, want myapp-dev:latest", got)
	}
}

func TestStartService_BuildsWhenImageMissing(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	mock.ImagesExist[buildTestTag] = false

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err != nil {
		t.Fatalf("startServiceWithMutagen: %v", err)
	}

	if mock.CallCount("BuildImage") != 1 {
		t.Fatalf("BuildImage called %d times, want 1", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("PullImage") != 0 {
		t.Errorf("PullImage called %d times, want 0 for build: services", mock.CallCount("PullImage"))
	}

	built := mock.BuiltImages[buildTestTag]
	if built.Labels[BuildHashLabel] == "" {
		t.Error("built image missing the build-hash label")
	}
	if built.Dockerfile != filepath.Join(p.Dir, ".zdev", "Dockerfile") {
		t.Errorf("Dockerfile = %q, want resolved against project root", built.Dockerfile)
	}
	if built.Context != p.Dir {
		t.Errorf("Context = %q, want project root %q (context is always the project root)", built.Context, p.Dir)
	}

	created := mock.Containers[p.ContainerName("app")]
	if created.Image != buildTestTag {
		t.Errorf("container image = %q, want %q", created.Image, buildTestTag)
	}
}

func TestStartService_SkipsBuildWhenFresh(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	seedFreshImage(t, p, mock)

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err != nil {
		t.Fatalf("startServiceWithMutagen: %v", err)
	}

	if mock.CallCount("BuildImage") != 0 {
		t.Errorf("BuildImage called %d times, want 0 when image is fresh", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("CreateContainer") != 1 {
		t.Errorf("CreateContainer called %d times, want 1", mock.CallCount("CreateContainer"))
	}
}

func TestStartService_RebuildsAndRecreatesOnDockerfileChange(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	seedFreshImage(t, p, mock)

	// Simulate a running container from the previous build
	containerName := p.ContainerName("app")
	mock.ContainersExist[containerName] = true
	mock.ContainersRunning[containerName] = true

	writeTestDockerfile(t, p.Dir, "FROM alpine:latest\nRUN apk add --no-cache curl\n")

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err != nil {
		t.Fatalf("startServiceWithMutagen: %v", err)
	}

	if mock.CallCount("BuildImage") != 1 {
		t.Errorf("BuildImage called %d times, want 1 after Dockerfile edit", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("RemoveContainer") != 1 {
		t.Errorf("RemoveContainer called %d times, want 1 (recreate to pick up new image)", mock.CallCount("RemoveContainer"))
	}
	if mock.CallCount("CreateContainer") != 1 {
		t.Errorf("CreateContainer called %d times, want 1", mock.CallCount("CreateContainer"))
	}
}

func TestStartService_NoBuildMissingImageErrors(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	p.BuildMode = BuildNever
	mock.ImagesExist[buildTestTag] = false

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err == nil {
		t.Fatal("expected error with --no-build and missing image")
	}
	if !strings.Contains(err.Error(), "--no-build") || !strings.Contains(err.Error(), buildTestTag) {
		t.Errorf("error should name the image and --no-build, got: %v", err)
	}
	if mock.CallCount("BuildImage") != 0 {
		t.Errorf("BuildImage called %d times, want 0", mock.CallCount("BuildImage"))
	}
}

func TestStartService_NoBuildUsesStaleImage(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	p.BuildMode = BuildNever
	mock.ImagesExist[buildTestTag] = true
	mock.ImageLabels[buildTestTag] = map[string]string{BuildHashLabel: "outdated"}

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err != nil {
		t.Fatalf("startServiceWithMutagen: %v", err)
	}
	if mock.CallCount("BuildImage") != 0 {
		t.Errorf("BuildImage called %d times, want 0 with --no-build", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("CreateContainer") != 1 {
		t.Errorf("CreateContainer called %d times, want 1", mock.CallCount("CreateContainer"))
	}
}

func TestStartService_ForceBuildRebuildsFreshImage(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	p.BuildMode = BuildAlways
	seedFreshImage(t, p, mock)

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err != nil {
		t.Fatalf("startServiceWithMutagen: %v", err)
	}
	if mock.CallCount("BuildImage") != 1 {
		t.Errorf("BuildImage called %d times, want 1 with --build", mock.CallCount("BuildImage"))
	}
}

// seedRunningBuildService puts a running container in the mock whose config
// hash matches the current config, so only build staleness can trigger Update.
func seedRunningBuildService(t *testing.T, mock *runtime.MockRuntime, p *Project) {
	t.Helper()
	cfg := p.buildContainerConfig("app", p.Config.Services["app"], false, nil)
	mock.Containers[cfg.Name] = cfg
	mock.ContainersExist[cfg.Name] = true
	mock.ContainersRunning[cfg.Name] = true
	mock.NetworksExist[p.NetworkName()] = true
}

func TestUpdate_RebuildsStaleImage(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	seedRunningBuildService(t, mock, p)
	mock.ImagesExist[buildTestTag] = true
	mock.ImageLabels[buildTestTag] = map[string]string{BuildHashLabel: "outdated"}

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated {
		t.Error("expected Update to report changes for a stale build image")
	}
	if mock.CallCount("BuildImage") != 1 {
		t.Errorf("BuildImage called %d times, want 1", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("RemoveContainer") != 1 {
		t.Errorf("RemoveContainer called %d times, want 1", mock.CallCount("RemoveContainer"))
	}
	if mock.CallCount("CreateContainer") != 1 {
		t.Errorf("CreateContainer called %d times, want 1", mock.CallCount("CreateContainer"))
	}
}

func TestUpdate_FreshBuildImageIsNoop(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newBuildTestProject(t, mock, nil)
	seedRunningBuildService(t, mock, p)
	seedFreshImage(t, p, mock)

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated {
		t.Error("expected no changes when build image is fresh")
	}
	if mock.CallCount("BuildImage") != 0 {
		t.Errorf("BuildImage called %d times, want 0", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("RemoveContainer") != 0 {
		t.Errorf("RemoveContainer called %d times, want 0", mock.CallCount("RemoveContainer"))
	}
}
