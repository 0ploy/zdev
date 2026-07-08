package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// KeyPrefix marks an env value as a reference to a variable in the
// project's 1Password Environment (op-env://<VARIABLE_NAME>). The
// Environment itself is selected by `secrets.op-env` (its ID) in
// .zdev/config.yaml.
const KeyPrefix = "op-env://"

// IsRef reports whether an env value references a 1Password Environment
// variable.
func IsRef(value string) bool {
	return strings.HasPrefix(value, KeyPrefix)
}

// Key extracts the variable name from an op-env:// reference.
func Key(ref string) string {
	return strings.TrimPrefix(ref, KeyPrefix)
}

// ValidateRef checks that a reference names a plausible environment
// variable.
func ValidateRef(ref string) error {
	if !IsRef(ref) {
		return fmt.Errorf("not a secret reference (missing %s prefix): %s", KeyPrefix, ref)
	}
	key := Key(ref)
	if key == "" {
		return fmt.Errorf("invalid secret reference %q: missing variable name after %s", ref, KeyPrefix)
	}
	if strings.ContainsAny(key, "=\n\r ") || strings.Contains(key, "/") {
		return fmt.Errorf("invalid secret reference %q: variable name must not contain spaces, '=', '/' or newlines", ref)
	}
	return nil
}

// Resolver resolves environment variable keys against a 1Password
// Environment. Input is the Environment ID and a set of deduplicated
// variable names; output maps each name to its value.
type Resolver interface {
	Resolve(ctx context.Context, envID string, keys []string) (map[string]string, error)
}

// HashValues returns a deterministic sha256 over resolved secret values,
// keyed by variable name. Used for the zdev.secrets-hash container label
// so a later refresh can detect rotation without storing the values
// anywhere.
func HashValues(values map[string]string) string {
	pairs := make([]string, 0, len(values))
	for key, val := range values {
		pairs = append(pairs, key+"\x00"+val)
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\x01")))
	return hex.EncodeToString(sum[:])
}
