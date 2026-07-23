package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

func TestFirstServiceImageSupportsDockerfileOnlyService(t *testing.T) {
	p := &Project{
		Config: &config.ProjectConfig{
			Name: "demo",
			Services: map[string]config.ServiceConfig{
				"app": {Dockerfile: "Dockerfile"},
			},
		},
		Runtime: runtime.NewMockRuntime(),
	}

	if got, want := p.firstServiceImage(), "zdev-demo-app:latest"; got != want {
		t.Fatalf("firstServiceImage() = %q, want %q", got, want)
	}
}

func TestUpdateConfigName_ReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	zdevDir := filepath.Join(dir, ".zdev")
	os.MkdirAll(zdevDir, 0755)

	configPath := filepath.Join(zdevDir, "config.yaml")
	original := "name: old-project\n\nservices:\n  app:\n    image: nginx\n"
	os.WriteFile(configPath, []byte(original), 0644)

	if err := updateConfigName(dir, "new-project"); err != nil {
		t.Fatalf("updateConfigName failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)

	if !strings.Contains(content, "name: new-project") {
		t.Errorf("expected 'name: new-project' in config, got:\n%s", content)
	}
	if strings.Contains(content, "old-project") {
		t.Errorf("old name should be replaced, got:\n%s", content)
	}
	// Verify rest of config preserved
	if !strings.Contains(content, "services:") {
		t.Errorf("rest of config should be preserved, got:\n%s", content)
	}
}

func TestUpdateConfigName_AddsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	zdevDir := filepath.Join(dir, ".zdev")
	os.MkdirAll(zdevDir, 0755)

	configPath := filepath.Join(zdevDir, "config.yaml")
	original := "services:\n  app:\n    image: nginx\n"
	os.WriteFile(configPath, []byte(original), 0644)

	if err := updateConfigName(dir, "my-project"); err != nil {
		t.Fatalf("updateConfigName failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)

	if !strings.HasPrefix(content, "name: my-project\n") {
		t.Errorf("expected name added at top, got:\n%s", content)
	}
	if !strings.Contains(content, "services:") {
		t.Errorf("rest of config should be preserved, got:\n%s", content)
	}
}

func TestUpdateConfigName_PreservesComments(t *testing.T) {
	dir := t.TempDir()
	zdevDir := filepath.Join(dir, ".zdev")
	os.MkdirAll(zdevDir, 0755)

	configPath := filepath.Join(zdevDir, "config.yaml")
	original := "# My project config\nname: old-name\n\n# Services\nservices:\n  app:\n    image: nginx\n"
	os.WriteFile(configPath, []byte(original), 0644)

	if err := updateConfigName(dir, "new-name"); err != nil {
		t.Fatalf("updateConfigName failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)

	if !strings.Contains(content, "# My project config") {
		t.Errorf("comments should be preserved, got:\n%s", content)
	}
	if !strings.Contains(content, "# Services") {
		t.Errorf("comments should be preserved, got:\n%s", content)
	}
	if !strings.Contains(content, "name: new-name") {
		t.Errorf("name should be updated, got:\n%s", content)
	}
}

func TestUpdateConfigName_HandlesQuotedName(t *testing.T) {
	dir := t.TempDir()
	zdevDir := filepath.Join(dir, ".zdev")
	os.MkdirAll(zdevDir, 0755)

	configPath := filepath.Join(zdevDir, "config.yaml")
	original := "name: \"old-name\"\nservices:\n  app:\n    image: nginx\n"
	os.WriteFile(configPath, []byte(original), 0644)

	if err := updateConfigName(dir, "new-name"); err != nil {
		t.Fatalf("updateConfigName failed: %v", err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)

	if !strings.Contains(content, "name: new-name") {
		t.Errorf("quoted name should be replaced, got:\n%s", content)
	}
}
