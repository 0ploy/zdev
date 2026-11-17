package ssl

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/tools"
)

// Per-project certificates cover routed hostnames the main wildcard cert
// can't: a TLS wildcard matches exactly one label, so `*.<domain>` covers
// `myproj.<domain>` but not `api.myproj.<domain>`. They are owned by the
// project lifecycle (the project knows its hostnames; the router doesn't)
// and picked up live by Traefik's watched file provider - no router restart.
//
// Layout:
//   ~/.zdev/certs/projects/<project>.pem, <project>.key
//     (not <project>-key.pem: project "foo"'s key would collide with
//     project "foo-key"'s cert)
//   ~/.zdev/traefik/project-<project>.yaml (tls.certificates entry)
//
// The yaml carries a hash of the cert so it changes on every regeneration:
// Traefik watches only the dynamic dir, never the cert files it references,
// so a cert rewritten under an unchanged yaml would never be reloaded.

const (
	projectCertsSubdir = "projects"
	projectYAMLPrefix  = "project-"
)

// ProjectCertPaths returns the cert and key paths for a project's certificate.
func ProjectCertPaths(project string) (certPath, keyPath string) {
	dir := filepath.Join(config.GetCertsDir(), projectCertsSubdir)
	return filepath.Join(dir, project+".pem"), filepath.Join(dir, project+".key")
}

// ProjectTraefikConfigPath returns the Traefik dynamic config file for a project's certificate.
func ProjectTraefikConfigPath(project string) string {
	return filepath.Join(config.GetTraefikConfigDir(), projectYAMLPrefix+project+".yaml")
}

// ProjectCertNames returns the certificate names a project needs beyond the
// main cert (*.<base>, *.shared.<base>, <base>, localhost, 127.0.0.1).
// A host nested deeper than one label under the base domain needs
// `*.<parent>`; a host outside the base domain needs the exact host.
// The result is sorted and deduplicated; empty means no project cert.
func ProjectCertNames(baseDomain string, hosts []string) []string {
	base := normalizeHost(baseDomain)
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}

	for _, h := range hosts {
		h = normalizeHost(h)
		if h == "" || h == base || h == "localhost" || h == "127.0.0.1" {
			continue
		}
		if base == "" || !strings.HasSuffix(h, "."+base) {
			add(h)
			continue
		}
		labels := strings.Split(strings.TrimSuffix(h, "."+base), ".")
		if len(labels) == 1 {
			continue // *.<base>
		}
		if len(labels) == 2 && labels[1] == "shared" {
			continue // *.shared.<base>
		}
		add("*." + h[strings.Index(h, ".")+1:])
	}

	sort.Strings(names)
	return names
}

func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
}

// RemoveProjectCert deletes a project's certificate, key and Traefik config.
// Missing files are not an error. Needs no mkcert, so it is safe to call from
// any path that unregisters a project.
func RemoveProjectCert(project string) error {
	certPath, keyPath := ProjectCertPaths(project)
	var errs []error
	for _, path := range []string{ProjectTraefikConfigPath(project), certPath, keyPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SweepOrphanProjectCerts deletes per-project cert files and Traefik configs
// whose project is not in keep (e.g. state entries removed while zdev
// couldn't clean up). Needs no mkcert.
func SweepOrphanProjectCerts(keep map[string]bool) error {
	var errs []error

	yamls, _ := filepath.Glob(filepath.Join(config.GetTraefikConfigDir(), projectYAMLPrefix+"*.yaml"))
	for _, path := range yamls {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), projectYAMLPrefix), ".yaml")
		if !keep[name] {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}

	files, _ := filepath.Glob(filepath.Join(config.GetCertsDir(), projectCertsSubdir, "*"))
	for _, path := range files {
		base := filepath.Base(path)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		if !keep[name] {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// FindMkcert returns the mkcert binary path: system PATH first, then zdev's
// own bin directory. Never downloads (that's firstrun/systemcheck's job).
func FindMkcert() (string, bool) {
	if path, found := tools.FindInPath("mkcert"); found {
		return path, true
	}
	toolMgr, err := tools.NewManager()
	if err != nil {
		return "", false
	}
	if toolMgr.ToolExists(tools.MkcertTool()) {
		return toolMgr.GetToolPath(tools.MkcertTool()), true
	}
	return "", false
}

// NewDefaultCertManager returns a CertManager backed by an already installed mkcert.
func NewDefaultCertManager() (*CertManager, error) {
	path, ok := FindMkcert()
	if !ok {
		return nil, fmt.Errorf("mkcert not found (run 'zdev systemcheck')")
	}
	return NewCertManager(path)
}

// ProjectCertUpToDate reports whether the project's cert exists with exactly
// these names, is signed by the current mkcert CA, and is wired into Traefik.
func (c *CertManager) ProjectCertUpToDate(ctx context.Context, project string, names []string) (bool, error) {
	certPath, keyPath := ProjectCertPaths(project)
	if _, err := os.Stat(keyPath); err != nil {
		return false, nil
	}
	cert, raw, err := readCert(certPath)
	if err != nil {
		return false, nil
	}
	if !sameNames(certNames(cert), names) {
		return false, nil
	}
	valid, err := c.signedByCurrentCA(ctx, cert)
	if err != nil || !valid {
		return false, err
	}
	existing, err := os.ReadFile(ProjectTraefikConfigPath(project))
	if err != nil || string(existing) != projectTraefikConfig(project, raw) {
		return false, nil
	}
	return true, nil
}

// WriteProjectCert (re)generates the project's cert for names and writes its
// Traefik config.
func (c *CertManager) WriteProjectCert(ctx context.Context, project string, names []string) error {
	if len(names) == 0 {
		return RemoveProjectCert(project)
	}
	certPath, keyPath := ProjectCertPaths(project)
	if err := c.mkcert.GenerateCert(ctx, certPath, keyPath, names...); err != nil {
		return err
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read generated certificate: %w", err)
	}
	if err := os.MkdirAll(config.GetTraefikConfigDir(), 0755); err != nil {
		return fmt.Errorf("failed to create traefik config directory: %w", err)
	}
	if err := os.WriteFile(ProjectTraefikConfigPath(project), []byte(projectTraefikConfig(project, raw)), 0644); err != nil {
		return fmt.Errorf("failed to write traefik config: %w", err)
	}
	return nil
}

// RefreshForCA regenerates the main cert and the given projects' certs when
// they are no longer signed by the current mkcert CA (e.g. after a CA
// reinstall). Certs are regenerated with their existing names, so this needs
// no project config. Missing certs are left alone. Returns the projects whose
// certs were regenerated and whether the main cert was.
func (c *CertManager) RefreshForCA(ctx context.Context, domain string, projects []string) (mainRegenerated bool, regenerated []string, err error) {
	var errs []error

	if c.CertsExist() {
		_, _, mainRegenerated, err = c.EnsureCerts(ctx, domain)
		if err != nil {
			errs = append(errs, err)
		}
	}

	for _, project := range projects {
		certPath, _ := ProjectCertPaths(project)
		cert, _, err := readCert(certPath)
		if err != nil {
			continue
		}
		valid, err := c.signedByCurrentCA(ctx, cert)
		if err != nil {
			errs = append(errs, err)
			break // CA root unreadable - every other check would fail the same way
		}
		if valid {
			continue
		}
		if err := c.WriteProjectCert(ctx, project, certNames(cert)); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", project, err))
			continue
		}
		regenerated = append(regenerated, project)
	}

	return mainRegenerated, regenerated, errors.Join(errs...)
}

// signedByCurrentCA verifies cert against the mkcert CA root (in-process -
// this runs on every project start, so no openssl subprocess). Expired certs
// fail verification too, which is the right outcome: they get regenerated.
func (c *CertManager) signedByCurrentCA(ctx context.Context, cert *x509.Certificate) (bool, error) {
	roots, err := c.caPool(ctx)
	if err != nil {
		return false, err
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err == nil, nil
}

func (c *CertManager) caPool(ctx context.Context) (*x509.CertPool, error) {
	if c.roots != nil {
		return c.roots, nil
	}
	caRoot, err := c.mkcert.GetCARoot(ctx)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(strings.TrimSpace(caRoot), "rootCA.pem"))
	if err != nil {
		return nil, fmt.Errorf("failed to read mkcert CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("failed to parse mkcert CA")
	}
	c.roots = pool
	return pool, nil
}

func readCert(path string) (*x509.Certificate, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("%s: no certificate found", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, raw, nil
}

// certNames returns the SANs of cert in the form mkcert takes them.
func certNames(cert *x509.Certificate) []string {
	names := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	sort.Strings(names)
	return names
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string{}, a...)
	bs := append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func projectTraefikConfig(project string, certPEM []byte) string {
	sum := sha256.Sum256(certPEM)
	return fmt.Sprintf(`# Traefik TLS certificate for project %[1]s
# This file is auto-generated by zdev
# cert-sha256: %[2]s

tls:
  certificates:
    - certFile: /etc/traefik/certs/%[3]s/%[1]s.pem
      keyFile: /etc/traefik/certs/%[3]s/%[1]s.key
`, project, hex.EncodeToString(sum[:]), projectCertsSubdir)
}
