package project

import (
	"context"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
)

const (
	testSecretsEnvID = "b7qmzx3kfpwj4hn2c6t8vydl5a"
	testRef          = "op-env://API_KEY"
	testRefValue     = "resolved-secret-value"
)

// newSecretsProject builds a test project wired to a 1Password
// Environment whose app service has one op-env:// reference in env,
// backed by a populated mock resolver.
func newSecretsProject(t *testing.T) (*Project, *runtime.MockRuntime, *secrets.Mock) {
	t.Helper()
	cleanup := setupTestEnv(t)
	t.Cleanup(cleanup)

	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	p.Config.Secrets = config.ProjectSecretsConfig{OpEnv: testSecretsEnvID}
	svc := p.Config.Services["app"]
	svc.Environment = map[string]string{
		"API_URL": "https://example.com/api",
		"API_KEY": testRef,
	}
	p.Config.Services["app"] = svc

	resolver := &secrets.Mock{Values: map[string]string{"API_KEY": testRefValue}}
	p.Secrets = resolver

	mock.NetworksExist[p.NetworkName()] = true
	mock.VolumesExist[p.VolumeName("data")] = true
	mock.ImagesExist["alpine:latest"] = true

	return p, mock, resolver
}

// seedCreatedWithSecrets seeds the app container exactly as a real
// creation would have: config hash over unresolved refs, env resolved,
// secrets hash stamped.
func seedCreatedWithSecrets(t *testing.T, p *Project, mock *runtime.MockRuntime) {
	t.Helper()
	svc := p.Config.Services["app"]
	cfg := p.buildContainerConfig("app", svc, false, nil)
	if err := p.resolveSecretEnv(context.Background(), "app", &cfg); err != nil {
		t.Fatalf("resolveSecretEnv: %v", err)
	}
	mock.Containers[cfg.Name] = cfg
	mock.ContainersExist[cfg.Name] = true
	mock.ContainersRunning[cfg.Name] = true
}

func TestUpdate_CreatesWithResolvedSecrets(t *testing.T) {
	p, mock, resolver := newSecretsProject(t)

	// Expected config hash BEFORE resolution: over the unresolved ref.
	expectedHash := p.buildContainerConfig("app", p.Config.Services["app"], false, nil).Labels[runtime.ConfigHashLabel]

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if !updated {
		t.Error("expected Update to create the service")
	}

	created := mock.Containers[p.ContainerName("app")]
	if created.Env["API_KEY"] != testRefValue {
		t.Errorf("API_KEY = %q, want resolved value %q", created.Env["API_KEY"], testRefValue)
	}
	if created.Env["API_URL"] != "https://example.com/api" {
		t.Errorf("non-ref env value changed: %q", created.Env["API_URL"])
	}
	if created.Labels[runtime.ConfigHashLabel] != expectedHash {
		t.Error("config hash must cover the UNRESOLVED reference, not the secret value")
	}
	if created.Labels[runtime.SecretsHashLabel] == "" {
		t.Error("secrets hash label missing on created container")
	}
	if len(resolver.Calls) == 0 || resolver.Calls[0].EnvID != testSecretsEnvID {
		t.Errorf("resolver should be called with the configured environment ID, calls: %v", resolver.Calls)
	}
}

func TestUpdate_NoRecreateOnSecretRotation(t *testing.T) {
	p, mock, _ := newSecretsProject(t)
	seedCreatedWithSecrets(t, p, mock)

	// Rotate the secret in "1Password" and use a fresh resolver so any
	// resolution during Update is observable.
	resolver := &secrets.Mock{Values: map[string]string{"API_KEY": "rotated-value"}}
	p.Secrets = resolver

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated {
		t.Error("rotation must not trigger recreate without --refresh-secrets")
	}
	if resolver.CallCount() != 0 {
		t.Errorf("resolver called %d times on the compare path, want 0", resolver.CallCount())
	}
}

func TestUpdate_RecreatesOnSecretRefChange(t *testing.T) {
	p, mock, resolver := newSecretsProject(t)
	seedCreatedWithSecrets(t, p, mock)

	svc := p.Config.Services["app"]
	svc.Environment["API_KEY"] = "op-env://OTHER_API_KEY"
	p.Config.Services["app"] = svc
	resolver.Values["OTHER_API_KEY"] = "other-value"

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if !updated {
		t.Error("changing the reference string must trigger recreate")
	}
	created := mock.Containers[p.ContainerName("app")]
	if created.Env["API_KEY"] != "other-value" {
		t.Errorf("API_KEY = %q, want %q", created.Env["API_KEY"], "other-value")
	}
}

func TestUpdate_RefreshSecretsRecreatesOnRotation(t *testing.T) {
	p, mock, _ := newSecretsProject(t)
	seedCreatedWithSecrets(t, p, mock)
	oldSecretsHash := mock.Containers[p.ContainerName("app")].Labels[runtime.SecretsHashLabel]

	p.Secrets = &secrets.Mock{Values: map[string]string{"API_KEY": "rotated-value"}}
	p.RefreshSecrets = true

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if !updated {
		t.Error("--refresh-secrets must recreate when values rotated")
	}
	created := mock.Containers[p.ContainerName("app")]
	if created.Env["API_KEY"] != "rotated-value" {
		t.Errorf("API_KEY = %q, want rotated value", created.Env["API_KEY"])
	}
	if created.Labels[runtime.SecretsHashLabel] == oldSecretsHash {
		t.Error("secrets hash should be restamped after rotation")
	}
}

func TestUpdate_RefreshSecretsUnchangedNoRecreate(t *testing.T) {
	p, mock, resolver := newSecretsProject(t)
	seedCreatedWithSecrets(t, p, mock)

	p.RefreshSecrets = true

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated {
		t.Error("--refresh-secrets must not recreate when values are unchanged")
	}
	if resolver.CallCount() == 0 {
		t.Error("--refresh-secrets should have resolved fresh values for comparison")
	}
	if mock.CallCount("RemoveContainer") != 0 {
		t.Errorf("RemoveContainer called %d times, want 0", mock.CallCount("RemoveContainer"))
	}
}

func TestUpdate_RefreshSecretsRecreatesPreFeatureContainer(t *testing.T) {
	p, mock, _ := newSecretsProject(t)

	// Seed WITHOUT resolveSecretEnv: simulates a container created
	// before secret support (raw ref in env, no secrets hash label).
	svc := p.Config.Services["app"]
	cfg := p.buildContainerConfig("app", svc, false, nil)
	mock.Containers[cfg.Name] = cfg
	mock.ContainersExist[cfg.Name] = true
	mock.ContainersRunning[cfg.Name] = true

	p.RefreshSecrets = true

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if !updated {
		t.Error("--refresh-secrets must recreate containers without a secrets hash label")
	}
	created := mock.Containers[p.ContainerName("app")]
	if created.Env["API_KEY"] != testRefValue {
		t.Errorf("API_KEY = %q, want resolved value", created.Env["API_KEY"])
	}
}

func TestUpdate_SecretResolveErrorAborts(t *testing.T) {
	p, mock, resolver := newSecretsProject(t)
	resolver.Err = context.DeadlineExceeded
	resolver.Values = nil

	_, err := p.Update(context.Background())
	if err == nil {
		t.Fatal("expected Update to fail on resolution error")
	}
	for _, want := range []string{"app", "API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if mock.CallCount("CreateContainer") != 0 {
		t.Errorf("CreateContainer called %d times after resolve failure, want 0", mock.CallCount("CreateContainer"))
	}
}

func TestUpdate_MissingEnvironmentIDError(t *testing.T) {
	p, mock, _ := newSecretsProject(t)
	p.Config.Secrets.OpEnv = ""

	_, err := p.Update(context.Background())
	if err == nil {
		t.Fatal("expected Update to fail when refs are used without secrets.op-env")
	}
	if !strings.Contains(err.Error(), "secrets.op-env") {
		t.Errorf("error should point at the missing config field: %v", err)
	}
	if mock.CallCount("CreateContainer") != 0 {
		t.Errorf("CreateContainer called %d times, want 0", mock.CallCount("CreateContainer"))
	}
}

func TestUpdate_ProjectLevelRefResolvedForAllServices(t *testing.T) {
	p, mock, _ := newSecretsProject(t)

	// Move the ref to project-level env and add a second service.
	svc := p.Config.Services["app"]
	svc.Environment = nil
	p.Config.Services["app"] = svc
	p.Config.Services["worker"] = config.ServiceConfig{Image: "alpine:latest", Command: "sleep infinity"}
	p.Config.Environment = map[string]string{"API_KEY": testRef}

	updated, err := p.Update(context.Background())
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if !updated {
		t.Error("expected Update to create services")
	}
	for _, name := range []string{"app", "worker"} {
		created := mock.Containers[p.ContainerName(name)]
		if created.Env["API_KEY"] != testRefValue {
			t.Errorf("service %s: API_KEY = %q, want resolved value", name, created.Env["API_KEY"])
		}
	}
}

func TestComputeConfigHash_ExcludesSecretsHashLabel(t *testing.T) {
	cfg := baseCfg()
	h1 := runtime.ComputeConfigHash(cfg)

	cfg.Labels[runtime.SecretsHashLabel] = "some-secrets-hash"
	h2 := runtime.ComputeConfigHash(cfg)

	if h1 != h2 {
		t.Errorf("hash must ignore runtime.SecretsHashLabel: %s vs %s", h1, h2)
	}
}

func TestEnvSecretKeys(t *testing.T) {
	keys := envSecretKeys(map[string]string{
		"A": "op-env://API_KEY",
		"B": "op-env://API_KEY",
		"C": "op-env://DB_PASS",
		"D": "plain-value",
	})
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2 (deduplicated): %v", len(keys), keys)
	}
	if keys[0] != "API_KEY" || keys[1] != "DB_PASS" {
		t.Errorf("keys not sorted/deduped: %v", keys)
	}
}
