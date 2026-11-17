package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/0ploy/zdev/internal/config"
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
