package project

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ssl"
	"github.com/0ploy/zdev/internal/ui"
)

// newCertManager is the mkcert seam: tests swap in a CertManager backed by an
// in-process CA so `make test` needs no mkcert binary.
var newCertManager = ssl.NewDefaultCertManager

// ensureProjectCert keeps the project's own certificate in line with its
// routed hostnames: names the main wildcard cert can't cover (nested deeper
// than one label under the domain, or outside it) get one cert per project,
// served by Traefik via SNI. Runs on every start/update, so the no-cert path
// is file deletes only and the up-to-date path never invokes mkcert to sign.
//
// Failures warn instead of failing the start: without the cert those hosts
// still route, they just get the default certificate - same as before.
func (p *Project) ensureProjectCert(ctx context.Context) {
	var names []string
	var gcfg *config.GlobalConfig
	if isTLSAvailable() {
		if cfg, err := config.LoadGlobalConfig(); err == nil {
			gcfg = cfg
			names = ssl.ProjectCertNames(cfg.Domain, p.routedHosts())
		}
	}

	if len(names) == 0 {
		if err := ssl.RemoveProjectCert(p.Config.Name); err != nil {
			fmt.Printf("Warning: failed to remove project certificate: %v\n", err)
		}
		return
	}

	mgr, err := newCertManager()
	if err != nil {
		fmt.Printf("Warning: no HTTPS certificate for %s: %v\n", strings.Join(names, ", "), err)
		return
	}
	upToDate, err := mgr.ProjectCertUpToDate(ctx, p.Config.Name, names)
	if err != nil {
		fmt.Printf("Warning: failed to check project certificate: %v\n", err)
		return
	}
	if upToDate {
		return
	}

	ui.StatusStep(fmt.Sprintf("Issuing HTTPS certificate for %s", strings.Join(names, ", ")), ui.PlainMode(gcfg.Terminal.Plain))
	if err := mgr.WriteProjectCert(ctx, p.Config.Name, names); err != nil {
		fmt.Printf("Warning: failed to issue project certificate: %v\n", err)
	}
}

// Where project containers find the zdev trust files.
const (
	// containerTrustDir holds ca.pem and ca-bundle.pem. Mounted as a
	// directory, so every kind of update on the host reaches containers.
	containerTrustDir   = "/etc/zdev"
	containerCAPath     = containerTrustDir + "/" + ssl.CAFileName
	containerBundlePath = containerTrustDir + "/" + ssl.CABundleFileName
)

// systemCABundlePaths are the images' system CA files the bundle is mounted
// over. Between them they cover Debian, Ubuntu, Alpine, Wolfi, Arch,
// distroless and scratch (Go reads the first), Fedora, RHEL and Amazon
// Linux (the second; a symlink in those images, Docker mounts through it),
// and openSUSE (the third). A path the image lacks is created by Docker and
// read by nobody.
var systemCABundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
}

// ensureCATrustFiles creates the trust files before any container is built,
// so even the first start mounts them. Only when ca.pem is missing: keeping
// them current is EnsureCerts' job (runs on every router start). Best
// effort - without them HTTPS calls from containers to *.<domain> fail to
// verify, nothing else.
func (p *Project) ensureCATrustFiles(ctx context.Context) {
	if !isTLSAvailable() {
		return
	}
	if _, err := os.Stat(ssl.CAPath()); err == nil {
		return
	}
	mgr, err := newCertManager()
	if err != nil {
		fmt.Printf("Warning: containers won't trust the zdev CA: %v\n", err)
		return
	}
	if err := mgr.SyncCATrust(ctx); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}
}

// applyCATrust makes a container trust the zdev CA, so HTTPS calls to other
// projects' real URLs verify:
//
//   - the trust dir, read-only, at /etc/zdev
//   - the bundle (host public roots + zdev CA), read-only, over the system
//     CA files - OpenSSL, curl, PHP, Python and Go read these (tested)
//   - NODE_EXTRA_CA_CERTS=/etc/zdev/ca.pem (Node ignores the system store)
//   - REQUESTS_CA_BUNDLE=/etc/zdev/ca-bundle.pem (requests ships certifi)
//
// The bundle is a complete trust store, so public HTTPS keeps working. No
// SSL_CERT_FILE: the system paths already cover what it would. Not covered:
// Java (own keystore), Rust built with webpki-roots, and images that
// regenerate their CA file at runtime. User-set env vars and user mounts on
// the same paths win.
func applyCATrust(cfg *runtime.ContainerConfig) {
	if !isTLSAvailable() {
		return
	}
	if _, err := os.Stat(ssl.CAPath()); err != nil {
		return
	}
	mounted := make(map[string]bool, len(cfg.Volumes))
	for _, v := range cfg.Volumes {
		mounted[v.Target] = true
	}
	mount := func(source, target string) {
		if !mounted[target] {
			cfg.Volumes = append(cfg.Volumes, runtime.VolumeMount{Source: source, Target: target, ReadOnly: true})
			mounted[target] = true
		}
	}
	setEnv := func(key, value string) {
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		if _, set := cfg.Env[key]; !set {
			cfg.Env[key] = value
		}
	}

	mount(config.GetTrustDir(), containerTrustDir)
	setEnv("NODE_EXTRA_CA_CERTS", containerCAPath)

	// Without the bundle (no host roots found) the system files stay as the
	// image has them.
	if _, err := os.Stat(ssl.CABundlePath()); err != nil {
		return
	}
	for _, target := range systemCABundlePaths {
		mount(ssl.CABundlePath(), target)
	}
	setEnv("REQUESTS_CA_BUNDLE", containerBundlePath)
}
