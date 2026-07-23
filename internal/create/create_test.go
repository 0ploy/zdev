package create

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{
		"myapp",
		"my-app",
		"a",
		"a1",
		"my-express-app",
		"app123",
		"a-b-c",
	}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"MyApp",
		"-myapp",
		"myapp-",
		"-",
		"my_app",
		"my app",
		"my.app",
		"ALLCAPS",
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}

	// Max length (63 chars)
	longValid := "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0" // 63 chars
	if err := ValidateName(longValid); err != nil {
		t.Errorf("ValidateName(63 chars) = %v, want nil", err)
	}

	// Too long (64 chars)
	tooLong := longValid + "a"
	if err := ValidateName(tooLong); err == nil {
		t.Errorf("ValidateName(64 chars) = nil, want error")
	}
}

func TestResolveTemplate_Local(t *testing.T) {
	// Create a temp directory to use as a template
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		template string
		wantType string
		wantErr  bool
	}{
		{
			name:     "absolute path",
			template: tmpDir,
			wantType: "local",
		},
		{
			name:     "relative path with dot",
			template: ".",
			wantType: "local",
		},
		{
			name:     "relative path with dot-dot",
			template: "..",
			wantType: "local",
		},
		{
			name:     "nonexistent path",
			template: "/nonexistent/path/to/template",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := ResolveTemplate(tt.template, "", "")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if src.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", src.Type, tt.wantType)
			}
		})
	}
}

func TestResolveTemplate_GitHub(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		branch    string
		tag       string
		wantOwner string
		wantRepo  string
		wantRef   string
		wantErr   bool
	}{
		{
			name:      "full owner/repo",
			template:  "myorg/mytemplate",
			wantOwner: "myorg",
			wantRepo:  "mytemplate",
		},
		{
			name:      "bare name",
			template:  "express",
			wantOwner: "0ploy",
			wantRepo:  "zdev-template-express",
		},
		{
			name:      "bare name with branch",
			template:  "express",
			branch:    "develop",
			wantOwner: "0ploy",
			wantRepo:  "zdev-template-express",
			wantRef:   "develop",
		},
		{
			name:      "full repo with tag",
			template:  "myorg/myrepo",
			tag:       "v1.0",
			wantOwner: "myorg",
			wantRepo:  "myrepo",
			wantRef:   "v1.0",
		},
		{
			name:     "branch and tag both set",
			template: "express",
			branch:   "main",
			tag:      "v1.0",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := ResolveTemplate(tt.template, tt.branch, tt.tag)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if src.Type != "github" {
				t.Errorf("Type = %q, want %q", src.Type, "github")
			}
			if src.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", src.Owner, tt.wantOwner)
			}
			if src.Repo != tt.wantRepo {
				t.Errorf("Repo = %q, want %q", src.Repo, tt.wantRepo)
			}
			if src.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", src.Ref, tt.wantRef)
			}
		})
	}
}

func TestResolveTemplate_BranchTagWithLocal(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ResolveTemplate(tmpDir, "main", "")
	if err == nil {
		t.Fatal("expected error when using --branch with local template")
	}

	_, err = ResolveTemplate(tmpDir, "", "v1.0")
	if err == nil {
		t.Fatal("expected error when using --tag with local template")
	}
}

func TestCopyLocal(t *testing.T) {
	// Create source template structure
	srcDir := t.TempDir()

	// Create files and directories
	os.MkdirAll(filepath.Join(srcDir, ".zdev", "commands"), 0755)
	os.WriteFile(filepath.Join(srcDir, ".zdev", "config.yaml"), []byte("version: 1"), 0644)
	os.WriteFile(filepath.Join(srcDir, ".zdev", "commands", "setup.just"), []byte("default:\n  echo hi"), 0644)
	os.WriteFile(filepath.Join(srcDir, "app.js"), []byte("console.log('hello')"), 0644)
	os.WriteFile(filepath.Join(srcDir, "package.json"), []byte("{}"), 0644)

	// Create a .git directory that should be excluded
	os.MkdirAll(filepath.Join(srcDir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(srcDir, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0644)

	// Copy to destination
	dstDir := t.TempDir()
	if err := CopyLocal(srcDir, dstDir); err != nil {
		t.Fatalf("CopyLocal() error: %v", err)
	}

	// Verify expected files exist
	expectedFiles := []string{
		".zdev/config.yaml",
		".zdev/commands/setup.just",
		"app.js",
		"package.json",
	}
	for _, f := range expectedFiles {
		path := filepath.Join(dstDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s not found: %v", f, err)
		}
	}

	// Verify .git was excluded
	gitDir := filepath.Join(dstDir, ".git")
	if _, err := os.Stat(gitDir); !os.IsNotExist(err) {
		t.Errorf(".git directory should not have been copied")
	}

	// Verify file contents
	data, err := os.ReadFile(filepath.Join(dstDir, ".zdev", "config.yaml"))
	if err != nil {
		t.Fatalf("failed to read copied config: %v", err)
	}
	if string(data) != "version: 1" {
		t.Errorf("config content = %q, want %q", string(data), "version: 1")
	}
}

func TestCopyLocalPreservesSafeSymlink(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "target.txt"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	dstDir := t.TempDir()
	if err := CopyLocal(srcDir, dstDir); err != nil {
		t.Fatalf("CopyLocal() error: %v", err)
	}
	target, err := os.Readlink(filepath.Join(dstDir, "link.txt"))
	if err != nil {
		t.Fatalf("copied link is not a symlink: %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("copied symlink target = %q, want target.txt", target)
	}
}

func TestCopyLocalRejectsEscapingSymlink(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.Symlink("../secret", filepath.Join(srcDir, "link")); err != nil {
		t.Fatal(err)
	}

	err := CopyLocal(srcDir, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("CopyLocal() error = %v, want escaping symlink error", err)
	}
}

func TestExtractTarGzRejectsTraversalAsFirstEntry(t *testing.T) {
	archive := makeTarGz(t, []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0644},
	})
	if err := extractTarGz(bytes.NewReader(archive), t.TempDir()); err == nil {
		t.Fatal("extractTarGz() accepted a traversal entry")
	}
}

func TestExtractTarGzRejectsSymlinkPivot(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "root/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "root/link", Typeflag: tar.TypeSymlink, Linkname: "directory"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "root/link/file", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	err := extractTarGz(bytes.NewReader(archive.Bytes()), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("extractTarGz() error = %v, want symlink traversal error", err)
	}
}

func makeTarGz(t *testing.T, headers []tar.Header) []byte {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for i := range headers {
		if err := tw.WriteHeader(&headers[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestResolveTemplate_FileNotDir(t *testing.T) {
	// Create a temp file (not directory)
	tmpFile := filepath.Join(t.TempDir(), "notadir")
	os.WriteFile(tmpFile, []byte("test"), 0644)

	_, err := ResolveTemplate(tmpFile, "", "")
	if err == nil {
		t.Fatal("expected error when template is a file, not directory")
	}
}
