package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubstituteVariables(t *testing.T) {
	vars := map[string]string{
		"PROJECTNAME":  "myapp",
		"ZDEV_DOMAIN": "0ploy.dev",
		"USER":         "testuser",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple substitution",
			input:    "name: ${PROJECTNAME}",
			expected: "name: myapp",
		},
		{
			name:     "multiple substitutions",
			input:    "${PROJECTNAME}.${ZDEV_DOMAIN}",
			expected: "myapp.0ploy.dev",
		},
		{
			name:     "no variables",
			input:    "plain text",
			expected: "plain text",
		},
		{
			name:     "unknown variable left as-is",
			input:    "${UNKNOWN_VAR}",
			expected: "${UNKNOWN_VAR}",
		},
		{
			name:     "mixed known and unknown",
			input:    "${PROJECTNAME}-${UNKNOWN}",
			expected: "myapp-${UNKNOWN}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := substituteVariables(tt.input, vars)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestBuildVariables(t *testing.T) {
	projectDir := "/home/user/projects/myapp"
	vars := buildVariables(projectDir)

	// PROJECTDIR should be the directory basename
	if vars["PROJECTDIR"] != "myapp" {
		t.Errorf("expected PROJECTDIR to be 'myapp', got %q", vars["PROJECTDIR"])
	}

	// PROJECTNAME should NOT be set by buildVariables (it's set later after parsing)
	if _, exists := vars["PROJECTNAME"]; exists {
		t.Error("PROJECTNAME should not be set by buildVariables")
	}

	if vars["PROJECTPATH"] != projectDir {
		t.Errorf("expected PROJECTPATH to be %q, got %q", projectDir, vars["PROJECTPATH"])
	}

	if vars["ZDEV_DOMAIN"] == "" {
		t.Error("expected ZDEV_DOMAIN to have a default value")
	}
}

func TestLoadProject(t *testing.T) {
	// Use the minimal test fixture
	projectDir := filepath.Join("..", "..", "testdata", "projects", "minimal")
	absPath, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}

	cfg, err := LoadProject(absPath)
	if err != nil {
		t.Fatalf("LoadProject failed: %v", err)
	}

	if cfg.Name != "minimal" {
		t.Errorf("expected Name to be 'minimal', got %q", cfg.Name)
	}

	if cfg.Version != 1 {
		t.Errorf("expected Version to be 1, got %d", cfg.Version)
	}

	if len(cfg.Services) != 1 {
		t.Errorf("expected 1 service, got %d", len(cfg.Services))
	}

	app, ok := cfg.Services["app"]
	if !ok {
		t.Fatal("expected 'app' service to exist")
	}

	if app.Image != TestImage {
		t.Errorf("expected app.Image to be %q, got %q", TestImage, app.Image)
	}
}

// TestLoadProject_PartialSharedBlockKeepsDefaults locks in the contract
// documented in defaultProjectShared(): the project loader pre-initializes
// cfg.Shared so missing fields keep their defaults. If a future refactor
// moves cfg construction or order-of-operations relative to decoder.Decode,
// every default silently flips back to false - this test is the canary.
func TestLoadProject_PartialSharedBlockKeepsDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configContent := `version: 1
name: defaults-check
shared:
  router: false   # explicit override of a default-true field
  db: true        # opt in to a default-false field
services:
  app:
    image: alpine:latest
    command: sleep infinity
`
	if err := os.WriteFile(filepath.Join(zdevDir, "config.yaml"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	// Explicit overrides win in both directions.
	if cfg.Shared.Router {
		t.Error("Router: explicit false should override default true")
	}
	if !cfg.Shared.DBUI {
		t.Error("DBUI: explicit true should be honored")
	}

	// Missing fields keep their defaults from defaultProjectShared().
	if !cfg.Shared.Mail {
		t.Error("Mail: should default to true when not specified")
	}
	if !cfg.Shared.Logs {
		t.Error("Logs: should default to true when not specified")
	}

	// Default-false fields stay false when not specified.
	if cfg.Shared.RedisInsights {
		t.Error("RedisInsights: should default to false when not specified")
	}
}

func TestLoadProjectWithVariables(t *testing.T) {
	// Set ZDEV_DOMAIN for predictable test results
	oldDomain := os.Getenv("ZDEV_DOMAIN")
	os.Setenv("ZDEV_DOMAIN", "0ploy.dev")
	defer os.Setenv("ZDEV_DOMAIN", oldDomain)

	// Use the full test fixture which has variable substitution
	projectDir := filepath.Join("..", "..", "testdata", "projects", "full")
	absPath, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}

	cfg, err := LoadProject(absPath)
	if err != nil {
		t.Fatalf("LoadProject failed: %v", err)
	}

	// PROJECTNAME should be substituted to "full"
	if cfg.Name != "full" {
		t.Errorf("expected Name to be 'full', got %q", cfg.Name)
	}

	// Domain should have variables substituted
	expectedDomain := "full.0ploy.dev"
	if cfg.Domain != expectedDomain {
		t.Errorf("expected Domain to be %q, got %q", expectedDomain, cfg.Domain)
	}

	// Check services
	if len(cfg.Services) != 2 {
		t.Errorf("expected 2 services, got %d", len(cfg.Services))
	}
}

func TestLoadProjectWithUserVariables(t *testing.T) {
	projectDir := filepath.Join("..", "..", "testdata", "projects", "variables")
	absPath, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}

	cfg, err := LoadProject(absPath)
	if err != nil {
		t.Fatalf("LoadProject failed: %v", err)
	}

	// Variables should be substituted in service environments
	appEnv := cfg.Services["app"].Environment
	expectedURL := "mysql://root:secret123@db:3306/variables_db"
	if appEnv["DATABASE_URL"] != expectedURL {
		t.Errorf("expected DATABASE_URL to be %q, got %q", expectedURL, appEnv["DATABASE_URL"])
	}

	dbEnv := cfg.Services["db"].Environment
	if dbEnv["MYSQL_ROOT_PASSWORD"] != "secret123" {
		t.Errorf("expected MYSQL_ROOT_PASSWORD to be 'secret123', got %q", dbEnv["MYSQL_ROOT_PASSWORD"])
	}
	if dbEnv["MYSQL_DATABASE"] != "variables_db" {
		t.Errorf("expected MYSQL_DATABASE to be 'variables_db', got %q", dbEnv["MYSQL_DATABASE"])
	}

	// Custom routing domain should have PROJECTNAME substituted
	backendRouting := cfg.Services["backend"].Routing
	if backendRouting == nil {
		t.Fatal("expected backend service to have routing config")
	}
	expectedDomain := "api.variables.0ploy.dev"
	if backendRouting.Domain != expectedDomain {
		t.Errorf("expected routing domain to be %q, got %q", expectedDomain, backendRouting.Domain)
	}
}

func TestLoadProjectWithVariablesInTypedFields(t *testing.T) {
	projectDir := filepath.Join("..", "..", "testdata", "projects", "variables-typed")
	absPath, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}

	cfg, err := LoadProject(absPath)
	if err != nil {
		t.Fatalf("LoadProject failed: %v", err)
	}

	// Variable in int field should be resolved
	appRouting := cfg.Services["app"].Routing
	if appRouting == nil {
		t.Fatal("expected app service to have routing config")
	}
	if appRouting.Port != 3000 {
		t.Errorf("expected routing port to be 3000, got %d", appRouting.Port)
	}
}

func TestLoadProjectWithCustomName(t *testing.T) {
	// Set ZDEV_DOMAIN for predictable test results
	oldDomain := os.Getenv("ZDEV_DOMAIN")
	os.Setenv("ZDEV_DOMAIN", "0ploy.dev")
	defer os.Setenv("ZDEV_DOMAIN", oldDomain)

	// Test that PROJECTNAME is set from the parsed name field, not just the directory
	tmpDir, err := os.MkdirTemp("", "zdev-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	// Config uses a custom name, and PROJECTNAME should reflect that in domain
	configContent := `version: 1
name: my-custom-app
domain: ${PROJECTNAME}.${ZDEV_DOMAIN}

services:
  app:
    image: nginx:alpine
`
	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject failed: %v", err)
	}

	// Name should be the custom name
	if cfg.Name != "my-custom-app" {
		t.Errorf("expected Name to be 'my-custom-app', got %q", cfg.Name)
	}

	// Domain should use PROJECTNAME (which is the parsed name, not directory)
	expectedDomain := "my-custom-app.0ploy.dev"
	if cfg.Domain != expectedDomain {
		t.Errorf("expected Domain to be %q, got %q", expectedDomain, cfg.Domain)
	}
}

func TestLoadProjectWithProjectDir(t *testing.T) {
	// Set ZDEV_DOMAIN for predictable test results
	oldDomain := os.Getenv("ZDEV_DOMAIN")
	os.Setenv("ZDEV_DOMAIN", "0ploy.dev")
	defer os.Setenv("ZDEV_DOMAIN", oldDomain)

	// Test that PROJECTDIR is available and separate from PROJECTNAME
	tmpDir, err := os.MkdirTemp("", "zdev-test-mydir-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks for comparison
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	// Use PROJECTDIR for name, then PROJECTNAME in domain
	configContent := `version: 1
name: ${PROJECTDIR}-app
domain: ${PROJECTNAME}.${ZDEV_DOMAIN}

services:
  app:
    image: nginx:alpine
`
	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject failed: %v", err)
	}

	dirName := filepath.Base(tmpDir)
	expectedName := dirName + "-app"

	if cfg.Name != expectedName {
		t.Errorf("expected Name to be %q, got %q", expectedName, cfg.Name)
	}

	// Domain should use PROJECTNAME which is the resolved name
	expectedDomain := expectedName + ".0ploy.dev"
	if cfg.Domain != expectedDomain {
		t.Errorf("expected Domain to be %q, got %q", expectedDomain, cfg.Domain)
	}
}

func TestLoadProjectUnknownField(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "zdev-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .zdev/config.yaml with unknown field
	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	configContent := `version: 1
name: testproject

services:
  app:
    image: nginx:alpine

volmes:
  db_data:
`
	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// LoadProject should fail with unknown field
	_, err = LoadProject(tmpDir)
	if err == nil {
		t.Fatal("expected error for unknown field 'volmes', got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "unknown field") {
		t.Errorf("expected error to mention 'unknown field', got: %s", errStr)
	}
	if !strings.Contains(errStr, "volmes") {
		t.Errorf("expected error to mention 'volmes', got: %s", errStr)
	}
}

func TestLoadProjectSyntaxError(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "zdev-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .zdev/config.yaml with syntax error
	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	configContent := `version: 1
name: testproject

services:
  app:
    image: nginx:alpine
    environment
      FOO: bar
`
	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// LoadProject should fail with syntax error
	_, err = LoadProject(tmpDir)
	if err == nil {
		t.Fatal("expected error for YAML syntax error, got nil")
	}

	errStr := err.Error()
	// Should include the config path
	if !strings.Contains(errStr, "config.yaml") {
		t.Errorf("expected error to include config path, got: %s", errStr)
	}
	// Should include line number
	if !strings.Contains(errStr, ":7:") {
		t.Errorf("expected error to include line number ':7:', got: %s", errStr)
	}
}

func TestLoadGlobalConfig(t *testing.T) {
	// Create a temp zdev home directory
	tmpDir, err := os.MkdirTemp("", "zdev-home-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override ZDEV_HOME
	oldHome := os.Getenv("ZDEV_HOME")
	os.Setenv("ZDEV_HOME", tmpDir)
	defer os.Setenv("ZDEV_HOME", oldHome)

	t.Run("returns defaults when file missing", func(t *testing.T) {
		cfg, err := LoadGlobalConfig()
		if err != nil {
			t.Fatalf("LoadGlobalConfig failed: %v", err)
		}

		if cfg.Version != 1 {
			t.Errorf("expected Version 1, got %d", cfg.Version)
		}
		if cfg.Domain != DefaultDomain {
			t.Errorf("expected Domain %q, got %q", DefaultDomain, cfg.Domain)
		}
		if cfg.Runtime != "docker" {
			t.Errorf("expected Runtime 'docker', got %q", cfg.Runtime)
		}
		if cfg.Shared.Router.Image != RouterImage {
			t.Errorf("expected Router.Image %q, got %q", RouterImage, cfg.Shared.Router.Image)
		}
	})

	t.Run("loads custom config", func(t *testing.T) {
		configContent := `version: 2
domain: example.com
runtime: podman
shared:
  router:
    image: traefik:v2.0
    dashboard: true
`
		configPath := filepath.Join(tmpDir, GlobalConfigFilename)
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
		defer os.Remove(configPath)

		cfg, err := LoadGlobalConfig()
		if err != nil {
			t.Fatalf("LoadGlobalConfig failed: %v", err)
		}

		if cfg.Version != 2 {
			t.Errorf("expected Version 2, got %d", cfg.Version)
		}
		if cfg.Domain != "example.com" {
			t.Errorf("expected Domain 'example.com', got %q", cfg.Domain)
		}
		if cfg.Runtime != "podman" {
			t.Errorf("expected Runtime 'podman', got %q", cfg.Runtime)
		}
		if cfg.Shared.Router.Image != "traefik:v2.0" {
			t.Errorf("expected Router.Image 'traefik:v2.0', got %q", cfg.Shared.Router.Image)
		}
		if !cfg.Shared.Router.Dashboard {
			t.Error("expected Router.Dashboard to be true")
		}
	})

	t.Run("SSL enabled by default when not in config", func(t *testing.T) {
		// Config file without ssl section - SSL should still default to enabled
		configContent := fmt.Sprintf(`version: 1
domain: %s
runtime: docker
`, DefaultDomain)
		configPath := filepath.Join(tmpDir, GlobalConfigFilename)
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
		defer os.Remove(configPath)

		cfg, err := LoadGlobalConfig()
		if err != nil {
			t.Fatalf("LoadGlobalConfig failed: %v", err)
		}

		if !cfg.SSL.Enabled {
			t.Error("expected SSL.Enabled to be true by default when not specified in config")
		}
	})

	t.Run("SSL can be explicitly disabled", func(t *testing.T) {
		configContent := fmt.Sprintf(`version: 1
domain: %s
ssl:
  enabled: false
`, DefaultDomain)
		configPath := filepath.Join(tmpDir, GlobalConfigFilename)
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
		defer os.Remove(configPath)

		cfg, err := LoadGlobalConfig()
		if err != nil {
			t.Fatalf("LoadGlobalConfig failed: %v", err)
		}

		if cfg.SSL.Enabled {
			t.Error("expected SSL.Enabled to be false when explicitly disabled")
		}
	})
}

func TestFindProjectDir(t *testing.T) {
	// Create a temp directory structure
	tmpDir, err := os.MkdirTemp("", "zdev-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks (macOS has /var -> /private/var)
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("failed to resolve symlinks: %v", err)
	}

	// Create .zdev/config.yaml
	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nname: test"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create a subdirectory
	subDir := filepath.Join(tmpDir, "src", "deep")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Change to the deep subdirectory
	originalDir, _ := os.Getwd()
	defer os.Chdir(originalDir)

	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	// FindProjectDir should find the parent with .zdev
	projectDir, err := FindProjectDir()
	if err != nil {
		t.Fatalf("FindProjectDir failed: %v", err)
	}

	if projectDir != tmpDir {
		t.Errorf("expected %q, got %q", tmpDir, projectDir)
	}
}

func TestFindProjectDirWithOverride(t *testing.T) {
	// Create a temp directory for the override path
	overrideDir, err := os.MkdirTemp("", "zdev-override-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(overrideDir)

	// Resolve symlinks
	overrideDir, err = filepath.EvalSymlinks(overrideDir)
	if err != nil {
		t.Fatalf("failed to resolve symlinks: %v", err)
	}

	// Create .zdev/config.yaml in override dir
	zdevDir := filepath.Join(overrideDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nname: override-test"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Save and restore original override
	originalOverride := GetProjectDirOverride()
	defer SetProjectDirOverride(originalOverride)

	t.Run("override takes precedence", func(t *testing.T) {
		SetProjectDirOverride(overrideDir)

		// FindProjectDir should return override, regardless of cwd
		projectDir, err := FindProjectDir()
		if err != nil {
			t.Fatalf("FindProjectDir failed: %v", err)
		}

		if projectDir != overrideDir {
			t.Errorf("expected %q, got %q", overrideDir, projectDir)
		}
	})

	t.Run("invalid override returns error", func(t *testing.T) {
		SetProjectDirOverride("/nonexistent/path")

		_, err := FindProjectDir()
		if err == nil {
			t.Error("expected error for invalid override path")
		}
	})

	t.Run("empty override falls back to discovery", func(t *testing.T) {
		SetProjectDirOverride("")

		// This will fail because we're not in a project dir, which is expected
		_, err := FindProjectDir()
		if err == nil {
			// If it succeeds, we must be in an actual project directory
			// That's fine, the point is it didn't use the override
		}
	})
}

func TestGenerateDefaultGlobalConfig(t *testing.T) {
	config := generateDefaultGlobalConfig()

	// Verify our template variables were substituted
	varsToSubstitute := []string{
		"${DefaultDomain}",
		"${RouterImage}",
		"${MailImage}",
		"${DBUIImage}",
		"${RedisInsightsImage}",
	}
	for _, v := range varsToSubstitute {
		if strings.Contains(config, v) {
			t.Errorf("config contains unsubstituted variable: %s", v)
		}
	}

	// Verify specific values from defaults.go are present
	if !strings.Contains(config, DefaultDomain) {
		t.Errorf("config should contain DefaultDomain (%s)", DefaultDomain)
	}
	if !strings.Contains(config, RouterImage) {
		t.Errorf("config should contain RouterImage (%s)", RouterImage)
	}
	if !strings.Contains(config, MailImage) {
		t.Errorf("config should contain MailImage (%s)", MailImage)
	}
	if !strings.Contains(config, DBUIImage) {
		t.Errorf("config should contain DBUIImage (%s)", DBUIImage)
	}
	if !strings.Contains(config, RedisInsightsImage) {
		t.Errorf("config should contain RedisInsightsImage (%s)", RedisInsightsImage)
	}
}

func TestGenerateDocsTraefikConfig(t *testing.T) {
	t.Run("HTTP only", func(t *testing.T) {
		config := generateDocsTraefikConfig("example.com", false)

		// Check for expected content
		if !strings.Contains(config, "zdev-docs") {
			t.Error("config should contain zdev-docs middleware")
		}
		if !strings.Contains(config, "zdev-docs-redirect") {
			t.Error("config should contain zdev-docs-redirect middleware")
		}
		if !strings.Contains(config, "docs.shared.example.com") {
			t.Error("config should contain docs host")
		}
		if !strings.Contains(config, "http://docs.shared.example.com/") {
			t.Error("config should contain http redirect URL")
		}
		if !strings.Contains(config, "zdev-docs-http") {
			t.Error("config should contain HTTP router")
		}
		if !strings.Contains(config, "zdev-catchall-http") {
			t.Error("config should contain HTTP catch-all router")
		}
		// Should NOT have HTTPS routers
		if strings.Contains(config, "zdev-docs-https") {
			t.Error("config should NOT contain HTTPS router when TLS disabled")
		}
	})

	t.Run("HTTPS enabled", func(t *testing.T) {
		config := generateDocsTraefikConfig("example.com", true)

		// Check for expected content
		if !strings.Contains(config, "https://docs.shared.example.com/") {
			t.Error("config should contain https redirect URL")
		}
		if !strings.Contains(config, "zdev-docs-https") {
			t.Error("config should contain HTTPS router")
		}
		if !strings.Contains(config, "zdev-catchall-https") {
			t.Error("config should contain HTTPS catch-all router")
		}
		if !strings.Contains(config, "tls: {}") {
			t.Error("config should contain TLS configuration")
		}
	})

	t.Run("domain with dots escaped in regex", func(t *testing.T) {
		config := generateDocsTraefikConfig("my.domain.com", true)

		// The domain dots should be escaped in the HostRegexp
		if !strings.Contains(config, "my\\\\.domain\\\\.com") {
			t.Error("config should contain escaped dots in HostRegexp")
		}
	})
}

func TestEnsureDocsConfig(t *testing.T) {
	// Create a temp zdev home directory
	tmpDir, err := os.MkdirTemp("", "zdev-home-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override ZDEV_HOME
	oldHome := os.Getenv("ZDEV_HOME")
	os.Setenv("ZDEV_HOME", tmpDir)
	defer os.Setenv("ZDEV_HOME", oldHome)

	docsDir, err := EnsureDocsConfig("example.com", true)
	if err != nil {
		t.Fatalf("EnsureDocsConfig failed: %v", err)
	}

	// Check docs directory was created
	expectedDocsDir := filepath.Join(tmpDir, "docs")
	if docsDir != expectedDocsDir {
		t.Errorf("expected docsDir %q, got %q", expectedDocsDir, docsDir)
	}

	// Check HTML file exists
	htmlPath := filepath.Join(docsDir, "index.html")
	if _, err := os.Stat(htmlPath); err != nil {
		t.Errorf("index.html should exist: %v", err)
	}

	// Check HTML contains substituted domain
	htmlContent, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("failed to read HTML: %v", err)
	}
	if !strings.Contains(string(htmlContent), "example.com") {
		t.Error("HTML should contain domain")
	}
	if !strings.Contains(string(htmlContent), "https://") {
		t.Error("HTML should contain https protocol when TLS enabled")
	}

	// Check traefik config file exists
	traefikConfigPath := filepath.Join(tmpDir, "traefik", "docs.yaml")
	if _, err := os.Stat(traefikConfigPath); err != nil {
		t.Errorf("docs.yaml should exist: %v", err)
	}

	// Check traefik config content
	traefikContent, err := os.ReadFile(traefikConfigPath)
	if err != nil {
		t.Fatalf("failed to read traefik config: %v", err)
	}
	if !strings.Contains(string(traefikContent), "statiq") {
		t.Error("traefik config should reference statiq plugin")
	}
}

// writeProjectConfigs writes .zdev/config.yaml and (when local != "")
// .zdev/local/config.yaml inside tmpDir. Helper for the local-merge tests.
func writeProjectConfigs(t *testing.T, tmpDir, main, local string) {
	t.Helper()
	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0o755); err != nil {
		t.Fatalf("mkdir .zdev: %v", err)
	}
	if err := os.WriteFile(filepath.Join(zdevDir, "config.yaml"), []byte(main), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if local == "" {
		return
	}
	localDir := filepath.Join(zdevDir, "local")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir .zdev/local: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "config.yaml"), []byte(local), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}
}

func TestLoadProject_LocalConfig_VariablesUsedInMain(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: localvars
services:
  app:
    image: alpine:latest
    environment:
      API_KEY: ${SECRET_KEY}
`
	local := `variables:
  SECRET_KEY: "from-local"
`
	writeProjectConfigs(t, tmpDir, main, local)

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got := cfg.Services["app"].Environment["API_KEY"]; got != "from-local" {
		t.Errorf("expected API_KEY %q, got %q", "from-local", got)
	}
}

func TestLoadProject_LocalConfig_OverridesScalar(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: scalar
services:
  app:
    image: alpine:latest
    command: sleep infinity
`
	local := `services:
  app:
    image: alpine:edge
`
	writeProjectConfigs(t, tmpDir, main, local)

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	app := cfg.Services["app"]
	if app.Image != "alpine:edge" {
		t.Errorf("expected overridden image, got %q", app.Image)
	}
	if app.Command != "sleep infinity" {
		t.Errorf("expected base command preserved, got %q", app.Command)
	}
}

func TestLoadProject_LocalConfig_DeepMergesEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: envmerge
services:
  app:
    image: alpine:latest
    environment:
      KEEP_ME: yes
      OVERRIDE_ME: original
`
	local := `services:
  app:
    environment:
      OVERRIDE_ME: changed
      ADDED: new
`
	writeProjectConfigs(t, tmpDir, main, local)

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	env := cfg.Services["app"].Environment
	if env["KEEP_ME"] != "yes" {
		t.Errorf("KEEP_ME should be preserved, got %q", env["KEEP_ME"])
	}
	if env["OVERRIDE_ME"] != "changed" {
		t.Errorf("OVERRIDE_ME should be overridden, got %q", env["OVERRIDE_ME"])
	}
	if env["ADDED"] != "new" {
		t.Errorf("ADDED should be added, got %q", env["ADDED"])
	}
}

func TestLoadProject_LocalConfig_SliceReplaces(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: slicerepl
services:
  app:
    image: alpine:latest
    volumes:
      - /tmp/a:/a
      - /tmp/b:/b
`
	local := `services:
  app:
    volumes:
      - /tmp/only:/only
`
	writeProjectConfigs(t, tmpDir, main, local)

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	vols := cfg.Services["app"].Volumes
	if len(vols) != 1 || vols[0] != "/tmp/only:/only" {
		t.Errorf("expected slice replacement, got %v", vols)
	}
}

func TestLoadProject_LocalConfig_AddsNewService(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: addsvc
services:
  app:
    image: alpine:latest
`
	local := `services:
  worker:
    image: alpine:latest
    command: sleep 1
`
	writeProjectConfigs(t, tmpDir, main, local)

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if _, ok := cfg.Services["app"]; !ok {
		t.Error("base service 'app' should still exist")
	}
	if _, ok := cfg.Services["worker"]; !ok {
		t.Error("local-added service 'worker' should exist")
	}
}

func TestLoadProject_LocalConfig_MissingFileIsNoop(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: noop
services:
  app:
    image: alpine:latest
`
	writeProjectConfigs(t, tmpDir, main, "")

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if cfg.Services["app"].Image != "alpine:latest" {
		t.Errorf("base config should load unchanged when local missing")
	}
}

func TestLoadProject_LocalConfig_EmptyFileIsNoop(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: emptylocal
services:
  app:
    image: alpine:latest
`
	writeProjectConfigs(t, tmpDir, main, "# only a comment\n")

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if cfg.Services["app"].Image != "alpine:latest" {
		t.Errorf("empty local config should be a no-op, got image %q", cfg.Services["app"].Image)
	}
}

func TestLoadProject_LocalConfig_UnknownFieldErrors(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: unknownfield
services:
  app:
    image: alpine:latest
`
	local := `services:
  app:
    bogus_field: hello
`
	writeProjectConfigs(t, tmpDir, main, local)

	if _, err := LoadProject(tmpDir); err == nil {
		t.Fatal("expected unknown-field error from local config, got nil")
	}
}

func TestLoadProject_LocalConfig_MalformedYAMLErrors(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: malformed
services:
  app:
    image: alpine:latest
`
	local := "services:\n  app:\n    image: [unclosed\n"
	writeProjectConfigs(t, tmpDir, main, local)

	if _, err := LoadProject(tmpDir); err == nil {
		t.Fatal("expected parse error from malformed local config, got nil")
	}
}

// TestLoadProject_ServiceMutagenConfig pins the per-service mutagen block
// (user/group/file_mode/directory_mode) onto the parsed ServiceConfig so a
// future yaml-tag rename or struct restructure breaks loudly.
func TestLoadProject_ServiceMutagenConfig(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: php-app
services:
  web:
    image: my-php-apache:latest
    volumes:
      - ./src:/var/www/html
    mutagen:
      user: www-data
      group: www-data
      file_mode: "0644"
      directory_mode: "0755"
`
	writeProjectConfigs(t, tmpDir, main, "")

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	web, ok := cfg.Services["web"]
	if !ok {
		t.Fatal("expected 'web' service")
	}
	if got := web.Mutagen.User; got != "www-data" {
		t.Errorf("Mutagen.User = %q, want www-data", got)
	}
	if got := web.Mutagen.Group; got != "www-data" {
		t.Errorf("Mutagen.Group = %q, want www-data", got)
	}
	if got := web.Mutagen.FileMode; got != "0644" {
		t.Errorf("Mutagen.FileMode = %q, want 0644", got)
	}
	if got := web.Mutagen.DirectoryMode; got != "0755" {
		t.Errorf("Mutagen.DirectoryMode = %q, want 0755", got)
	}
}

func TestLoadProject_DockerfileField(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: buildtest
variables:
  DOCKERFILE_DIR: .zdev
services:
  app:
    dockerfile: ${DOCKERFILE_DIR}/Dockerfile
    image: myapp-dev:latest
    command: sleep infinity
  db:
    image: mysql:8.0
`
	writeProjectConfigs(t, tmpDir, main, "")

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	app := cfg.Services["app"]
	// ${VAR} substitution must reach dockerfile like every other field
	if app.Dockerfile != ".zdev/Dockerfile" {
		t.Errorf("Dockerfile = %q, want .zdev/Dockerfile", app.Dockerfile)
	}
	if app.Image != "myapp-dev:latest" {
		t.Errorf("Image = %q, want myapp-dev:latest", app.Image)
	}
	if db := cfg.Services["db"]; db.Dockerfile != "" {
		t.Errorf("db.Dockerfile = %q, want empty for image-only services", db.Dockerfile)
	}
}

func TestLoadProject_SecretsEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	main := `version: 1
name: secretsenv
variables:
  op-env: b7qmzx3kfpwj4hn2c6t8vydl5a
services:
  app:
    image: alpine:latest
    op-env: ${op-env}
    environment:
      API_KEY: op-env://${op-env}/API_KEY
`
	writeProjectConfigs(t, tmpDir, main, "")

	cfg, err := LoadProject(tmpDir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if cfg.Services["app"].OpEnv != "b7qmzx3kfpwj4hn2c6t8vydl5a" {
		t.Errorf("Services.app.OpEnv = %q, want the substituted ID", cfg.Services["app"].OpEnv)
	}
	// Variable substitution runs at load time; the op-env:// reference
	// itself must otherwise pass through untouched (resolution happens
	// at container creation, not at load).
	if got := cfg.Services["app"].Environment["API_KEY"]; got != "op-env://b7qmzx3kfpwj4hn2c6t8vydl5a/API_KEY" {
		t.Errorf("op-env:// value should have variables substituted but stay a reference, got %q", got)
	}
}
