package runtime

import "fmt"

// ScopedName builds the canonical <name>.<project>.zdev identifier zdev
// uses for container names, named volumes, and Docker DNS. It lives in
// this leaf package because both `project` and `services` need it; keep
// every consumer on this helper so the convention cannot drift.
func ScopedName(name, project string) string {
	return fmt.Sprintf("%s.%s.zdev", name, project)
}

// MutagenSyncVolumeName builds the name of the Docker volume backing a
// service's Mutagen sync: sync.<service>.<project>.zdev.
func MutagenSyncVolumeName(service, project string) string {
	return "sync." + ScopedName(service, project)
}
