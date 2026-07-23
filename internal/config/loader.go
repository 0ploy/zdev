package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed templates/global-config.yaml
var defaultGlobalConfig string

//go:embed templates/traefik-dynamic.yaml
var traefikDynamicConfig string

//go:embed templates/docs.html
var docsHTMLTemplate string

// ProjectInfo holds project display information for the docs page
type ProjectInfo struct {
	Name    string
	Domain  string
	Path    string
	Running bool
}

// LoadProject loads and parses a project config from the given directory.
// If .zdev/local/config.yaml exists, it is deep-merged on top of .zdev/config.yaml
// before variable substitution. Maps merge recursively; scalars and slices replace.
func LoadProject(projectDir string) (*ProjectConfig, error) {
	configPath := filepath.Join(projectDir, ".zdev", "config.yaml")
	localConfigPath := filepath.Join(projectDir, ".zdev", "local", "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	if localData, err := os.ReadFile(localConfigPath); err == nil {
		merged, mergeErr := mergeProjectYAML(data, localData)
		if mergeErr != nil {
			return nil, fmt.Errorf("%s: failed to merge with %s: %w", configPath, localConfigPath, mergeErr)
		}
		data = merged
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read local config %s: %w", localConfigPath, err)
	}

	// Build initial variables (PROJECTDIR, PROJECTPATH, etc. but not PROJECTNAME yet)
	vars := buildVariables(projectDir)
	dirName := vars["PROJECTDIR"]

	// First pass: substitute basic variables to resolve the name and variables fields.
	// Use a minimal struct to avoid type errors from unresolved user variables in typed fields
	// (e.g., ${PORT} in an int field can't be parsed until variables are substituted).
	content := substituteVariables(string(data), vars)

	var firstPass struct {
		Name      string            `yaml:"name"`
		Variables map[string]string `yaml:"variables"`
	}
	if err := yaml.Unmarshal([]byte(content), &firstPass); err != nil {
		return nil, formatConfigError(configPath, data, err)
	}

	// Determine the project name (use parsed name, or default to PROJECTDIR)
	// If name contains ${PROJECTNAME}, it's a circular reference - fall back to PROJECTDIR
	projectName := firstPass.Name
	if projectName == "" || strings.Contains(projectName, "${PROJECTNAME}") {
		projectName = dirName
	}

	// Second pass: add PROJECTNAME, resolve user-defined variables, then substitute
	vars["PROJECTNAME"] = projectName
	for k, v := range firstPass.Variables {
		// User variables don't override built-in variables
		if _, exists := vars[k]; !exists {
			// Resolve references in variable values (e.g. DB_NAME: ${PROJECTNAME}_db)
			vars[k] = substituteVariables(v, vars)
		}
	}
	content = substituteVariables(string(data), vars)

	// Re-parse with full variable substitution. Pre-initialize cfg.Shared
	// with the default-true services so yaml.v3 only overrides fields the
	// user explicitly sets. Bool fields can't distinguish "missing" from
	// "explicit false", so this is the only way to default them to true.
	cfg := ProjectConfig{
		Shared: defaultProjectShared(),
	}
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(content)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, formatConfigError(configPath, data, err)
	}

	// Set defaults
	if cfg.Name == "" {
		cfg.Name = projectName
	}

	// Default domain to {name}.{zdev_domain}
	if cfg.Domain == "" {
		cfg.Domain = cfg.Name + "." + DefaultDomain
	}

	// Default routing protocol to "http" whenever routing is configured.
	for name, svc := range cfg.Services {
		if svc.Routing != nil && svc.Routing.Protocol == "" {
			svc.Routing.Protocol = "http"
			cfg.Services[name] = svc
		}
	}

	if err := ValidateProjectConfig(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	return &cfg, nil
}

// mergeProjectYAML deep-merges override on top of base and returns marshaled YAML.
// Maps merge recursively; slices and scalars in override replace the base value.
// An empty or comments-only override is a no-op (returns base unchanged).
func mergeProjectYAML(base, override []byte) ([]byte, error) {
	var baseMap, overrideMap map[string]any
	if err := yaml.Unmarshal(base, &baseMap); err != nil {
		return nil, fmt.Errorf("parse base: %w", err)
	}
	if err := yaml.Unmarshal(override, &overrideMap); err != nil {
		return nil, fmt.Errorf("parse override: %w", err)
	}
	if overrideMap == nil {
		return base, nil
	}
	if baseMap == nil {
		baseMap = map[string]any{}
	}
	merged := deepMergeMaps(baseMap, overrideMap)
	return yaml.Marshal(merged)
}

// deepMergeMaps merges override into base. When both sides hold a map at the
// same key, recurse; otherwise override wins (including for slices and nil).
func deepMergeMaps(base, override map[string]any) map[string]any {
	for k, v := range override {
		if existing, ok := base[k]; ok {
			if existingMap, ok := existing.(map[string]any); ok {
				if newMap, ok := v.(map[string]any); ok {
					base[k] = deepMergeMaps(existingMap, newMap)
					continue
				}
			}
		}
		base[k] = v
	}
	return base
}

// formatConfigError creates a user-friendly error message for config parsing failures
func formatConfigError(configPath string, data []byte, err error) error {
	errStr := err.Error()
	lines := strings.Split(string(data), "\n")

	// Check if it's an unknown field error from KnownFields(true)
	// yaml.v3 format: "yaml: unmarshal errors:\n  line X: field Y not found in type Z"
	if strings.Contains(errStr, "not found in type") {
		// Use regex to extract line number and field name
		re := regexp.MustCompile(`line (\d+): field (\S+) not found`)
		if matches := re.FindStringSubmatch(errStr); len(matches) == 3 {
			lineNum := 0
			fmt.Sscanf(matches[1], "%d", &lineNum)
			fieldName := matches[2]
			if lineNum > 0 && lineNum <= len(lines) {
				lineContent := strings.TrimSpace(lines[lineNum-1])
				return fmt.Errorf("%s:%d: unknown field %q\n  > %s", configPath, lineNum, fieldName, lineContent)
			}
		}
		return fmt.Errorf("%s: unknown field in config: %s", configPath, errStr)
	}

	// Check for YAML syntax errors with line numbers
	// yaml.v3 format: "yaml: line X: <description>"
	if strings.HasPrefix(errStr, "yaml: line ") {
		var lineNum int
		if _, scanErr := fmt.Sscanf(errStr, "yaml: line %d:", &lineNum); scanErr == nil && lineNum > 0 {
			// Extract the actual error message after "yaml: line X: "
			prefix := fmt.Sprintf("yaml: line %d: ", lineNum)
			message := strings.TrimPrefix(errStr, prefix)

			if lineNum <= len(lines) {
				lineContent := strings.TrimSpace(lines[lineNum-1])
				return fmt.Errorf("%s:%d: %s\n  > %s", configPath, lineNum, message, lineContent)
			}
			return fmt.Errorf("%s:%d: %s", configPath, lineNum, message)
		}
	}

	// Fallback: just add the config path
	return fmt.Errorf("%s: %s", configPath, errStr)
}

// buildVariables creates the variable map for substitution
// Note: PROJECTNAME is NOT set here - it's set after parsing the name field
func buildVariables(projectDir string) map[string]string {
	vars := make(map[string]string)

	// Built-in variables
	// PROJECTDIR = directory basename (always the directory name)
	// PROJECTNAME = will be set later to the parsed 'name:' field value
	vars["PROJECTDIR"] = filepath.Base(projectDir)
	vars["PROJECTPATH"] = projectDir
	vars["ZDEV_HOME"] = getZdevHome()
	vars["ZDEV_DOMAIN"] = getZdevDomain()
	vars["USER"] = os.Getenv("USER")
	vars["HOME"] = os.Getenv("HOME")

	// Include all environment variables
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			// Don't override built-in variables
			if _, exists := vars[parts[0]]; !exists {
				vars[parts[0]] = parts[1]
			}
		}
	}

	return vars
}

// substituteVariables replaces ${VAR} patterns with their values
func substituteVariables(content string, vars map[string]string) string {
	// Match ${VAR} pattern
	re := regexp.MustCompile(`\$\{([^}]+)\}`)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		// Extract variable name (remove ${ and })
		varName := match[2 : len(match)-1]

		if value, ok := vars[varName]; ok {
			return value
		}

		// Leave unresolved variables as-is (or could return empty string)
		return match
	})
}

// getZdevHome returns the zdev home directory
func getZdevHome() string {
	if home := os.Getenv("ZDEV_HOME"); home != "" {
		return home
	}
	return filepath.Join(os.Getenv("HOME"), ".zdev")
}

// GetZdevHome returns the zdev home directory (exported version)
func GetZdevHome() string {
	return getZdevHome()
}

// getZdevDomain returns the default domain
// Priority: 1. ZDEV_DOMAIN env var, 2. global config, 3. default
func getZdevDomain() string {
	// Check environment variable first
	if domain := os.Getenv("ZDEV_DOMAIN"); domain != "" {
		return domain
	}

	// Try to read from global config
	globalConfigPath := filepath.Join(getZdevHome(), GlobalConfigFilename)
	if data, err := os.ReadFile(globalConfigPath); err == nil {
		var globalCfg GlobalConfig
		if err := yaml.Unmarshal(data, &globalCfg); err == nil && globalCfg.Domain != "" {
			return globalCfg.Domain
		}
	}

	return DefaultDomain
}

// GetZdevDomain returns the default domain (exported version)
func GetZdevDomain() string {
	return getZdevDomain()
}

// LoadGlobalConfig loads the global config from ~/.zdev/global-config.yaml
// Returns default config if file doesn't exist
// defaultGlobalConfig returns a GlobalConfig with all defaults populated.
// Single source of truth - used for both "file missing" and "file exists" paths.
func newDefaultGlobalConfig() GlobalConfig {
	return GlobalConfig{
		Version: 1,
		Domain:  DefaultDomain,
		Runtime: "docker",
		SSL: SSLConfig{
			Enabled: true,
		},
		Shared: SharedConfig{
			Router: RouterConfig{
				Image:     RouterImage,
				Dashboard: true,
			},
			Mail: MailConfig{
				Image: MailImage,
			},
			DBUI: DBUIConfig{
				Image: DBUIImage,
			},
			RedisInsights: RedisInsightsConfig{
				Image: RedisInsightsImage,
			},
			Logs: LogsConfig{
				Image: LogsImage,
			},
		},
		Terminal: TerminalConfig{
			Plain: false,
		},
		DNS: DNSFallbackConfig{
			Image: DNSImage,
		},
	}
}

func LoadGlobalConfig() (*GlobalConfig, error) {
	globalConfigPath := filepath.Join(getZdevHome(), GlobalConfigFilename)

	data, err := os.ReadFile(globalConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := newDefaultGlobalConfig()
			return &cfg, nil
		}
		return nil, fmt.Errorf("failed to read global config: %w", err)
	}

	// Start with defaults, then let YAML override
	cfg := newDefaultGlobalConfig()

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, formatConfigError(globalConfigPath, data, err)
	}
	if err := validateGlobalConfig(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", globalConfigPath, err)
	}

	return &cfg, nil
}

// EnsureGlobalConfig creates the global config file if it doesn't exist
// Returns the path to the config and whether it was newly created
func EnsureGlobalConfig() (string, bool, error) {
	zdevHome := getZdevHome()
	configPath := filepath.Join(zdevHome, GlobalConfigFilename)
	oldConfigPath := filepath.Join(zdevHome, "config.yaml")

	// Migrate old config.yaml to global-config.yaml if needed
	if _, err := os.Stat(oldConfigPath); err == nil {
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			// Old config exists but new doesn't - rename it
			if err := os.Rename(oldConfigPath, configPath); err != nil {
				return "", false, fmt.Errorf("failed to migrate config: %w", err)
			}
			return configPath, false, nil
		}
	}

	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil {
		return configPath, false, nil
	}

	// Create directory if needed
	if err := os.MkdirAll(zdevHome, 0755); err != nil {
		return "", false, fmt.Errorf("failed to create %s: %w", zdevHome, err)
	}

	// Generate config with current version constants
	configContent := generateDefaultGlobalConfig()

	// Write default config
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		return "", false, fmt.Errorf("failed to write config: %w", err)
	}

	return configPath, true, nil
}

// generateDefaultGlobalConfig creates the default global config by substituting
// variables in the embedded template with values from defaults.go
func generateDefaultGlobalConfig() string {
	vars := map[string]string{
		"DefaultDomain":      DefaultDomain,
		"RouterImage":        RouterImage,
		"MailImage":          MailImage,
		"DBUIImage":          DBUIImage,
		"RedisInsightsImage": RedisInsightsImage,
		"LogsImage":          LogsImage,
		"DNSImage":           DNSImage,
	}
	return substituteConfigVars(defaultGlobalConfig, vars)
}

// substituteConfigVars replaces ${VAR} placeholders in config with values from vars map
func substituteConfigVars(content string, vars map[string]string) string {
	result := content
	for key, value := range vars {
		result = strings.ReplaceAll(result, "${"+key+"}", value)
	}
	return result
}

// projectDirOverride allows overriding the project directory discovery
// Set via SetProjectDirOverride, used by --config flag
var projectDirOverride string

// SetProjectDirOverride sets an explicit project directory, bypassing discovery
// The path should be the directory containing .zdev/ (not .zdev/ itself)
func SetProjectDirOverride(dir string) {
	projectDirOverride = dir
}

// GetProjectDirOverride returns the current project directory override, if set
func GetProjectDirOverride() string {
	return projectDirOverride
}

// FindProjectDir walks up from the current directory to find a .zdev/config.yaml
// If a project directory override is set (via --config flag), it returns that instead
func FindProjectDir() (string, error) {
	// Check for explicit override first
	if projectDirOverride != "" {
		// Verify the config exists at the override path
		configPath := filepath.Join(projectDirOverride, ".zdev", "config.yaml")
		if _, err := os.Stat(configPath); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("no .zdev/config.yaml found at %s", projectDirOverride)
			}
			return "", fmt.Errorf("failed to access config at %s: %w", projectDirOverride, err)
		}
		return projectDirOverride, nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// Resolve symlinks (macOS has /var -> /private/var)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	// Get home directory to exclude ~/.zdev (global config, not a project)
	homeDir, _ := os.UserHomeDir()

	for {
		configPath := filepath.Join(dir, ".zdev", "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			// Skip if this is the global zdev home (~/.zdev), not a project
			if homeDir != "" && dir == homeDir {
				// This is ~/.zdev/config.yaml - the global config, not a project
				// Continue searching in parent (will eventually fail at root)
			} else {
				return dir, nil
			}
		}

		// Move to parent directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			return "", fmt.Errorf("no .zdev/config.yaml found in current directory or any parent")
		}
		dir = parent
	}
}

// GetTraefikConfigDir returns the path to the Traefik config directory
func GetTraefikConfigDir() string {
	return filepath.Join(getZdevHome(), "traefik")
}

// GetCertsDir returns the path to the certificates directory
func GetCertsDir() string {
	return filepath.Join(getZdevHome(), "certs")
}

// EnsureTraefikConfig creates the Traefik dynamic config file if certs exist
// Returns the config directory path, or empty string if certs don't exist
func EnsureTraefikConfig() (string, error) {
	certsDir := GetCertsDir()
	certPath := filepath.Join(certsDir, "cert.pem")
	keyPath := filepath.Join(certsDir, "key.pem")

	// Check if both cert files exist
	if _, err := os.Stat(certPath); err != nil {
		return "", nil // No certs, return empty (not an error)
	}
	if _, err := os.Stat(keyPath); err != nil {
		return "", nil // No key, return empty (not an error)
	}

	// Create traefik config directory
	traefikDir := GetTraefikConfigDir()
	if err := os.MkdirAll(traefikDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create traefik config directory: %w", err)
	}

	// Write the dynamic config file
	configPath := filepath.Join(traefikDir, "dynamic.yaml")
	if err := os.WriteFile(configPath, []byte(traefikDynamicConfig), 0644); err != nil {
		return "", fmt.Errorf("failed to write traefik config: %w", err)
	}

	return traefikDir, nil
}

// GetDNSConfigDir returns the path to the local DNS fallback config directory
func GetDNSConfigDir() string {
	return filepath.Join(getZdevHome(), "dnsmasq")
}

// EnsureDNSConfig writes the dnsmasq config for the local DNS fallback and
// returns the absolute path to the config FILE (bind-mounted at
// /etc/dnsmasq.conf - the dockurr/dnsmasq entrypoint reads that path). The
// config makes dnsmasq authoritative for *.<domain> -> 127.0.0.1 with no
// upstream: the host resolver only routes <domain> queries here, so
// unrelated names never reach it. Rewritten on every start so a domain
// change is picked up (the container also recreates - the domain is stamped
// as a label, see services.DNSContainerConfig).
func EnsureDNSConfig(domain string) (string, error) {
	dnsDir := GetDNSConfigDir()
	if err := os.MkdirAll(dnsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create dns config directory: %w", err)
	}

	content := fmt.Sprintf("# Managed by zdev - do not edit.\n"+
		"address=/%s/127.0.0.1\n"+
		"no-resolv\n"+
		"no-hosts\n"+
		"cache-size=0\n", domain)

	configPath := filepath.Join(dnsDir, "dnsmasq.conf")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write dns config: %w", err)
	}

	return configPath, nil
}

// GetDocsDir returns the path to the docs directory
func GetDocsDir() string {
	return filepath.Join(getZdevHome(), "docs")
}

// EnsureDocsConfig creates the docs HTML file and Traefik routing config
// Returns the docs directory path
func EnsureDocsConfig(domain string, tlsEnabled bool) (string, error) {
	return EnsureDocsConfigWithProjects(domain, tlsEnabled, nil)
}

// EnsureDocsConfigWithProjects creates the docs HTML file with project information
// Returns the docs directory path
func EnsureDocsConfigWithProjects(domain string, tlsEnabled bool, projects []ProjectInfo) (string, error) {
	docsDir := GetDocsDir()
	traefikDir := GetTraefikConfigDir()

	// Create directories
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create docs directory: %w", err)
	}
	if err := os.MkdirAll(traefikDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create traefik config directory: %w", err)
	}

	// Determine protocol
	protocol := "http"
	if tlsEnabled {
		protocol = "https"
	}

	// Generate projects section HTML
	projectsSection := generateProjectsSection(projects, protocol)

	// Generate docs HTML with variable substitution
	vars := map[string]string{
		"DOMAIN":           domain,
		"PROTOCOL":         protocol,
		"PROJECTS_SECTION": projectsSection,
	}
	docsHTML := substituteConfigVars(docsHTMLTemplate, vars)

	// Write docs HTML
	htmlPath := filepath.Join(docsDir, "index.html")
	if err := os.WriteFile(htmlPath, []byte(docsHTML), 0644); err != nil {
		return "", fmt.Errorf("failed to write docs HTML: %w", err)
	}

	// Generate Traefik docs routing config
	docsConfig := generateDocsTraefikConfig(domain, tlsEnabled)
	configPath := filepath.Join(traefikDir, "docs.yaml")
	if err := os.WriteFile(configPath, []byte(docsConfig), 0644); err != nil {
		return "", fmt.Errorf("failed to write docs traefik config: %w", err)
	}

	return docsDir, nil
}

// generateProjectsSection creates the HTML for the projects section
func generateProjectsSection(projects []ProjectInfo, protocol string) string {
	if len(projects) == 0 {
		return `<div class="section">
            <div class="section-label">Projects</div>
            <p class="no-projects">No projects configured yet. Create a project with <code>.zdev/config.yaml</code> and run <code>zdev start</code>.</p>
        </div>`
	}

	var sb strings.Builder
	sb.WriteString(`<div class="section">
            <div class="section-label">Projects</div>
            <div class="projects-list">
`)

	for _, p := range projects {
		statusClass := "stopped"
		statusText := "stopped"
		if p.Running {
			statusClass = "running"
			statusText = "running"
		}

		url := fmt.Sprintf("%s://%s", protocol, p.Domain)

		sb.WriteString(fmt.Sprintf(`                <a href="%s" class="project-row %s">
                    <div class="project-name">
                        <h3>%s</h3>
                        <span class="project-status %s">%s</span>
                    </div>
                    <div class="project-meta">
                        <span class="url">%s</span>
                        <span class="path">%s</span>
                    </div>
                    <span class="project-open">open &rarr;</span>
                </a>
`, url, statusClass, html.EscapeString(p.Name), statusClass, statusText, html.EscapeString(p.Domain), html.EscapeString(p.Path)))
	}

	sb.WriteString(`            </div>
        </div>`)

	return sb.String()
}

// UpdateDocsProjects updates only the docs HTML with current project information
// This is a lightweight update that doesn't recreate the Traefik config
func UpdateDocsProjects(domain string, tlsEnabled bool, projects []ProjectInfo) error {
	docsDir := GetDocsDir()

	// Ensure directory exists
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		return fmt.Errorf("failed to create docs directory: %w", err)
	}

	// Determine protocol
	protocol := "http"
	if tlsEnabled {
		protocol = "https"
	}

	// Generate projects section HTML
	projectsSection := generateProjectsSection(projects, protocol)

	// Generate docs HTML with variable substitution
	vars := map[string]string{
		"DOMAIN":           domain,
		"PROTOCOL":         protocol,
		"PROJECTS_SECTION": projectsSection,
	}
	docsHTML := substituteConfigVars(docsHTMLTemplate, vars)

	// Write docs HTML
	htmlPath := filepath.Join(docsDir, "index.html")
	if err := os.WriteFile(htmlPath, []byte(docsHTML), 0644); err != nil {
		return fmt.Errorf("failed to write docs HTML: %w", err)
	}

	return nil
}

// generateDocsTraefikConfig creates the Traefik dynamic config for docs routing
func generateDocsTraefikConfig(domain string, tlsEnabled bool) string {
	protocol := "http"
	if tlsEnabled {
		protocol = "https"
	}

	// Escape dots in domain for regex
	escapedDomain := strings.ReplaceAll(domain, ".", "\\\\.")

	// Build config using fmt.Sprintf to handle backticks properly
	docsHost := "docs.shared." + domain
	redirectURL := protocol + "://docs.shared." + domain + "/"

	var sb strings.Builder
	sb.WriteString("# Auto-generated by zdev - do not edit manually\n")
	sb.WriteString("# Docs routing configuration\n\n")
	sb.WriteString("http:\n")
	sb.WriteString("  middlewares:\n")
	sb.WriteString("    # Statiq plugin serves static files from /docs\n")
	sb.WriteString("    zdev-docs:\n")
	sb.WriteString("      plugin:\n")
	sb.WriteString("        statiq:\n")
	sb.WriteString("          root: \"/docs\"\n\n")
	sb.WriteString("    # Redirect middleware for catch-all\n")
	sb.WriteString("    zdev-docs-redirect:\n")
	sb.WriteString("      redirectRegex:\n")
	sb.WriteString("        regex: \".*\"\n")
	sb.WriteString(fmt.Sprintf("        replacement: \"%s\"\n", redirectURL))
	sb.WriteString("        permanent: false\n\n")
	sb.WriteString("  routers:\n")
	sb.WriteString("    # Direct docs access (HTTP)\n")
	sb.WriteString("    zdev-docs-http:\n")
	sb.WriteString(fmt.Sprintf("      rule: \"Host(`%s`)\"\n", docsHost))
	sb.WriteString("      entryPoints:\n")
	sb.WriteString("        - http\n")
	sb.WriteString("      middlewares:\n")
	sb.WriteString("        - zdev-docs\n")
	sb.WriteString("      service: noop@internal\n")
	sb.WriteString("      priority: 100\n\n")
	sb.WriteString("    # Catch-all redirect (HTTP)\n")
	sb.WriteString("    zdev-catchall-http:\n")
	sb.WriteString(fmt.Sprintf("      rule: \"HostRegexp(`.*\\\\.%s`)\"\n", escapedDomain))
	sb.WriteString("      entryPoints:\n")
	sb.WriteString("        - http\n")
	sb.WriteString("      middlewares:\n")
	sb.WriteString("        - zdev-docs-redirect\n")
	sb.WriteString("      service: noop@internal\n")
	sb.WriteString("      priority: 1\n")

	// Add HTTPS routers if TLS is enabled
	if tlsEnabled {
		sb.WriteString("\n    # Direct docs access (HTTPS)\n")
		sb.WriteString("    zdev-docs-https:\n")
		sb.WriteString(fmt.Sprintf("      rule: \"Host(`%s`)\"\n", docsHost))
		sb.WriteString("      entryPoints:\n")
		sb.WriteString("        - https\n")
		sb.WriteString("      middlewares:\n")
		sb.WriteString("        - zdev-docs\n")
		sb.WriteString("      service: noop@internal\n")
		sb.WriteString("      priority: 100\n")
		sb.WriteString("      tls: {}\n\n")
		sb.WriteString("    # Catch-all redirect (HTTPS)\n")
		sb.WriteString("    zdev-catchall-https:\n")
		sb.WriteString(fmt.Sprintf("      rule: \"HostRegexp(`.*\\\\.%s`)\"\n", escapedDomain))
		sb.WriteString("      entryPoints:\n")
		sb.WriteString("        - https\n")
		sb.WriteString("      middlewares:\n")
		sb.WriteString("        - zdev-docs-redirect\n")
		sb.WriteString("      service: noop@internal\n")
		sb.WriteString("      priority: 1\n")
		sb.WriteString("      tls: {}\n")
	}

	return sb.String()
}
