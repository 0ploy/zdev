package create

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

const (
	// DefaultGitHubOrg is the default GitHub organization for template shorthand names
	DefaultGitHubOrg = "0ploy"

	// TemplateRepoPrefix is prepended to bare template names for GitHub resolution
	TemplateRepoPrefix = "zdev-template-"
)

// TemplateSource represents where a template comes from
type TemplateSource struct {
	Type  string // "local" or "github"
	Path  string // local: absolute path
	Owner string // github: org/user
	Repo  string // github: repository name
	Ref   string // github: branch or tag (empty = repo default)
}

// githubIdentRegex validates GitHub owner and repo names
var githubIdentRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateName checks that a project name is DNS-safe
func ValidateName(name string) error {
	return config.ValidateProjectName(name)
}

// ResolveTemplate parses a template argument into a TemplateSource
//
// Resolution rules:
//   - Starts with /, ./, ../, ~ -> local directory
//   - Contains / (e.g. myorg/myrepo) -> GitHub owner/repo
//   - Bare name (e.g. express) -> GitHub 0ploy/zdev-template-<name>
func ResolveTemplate(template, branch, tag string) (*TemplateSource, error) {
	if branch != "" && tag != "" {
		return nil, fmt.Errorf("--branch and --tag are mutually exclusive")
	}

	ref := branch
	if tag != "" {
		ref = tag
	}

	// Local path detection
	if isLocalPath(template) {
		if ref != "" {
			return nil, fmt.Errorf("--branch and --tag can only be used with GitHub templates")
		}

		path := template
		// Expand ~/ to home directory (not ~user/ syntax)
		if path == "~" || strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to expand ~: %w", err)
			}
			path = filepath.Join(home, path[2:])
		}

		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve path: %w", err)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("template directory not found: %s", absPath)
			}
			return nil, fmt.Errorf("failed to access template directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("template path is not a directory: %s", absPath)
		}

		return &TemplateSource{
			Type: "local",
			Path: absPath,
		}, nil
	}

	// GitHub: contains / -> owner/repo
	if strings.Contains(template, "/") {
		parts := strings.SplitN(template, "/", 2)
		if !githubIdentRegex.MatchString(parts[0]) || !githubIdentRegex.MatchString(parts[1]) {
			return nil, fmt.Errorf("invalid GitHub repository: %s (owner and repo must contain only alphanumeric characters, dots, hyphens, and underscores)", template)
		}
		return &TemplateSource{
			Type:  "github",
			Owner: parts[0],
			Repo:  parts[1],
			Ref:   ref,
		}, nil
	}

	// Bare name -> default org with prefix
	if !githubIdentRegex.MatchString(template) {
		return nil, fmt.Errorf("invalid template name: %s (must contain only alphanumeric characters, dots, hyphens, and underscores)", template)
	}
	return &TemplateSource{
		Type:  "github",
		Owner: DefaultGitHubOrg,
		Repo:  TemplateRepoPrefix + template,
		Ref:   ref,
	}, nil
}

// isLocalPath returns true if the template string looks like a local filesystem path
func isLocalPath(template string) bool {
	return strings.HasPrefix(template, "/") ||
		strings.HasPrefix(template, "./") ||
		strings.HasPrefix(template, "../") ||
		strings.HasPrefix(template, "~/") ||
		template == "." ||
		template == ".." ||
		template == "~"
}

// CopyLocal copies a local template directory to the target, excluding .git/
func CopyLocal(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from source
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("failed to compute relative path: %w", err)
		}

		// Skip .git directory
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}

		targetPath := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		if d.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("failed to read symlink %s: %w", path, err)
			}
			if filepath.IsAbs(linkTarget) || !pathWithin(src, filepath.Join(filepath.Dir(path), linkTarget)) {
				return fmt.Errorf("template symlink %s -> %s escapes the template directory", rel, linkTarget)
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create symlink parent: %w", err)
			}
			if err := os.Symlink(linkTarget, targetPath); err != nil {
				return fmt.Errorf("failed to copy symlink %s: %w", rel, err)
			}
			return nil
		}

		// Copy file
		return copyFile(path, targetPath)
	})
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyFile copies a single file preserving permissions
func copyFile(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("failed to create target file %s: %w", dst, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy %s: %w", src, err)
	}

	return nil
}

// DownloadGitHub downloads and extracts a GitHub repo tarball into the target directory
func DownloadGitHub(ctx context.Context, source *TemplateSource, dst string) error {
	// Build URL
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball", source.Owner, source.Repo)
	if source.Ref != "" {
		url += "/" + source.Ref
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download template: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// OK
	case http.StatusNotFound:
		return fmt.Errorf("template repository %s/%s not found on GitHub", source.Owner, source.Repo)
	case http.StatusForbidden:
		return fmt.Errorf("GitHub API rate limit exceeded, try again later")
	default:
		return fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	return extractTarGz(resp.Body, dst)
}

// extractTarGz extracts a gzipped tar archive, stripping the root directory
func extractTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("failed to decompress: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// The first entry is the root directory (e.g. "owner-repo-sha/")
	// We need to detect and strip it
	var rootPrefix string
	var totalSize int64
	var entryCount int

	const (
		maxArchiveEntries = 100_000
		maxFileSize       = int64(1 << 30)
		maxArchiveSize    = int64(4 << 30)
	)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Skip pax headers (GitHub tarballs include these before the actual content)
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		entryCount++
		if entryCount > maxArchiveEntries {
			return fmt.Errorf("template archive contains more than %d entries", maxArchiveEntries)
		}

		cleanArchiveName := pathpkg.Clean(header.Name)
		if cleanArchiveName == "." || cleanArchiveName == ".." ||
			strings.HasPrefix(cleanArchiveName, "../") || pathpkg.IsAbs(cleanArchiveName) {
			return fmt.Errorf("tar entry %q attempts path traversal", header.Name)
		}

		// Detect root prefix from the first real entry
		if rootPrefix == "" {
			root := strings.SplitN(cleanArchiveName, "/", 2)[0]
			if root == "" || root == "." || root == ".." {
				return fmt.Errorf("tar entry %q has an invalid root directory", header.Name)
			}
			rootPrefix = root + "/"
		}

		// Strip root prefix
		rootName := strings.TrimSuffix(rootPrefix, "/")
		if cleanArchiveName != rootName && !strings.HasPrefix(cleanArchiveName, rootPrefix) {
			return fmt.Errorf("tar entry %q is outside archive root %q", header.Name, strings.TrimSuffix(rootPrefix, "/"))
		}
		if cleanArchiveName == rootName {
			continue
		}
		name := strings.TrimPrefix(cleanArchiveName, rootPrefix)

		targetPath := filepath.Join(dst, filepath.FromSlash(name))
		if !pathWithin(dst, targetPath) {
			return fmt.Errorf("tar entry %q attempts path traversal", header.Name)
		}
		if err := ensureNoSymlinkPath(dst, targetPath); err != nil {
			return fmt.Errorf("tar entry %q: %w", header.Name, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", name, err)
			}

		case tar.TypeReg:
			if header.Size < 0 || header.Size > maxFileSize {
				return fmt.Errorf("tar entry %q exceeds the %d byte file limit", header.Name, maxFileSize)
			}
			if header.Size > maxArchiveSize-totalSize {
				return fmt.Errorf("template archive exceeds the %d byte extraction limit", maxArchiveSize)
			}
			totalSize += header.Size

			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			// Strip special bits (setuid/setgid), keep rwx only
			mode := os.FileMode(header.Mode) & 0777
			if mode == 0 {
				mode = 0644
			}

			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", name, err)
			}

			written, err := io.CopyN(f, tr, header.Size)
			if err != nil {
				f.Close()
				return fmt.Errorf("failed to write file %s: %w", name, err)
			}
			if written != header.Size {
				f.Close()
				return fmt.Errorf("failed to write file %s: wrote %d of %d bytes", name, written, header.Size)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("failed to close file %s: %w", name, err)
			}

		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
			// Validate symlink target stays within dst to prevent path traversal
			linkTarget := header.Linkname
			if filepath.IsAbs(linkTarget) {
				return fmt.Errorf("tar entry %q contains absolute symlink target %q", header.Name, linkTarget)
			}
			resolvedLink := filepath.Join(filepath.Dir(targetPath), filepath.FromSlash(linkTarget))
			if !pathWithin(dst, resolvedLink) {
				return fmt.Errorf("tar entry %q symlink target %q escapes destination", header.Name, linkTarget)
			}
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("failed to create symlink %s: %w", name, err)
			}
		case tar.TypeRegA:
			return fmt.Errorf("tar entry %q uses an unsupported legacy file type", header.Name)
		case tar.TypeLink:
			return fmt.Errorf("tar entry %q uses an unsupported hard link", header.Name)
		}
	}

	return nil
}

func ensureNoSymlinkPath(root, target string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return err
	}
	current := filepath.Clean(root)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses an existing symlink", rel)
		}
	}
	return nil
}

// DisplayName returns a human-readable name for the template source
func (s *TemplateSource) DisplayName() string {
	if s.Type == "local" {
		return s.Path
	}
	name := s.Owner + "/" + s.Repo
	if s.Ref != "" {
		name += "@" + s.Ref
	}
	return name
}
