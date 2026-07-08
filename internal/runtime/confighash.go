package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ConfigHashLabel is the label that carries a deterministic hash of the
// expected container config. Callers that recreate containers on drift
// compare this label against a freshly computed hash; any mismatch means
// the container's baked config has diverged from what the current code
// would produce, and the container must be recreated.
const ConfigHashLabel = "zdev.config-hash"

// SecretsHashLabel carries a sha256 over the resolved secret values a
// container was created with (see secrets.HashValues). It lets
// `zdev update --refresh-secrets` detect rotation without resolving on
// the regular compare path. Excluded from the config hash alongside
// ConfigHashLabel: it is stamped after resolution, which happens after
// StampConfigHash, and must not perturb drift detection.
const SecretsHashLabel = "zdev.secrets-hash"

// OpEnvLabel records the 1Password Environment attached to a container
// via services.<name>.op-env. Deliberately INCLUDED in the config hash
// (unlike SecretsHashLabel): attaching, changing, or removing the
// Environment must trigger a recreate, while its content rotating must
// not (that's `zdev update --refresh-secrets`).
const OpEnvLabel = "zdev.op-env"

// ComputeConfigHash returns a deterministic sha256 of the fields that
// define container identity for recreation purposes. The ConfigHashLabel
// itself is excluded so the hash doesn't depend on its own value.
func ComputeConfigHash(cfg ContainerConfig) string {
	envPairs := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		envPairs = append(envPairs, k+"="+v)
	}
	sort.Strings(envPairs)

	labelPairs := make([]string, 0, len(cfg.Labels))
	for k, v := range cfg.Labels {
		if k == ConfigHashLabel || k == SecretsHashLabel {
			continue
		}
		labelPairs = append(labelPairs, k+"="+v)
	}
	sort.Strings(labelPairs)

	volumes := make([]string, 0, len(cfg.Volumes))
	for _, v := range cfg.Volumes {
		volumes = append(volumes, fmt.Sprintf("%s:%s:%v", v.Source, v.Target, v.ReadOnly))
	}
	sort.Strings(volumes)

	ports := append([]string(nil), cfg.Ports...)
	sort.Strings(ports)

	aliases := append([]string(nil), cfg.Aliases...)
	sort.Strings(aliases)

	payload := struct {
		Image       string
		WorkingDir  string
		Command     []string
		Env         []string
		Labels      []string
		Volumes     []string
		Ports       []string
		NetworkName string
		Aliases     []string
	}{
		Image:       cfg.Image,
		WorkingDir:  cfg.WorkingDir,
		Command:     cfg.Command,
		Env:         envPairs,
		Labels:      labelPairs,
		Volumes:     volumes,
		Ports:       ports,
		NetworkName: cfg.NetworkName,
		Aliases:     aliases,
	}

	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// StampConfigHash computes and attaches ConfigHashLabel to cfg.Labels
// in place. Call this last, after every other label is set, so the hash
// covers the final state.
func StampConfigHash(cfg *ContainerConfig) {
	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	cfg.Labels[ConfigHashLabel] = ComputeConfigHash(*cfg)
}
