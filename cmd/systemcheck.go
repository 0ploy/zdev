package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/firstrun"
	"github.com/0ploy/zdev/internal/resolver"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/services"
	"github.com/0ploy/zdev/internal/ssl"
	"github.com/0ploy/zdev/internal/tools"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

// plainMode is cached for the systemcheck command
var sysPlainMode bool

var installCAFlag bool

var systemcheckCmd = &cobra.Command{
	Use:   "systemcheck",
	Short: "Check system dependencies and zdev setup",
	Long: `Verify that all required dependencies are installed and configured correctly.

This command checks:
- Docker availability
- mkcert installation
- Local CA installation
- SSL certificates
- Router status

On first run, this command will also perform initial setup including
downloading mkcert and generating SSL certificates.`,
	RunE: runSystemcheck,
}

func init() {
	systemcheckCmd.Flags().BoolVar(&installCAFlag, "install-ca", false, "Install the local CA certificate")
	rootCmd.AddCommand(systemcheckCmd)
}

// RunSystemcheckIfNeeded runs systemcheck if zdev is not initialized
// Returns true if systemcheck was run, false if already initialized
func RunSystemcheckIfNeeded() (bool, error) {
	// Ensure global config exists
	configPath, created, err := config.EnsureGlobalConfig()
	if err != nil {
		fmt.Printf("Warning: could not create global config: %v\n", err)
	} else if created {
		fmt.Printf("Created default global config: %s\n", configPath)
		fmt.Println("You can edit this file to customize zdev settings.")
		fmt.Println()
	}

	// Load global config
	globalCfg, err := config.LoadGlobalConfig()
	if err != nil {
		return false, fmt.Errorf("failed to load global config: %w", err)
	}

	// Check if first-run setup is needed
	firstrunMgr, err := firstrun.NewManager(globalCfg)
	if err != nil {
		return false, fmt.Errorf("failed to initialize first-run manager: %w", err)
	}

	if firstrunMgr.IsInitialized() {
		return false, nil
	}

	// Run first-time setup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	setupRan, err := firstrunMgr.RunSetup(ctx)
	if err != nil {
		return true, fmt.Errorf("first-run setup failed: %w", err)
	}

	// If setup completed successfully, start services and open docs
	if setupRan {
		openDocsAfterFirstRun(ctx, globalCfg)
	}

	return true, nil
}

// openDocsAfterFirstRun starts shared services and opens the docs page
func openDocsAfterFirstRun(ctx context.Context, cfg *config.GlobalConfig) {
	fmt.Println("Starting shared services...")
	fmt.Println()

	mgr := services.NewManager(cfg)

	// Start router (required for docs)
	if err := mgr.StartRouter(ctx); err != nil {
		fmt.Printf("Warning: could not start router: %v\n", err)
		return
	}

	// Start other services
	_ = mgr.StartMail(ctx)
	_ = mgr.StartDBUI(ctx)

	// Bring the local DNS fallback container back up if the host is
	// configured to use it (otherwise *.<domain> would stop resolving).
	ensureDNSFallbackRunning(ctx, cfg)

	// Build docs URL
	url := fmt.Sprintf("%s://docs.shared.%s", schemeFor(cfg), cfg.Domain)

	fmt.Println()
	fmt.Println("Opening documentation...")
	fmt.Println()

	// Open docs in browser
	if err := openBrowser(url); err != nil {
		fmt.Printf("Could not open browser. Visit: %s\n", url)
	}
}

func runSystemcheck(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Ensure global config exists
	configPath, created, err := config.EnsureGlobalConfig()
	if err != nil {
		fmt.Printf("Warning: could not create global config: %v\n", err)
	} else if created {
		fmt.Printf("Created default global config: %s\n", configPath)
		fmt.Println("You can edit this file to customize zdev settings.")
		fmt.Println()
	}

	// Load global config
	globalCfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	// Set plain mode from config
	sysPlainMode = ui.PlainMode(globalCfg.Terminal.Plain)

	// Check if first-run setup is needed
	firstrunMgr, err := firstrun.NewManager(globalCfg)
	if err != nil {
		return fmt.Errorf("failed to initialize first-run manager: %w", err)
	}

	if !firstrunMgr.IsInitialized() {
		// Run first-time setup
		setupRan, err := firstrunMgr.RunSetup(ctx)
		if err != nil {
			// First-run errors are not fatal - continue with systemcheck
			fmt.Printf("Warning: first-run setup incomplete: %v\n", err)
			fmt.Println()
		} else if setupRan {
			// Setup completed - start services and open docs
			openDocsAfterFirstRun(ctx, globalCfg)
		}
	}

	fmt.Println()
	fmt.Println("zdev System Check")
	fmt.Println("==================")
	fmt.Println()

	issues := 0

	// Check Docker
	issues += checkDocker(ctx)

	// Check mkcert
	mkcertPath, mkcertIssues := checkMkcert(ctx, globalCfg)
	issues += mkcertIssues

	// Check just (lazily downloaded for project commands)
	checkJust(ctx)

	// Check mutagen (lazily downloaded when sync is enabled)
	checkMutagen(ctx, globalCfg)

	// Check 1Password CLI (user-installed, only needed for op-env secrets)
	issues += check1Password(ctx)

	// Handle --install-ca flag
	if installCAFlag && mkcertPath != "" {
		fmt.Println()
		fmt.Println("Installing local CA...")
		mkcert := tools.NewMkcert(mkcertPath)
		if err := mkcert.InstallCA(ctx); err != nil {
			fmt.Printf("  %s CA installation failed: %v\n", statusText("FAILED"), err)
			issues++
		} else {
			fmt.Printf("  %s CA installed successfully\n", statusText("OK"))
		}
		fmt.Println()
	}

	// Check local CA (only if mkcert is available)
	if mkcertPath != "" {
		issues += checkCA(ctx, mkcertPath, globalCfg)
	}

	// Check certificates
	issues += checkCertificates(globalCfg)

	// Check router status
	issues += checkRouter(ctx, globalCfg)

	// Check domain DNS resolution (and offer the local fallback if a
	// rebinding-protecting router is blocking the wildcard record)
	issues += checkDNS(ctx, globalCfg)

	// Summary
	fmt.Println()
	if issues == 0 {
		fmt.Printf("%s\n", ui.Color("All checks passed!", "green", sysPlainMode))
	} else {
		fmt.Printf("%s: %d\n", ui.Color("Issues found", "yellow", sysPlainMode), issues)
		if mkcertPath == "" {
			fmt.Println("Run 'zdev systemcheck' to download mkcert and complete setup.")
		} else if !installCAFlag {
			fmt.Println("Run 'zdev systemcheck --install-ca' to install the local CA.")
		}
	}
	fmt.Println()

	return nil
}

// statusText returns colored status text
func statusText(status string) string {
	var color string
	switch status {
	case "OK", "running":
		color = "green"
	case "MISSING", "FAILED", "ERROR", "stopped":
		color = "red"
	case "SKIP":
		color = "yellow"
	default:
		return status
	}
	return ui.Color(status, color, sysPlainMode)
}

func checkDocker(ctx context.Context) int {
	fmt.Print("Docker:        ")

	_, found := tools.FindInPath("docker")
	if !found {
		fmt.Printf("%s (not found in PATH)\n", statusText("MISSING"))
		return 1
	}

	version, err := tools.RunTool(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		fmt.Printf("%s (not running)\n", statusText("ERROR"))
		return 1
	}

	fmt.Printf("%s (version %s)\n", statusText("OK"), version)
	return 0
}

func checkMkcert(ctx context.Context, cfg *config.GlobalConfig) (string, int) {
	fmt.Print("mkcert:        ")

	if !cfg.SSL.Enabled {
		fmt.Printf("%s (SSL disabled in config)\n", statusText("SKIP"))
		return "", 0
	}

	// Check system PATH first
	if path, found := tools.FindInPath("mkcert"); found {
		version, err := tools.RunTool(ctx, path, "-version")
		if err != nil {
			version = "unknown"
		}
		fmt.Printf("%s (%s %s)\n", statusText("OK"), path, version)
		return path, 0
	}

	// Check zdev bin directory
	zdevBinPath := filepath.Join(config.GetZdevHome(), "bin", "mkcert")
	if _, err := os.Stat(zdevBinPath); err == nil {
		version, err := tools.RunTool(ctx, zdevBinPath, "-version")
		if err != nil {
			version = "unknown"
		}
		fmt.Printf("%s (%s %s)\n", statusText("OK"), zdevBinPath, version)
		return zdevBinPath, 0
	}

	fmt.Printf("%s (not installed)\n", statusText("MISSING"))
	fmt.Println("               Run 'zdev systemcheck' to download mkcert")
	return "", 1
}

// checkJust reports the just version. Just is fetched on demand for project
// commands, so absence is informational, not an error.
func checkJust(ctx context.Context) {
	fmt.Print("just:          ")

	path, found := tools.FindInPath("just")
	if !found {
		zdevBinPath := filepath.Join(config.GetZdevHome(), "bin", "just")
		if _, err := os.Stat(zdevBinPath); err == nil {
			path = zdevBinPath
			found = true
		}
	}

	if !found {
		fmt.Printf("%s (not yet downloaded - fetched on first project command)\n", statusText("SKIP"))
		return
	}

	version := strings.TrimPrefix(firstLine(tools.RunTool(ctx, path, "--version")), "just ")
	if version == "" {
		version = "unknown"
	}
	fmt.Printf("%s (%s %s)\n", statusText("OK"), path, version)
}

// checkMutagen reports the mutagen version. Mutagen is fetched on demand when
// a project enables file sync; absence is informational.
func checkMutagen(ctx context.Context, cfg *config.GlobalConfig) {
	fmt.Print("mutagen:       ")

	if !cfg.IsMutagenEnabled() {
		fmt.Printf("%s (disabled in global config)\n", statusText("SKIP"))
		return
	}

	path, found := tools.FindInPath("mutagen")
	if !found {
		zdevBinPath := filepath.Join(config.GetZdevHome(), "bin", "mutagen")
		if _, err := os.Stat(zdevBinPath); err == nil {
			path = zdevBinPath
			found = true
		}
	}

	if !found {
		fmt.Printf("%s (not yet downloaded - fetched on first sync-enabled start)\n", statusText("SKIP"))
		return
	}

	version := firstLine(tools.RunTool(ctx, path, "version"))
	if version == "" {
		version = "unknown"
	}
	fmt.Printf("%s (%s %s)\n", statusText("OK"), path, version)
}

// check1Password reports the 1Password CLI status. op is user-installed
// (never auto-downloaded) and only required when the current project's
// config contains op-env:// secret references, so absence is
// informational unless such a project is present. Environments need the
// beta CLI build.
func check1Password(ctx context.Context) int {
	fmt.Print("1password:     ")

	// Detect whether the current directory's project uses op-env:// refs.
	// Errors are ignored: systemcheck often runs outside a project.
	usesRefs := false
	if dir, err := config.FindProjectDir(); err == nil {
		if cfg, err := config.LoadProject(dir); err == nil {
			for _, svc := range cfg.Services {
				if projectServiceUsesSecrets(cfg.Environment, svc) {
					usesRefs = true
					break
				}
			}
		}
	}

	path, found := tools.FindInPath("op")
	if !found {
		if usesRefs {
			fmt.Printf("%s (not found in PATH but this project uses op-env:// secret references)\n", statusText("MISSING"))
			fmt.Println("               Install it: brew install 1password-cli@beta")
			return 1
		}
		fmt.Printf("%s (not installed - only needed for op-env:// secret references)\n", statusText("SKIP"))
		return 0
	}

	version := firstLine(tools.RunTool(ctx, path, "--version"))
	if version == "" {
		version = "unknown"
	}

	// Environments are beta-CLI-only; a stable build has no
	// `op environment` command family.
	if _, err := tools.RunTool(ctx, path, "environment", "--help"); err != nil {
		if usesRefs {
			fmt.Printf("%s (%s %s - no Environments support, this project needs it)\n", statusText("MISSING"), path, version)
			fmt.Println("               Install the beta build: brew install 1password-cli@beta")
			return 1
		}
		fmt.Printf("%s (%s %s - stable build, Environments need the beta)\n", statusText("SKIP"), path, version)
		return 0
	}

	if usesRefs {
		// whoami fails fast without prompting when no session exists.
		if _, err := tools.RunTool(ctx, path, "whoami"); err != nil {
			fmt.Printf("%s (%s %s - not signed in, run: op signin)\n", statusText("SKIP"), path, version)
			return 0
		}
	}

	fmt.Printf("%s (%s %s)\n", statusText("OK"), path, version)
	return 0
}

// projectServiceUsesSecrets reports whether the service pulls anything
// from 1Password: an attached Environment (op-env:) or op-env://
// references in the merged project+service env.
func projectServiceUsesSecrets(projectEnv map[string]string, svc config.ServiceConfig) bool {
	if svc.OpEnv != "" {
		return true
	}
	for _, v := range projectEnv {
		if secrets.IsRef(v) {
			return true
		}
	}
	for _, v := range svc.Environment {
		if secrets.IsRef(v) {
			return true
		}
	}
	return false
}

// firstLine returns the first non-empty line of out, ignoring err. mkcert/just
// emit one-liners; mutagen emits a multi-line block where the first line is
// the version.
func firstLine(out string, err error) string {
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func checkCA(ctx context.Context, mkcertPath string, cfg *config.GlobalConfig) int {
	fmt.Print("Local CA:      ")

	mkcert := tools.NewMkcert(mkcertPath)

	caRoot, err := mkcert.GetCARoot(ctx)
	if err != nil {
		fmt.Printf("%s (failed to get CA root: %v)\n", statusText("ERROR"), err)
		return 1
	}

	initialized, err := mkcert.IsCAInitialized(ctx)
	if err != nil {
		fmt.Printf("%s (failed to check: %v)\n", statusText("ERROR"), err)
		return 1
	}

	if !initialized {
		fmt.Printf("%s (not initialized)\n", statusText("MISSING"))
		fmt.Println("               Run 'zdev systemcheck --install-ca' to install")
		return 1
	}

	// CA files exist - also check if trusted by the system
	certPath := filepath.Join(config.GetCertsDir(), ssl.CertFileName)
	if _, err := os.Stat(certPath); err == nil {
		trusted, err := mkcert.IsCATrusted(ctx, certPath)
		if err == nil && !trusted {
			fmt.Printf("%s (%s - not trusted by system)\n", statusText("MISSING"), caRoot)
			fmt.Println("               Run 'zdev systemcheck --install-ca' to install")
			return 1
		}
	}

	fmt.Printf("%s (%s)\n", statusText("OK"), caRoot)
	return 0
}

func checkCertificates(cfg *config.GlobalConfig) int {
	fmt.Print("Certificates:  ")

	if !cfg.SSL.Enabled {
		fmt.Printf("%s (SSL disabled in config)\n", statusText("SKIP"))
		return 0
	}

	certsDir := config.GetCertsDir()
	certPath := filepath.Join(certsDir, ssl.CertFileName)
	keyPath := filepath.Join(certsDir, ssl.KeyFileName)

	certExists := false
	keyExists := false

	if _, err := os.Stat(certPath); err == nil {
		certExists = true
	}
	if _, err := os.Stat(keyPath); err == nil {
		keyExists = true
	}

	if !certExists || !keyExists {
		fmt.Printf("%s (not generated)\n", statusText("MISSING"))
		fmt.Println("               Run 'zdev systemcheck' to generate certificates")
		return 1
	}

	fmt.Printf("%s (*.%s)\n", statusText("OK"), cfg.Domain)
	return 0
}

func checkRouter(ctx context.Context, cfg *config.GlobalConfig) int {
	fmt.Print("Router:        ")

	mgr := services.NewManager(cfg)
	status, err := mgr.RouterStatus(ctx)
	if err != nil {
		fmt.Printf("%s (failed to check: %v)\n", statusText("ERROR"), err)
		return 1
	}

	if !status.Running {
		fmt.Printf("%s\n", statusText("stopped"))
		fmt.Println("               Run 'zdev services start' to start the router")
		return 0 // Not running is not an error, just informational
	}

	// Check if TLS is enabled
	tlsInfo := ""
	if cfg.SSL.Enabled {
		// Check if certs exist
		certsDir := config.GetCertsDir()
		certPath := filepath.Join(certsDir, ssl.CertFileName)
		if _, err := os.Stat(certPath); err == nil {
			tlsInfo = ", TLS"
		}
	}

	fmt.Printf("%s (ports 80, 443%s)\n", statusText("running"), tlsInfo)
	return 0
}

// checkDNS verifies the project domain resolves to 127.0.0.1 via system
// DNS. When a rebinding-protecting router is blocking the wildcard record
// (the domain resolves via public DNS but not system DNS), it offers to
// enable the local DNS fallback interactively.
func checkDNS(ctx context.Context, cfg *config.GlobalConfig) int {
	fmt.Print("DNS:           ")

	installed, _ := resolver.IsInstalled(cfg.Domain)

	result, err := config.VerifyDomainDNS(cfg.Domain)
	if err == nil {
		if installed {
			fmt.Printf("%s (*.%s via local fallback)\n", statusText("OK"), cfg.Domain)
		} else {
			fmt.Printf("%s (*.%s resolves to 127.0.0.1)\n", statusText("OK"), cfg.Domain)
		}
		return 0
	}

	// The domain resolves to loopback via public DNS but not system DNS:
	// the classic rebinding-protection signature.
	if result != nil && result.ResolvesTo127 {
		fmt.Printf("%s (system DNS can't resolve *.%s)\n", statusText("MISSING"), cfg.Domain)

		if installed {
			fmt.Println("               Local DNS fallback is configured but not resolving.")
			fmt.Println("               Check the DNS container: zdev dns status")
			return 1
		}

		fmt.Println("               Your router appears to block answers pointing at 127.0.0.1")
		fmt.Println("               (DNS rebinding protection).")

		if supported, reason := resolver.Supported(); !supported {
			fmt.Printf("               Automatic fallback unavailable: %s\n", reason)
			fmt.Println("               Enable manually - see 'zdev dns enable'.")
			return 1
		}

		fmt.Printf("               zdev can route *.%s through a local DNS container,\n", cfg.Domain)
		fmt.Println("               bypassing your router for that domain only.")
		if !confirm("               Enable the local DNS fallback now? [y/N]: ") {
			fmt.Println("               Skipped. Enable later with: zdev dns enable")
			return 1
		}
		if err := enableDNSFallback(ctx, cfg); err != nil {
			fmt.Printf("               %s %v\n", statusText("FAILED"), err)
			return 1
		}
		fmt.Printf("               %s local DNS fallback enabled for *.%s\n", statusText("OK"), cfg.Domain)
		return 0
	}

	fmt.Printf("%s (*.%s does not resolve to 127.0.0.1)\n", statusText("MISSING"), cfg.Domain)
	return 1
}
