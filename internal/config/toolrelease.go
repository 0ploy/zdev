package config

import "strings"

// ToolRelease pins a binary zdev downloads from a project's releases: the
// host tools (mkcert, just, mutagen) and the agent sandbox's (jq, gh, glab).
// The pins live in defaults.go, each under a "renovate:" comment (see there).
type ToolRelease struct {
	Name    string
	Version string // plain version; Renovate's extractVersion strips the tag's prefix
	// URL takes {version}, {os} and {arch}: GOOS and GOARCH, renamed through
	// OSNames and ArchNames for projects that call them something else.
	URL       string
	OSNames   map[string]string
	ArchNames map[string]string
	// ArchivePath is the binary inside a .tar.gz download ({version}, {os},
	// {arch} as in URL); "" for a plain binary or when the caller finds it
	// by name.
	ArchivePath string
	// SHA256 is the download's checksum by "<goos>/<goarch>".
	SHA256 map[string]string
}

func (r ToolRelease) expand(s, goos, goarch string) string {
	if v, ok := r.OSNames[goos]; ok {
		goos = v
	}
	if v, ok := r.ArchNames[goarch]; ok {
		goarch = v
	}
	return strings.NewReplacer("{version}", r.Version, "{os}", goos, "{arch}", goarch).Replace(s)
}

// DownloadURL is the release asset for goos/goarch.
func (r ToolRelease) DownloadURL(goos, goarch string) string {
	return r.expand(r.URL, goos, goarch)
}

// BinaryPath is ArchivePath for goos/goarch ("" for a plain binary).
func (r ToolRelease) BinaryPath(goos, goarch string) string {
	return r.expand(r.ArchivePath, goos, goarch)
}

// Checksum is the pinned SHA-256 of the download for goos/goarch ("" when
// that platform isn't pinned).
func (r ToolRelease) Checksum(goos, goarch string) string {
	return r.SHA256[goos+"/"+goarch]
}
