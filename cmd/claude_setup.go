package cmd

// zdev agent setup: walks the developer through what agent sandboxes need
// from them. Claude's login stays the shared /login store (renewed by Claude
// itself, so it can't live in a read-only place); the project's git host
// (GitHub, or GitLab including self-hosted instances, found from the origin
// remote) gets a token per project, limited to the project's repository where
// the host allows it, kept in 1Password or a file only the developer can read,
// and granted in the global config.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/tools"
	"github.com/0ploy/zdev/internal/ui"
)

// githubHosts are where a GitHub token is swapped in: git over HTTPS and the
// API (gh in the sandbox reads GH_TOKEN).
var githubHosts = []string{"github.com", "api.github.com"}

// githubAPI is the API base, replaceable in tests.
var githubAPI = "https://api.github.com"

func runClaudeSetup(ctx context.Context) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("zdev agent setup asks questions: run it in a terminal")
	}
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	plain := ui.PlainMode(gcfg.Terminal.Plain)
	in := bufio.NewReader(os.Stdin)

	ui.StatusStep("Claude Code login", plain)
	describeClaudeLogin(filepath.Join(config.GetZdevHome(), "agent"))

	dir, err := config.FindProjectDir()
	if err != nil {
		fmt.Println("\nRun zdev agent setup in a project to give its sandbox a GitHub or GitLab token.")
		return nil
	}
	pcfg, err := config.LoadProject(dir)
	if err != nil {
		return err
	}
	if err := setupSandboxService(in, plain, gcfg, &project.Project{Dir: dir, Config: pcfg}); err != nil {
		return err
	}
	host, path, ok := originRemote(dir)
	if !ok {
		ui.StatusStep("Git hosting for "+pcfg.Name, plain)
		fmt.Println("The project has no origin remote: nothing to set up.")
		return nil
	}
	if host == "github.com" {
		owner, repo, ok := strings.Cut(path, "/")
		if !ok || strings.Contains(repo, "/") {
			fmt.Printf("origin (%s) is not a GitHub repository: nothing to set up.\n", path)
			return nil
		}
		return setupGitHub(ctx, in, plain, gcfg, pcfg.Name, owner, repo)
	}
	if isGitLab(ctx, host) {
		return setupGitLab(ctx, in, plain, gcfg, pcfg.Name, host, path)
	}
	ui.StatusStep("Git hosting for "+pcfg.Name, plain)
	fmt.Printf("origin is on %s, which zdev agent setup doesn't recognize (it sets up GitHub and GitLab).\n", host)
	fmt.Println("A token can still be granted by hand: agent.credentials in ~/.zdev/global-config.yaml (docs/configuration.md).")
	return nil
}

// setupSandboxService shows which service's image sessions run in, and when
// zdev can't tell on its own (no app service, several that mount the
// project), asks and stores the answer as agent.projects.<project>.service.
func setupSandboxService(in *bufio.Reader, plain bool, gcfg *config.GlobalConfig, proj *project.Project) error {
	name := proj.Config.Name
	ui.StatusStep("Sandbox image for "+name, plain)
	configured := gcfg.AgentProject(name).Service
	service, err := proj.AgentDefaultService(configured)
	switch {
	case err == nil && configured != "":
		fmt.Printf("Sessions run in service %s's image (agent.projects.%s.service); zdev claude <service> picks another.\n", service, name)
		return nil
	case err == nil:
		fmt.Printf("Sessions run in service %s's image; zdev claude <service> picks another.\n", service)
		return nil
	}
	candidates := proj.AgentServiceCandidates()
	if len(candidates) == 0 {
		fmt.Println("No service mounts the project directory, so a sandbox couldn't see the source: add a service with the project mounted (e.g. ${PROJECTPATH}:/app).")
		return nil
	}
	fmt.Println("There is no service named app, and several mount the project. Which one's image should sessions run in?")
	for i, c := range candidates {
		fmt.Printf("  %d) %s (%s)\n", i+1, c, proj.Config.Services[c].Image)
	}
	fmt.Printf("  %d) Decide each time (zdev claude <service>)\n", len(candidates)+1)
	var n int
	if _, err := fmt.Sscan(ask(in, "Choice [1]: ")+" ", &n); err != nil {
		n = 1
	}
	if n < 1 || n > len(candidates) {
		return nil
	}
	choice := candidates[n-1]
	if err := config.EditGlobalConfig(func(root *yaml.Node) error {
		m, err := config.YAMLMapping(root, "agent", "projects", name)
		if err != nil {
			return err
		}
		return config.SetYAML(m, "service", choice)
	}); err != nil {
		return err
	}
	fmt.Printf("Sessions run in service %s's image (stored as agent.projects.%s.service).\n", choice, name)
	return nil
}

// setupGitHub offers the GitHub credentials for owner/repo.
func setupGitHub(ctx context.Context, in *bufio.Reader, plain bool, gcfg *config.GlobalConfig, project, owner, repo string) error {
	ui.StatusStep("GitHub for "+project, plain)
	fmt.Printf("Repository: %s/%s\n", owner, repo)
	if name := grantedFor(gcfg, project, "github.com"); name != "" {
		fmt.Printf("The sandbox gets credential %s (%s).\n", name, describeSource(gcfg.Agent.Credentials[name]))
		if !askYes(in, "Replace it? [y/N] ", false) {
			return nil
		}
	}

	fmt.Print(`
How should the sandbox reach GitHub?
  1) A fine-grained token for this repository only (recommended)
  2) Your gh login (gh auth token): every repository you can reach, no expiry
  3) Nothing for now
`)
	switch ask(in, "Choice [1]: ") {
	case "", "1":
		return setupGitHubToken(ctx, in, plain, project, owner, repo)
	case "2":
		if _, ok := tools.FindInPath("gh"); !ok {
			return errors.New("gh is not installed")
		}
		name := "github-gh"
		cred := config.AgentCredential{Hosts: githubHosts, Env: []string{"GH_TOKEN"}, Command: "gh auth token"}
		if err := grantCredential(project, name, cred); err != nil {
			return err
		}
		return verifyGitHubCredential(ctx, name, cred, owner, repo)
	}
	return nil
}

func setupGitHubToken(ctx context.Context, in *bufio.Reader, plain bool, project, owner, repo string) error {
	name := "github-" + credentialSlug(project)
	q := url.Values{}
	q.Set("name", truncate("zdev "+project, 40))
	q.Set("description", "zdev claude sandbox of "+project+" ("+owner+"/"+repo+")")
	q.Set("target_name", owner)
	q.Set("expires_in", "90")
	q.Set("contents", "write")
	q.Set("pull_requests", "write")
	q.Set("issues", "read")
	q.Set("actions", "read")
	link := "https://github.com/settings/personal-access-tokens/new?" + q.Encode()
	fmt.Printf(`
GitHub's page for a new fine-grained token comes pre-filled, except for one
thing a link can't set:

  Repository access: choose "Only select repositories" and pick %s/%s

Then "Generate token" and paste it here.

  %s

`, owner, repo, link)
	if askYes(in, "Open it in the browser? [Y/n] ", true) {
		_ = openBrowser(link)
	}
	var token string
	for {
		var err error
		if token, err = readSecret(in, "Paste the token (shown as *, Enter when done): "); err != nil {
			return err
		}
		switch {
		case token == "":
			fmt.Println("Nothing pasted.")
		case strings.Count(token, "github_pat_")+strings.Count(token, "ghp_") > 1:
			fmt.Println("That is more than one token (pasted twice?). Paste it once.")
			token = ""
		default:
			ui.StatusStep("Checking the token", plain)
			if err = checkGitHubToken(ctx, token, owner, repo); err == nil {
				break
			}
			fmt.Println(err)
			token = ""
		}
		if token != "" {
			break
		}
		if !askYes(in, "Try again? [Y/n] ", true) {
			return errors.New("no token set up")
		}
	}
	others, err := otherGitHubRepos(ctx, token, owner, owner+"/"+repo)
	if err != nil {
		fmt.Printf("Couldn't list the token's repositories: %v\n", err)
	} else if len(others) > 0 {
		fmt.Printf("The token also opens other private repositories of %s (%s): Repository access is not limited to %s.\n", owner, strings.Join(others, ", "), repo)
		if !askYes(in, "Keep it anyway? [y/N] ", false) {
			return errors.New("set Repository access to \"Only select repositories\" (on the token's page: Edit) and run zdev agent setup again")
		}
	}

	cred := config.AgentCredential{Hosts: githubHosts, Env: []string{"GH_TOKEN"}}
	op := secrets.NewOnePasswordCLI()
	if _, found := tools.FindInPath("op"); found && askYes(in, "Store it in 1Password? [Y/n] ", true) {
		ref, err := storeIn1Password(ctx, in, op, "zdev "+name, token, "github.com",
			"GitHub token for zdev claude sandboxes of "+owner+"/"+repo+", swapped in by zdev's egress proxy. Created by zdev agent setup.")
		if err != nil {
			return err
		}
		cred.Secret = ref
	} else {
		path, err := storeInFile(name, token)
		if err != nil {
			return err
		}
		cred.File = "~/" + strings.TrimPrefix(path, homeDir()+"/")
		fmt.Printf("Stored in %s (only you can read it).\n", path)
	}
	if err := grantCredential(project, name, cred); err != nil {
		return err
	}
	return verifyGitHubCredential(ctx, name, cred, owner, repo)
}

// storeIn1Password creates an API Credential item in a vault the developer
// picks and returns its reference by IDs (renames don't break it). The token
// goes to op on stdin, never on its command line.
func storeIn1Password(ctx context.Context, in *bufio.Reader, op *secrets.OnePasswordCLI, title, token, host, notes string) (string, error) {
	out, err := op.Op(ctx, nil, "vault", "list", "--permission", "create_items", "--format", "json")
	if err != nil {
		return "", err
	}
	var vaults []struct{ ID, Name string }
	if err := json.Unmarshal(out, &vaults); err != nil {
		return "", fmt.Errorf("op vault list: %w", err)
	}
	if len(vaults) == 0 {
		return "", errors.New("1Password has no vault you can create items in")
	}
	v := vaults[0]
	if len(vaults) > 1 {
		fmt.Println("Vault:")
		for i, vv := range vaults {
			fmt.Printf("  %d) %s\n", i+1, vv.Name)
		}
		for {
			var n int
			if _, err := fmt.Sscan(ask(in, "Choice [1]: ")+" ", &n); err != nil {
				n = 1
			}
			if n >= 1 && n <= len(vaults) {
				v = vaults[n-1]
				break
			}
		}
	}
	item := map[string]any{
		"title":    title,
		"category": "API_CREDENTIAL",
		"fields": []map[string]string{
			{"id": "credential", "type": "CONCEALED", "label": "credential", "value": token},
			{"id": "hostname", "type": "STRING", "label": "hostname", "value": host},
			{"id": "notesPlain", "type": "STRING", "purpose": "NOTES", "label": "notesPlain", "value": notes},
		},
	}
	tmpl, _ := json.Marshal(item)
	out, err = op.Op(ctx, tmpl, "item", "create", "--vault", v.ID, "--format", "json", "-")
	if err != nil {
		return "", err
	}
	var created struct {
		ID    string
		Vault struct{ ID string }
	}
	if err := json.Unmarshal(out, &created); err != nil || created.ID == "" {
		return "", errors.New("op item create: unexpected output")
	}
	fmt.Printf("Stored in 1Password: %q in vault %s.\n", title, v.Name)
	return fmt.Sprintf("op://%s/%s/credential", created.Vault.ID, created.ID), nil
}

func storeInFile(name, token string) (string, error) {
	dir := filepath.Join(config.GetZdevHome(), "agent", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// grantCredential writes the credential and grants it to the project in
// place of any other credential it had for the same host.
func grantCredential(project, name string, cred config.AgentCredential) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	// From what the project gets now: a project without its own list gets
	// the defaults, and its new list replaces them, so they must carry over.
	grants := []string{name}
	for _, c := range gcfg.AgentProject(project).Credentials {
		if c != name && !sharesHost(gcfg.Agent.Credentials[c], cred.Hosts) {
			grants = append(grants, c)
		}
	}
	return config.EditGlobalConfig(func(root *yaml.Node) error {
		creds, err := config.YAMLMapping(root, "agent", "credentials")
		if err != nil {
			return err
		}
		if err := config.SetYAML(creds, name, cred); err != nil {
			return err
		}
		p, err := config.YAMLMapping(root, "agent", "projects", project)
		if err != nil {
			return err
		}
		return config.SetYAML(p, "credentials", grants)
	})
}

// verifyGitHubCredential reads the credential the way a session does and
// asks GitHub who it belongs to.
func verifyGitHubCredential(ctx context.Context, name string, cred config.AgentCredential, owner, repo string) error {
	var op *secrets.OnePasswordCLI
	token, err := credentialValue(ctx, cred, &op)
	if err != nil {
		return fmt.Errorf("reading %s back: %w", name, err)
	}
	if err := checkGitHubToken(ctx, token, owner, repo); err != nil {
		return err
	}
	fmt.Printf("\nDone: sandboxes of this project get %s for %s (git over HTTPS and gh).\n", name, strings.Join(cred.Hosts, ", "))
	fmt.Println("It is in ~/.zdev/global-config.yaml under agent.credentials and agent.projects.")
	return nil
}

// checkGitHubToken requires the token to reach the repository with push.
func checkGitHubToken(ctx context.Context, token, owner, repo string) error {
	var user struct{ Login string }
	if err := githubGet(ctx, token, "/user", &user); err != nil {
		return err
	}
	var r struct {
		FullName    string `json:"full_name"`
		Permissions struct{ Push bool }
	}
	if err := githubGet(ctx, token, "/repos/"+owner+"/"+repo, &r); err != nil {
		return fmt.Errorf("the token can't read %s/%s: %w", owner, repo, err)
	}
	if !r.Permissions.Push {
		fmt.Printf("Note: the token reads %s but can't push (Contents: read only?).\n", r.FullName)
	}
	fmt.Printf("Token of %s, reaches %s.\n", user.Login, r.FullName)
	return nil
}

// otherGitHubRepos returns private repositories of owner besides want that
// the token can open. GitHub's repository list is the user's, not the
// token's (it lists every repository the account sees, whatever the token
// selects), so it only supplies candidates: each is opened with the token,
// and a token limited to want gets 404 for all of them. Public repositories
// say nothing (every token reads them), so they aren't probed.
func otherGitHubRepos(ctx context.Context, token, owner, want string) ([]string, error) {
	var repos []struct {
		FullName string `json:"full_name"`
		Private  bool
		Owner    struct{ Login string }
	}
	if err := githubGet(ctx, token, "/user/repos?per_page=100&sort=pushed", &repos); err != nil {
		return nil, err
	}
	var out []string
	probed := 0
	for _, r := range repos {
		if !r.Private || !strings.EqualFold(r.Owner.Login, owner) || strings.EqualFold(r.FullName, want) {
			continue
		}
		if probed++; probed > 3 {
			break
		}
		var v struct{}
		if err := githubGet(ctx, token, "/repos/"+r.FullName, &v); err == nil {
			out = append(out, r.FullName)
		}
	}
	return out, nil
}

func githubGet(ctx context.Context, token, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(res.Body).Decode(v)
	case http.StatusUnauthorized:
		return errors.New("GitHub rejects the token (401): wrong, expired or revoked")
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("GitHub says %d: the token has no access (wrong repository selected, or the organization hasn't approved it yet)", res.StatusCode)
	}
	return fmt.Errorf("GitHub says %d", res.StatusCode)
}

// remoteURL splits a git remote into host and repository path:
// https://[user@]host[:port]/path, ssh://[user@]host[:port]/path and
// user@host:path (scp-like), each with an optional .git.
var remoteURL = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?([^/:@]+)(?::\d+)?/|ssh://(?:[^@/]+@)?([^/:@]+)(?::\d+)?/|[^@/:]+@([^/:@]+):)(.+?)(?:\.git)?/?$`)

func parseRemote(remote string) (host, path string, ok bool) {
	m := remoteURL.FindStringSubmatch(remote)
	if m == nil {
		return "", "", false
	}
	host = strings.ToLower(m[1] + m[2] + m[3])
	if host == "" || m[4] == "" {
		return "", "", false
	}
	return host, m[4], true
}

// originRemote is the host and repository path of the project's origin.
func originRemote(dir string) (host, path string, ok bool) {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", "", false
	}
	return parseRemote(strings.TrimSpace(string(out)))
}

// sharesHost reports whether c covers any of hosts.
func sharesHost(c config.AgentCredential, hosts []string) bool {
	for _, h := range c.Hosts {
		for _, want := range hosts {
			if h == want {
				return true
			}
		}
	}
	return false
}

// grantedFor is the credential for host the project's sandbox gets, if any.
func grantedFor(gcfg *config.GlobalConfig, project, host string) string {
	for _, c := range gcfg.AgentProject(project).Credentials {
		if sharesHost(gcfg.Agent.Credentials[c], []string{host}) {
			return c
		}
	}
	return ""
}

func describeSource(c config.AgentCredential) string {
	switch {
	case c.Command != "":
		return "from `" + c.Command + "`"
	case strings.HasPrefix(c.Secret, "op://"):
		return "1Password item " + c.Secret
	case c.Secret != "":
		return "1Password Environment " + c.Secret
	}
	return "file " + c.File
}

// describeClaudeLogin reports whether the shared login store holds a login,
// without printing anything from it but the plan.
func describeClaudeLogin(agentDir string) {
	path := filepath.Join(agent.AuthDir(agentDir), ".credentials.json")
	var store struct {
		OAuth *struct {
			RefreshToken     string `json:"refreshToken"`
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &store)
	}
	if err != nil || store.OAuth == nil || store.OAuth.RefreshToken == "" {
		fmt.Println("Not logged in yet. Start `zdev claude` in a project and type /login: every sandbox shares that login.")
		return
	}
	plan := store.OAuth.SubscriptionType
	if plan == "" {
		plan = "unknown plan"
	}
	fmt.Printf("Logged in (%s), shared by every sandbox: %s\n", plan, path)
	fmt.Println("Claude renews it in that file (only you can read it). To switch accounts, use /login in a sandbox.")
}

func credentialSlug(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// readSecret reads a line without showing it: one * per character, so a
// paste is visibly received. Backspace and Ctrl-U edit, Ctrl-C cancels.
func readSecret(in *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = term.Restore(fd, state)
		fmt.Print("\r\n")
	}()
	var buf []rune
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return "", err
		}
		switch r {
		case '\r', '\n':
			return strings.TrimSpace(string(buf)), nil
		case 3: // Ctrl-C
			return "", errors.New("cancelled")
		case 127, 8: // backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Print("\b \b")
			}
		case 21: // Ctrl-U
			fmt.Print(strings.Repeat("\b \b", len(buf)))
			buf = buf[:0]
		default:
			if r >= ' ' {
				buf = append(buf, r)
				fmt.Print("*")
			}
		}
	}
}

func ask(in *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}

func askYes(in *bufio.Reader, prompt string, def bool) bool {
	switch strings.ToLower(ask(in, prompt)) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
