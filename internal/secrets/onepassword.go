package secrets

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/0ploy/zdev/internal/tools"
)

// OnePasswordCLI resolves op-env:// references by shelling out to the
// 1Password CLI (`op environment read`, beta). The binary is
// user-installed and looked up in PATH lazily on first Resolve, so
// projects without secret references never require it. One Resolve call
// fetches the whole Environment in a single op invocation (one auth
// prompt no matter how many variables); the parsed set is cached in
// memory for the lifetime of the process and never persisted.
type OnePasswordCLI struct {
	mu         sync.Mutex
	binary     string
	cache      map[string]map[string]string // envID -> KEY -> value
	sessionEnv []string                     // OP_SESSION_* captured from interactive signin, memory only

	// Seams injected by tests; nil selects the real implementation.
	lookPath      func(name string) (string, bool)
	isInteractive func() bool
	confirm       func(prompt string) bool
	runRead       func(ctx context.Context, binary, envID string, extraEnv []string) (stdout, stderr string, err error)
	runInstall    func(ctx context.Context) error
	runAccountAdd func(ctx context.Context, binary string) error
	runSignin     func(ctx context.Context, binary string) (token string, err error)
	listAccounts  func(ctx context.Context, binary string) ([]opAccount, error)
}

type opAccount struct {
	UserUUID string `json:"user_uuid"`
}

// NewOnePasswordCLI creates a resolver. It performs no PATH lookup and no
// op invocation until Resolve is first called with references.
func NewOnePasswordCLI() *OnePasswordCLI {
	return &OnePasswordCLI{cache: make(map[string]map[string]string)}
}

// Resolve fetches the 1Password Environment (once, cached) and returns
// the values for the requested variable names.
func (o *OnePasswordCLI) Resolve(ctx context.Context, envID string, keys []string) (map[string]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.cache == nil {
		o.cache = make(map[string]map[string]string)
	}

	vars, ok := o.cache[envID]
	if !ok {
		if err := o.ensureBinary(ctx); err != nil {
			return nil, err
		}
		fetched, err := o.readEnvironment(ctx, envID)
		if err != nil {
			return nil, err
		}
		o.cache[envID] = fetched
		vars = fetched
	}

	result := make(map[string]string, len(keys))
	var missing []string
	seen := make(map[string]bool)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		val, ok := vars[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		if val == "" {
			fmt.Printf("Warning: 1Password Environment variable %s is empty\n", key)
		}
		result[key] = val
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("variable(s) %s not found in 1Password Environment %s - check the Environment in the 1Password app (Developer > Environments)", strings.Join(missing, ", "), envID)
	}

	return result, nil
}

// ensureBinary locates op in PATH, offering a Homebrew install on an
// interactive terminal when it is missing. Environments require the
// beta CLI build.
func (o *OnePasswordCLI) ensureBinary(ctx context.Context) error {
	if o.binary != "" {
		return nil
	}

	lookPath := o.lookPath
	if lookPath == nil {
		lookPath = tools.FindInPath
	}

	if path, found := lookPath("op"); found {
		o.binary = path
		return nil
	}

	// Offer to install via Homebrew, but only on an interactive terminal
	// (never hang CI) and only when brew itself is available.
	if _, brewFound := lookPath("brew"); brewFound && o.interactive() {
		if o.ask("1Password CLI (op) is required - this project's config uses op-env:// secret references.\nInstall it now via Homebrew? [y/N] ") {
			if err := o.installViaBrew(ctx); err != nil {
				return fmt.Errorf("failed to install 1Password CLI: %w", err)
			}
			if path, found := lookPath("op"); found {
				o.binary = path
				return nil
			}
		}
	}

	return fmt.Errorf("1Password CLI (op) not found in PATH - this project's config uses op-env:// secret references\n\nInstall the beta build (Environments require it): brew install 1password-cli@beta\nDocs: https://www.1password.dev/environments")
}

// readEnvironment runs op environment read, driving an interactive
// signin and retrying exactly once when the failure is auth-shaped.
func (o *OnePasswordCLI) readEnvironment(ctx context.Context, envID string) (map[string]string, error) {
	run := o.runRead
	if run == nil {
		run = defaultRunRead
	}

	stdout, stderr, err := run(ctx, o.binary, envID, o.sessionEnv)
	if err != nil && isAuthError(stderr) && o.interactive() {
		if signinErr := o.recoverAuth(ctx); signinErr == nil {
			stdout, stderr, err = run(ctx, o.binary, envID, o.sessionEnv)
		}
	}
	if err != nil {
		return nil, wrapReadError(stderr, err)
	}

	return parseEnvironmentOutput(stdout), nil
}

// recoverAuth drives the interactive path back to a signed-in state:
// account setup if none is configured, then op signin. A captured session
// token (standalone CLI mode) is held in memory for subsequent calls; in
// desktop-app integration mode signin itself authorizes the CLI.
func (o *OnePasswordCLI) recoverAuth(ctx context.Context) error {
	if !o.ask("You are not signed in to 1Password. Sign in now? [y/N] ") {
		return fmt.Errorf("signin declined")
	}

	list := o.listAccounts
	if list == nil {
		list = defaultListAccounts
	}
	accounts, err := list(ctx, o.binary)
	if err != nil {
		accounts = nil
	}

	if len(accounts) == 0 {
		addAccount := o.runAccountAdd
		if addAccount == nil {
			addAccount = defaultRunAccountAdd
		}
		if err := addAccount(ctx, o.binary); err != nil {
			return fmt.Errorf("op account add failed: %w", err)
		}
		if accounts, err = list(ctx, o.binary); err != nil {
			accounts = nil
		}
	}

	signin := o.runSignin
	if signin == nil {
		signin = defaultRunSignin
	}
	token, err := signin(ctx, o.binary)
	if err != nil {
		return fmt.Errorf("op signin failed: %w", err)
	}

	// Standalone CLI mode prints a session token that is only valid for
	// processes that carry it in env. With desktop-app integration the
	// token is empty and the signin itself unlocked the CLI.
	if token != "" && len(accounts) > 0 {
		o.sessionEnv = append(o.sessionEnv, fmt.Sprintf("OP_SESSION_%s=%s", accounts[0].UserUUID, token))
	}

	return nil
}

func (o *OnePasswordCLI) interactive() bool {
	if o.isInteractive != nil {
		return o.isInteractive()
	}
	// term.IsTerminal, not an os.ModeCharDevice stat check: /dev/null is
	// a char device too and would make piped/CI runs look interactive.
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func (o *OnePasswordCLI) ask(prompt string) bool {
	if o.confirm != nil {
		return o.confirm(prompt)
	}
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

func (o *OnePasswordCLI) installViaBrew(ctx context.Context) error {
	if o.runInstall != nil {
		return o.runInstall(ctx)
	}
	cmd := exec.CommandContext(ctx, "brew", "install", "1password-cli@beta")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// authErrorPatterns are stderr fragments op emits when the failure is a
// missing/expired session rather than a bad Environment ID. Matched
// lowercase.
var authErrorPatterns = []string{
	"not currently signed in",
	"not signed in",
	"no accounts configured",
	"no account found",
	"authorization prompt dismissed",
	"authorization timeout",
	"session expired",
	"you are not authorized",
}

func isAuthError(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, pattern := range authErrorPatterns {
		if strings.Contains(s, pattern) {
			return true
		}
	}
	return false
}

// wrapReadError surfaces op's stderr (never stdout, which may hold
// secret values) with remediation hints for the known failure classes.
func wrapReadError(stderr string, err error) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	if isAuthError(stderr) {
		return fmt.Errorf("1Password secret resolution failed: %s\n\nSign in with: op signin\nOr enable the desktop app integration: 1Password > Settings > Developer > Integrate with 1Password CLI\nFor non-interactive use, set OP_SERVICE_ACCOUNT_TOKEN", detail)
	}
	// The stable CLI build predates Environments; only the beta has the
	// `op environment` command family.
	if strings.Contains(strings.ToLower(detail), "unknown command") {
		return fmt.Errorf("1Password secret resolution failed: the installed op CLI does not support Environments\n\nInstall the beta build: brew install 1password-cli@beta")
	}
	return fmt.Errorf("1Password secret resolution failed: %s", detail)
}

// parseEnvironmentOutput parses op environment read output: one
// KEY=VALUE per line, no quoting. Duplicate keys keep the last value
// (dotenv convention; 1Password does not deduplicate). Lines without
// '=' are skipped. Multiline values are not representable in this
// format and therefore unsupported.
func parseEnvironmentOutput(out string) map[string]string {
	vars := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		vars[key] = val
	}
	return vars
}

func defaultRunRead(ctx context.Context, binary, envID string, extraEnv []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, binary, "environment", "read", envID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func defaultRunAccountAdd(ctx context.Context, binary string) error {
	cmd := exec.CommandContext(ctx, binary, "account", "add")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func defaultRunSignin(ctx context.Context, binary string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, "signin", "--raw")
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func defaultListAccounts(ctx context.Context, binary string) ([]opAccount, error) {
	cmd := exec.CommandContext(ctx, binary, "account", "list", "--format=json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var accounts []opAccount
	if err := json.Unmarshal(stdout.Bytes(), &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}
