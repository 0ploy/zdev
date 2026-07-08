package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// KeyPrefix marks an env value as a reference to a variable in a
// 1Password Environment: op-env://<environment-id>/<VARIABLE>. The
// reference is self-contained (it carries its Environment ID), so one
// project can mix variables from multiple Environments. Whole-Environment
// injection uses the services.<name>.op-env config field instead.
const KeyPrefix = "op-env://"

// IsRef reports whether an env value references a 1Password Environment
// variable.
func IsRef(value string) bool {
	return strings.HasPrefix(value, KeyPrefix)
}

// ParseRef splits an op-env:// reference into its Environment ID and
// variable name. The full form op-env://<environment-id>/<VARIABLE> is
// required.
func ParseRef(ref string) (envID, key string, err error) {
	if !IsRef(ref) {
		return "", "", fmt.Errorf("not a secret reference (missing %s prefix): %s", KeyPrefix, ref)
	}
	envID, key, ok := strings.Cut(strings.TrimPrefix(ref, KeyPrefix), "/")
	if !ok || envID == "" || key == "" {
		return "", "", fmt.Errorf("invalid secret reference %q: expected %s<environment-id>/<VARIABLE>", ref, KeyPrefix)
	}
	if strings.ContainsAny(envID, "=\n\r ") {
		return "", "", fmt.Errorf("invalid secret reference %q: environment ID must not contain spaces, '=' or newlines", ref)
	}
	if strings.ContainsAny(key, "=\n\r ") || strings.Contains(key, "/") {
		return "", "", fmt.Errorf("invalid secret reference %q: variable name must not contain spaces, '=', '/' or newlines", ref)
	}
	return envID, key, nil
}

// Resolver reads a 1Password Environment: the full set of its variables
// as a name -> value map. Implementations cache per Environment ID, so
// any number of references and whole-Environment injections across all
// services cost one op call per distinct Environment per process.
type Resolver interface {
	ReadEnvironment(ctx context.Context, envID string) (map[string]string, error)
}

// HashValues returns a deterministic sha256 over resolved secret values,
// keyed by container env name. Used for the zdev.secrets-hash container
// label so a later refresh can detect rotation (and added/removed
// variables) without storing the values anywhere.
func HashValues(values map[string]string) string {
	pairs := make([]string, 0, len(values))
	for key, val := range values {
		pairs = append(pairs, key+"\x00"+val)
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\x01")))
	return hex.EncodeToString(sum[:])
}
