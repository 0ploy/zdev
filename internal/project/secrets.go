package project

import (
	"context"
	"fmt"
	"sort"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
)

// resolver returns the project's secret resolver, falling back to the
// 1Password CLI for directly constructed Project values.
func (p *Project) resolver() secrets.Resolver {
	if p.Secrets == nil {
		p.Secrets = secrets.NewOnePasswordCLI()
	}
	return p.Secrets
}

// envSecretKeys returns the sorted unique 1Password Environment variable
// names referenced by op-env:// env values.
func envSecretKeys(env map[string]string) []string {
	seen := make(map[string]bool)
	var keys []string
	for _, val := range env {
		if !secrets.IsRef(val) {
			continue
		}
		key := secrets.Key(val)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// serviceUsesSecretRefs reports whether a service's effective env
// (project-level env merged with service overrides, mirroring
// buildContainerConfig) contains op-env:// references.
func serviceUsesSecretRefs(projectEnv map[string]string, svc config.ServiceConfig) bool {
	return len(envSecretKeys(mergedServiceEnv(projectEnv, svc))) > 0
}

// mergedServiceEnv applies the same project-then-service env merge as
// buildContainerConfig, without the injected USER_ID/GROUP_ID.
func mergedServiceEnv(projectEnv map[string]string, svc config.ServiceConfig) map[string]string {
	env := make(map[string]string, len(projectEnv)+len(svc.Environment))
	for k, v := range projectEnv {
		env[k] = v
	}
	for k, v := range svc.Environment {
		env[k] = v
	}
	return env
}

// secretsEnvironmentID returns the configured 1Password Environment ID,
// or an actionable error when references are used without one.
func (p *Project) secretsEnvironmentID() (string, error) {
	if p.Config.Secrets.OpEnv == "" {
		return "", fmt.Errorf("config uses op-env:// secret references but secrets.op-env is not set\n\nAdd the 1Password Environment ID to .zdev/config.yaml:\n\nsecrets:\n  op-env: <environment-id>\n\nFind the ID in the 1Password app: Developer > Environments > Manage environment > Copy environment ID")
	}
	return p.Config.Secrets.OpEnv, nil
}

// resolveSecretEnv replaces op-env:// references in cfg.Env with values
// from the project's 1Password Environment and stamps SecretsHashLabel
// over them. Must run AFTER StampConfigHash (so the config hash covers
// the unresolved references and the compare path stays offline) and
// before CreateContainer.
func (p *Project) resolveSecretEnv(ctx context.Context, serviceName string, cfg *runtime.ContainerConfig) error {
	keys := envSecretKeys(cfg.Env)
	if len(keys) == 0 {
		return nil
	}

	envID, err := p.secretsEnvironmentID()
	if err != nil {
		return err
	}

	values, err := p.resolver().Resolve(ctx, envID, keys)
	if err != nil {
		var envKeys []string
		for k, v := range cfg.Env {
			if secrets.IsRef(v) {
				envKeys = append(envKeys, k)
			}
		}
		sort.Strings(envKeys)
		return fmt.Errorf("failed to resolve 1Password secrets for service %s (env: %v): %w", serviceName, envKeys, err)
	}

	for key, val := range cfg.Env {
		if !secrets.IsRef(val) {
			continue
		}
		if resolved, ok := values[secrets.Key(val)]; ok {
			cfg.Env[key] = resolved
		}
	}

	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	cfg.Labels[runtime.SecretsHashLabel] = secrets.HashValues(values)

	return nil
}

// serviceSecretsStale reports whether a service's secrets rotated since
// its container was created, by resolving fresh values and comparing
// their hash against the stamped SecretsHashLabel. Only called from the
// --refresh-secrets path; the regular update compare never resolves.
func (p *Project) serviceSecretsStale(ctx context.Context, serviceName string, svc config.ServiceConfig) (bool, error) {
	keys := envSecretKeys(mergedServiceEnv(p.Config.Environment, svc))
	if len(keys) == 0 {
		return false, nil
	}

	envID, err := p.secretsEnvironmentID()
	if err != nil {
		return false, err
	}

	values, err := p.resolver().Resolve(ctx, envID, keys)
	if err != nil {
		return false, fmt.Errorf("failed to resolve 1Password secrets for service %s: %w", serviceName, err)
	}

	labels, err := p.Runtime.GetContainerLabels(ctx, p.ContainerName(serviceName))
	if err != nil {
		return false, err
	}

	stamped, ok := labels[runtime.SecretsHashLabel]
	if !ok {
		// Container predates secret support (or was created before refs
		// were added); recreate so it picks up resolved values.
		return true, nil
	}

	return stamped != secrets.HashValues(values), nil
}
