package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ScopedName builds the canonical <name>.<project>.zdev identifier zdev
// uses for container names, named volumes, and Docker DNS. It lives in
// this leaf package because both `project` and `services` need it; keep
// every consumer on this helper so the convention cannot drift.
func ScopedName(name, project string) string {
	return fmt.Sprintf("%s.%s.zdev", name, project)
}

// MutagenSyncVolumeName builds the name of the Docker volume backing a
// service's Mutagen sync: sync.<service>.<project>.zdev. Used when a service
// has exactly one directory bind, which is the overwhelming majority - the
// name predates per-mount sync, so keeping it means those projects never
// re-seed their sync volume on upgrade.
func MutagenSyncVolumeName(service, project string) string {
	return "sync." + ScopedName(service, project)
}

// MutagenSyncVolumeNameForMount builds the sync volume name for one mount of a
// service that has several: sync.<service>-<slug>.<project>.zdev. Every
// directory bind needs its own volume, otherwise only one of them can be
// backed by sync and the rest silently degrade to raw bind mounts.
func MutagenSyncVolumeNameForMount(service, slug, project string) string {
	return "sync." + ScopedName(service+"-"+slug, project)
}

// mountSlugMaxLen is the longest slug kept verbatim; beyond it the slug is
// truncated and disambiguated with a hash of the full path.
const mountSlugMaxLen = 40

// mountSlugTruncLen leaves room for the "-" + 8 hex characters appended to a
// truncated slug.
const mountSlugTruncLen = 31

// MountSlug turns a container mount path into a token that is legal in both a
// Docker volume name and a Mutagen session name. Mutagen is the stricter of
// the two (alphanumeric and hyphen only), so that is what this produces.
// Deep paths would otherwise make unwieldy names, so anything long is cut and
// disambiguated with a hash of the full path.
func MountSlug(containerPath string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(containerPath) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
			continue
		}
		pendingHyphen = true
	}

	slug := b.String()
	if slug == "" {
		// The container path was "/" or had no alphanumerics at all.
		return "root"
	}
	if len(slug) > mountSlugMaxLen {
		sum := sha256.Sum256([]byte(containerPath))
		slug = strings.TrimRight(slug[:mountSlugTruncLen], "-") + "-" + hex.EncodeToString(sum[:])[:8]
	}
	return slug
}
