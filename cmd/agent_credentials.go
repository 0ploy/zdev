package cmd

// Credentials for agent sandboxes (PLAN_2 section 12): resolved on the host
// when a session starts, held only by the egress proxy in this process. The
// sandbox gets placeholders, a git credential helper that hands them to git,
// and the session CA in its trust bundle, so the proxy can swap the real
// token in for the credential's hosts.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/ssl"
)

// gitHTTPSHosts get their SSH remote URLs rewritten to HTTPS in every
// sandbox (SSH is off there), plus the exact hosts of granted credentials.
var gitHTTPSHosts = []string{"github.com", "gitlab.com", "bitbucket.org"}

// resolveCredentials reads the tokens a project is granted. A credential that
// can't be read is left out with a warning: the session works without it.
func resolveCredentials(ctx context.Context, gcfg *config.GlobalConfig, names []string) []agent.Credential {
	var out []agent.Credential
	var op secrets.Resolver
	for _, name := range names {
		c, ok := gcfg.Agent.Credentials[name]
		if !ok {
			continue
		}
		value, err := credentialValue(ctx, c, &op)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: credential %s: %v; the sandbox runs without it\n", name, err)
			continue
		}
		out = append(out, agent.Credential{Name: name, Hosts: c.Hosts, Placeholder: agent.NewPlaceholder(name), Value: value})
	}
	return out
}

func credentialValue(ctx context.Context, c config.AgentCredential, op *secrets.Resolver) (string, error) {
	if c.Command != "" {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "sh", "-c", c.Command)
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("`%s` failed: %w", c.Command, err)
		}
		v := strings.TrimSpace(string(out))
		if v == "" {
			return "", fmt.Errorf("`%s` printed nothing", c.Command)
		}
		return v, nil
	}
	envID, key, err := secrets.ParseRef(c.Secret)
	if err != nil {
		return "", err
	}
	if *op == nil {
		*op = secrets.NewOnePasswordCLI()
	}
	vars, err := (*op).ReadEnvironment(ctx, envID)
	if err != nil {
		return "", err
	}
	v, ok := vars[key]
	if !ok || v == "" {
		return "", fmt.Errorf("%s has no %s", envID, key)
	}
	return v, nil
}

// credentialEnv is the sandbox's view of the credentials: the placeholders in
// the configured variables and in ZDEV_CREDENTIAL_<NAME> (for the helper).
func credentialEnv(gcfg *config.GlobalConfig, creds []agent.Credential) map[string]string {
	env := map[string]string{}
	for _, c := range creds {
		env[credentialVar(c.Name)] = c.Placeholder
		for _, e := range gcfg.Agent.Credentials[c.Name].Env {
			env[e] = c.Placeholder
		}
	}
	return env
}

func credentialVar(name string) string {
	return "ZDEV_CREDENTIAL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// sandboxGitConfig is git configuration for the sandbox, as key/value pairs
// for GIT_CONFIG_*: SSH remotes rewritten to HTTPS, and a credential helper
// per credential host that answers with the placeholder.
func sandboxGitConfig(creds []agent.Credential) [][2]string {
	hosts := map[string]bool{}
	for _, h := range gitHTTPSHosts {
		hosts[h] = true
	}
	var out [][2]string
	for _, c := range creds {
		for _, h := range c.Hosts {
			if strings.Contains(h, "*") {
				continue
			}
			hosts[h] = true
			helper := fmt.Sprintf(`!f() { test "$1" = get && printf 'username=x-access-token\npassword=%%s\n' "$%s"; }; f`, credentialVar(c.Name))
			out = append(out, [2]string{"credential.https://" + h + ".helper", helper})
		}
	}
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Strings(names)
	for _, h := range names {
		out = append(out,
			[2]string{"url.https://" + h + "/.insteadOf", "git@" + h + ":"},
			[2]string{"url.https://" + h + "/.insteadOf", "ssh://git@" + h + "/"})
	}
	return out
}

// trustSessionCA swaps zdev's trust bundle in the container config for one
// that adds the session CA (written to dir, which the sandbox mounts at
// containerDir). Reports false when the config has no zdev bundle to extend:
// without the host's public roots there is nothing complete to replace the
// system bundle with.
func trustSessionCA(cfg *runtime.ContainerConfig, ca *agent.SessionCA, dir, containerDir string) (bool, error) {
	base, err := os.ReadFile(ssl.CABundlePath())
	if err != nil {
		return false, nil
	}
	found := false
	for i, v := range cfg.Volumes {
		if v.Source == ssl.CABundlePath() {
			found = true
			cfg.Volumes[i].Source = dir + "/ca-bundle.pem"
		}
	}
	if !found {
		return false, nil
	}
	if err := os.WriteFile(dir+"/ca-bundle.pem", ca.PEMBundle(base), 0o644); err != nil {
		return false, err
	}
	extra, _ := os.ReadFile(ssl.CAPath())
	if err := os.WriteFile(dir+"/extra-ca.pem", ca.PEMBundle(extra), 0o644); err != nil {
		return false, err
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	cfg.Env["NODE_EXTRA_CA_CERTS"] = containerDir + "/extra-ca.pem"
	cfg.Env["REQUESTS_CA_BUNDLE"] = containerDir + "/ca-bundle.pem"
	return true, nil
}

// describeCredentials is the system prompt's line per credential.
func describeCredentials(gcfg *config.GlobalConfig, creds []agent.Credential) []string {
	var out []string
	for _, c := range creds {
		vars := append([]string{credentialVar(c.Name)}, gcfg.Agent.Credentials[c.Name].Env...)
		out = append(out, strings.Join(vars, ", ")+" for "+strings.Join(c.Hosts, ", "))
	}
	return out
}
