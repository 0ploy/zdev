package firstrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/ssl"
	"github.com/0ploy/zdev/internal/tools"
	"github.com/0ploy/zdev/internal/ui"
)

const (
	// InitializedFileName is the marker file indicating setup is complete
	InitializedFileName = ".initialized"
)

// Manager handles first-run detection and setup
type Manager struct {
	zdevHome string
	domain    string
	sslEnabled bool
}

// NewManager creates a new first-run manager
func NewManager(cfg *config.GlobalConfig) (*Manager, error) {
	return &Manager{
		zdevHome: config.GetZdevHome(),
		domain:    cfg.Domain,
		sslEnabled: cfg.SSL.Enabled,
	}, nil
}

// IsInitialized checks if zdev has been initialized
func (m *Manager) IsInitialized() bool {
	markerPath := filepath.Join(m.zdevHome, InitializedFileName)
	_, err := os.Stat(markerPath)
	return err == nil
}

// MarkInitialized creates the .initialized marker file
func (m *Manager) MarkInitialized() error {
	if err := os.MkdirAll(m.zdevHome, 0755); err != nil {
		return fmt.Errorf("failed to create zdev home: %w", err)
	}

	markerPath := filepath.Join(m.zdevHome, InitializedFileName)
	if err := os.WriteFile(markerPath, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to create marker file: %w", err)
	}

	return nil
}

// RunSetup performs the first-run setup interactively
// Returns true if setup was performed (vs already initialized)
func (m *Manager) RunSetup(ctx context.Context) (bool, error) {
	if m.IsInitialized() {
		return false, nil
	}

	fmt.Println()
	fmt.Println("Welcome to zdev!")
	fmt.Println()
	fmt.Println("Performing first-time setup...")
	fmt.Println()

	// Step 1: Check Docker
	fmt.Println("[1/4] Checking Docker...")
	if err := m.checkDocker(ctx); err != nil {
		return false, fmt.Errorf("Docker check failed: %w", err)
	}
	fmt.Println("      Docker is available")
	fmt.Println()

	// If SSL is disabled, skip mkcert/CA/cert steps
	if !m.sslEnabled {
		fmt.Println("[2/4] SSL disabled in config, skipping mkcert setup")
		fmt.Println()
		fmt.Println("[3/4] Skipping CA installation (SSL disabled)")
		fmt.Println()
		fmt.Println("[4/4] Skipping certificate generation (SSL disabled)")
		fmt.Println()

		if err := m.MarkInitialized(); err != nil {
			return false, err
		}

		fmt.Println("Setup complete!")
		fmt.Println()
		return true, nil
	}

	// Step 2: Check/Install mkcert
	fmt.Println("[2/4] Checking mkcert...")
	mkcertPath, err := m.ensureMkcert(ctx)
	if err != nil {
		return false, fmt.Errorf("mkcert setup failed: %w", err)
	}
	fmt.Println()

	// Step 3: Generate certificates (before CA check - mkcert can generate without trusted CA)
	fmt.Println("[3/4] Generating certificates...")
	certsCreated, certPath, err := m.ensureCertsWithPath(ctx, mkcertPath)
	if err != nil {
		fmt.Printf("      Warning: %v\n", err)
		fmt.Println("      HTTPS will not be available.")
		fmt.Println()
	} else if certsCreated {
		fmt.Printf("      Generated certificate for *.%s\n", m.domain)
	} else {
		fmt.Printf("      Certificates already exist for *.%s\n", m.domain)
	}
	fmt.Println()

	// Step 4: Verify CA is trusted (by checking if cert is valid)
	fmt.Println("[4/4] Verifying CA trust...")
	if certPath != "" {
		trusted, err := m.verifyCertTrust(ctx, mkcertPath, certPath)
		if err != nil {
			fmt.Printf("      Warning: could not verify trust: %v\n", err)
		} else if trusted {
			fmt.Println("      CA is trusted")
		} else {
			// CA not trusted - prompt for installation
			fmt.Printf("      %s\n", ui.Color("CA is not trusted by your system.", "red", false))
			fmt.Println()
			fmt.Println("      zdev uses mkcert (https://mkcert.dev) to generate locally-trusted")
			fmt.Println("      certificates for HTTPS. To trust these certificates, the CA must be")
			fmt.Println("      installed in your system keychain.")
			fmt.Println()
			fmt.Println("      You may be prompted for your password (possibly twice).")
			fmt.Println()
			fmt.Println("      To skip HTTPS setup, cancel the prompt and disable SSL in")
			fmt.Println("      ~/.zdev/global-config.yaml:")
			fmt.Printf("        %s\n", ui.Color("ssl:", "cyan", false))
			fmt.Printf("          %s\n", ui.Color("enabled: false", "cyan", false))
			fmt.Println()

			// Try to install CA
			mkcert := tools.NewMkcert(mkcertPath)
			if err := mkcert.InstallCA(ctx); err != nil {
				fmt.Println("      CA installation was cancelled or failed.")
				fmt.Println()
				fmt.Println("      HTTPS will show certificate warnings in your browser.")
				fmt.Println()
				fmt.Println("      Options:")
				fmt.Println("        1. Run CA installation later:")
				fmt.Printf("           %s\n", ui.Color(mkcertPath+" -install", "cyan", false))
				fmt.Println()
				fmt.Println("        2. Disable SSL in ~/.zdev/global-config.yaml:")
				fmt.Printf("           %s\n", ui.Color("ssl:", "cyan", false))
				fmt.Printf("             %s\n", ui.Color("enabled: false", "cyan", false))
				fmt.Println()
				fmt.Println("Setup incomplete. Run 'zdev systemcheck' again after resolving.")
				fmt.Println()
				// Don't mark as initialized so user can retry
				return false, nil
			}

			// Re-verify that CA is now trusted
			trustedNow, err := m.verifyCertTrust(ctx, mkcertPath, certPath)
			if err != nil || !trustedNow {
				fmt.Println("      CA installation completed but verification still fails.")
				fmt.Println("      You may need to restart your browser or system.")
				fmt.Println()
				fmt.Println("Setup incomplete. Run 'zdev systemcheck' again after resolving.")
				fmt.Println()
				return false, nil
			}
			fmt.Printf("      %s\n", ui.Color("CA installed and verified successfully", "green", false))
			fmt.Println()

			// Regenerate certs to ensure they're signed by the current CA
			fmt.Println("      Regenerating certificates...")
			certsCreated, newCertPath, err := m.ensureCertsWithPath(ctx, mkcertPath)
			if err != nil {
				fmt.Printf("      Warning: failed to regenerate certificates: %v\n", err)
			} else if certsCreated {
				certPath = newCertPath
				fmt.Printf("      %s\n", ui.Color("Certificates regenerated for new CA", "green", false))
			} else {
				fmt.Printf("      %s\n", ui.Color("Certificates verified", "green", false))
			}
		}
	}
	fmt.Println()

	// Mark as initialized
	if err := m.MarkInitialized(); err != nil {
		return false, err
	}

	fmt.Printf("%s\n", ui.Color("Setup complete!", "green", false))
	fmt.Println()

	return true, nil
}

// checkDocker verifies Docker is available
func (m *Manager) checkDocker(ctx context.Context) error {
	_, found := tools.FindInPath("docker")
	if !found {
		return fmt.Errorf("docker not found in PATH")
	}

	// Try running docker version
	_, err := tools.RunTool(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return fmt.Errorf("docker is not running: %w", err)
	}

	return nil
}

// ensureMkcert ensures mkcert is available, downloading if necessary
func (m *Manager) ensureMkcert(ctx context.Context) (string, error) {
	// First check if mkcert is in system PATH
	if path, found := tools.FindInPath("mkcert"); found {
		fmt.Printf("      Found system mkcert at %s\n", path)
		return path, nil
	}

	// Need to download
	fmt.Println("      Downloading mkcert...")
	toolMgr, err := tools.NewManager()
	if err != nil {
		return "", err
	}

	mkcertTool := tools.MkcertTool()
	path, err := toolMgr.EnsureTool(ctx, mkcertTool)
	if err != nil {
		return "", fmt.Errorf("failed to download mkcert: %w", err)
	}

	fmt.Printf("      Installed to %s\n", path)
	return path, nil
}

// ensureCertsWithPath generates certificates if they don't exist
// Returns (created, certPath, error)
func (m *Manager) ensureCertsWithPath(ctx context.Context, mkcertPath string) (bool, string, error) {
	certMgr, err := ssl.NewCertManager(mkcertPath)
	if err != nil {
		return false, "", err
	}

	certPath, _, created, err := certMgr.EnsureCerts(ctx, m.domain)
	if err != nil {
		return false, "", err
	}

	return created, certPath, nil
}

// verifyCertTrust checks if the certificate is trusted by the system
// Returns (trusted, error)
func (m *Manager) verifyCertTrust(ctx context.Context, mkcertPath, certPath string) (bool, error) {
	mkcert := tools.NewMkcert(mkcertPath)
	return mkcert.IsCATrusted(ctx, certPath)
}
