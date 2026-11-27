package cmd

// zdev agent credentials: the tokens the egress proxy swaps in for agent
// sandboxes (agent.credentials in ~/.zdev/global-config.yaml) and which
// projects get them. Values are never printed: list describes where a token
// comes from, test says only whether it can be read.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/tools"
)

// credentialName is what agent.credentials accepts as a name.
var credentialName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// credentialPreset fills hosts and variables for a common service.
type credentialPreset struct {
	Name, Title string
	Hosts, Env  []string
	Note        string
}

var credentialPresets = []credentialPreset{
	{Name: "npm", Title: "npm registry", Hosts: []string{"registry.npmjs.org"}, Env: []string{"NPM_TOKEN"},
		Note: "npm reads it when the project's .npmrc has: //registry.npmjs.org/:_authToken=${NPM_TOKEN}"},
}

var agentCredentialsCmd = &cobra.Command{
	Use:   "credentials",
	Short: "Manage the tokens agent sandboxes get (swapped in by the egress proxy)",
	Long: `Manage the tokens agent sandboxes get.

A credential is a token from a command, 1Password or a file only you can read,
for a list of hosts. The sandbox sees a placeholder in the credential's
variables; the egress proxy on this machine swaps in the real token for those
hosts only, in request headers (Authorization, Basic auth, API key headers).
Tokens a tool sends in the URL or the request body, or uses to sign requests
(AWS), are not swapped.

Credentials and grants live in ~/.zdev/global-config.yaml (agent.credentials,
agent.projects.<project>.credentials, agent.defaults.credentials), never in the
project config: the agent can edit that one.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error { return runCredentialsList(cmd.OutOrStdout()) },
}

var agentCredentialsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List credentials, where each comes from and which projects get it",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return runCredentialsList(cmd.OutOrStdout()) },
}

var credAddFlags struct {
	hosts, env               []string
	command, secret, file    string
	preset                   string
	grant, noGrant, grantAll bool
}

var agentCredentialsAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a credential (asks for what isn't given as flags)",
	Long: `Add a credential. Without flags it asks: which hosts, which variables, and where
the token comes from (paste it and store it in 1Password or a file only you can
read, an existing 1Password reference, or a command). With --host and one of
--command, --secret or --file it runs without questions.

  zdev agent credentials add npm --preset npm
  zdev agent credentials add sentry --host sentry.io --env SENTRY_AUTH_TOKEN --secret op://Dev/Sentry/credential`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return runCredentialsAdd(cmd.Context(), args[0]) },
}

var credGrantAll bool

var agentCredentialsGrantCmd = &cobra.Command{
	Use:   "grant <name>",
	Short: "Give this project's sandboxes a credential (--all: every project)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCredentialsGrant(cmd.OutOrStdout(), args[0], credGrantAll, true)
	},
}

var agentCredentialsRevokeCmd = &cobra.Command{
	Use:   "revoke <name>",
	Short: "Take a credential away from this project's sandboxes (--all: from the defaults)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCredentialsGrant(cmd.OutOrStdout(), args[0], credGrantAll, false)
	},
}

var credRemoveYes bool

var agentCredentialsRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Delete a credential and every grant of it",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return runCredentialsRemove(cmd.OutOrStdout(), args[0]) },
}

var agentCredentialsTestCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Read a credential the way a session does (never prints it)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCredentialsTest(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	f := agentCredentialsAddCmd.Flags()
	f.StringSliceVar(&credAddFlags.hosts, "host", nil, "host the token is for (repeatable; *.example.com for subdomains)")
	f.StringSliceVar(&credAddFlags.env, "env", nil, "variable that holds the placeholder (repeatable)")
	f.StringVar(&credAddFlags.command, "command", "", "command that prints the token (e.g. \"gh auth token\")")
	f.StringVar(&credAddFlags.secret, "secret", "", "1Password reference: op://<vault>/<item>/<field> or op-env://<environment-id>/<VARIABLE>")
	f.StringVar(&credAddFlags.file, "file", "", "file holding the token (mode 0600, yours)")
	f.StringVar(&credAddFlags.preset, "preset", "", "fill hosts and variables for a known service: "+presetNames())
	f.BoolVar(&credAddFlags.grant, "grant", false, "grant it to this project without asking")
	f.BoolVar(&credAddFlags.noGrant, "no-grant", false, "don't grant it")
	f.BoolVar(&credAddFlags.grantAll, "grant-all", false, "grant it to every project (agent.defaults)")
	agentCredentialsGrantCmd.Flags().BoolVar(&credGrantAll, "all", false, "every project (agent.defaults)")
	agentCredentialsRevokeCmd.Flags().BoolVar(&credGrantAll, "all", false, "from agent.defaults")
	agentCredentialsRemoveCmd.Flags().BoolVarP(&credRemoveYes, "yes", "y", false, "don't ask")
	agentCredentialsCmd.AddCommand(agentCredentialsListCmd, agentCredentialsAddCmd, agentCredentialsGrantCmd,
		agentCredentialsRevokeCmd, agentCredentialsRemoveCmd, agentCredentialsTestCmd)
}

func presetNames() string {
	var n []string
	for _, p := range credentialPresets {
		n = append(n, p.Name)
	}
	return strings.Join(n, ", ")
}

// currentProjectName is the project in the working directory, if any.
func currentProjectName() (string, bool) {
	dir, err := config.FindProjectDir()
	if err != nil {
		return "", false
	}
	pcfg, err := config.LoadProject(dir)
	if err != nil {
		return "", false
	}
	return pcfg.Name, true
}

func runCredentialsList(w io.Writer) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	if len(gcfg.Agent.Credentials) == 0 {
		fmt.Fprintln(w, "No credentials yet. zdev agent setup (in a project) sets up GitHub or GitLab;")
		fmt.Fprintln(w, "zdev agent credentials add <name> adds any other token.")
		return nil
	}
	current, inProject := currentProjectName()
	effective := map[string]bool{}
	if inProject {
		for _, c := range gcfg.AgentProject(current).Credentials {
			effective[c] = true
		}
	}
	names := make([]string, 0, len(gcfg.Agent.Credentials))
	for n := range gcfg.Agent.Credentials {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := gcfg.Agent.Credentials[n]
		mark := ""
		if effective[n] {
			mark = "  (this project gets it)"
		}
		fmt.Fprintf(w, "%s%s\n", n, mark)
		fmt.Fprintf(w, "  hosts:   %s\n", strings.Join(c.Hosts, ", "))
		if len(c.Env) > 0 {
			fmt.Fprintf(w, "  env:     %s\n", strings.Join(c.Env, ", "))
		}
		fmt.Fprintf(w, "  source:  %s\n", describeSource(c))
		fmt.Fprintf(w, "  granted: %s\n", describeGrants(gcfg, n))
	}
	if own := projectsWithOwnList(gcfg); len(own) > 0 && len(gcfg.Agent.Defaults.Credentials) > 0 {
		fmt.Fprintf(w, "\nProjects with their own list don't get the defaults: %s\n", strings.Join(own, ", "))
	}
	return nil
}

func describeGrants(gcfg *config.GlobalConfig, name string) string {
	var where []string
	for _, c := range gcfg.Agent.Defaults.Credentials {
		if c == name {
			where = append(where, "every project (defaults)")
		}
	}
	var projects []string
	for p, pc := range gcfg.Agent.Projects {
		for _, c := range pc.Credentials {
			if c == name {
				projects = append(projects, p)
			}
		}
	}
	sort.Strings(projects)
	where = append(where, projects...)
	if len(where) == 0 {
		return "no project"
	}
	return strings.Join(where, ", ")
}

// projectsWithOwnList are the projects whose credentials list replaces the
// defaults.
func projectsWithOwnList(gcfg *config.GlobalConfig) []string {
	var out []string
	for p, pc := range gcfg.Agent.Projects {
		if pc.Credentials != nil {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func runCredentialsAdd(ctx context.Context, name string) error {
	if !credentialName.MatchString(name) {
		return fmt.Errorf("%q: use lowercase letters, digits and -", name)
	}
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	if _, exists := gcfg.Agent.Credentials[name]; exists {
		return fmt.Errorf("credential %s exists already: zdev agent credentials remove %s first", name, name)
	}
	f := credAddFlags
	cred := config.AgentCredential{Hosts: f.hosts, Env: f.env, Command: f.command, Secret: f.secret, File: f.file}
	var note string
	if f.preset != "" {
		p, ok := findPreset(f.preset)
		if !ok {
			return fmt.Errorf("unknown preset %q (known: %s)", f.preset, presetNames())
		}
		if len(cred.Hosts) == 0 {
			cred.Hosts = p.Hosts
		}
		if len(cred.Env) == 0 {
			cred.Env = p.Env
		}
		note = p.Note
	}
	sources := 0
	for _, s := range []string{cred.Command, cred.Secret, cred.File} {
		if s != "" {
			sources++
		}
	}
	if sources > 1 {
		return errors.New("give only one of --command, --secret and --file")
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	in := bufio.NewReader(os.Stdin)
	if len(cred.Hosts) == 0 || sources == 0 {
		if !interactive {
			return errors.New("not a terminal: give --host and one of --command, --secret, --file (or --preset)")
		}
		if err := askCredential(ctx, in, name, &cred, &note); err != nil {
			return err
		}
	}
	if err := checkCredential(name, cred); err != nil {
		return err
	}

	// Grant: flags decide; otherwise ask in a project, else leave it.
	project, inProject := currentProjectName()
	grantProject, grantAll := f.grant, f.grantAll
	if !f.noGrant && !grantProject && !grantAll && interactive {
		if inProject {
			grantProject = askYes(in, fmt.Sprintf("Grant it to %s? [Y/n] ", project), true)
		} else {
			grantAll = askYes(in, "Grant it to every project? [y/N] ", false)
		}
	}
	if grantProject && !inProject {
		return errors.New("--grant needs a project: run it in one, or use --grant-all")
	}
	err = config.EditGlobalConfig(func(root *yaml.Node) error {
		creds, err := config.YAMLMapping(root, "agent", "credentials")
		if err != nil {
			return err
		}
		return config.SetYAML(creds, name, cred)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Added %s for %s (%s).\n", name, strings.Join(cred.Hosts, ", "), describeSource(cred))
	switch {
	case grantAll:
		if err := runCredentialsGrant(os.Stdout, name, true, true); err != nil {
			return err
		}
	case grantProject:
		if err := runCredentialsGrant(os.Stdout, name, false, true); err != nil {
			return err
		}
	default:
		fmt.Printf("Not granted yet: zdev agent credentials grant %s (in a project, or --all).\n", name)
	}
	if note != "" {
		fmt.Println(note)
	}
	return nil
}

func findPreset(name string) (credentialPreset, bool) {
	for _, p := range credentialPresets {
		if p.Name == name {
			return p, true
		}
	}
	return credentialPreset{}, false
}

// askCredential fills what the flags left open.
func askCredential(ctx context.Context, in *bufio.Reader, name string, cred *config.AgentCredential, note *string) error {
	if len(cred.Hosts) == 0 {
		fmt.Println("What is the token for?")
		for i, p := range credentialPresets {
			fmt.Printf("  %d) %s (%s)\n", i+1, p.Title, strings.Join(p.Hosts, ", "))
		}
		fmt.Printf("  %d) Something else\n", len(credentialPresets)+1)
		var n int
		if _, err := fmt.Sscan(ask(in, fmt.Sprintf("Choice [%d]: ", len(credentialPresets)+1))+" ", &n); err == nil && n >= 1 && n <= len(credentialPresets) {
			p := credentialPresets[n-1]
			cred.Hosts, *note = p.Hosts, p.Note
			if len(cred.Env) == 0 {
				cred.Env = p.Env
			}
		} else {
			fmt.Println(`The proxy swaps the token in only for these hosts, and only in request headers.`)
			for len(cred.Hosts) == 0 {
				cred.Hosts = splitList(ask(in, "Hosts (comma-separated, *.example.com for subdomains): "))
			}
			if len(cred.Env) == 0 {
				cred.Env = splitList(ask(in, "Variables the tool reads the token from (comma-separated, e.g. SENTRY_AUTH_TOKEN): "))
			}
		}
	}
	if cred.Command != "" || cred.Secret != "" || cred.File != "" {
		return nil
	}
	fmt.Print(`
Where does the token come from?
  1) Paste it now (stored in 1Password or a file only you can read)
  2) A 1Password reference (op://<vault>/<item>/<field> or op-env://<environment-id>/<VARIABLE>)
  3) A command that prints it (e.g. "gh auth token")
`)
	switch ask(in, "Choice [1]: ") {
	case "2":
		cred.Secret = ask(in, "Reference: ")
	case "3":
		cred.Command = ask(in, "Command: ")
	default:
		token, err := readSecret(in, "Paste the token (shown as *, Enter when done): ")
		if err != nil {
			return err
		}
		if token == "" {
			return errors.New("nothing pasted")
		}
		if _, found := tools.FindInPath("op"); found && askYes(in, "Store it in 1Password? [Y/n] ", true) {
			ref, err := storeIn1Password(ctx, in, secrets.NewOnePasswordCLI(), "zdev "+name, token, cred.Hosts[0],
				"Token for zdev claude sandboxes ("+strings.Join(cred.Hosts, ", ")+"), swapped in by zdev's egress proxy. Created by zdev agent credentials.")
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
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// checkCredential applies the global config's validation to one credential
// before it is written, so a mistake is reported here and not by the next
// command that loads the config.
func checkCredential(name string, cred config.AgentCredential) error {
	probe := config.AgentConfig{Credentials: map[string]config.AgentCredential{name: cred}}
	return config.ValidateAgentConfig(probe)
}

// runCredentialsGrant grants (or revokes) name for the current project, or
// for the defaults with all. A project's own list replaces the defaults, so
// one that has none starts from what it gets now.
func runCredentialsGrant(w io.Writer, name string, all, grant bool) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	if _, ok := gcfg.Agent.Credentials[name]; !ok && grant {
		return fmt.Errorf("no credential %s (zdev agent credentials list)", name)
	}
	var keys []string
	var list []string
	var where string
	if all {
		keys, list, where = []string{"agent", "defaults"}, gcfg.Agent.Defaults.Credentials, "every project (defaults)"
	} else {
		project, ok := currentProjectName()
		if !ok {
			return errors.New("not in a project: run it in one, or use --all")
		}
		keys, list, where = []string{"agent", "projects", project}, gcfg.AgentProject(project).Credentials, project
	}
	next := []string{}
	has := false
	for _, c := range list {
		if c == name {
			has = true
			if !grant {
				continue
			}
		}
		next = append(next, c)
	}
	switch {
	case grant && has:
		fmt.Fprintf(w, "%s already gets %s.\n", where, name)
		return nil
	case !grant && !has:
		fmt.Fprintf(w, "%s doesn't get %s.\n", where, name)
		return nil
	case grant:
		next = append(next, name)
	}
	err = config.EditGlobalConfig(func(root *yaml.Node) error {
		m, err := config.YAMLMapping(root, keys...)
		if err != nil {
			return err
		}
		return config.SetYAML(m, "credentials", next)
	})
	if err != nil {
		return err
	}
	if grant {
		fmt.Fprintf(w, "Granted %s to %s; the next session gets it.\n", name, where)
	} else {
		fmt.Fprintf(w, "Revoked %s from %s; the next session runs without it.\n", name, where)
	}
	if all {
		if own := projectsWithOwnList(gcfg); len(own) > 0 {
			fmt.Fprintf(w, "Projects with their own list don't follow the defaults: %s (grant it there separately).\n", strings.Join(own, ", "))
		}
	}
	return nil
}

func runCredentialsRemove(w io.Writer, name string) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	cred, ok := gcfg.Agent.Credentials[name]
	if !ok {
		return fmt.Errorf("no credential %s (zdev agent credentials list)", name)
	}
	in := bufio.NewReader(os.Stdin)
	if !credRemoveYes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("not a terminal: confirm with --yes")
		}
		if !askYes(in, fmt.Sprintf("Remove %s (granted to %s)? [y/N] ", name, describeGrants(gcfg, name)), false) {
			return nil
		}
	}
	err = config.EditGlobalConfig(func(root *yaml.Node) error {
		creds, err := config.YAMLMapping(root, "agent", "credentials")
		if err != nil {
			return err
		}
		config.DeleteYAML(creds, name)
		if len(gcfg.Agent.Defaults.Credentials) > 0 {
			d, err := config.YAMLMapping(root, "agent", "defaults")
			if err != nil {
				return err
			}
			if err := config.SetYAML(d, "credentials", without(gcfg.Agent.Defaults.Credentials, name)); err != nil {
				return err
			}
		}
		for p, pc := range gcfg.Agent.Projects {
			if pc.Credentials == nil {
				continue
			}
			m, err := config.YAMLMapping(root, "agent", "projects", p)
			if err != nil {
				return err
			}
			if err := config.SetYAML(m, "credentials", without(pc.Credentials, name)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Removed %s and its grants.\n", name)
	switch {
	case cred.File != "" && isZdevCredentialFile(cred.File):
		path := expandHome(cred.File)
		if err := os.Remove(path); err == nil {
			fmt.Fprintf(w, "Deleted %s.\n", path)
		}
	case strings.HasPrefix(cred.Secret, "op://"):
		fmt.Fprintf(w, "The 1Password item (%s) stays; delete it in 1Password if you no longer need it.\n", cred.Secret)
	}
	return nil
}

// isZdevCredentialFile reports whether path is one zdev stored itself (in
// ~/.zdev/agent/credentials), the only kind remove may delete.
func isZdevCredentialFile(path string) bool {
	dir := filepath.Join(config.GetZdevHome(), "agent", "credentials") + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(expandHome(path)), dir)
}

func without(list []string, name string) []string {
	out := []string{}
	for _, v := range list {
		if v != name {
			out = append(out, v)
		}
	}
	return out
}

func runCredentialsTest(ctx context.Context, w io.Writer, name string) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	cred, ok := gcfg.Agent.Credentials[name]
	if !ok {
		return fmt.Errorf("no credential %s (zdev agent credentials list)", name)
	}
	var op *secrets.OnePasswordCLI
	v, err := credentialValue(ctx, cred, &op)
	if err != nil {
		return fmt.Errorf("%s can't be read: %w", name, err)
	}
	fmt.Fprintf(w, "%s is readable: %d characters, %s. Granted to %s.\n", name, len(v), describeSource(cred), describeGrants(gcfg, name))
	return nil
}
