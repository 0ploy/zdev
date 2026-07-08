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

// serviceUsesSecrets reports whether the service pulls anything from
// 1Password: a whole attached Environment (op-env:) or op-env://
// references in its effective env.
func (p *Project) serviceUsesSecrets(svc config.ServiceConfig) bool {
	if svc.OpEnv != "" {
		return true
	}
	for _, v := range p.containerEnv(svc) {
		if secrets.IsRef(v) {
			return true
		}
	}
	return false
}

// secretValuesFor resolves everything the service pulls from 1Password,
// keyed by container env name: every variable of the attached
// Environment (svc.OpEnv) that baseEnv does not set explicitly, plus
// each op-env://<environment-id>/<VARIABLE> reference in baseEnv. The
// resolver caches per Environment ID, so any number of services and
// references cost one op call per distinct Environment.
func (p *Project) secretValuesFor(ctx context.Context, serviceName string, svc config.ServiceConfig, baseEnv map[string]string) (map[string]string, error) {
	resolved := make(map[string]string)

	if svc.OpEnv != "" {
		vars, err := p.resolver().ReadEnvironment(ctx, svc.OpEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to read 1Password Environment %s for service %s: %w", svc.OpEnv, serviceName, err)
		}
		for k, v := range vars {
			if _, exists := baseEnv[k]; exists {
				continue // explicit environment: entries win over injection
			}
			resolved[k] = v
		}
	}

	// Sorted iteration so warnings and errors are deterministic.
	envKeys := make([]string, 0, len(baseEnv))
	for k := range baseEnv {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	for _, envKey := range envKeys {
		val := baseEnv[envKey]
		if !secrets.IsRef(val) {
			continue
		}
		envID, varName, err := secrets.ParseRef(val)
		if err != nil {
			return nil, fmt.Errorf("service %s, env %s: %w", serviceName, envKey, err)
		}
		vars, err := p.resolver().ReadEnvironment(ctx, envID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s for service %s (env %s): %w", val, serviceName, envKey, err)
		}
		value, ok := vars[varName]
		if !ok {
			return nil, fmt.Errorf("variable %s not found in 1Password Environment %s (service %s, env %s) - check the Environment in the 1Password app (Developer > Environments)", varName, envID, serviceName, envKey)
		}
		if value == "" {
			fmt.Printf("Warning: 1Password Environment variable %s is empty (service %s, env %s)\n", varName, serviceName, envKey)
		}
		resolved[envKey] = value
	}

	return resolved, nil
}

// resolveSecretEnv injects the service's 1Password secrets into cfg.Env
// (whole attached Environment plus op-env:// references) and stamps
// SecretsHashLabel over the injected values. Must run AFTER
// StampConfigHash (so the config hash covers the unresolved references
// and the compare path stays offline) and before CreateContainer.
func (p *Project) resolveSecretEnv(ctx context.Context, serviceName string, svc config.ServiceConfig, cfg *runtime.ContainerConfig) error {
	if !p.serviceUsesSecrets(svc) {
		return nil
	}

	values, err := p.secretValuesFor(ctx, serviceName, svc, cfg.Env)
	if err != nil {
		return err
	}

	for k, v := range values {
		cfg.Env[k] = v
	}

	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	cfg.Labels[runtime.SecretsHashLabel] = secrets.HashValues(values)

	return nil
}

// prefetchSecrets resolves the service's secrets without applying them,
// warming the per-Environment cache and surfacing resolution failures
// (malformed reference, unknown variable, not signed in) BEFORE the old
// container is stopped and removed on the recreate path.
func (p *Project) prefetchSecrets(ctx context.Context, serviceName string, svc config.ServiceConfig) error {
	if !p.serviceUsesSecrets(svc) {
		return nil
	}
	_, err := p.secretValuesFor(ctx, serviceName, svc, p.containerEnv(svc))
	return err
}

// serviceSecretsStale reports whether a service's secrets changed since
// its container was created (rotated values, or variables added to or
// removed from the attached Environment), by resolving fresh values and
// comparing their hash against the stamped SecretsHashLabel. Only called
// from the --refresh-secrets path; the regular update compare never
// resolves.
func (p *Project) serviceSecretsStale(ctx context.Context, serviceName string, svc config.ServiceConfig) (bool, error) {
	if !p.serviceUsesSecrets(svc) {
		return false, nil
	}

	values, err := p.secretValuesFor(ctx, serviceName, svc, p.containerEnv(svc))
	if err != nil {
		return false, err
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
