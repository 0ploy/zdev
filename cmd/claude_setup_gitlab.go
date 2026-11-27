package cmd

// zdev agent setup for GitLab, gitlab.com or a self-hosted instance: the
// counterpart of GitHub's fine-grained token is a project access token, which
// reaches only this project. GitLab can't pre-fill its form (only the
// personal access token page takes URL parameters), so the setup opens the
// project's token page and lists what to enter.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/tools"
	"github.com/0ploy/zdev/internal/ui"
)

// gitlabBase is a GitLab instance's base URL, replaceable in tests.
var gitlabBase = func(host string) string { return "https://" + host }

// gitlabDeveloper is GitLab's Developer role: push to unprotected branches,
// open merge requests, read pipelines.
const gitlabDeveloper = 30

// isGitLab reports whether host runs GitLab: gitlab.com, or an instance whose
// API answers an anonymous version request with GitLab's 401 JSON.
func isGitLab(ctx context.Context, host string) bool {
	if host == "gitlab.com" {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitlabBase(host)+"/api/v4/version", nil)
	if err != nil {
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var body struct{ Message string }
	return res.StatusCode == http.StatusUnauthorized &&
		json.NewDecoder(res.Body).Decode(&body) == nil && strings.Contains(body.Message, "401")
}

// setupGitLab offers the GitLab credentials for the project at path on host.
func setupGitLab(ctx context.Context, in *bufio.Reader, plain bool, gcfg *config.GlobalConfig, project, host, path string) error {
	ui.StatusStep("GitLab for "+project, plain)
	fmt.Printf("Repository: %s/%s\n", host, path)
	if name := grantedFor(gcfg, project, host); name != "" {
		fmt.Printf("The sandbox gets credential %s (%s).\n", name, describeSource(gcfg.Agent.Credentials[name]))
		if !askYes(in, "Replace it? [y/N] ", false) {
			return nil
		}
	}
	paid := ""
	if host == "gitlab.com" {
		paid = ", Premium on gitlab.com"
	}
	fmt.Printf(`
How should the sandbox reach %s?
  1) A project access token for this repository only (recommended; needs Maintainer%s)
  2) A personal access token: every project you can reach
  3) Your glab login (glab config get token): every project you can reach
  4) Nothing for now
`, host, paid)
	switch ask(in, "Choice [1]: ") {
	case "", "1":
		return setupGitLabToken(ctx, in, plain, project, host, path, true)
	case "2":
		return setupGitLabToken(ctx, in, plain, project, host, path, false)
	case "3":
		if _, ok := tools.FindInPath("glab"); !ok {
			return errors.New("glab is not installed")
		}
		name := "gitlab-glab-" + credentialSlug(host)
		cred := config.AgentCredential{Hosts: []string{host}, Env: []string{"GITLAB_TOKEN"},
			Command: "glab config get token --host " + host}
		if err := grantCredential(project, name, cred); err != nil {
			return err
		}
		return verifyGitLabCredential(ctx, name, cred, host, path)
	}
	return nil
}

func setupGitLabToken(ctx context.Context, in *bufio.Reader, plain bool, project, host, path string, projectToken bool) error {
	name := "gitlab-" + credentialSlug(project)
	tokenName := truncate("zdev "+project, 40)
	expires := time.Now().AddDate(0, 0, 90).Format("2006-01-02")
	var link string
	if projectToken {
		link = gitlabBase(host) + "/" + path + "/-/settings/access_tokens"
		fmt.Printf(`
Create a project access token on the project's page ("Add new token").
GitLab can't pre-fill this form, so enter:

  Token name:       %s
  Description:      zdev claude sandbox of %s
  Expiration date:  %s (90 days)
  Role:             Developer
  Scopes:           api

Then "Create project access token" and paste it here.

  %s

`, tokenName, project, expires, link)
	} else {
		q := url.Values{}
		q.Set("name", tokenName)
		q.Set("description", "zdev claude sandbox of "+project+" ("+path+")")
		q.Set("scopes", "api")
		link = gitlabBase(host) + "/-/user_settings/personal_access_tokens?" + q.Encode()
		fmt.Printf(`
GitLab's page for a new personal access token comes pre-filled (name,
description, scope api), except the expiration date: set %s (90 days).
The token reaches every project you can reach, not only %s.

Then "Create token" and paste it here.

  %s

`, expires, path, link)
	}
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
		case strings.Count(token, "glpat-") > 1:
			fmt.Println("That is more than one token (pasted twice?). Paste it once.")
			token = ""
		default:
			ui.StatusStep("Checking the token", plain)
			if err = checkGitLabToken(ctx, host, token, path); err == nil {
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
	if projectToken {
		others, err := otherGitLabProjects(ctx, host, token, path)
		if err != nil {
			fmt.Printf("Couldn't list the token's projects: %v\n", err)
		} else if len(others) > 0 {
			fmt.Printf("The token also reaches %s: it is not a project access token of %s.\n", strings.Join(others, ", "), path)
			if !askYes(in, "Keep it anyway? [y/N] ", false) {
				return errors.New("create the token under the project's Settings > Access tokens and run zdev agent setup again")
			}
		}
	}

	cred := config.AgentCredential{Hosts: []string{host}, Env: []string{"GITLAB_TOKEN"}}
	op := secrets.NewOnePasswordCLI()
	if _, found := tools.FindInPath("op"); found && askYes(in, "Store it in 1Password? [Y/n] ", true) {
		ref, err := storeIn1Password(ctx, in, op, "zdev "+name, token, host,
			"GitLab token for zdev claude sandboxes of "+path+", swapped in by zdev's egress proxy. Created by zdev agent setup.")
		if err != nil {
			return err
		}
		cred.Secret = ref
	} else {
		file, err := storeInFile(name, token)
		if err != nil {
			return err
		}
		cred.File = "~/" + strings.TrimPrefix(file, homeDir()+"/")
		fmt.Printf("Stored in %s (only you can read it).\n", file)
	}
	if err := grantCredential(project, name, cred); err != nil {
		return err
	}
	return verifyGitLabCredential(ctx, name, cred, host, path)
}

// verifyGitLabCredential reads the credential the way a session does and
// asks GitLab who it belongs to.
func verifyGitLabCredential(ctx context.Context, name string, cred config.AgentCredential, host, path string) error {
	var op *secrets.OnePasswordCLI
	token, err := credentialValue(ctx, cred, &op)
	if err != nil {
		return fmt.Errorf("reading %s back: %w", name, err)
	}
	if err := checkGitLabToken(ctx, host, token, path); err != nil {
		return err
	}
	fmt.Printf("\nDone: sandboxes of this project get %s for %s (git over HTTPS and glab).\n", name, host)
	fmt.Println("It is in ~/.zdev/global-config.yaml under agent.credentials and agent.projects.")
	return nil
}

// checkGitLabToken requires the token to reach the project; without the
// Developer role it can read but not push, which is only noted.
func checkGitLabToken(ctx context.Context, host, token, path string) error {
	var user struct{ Username string }
	if err := gitlabGet(ctx, host, token, "/user", &user); err != nil {
		return err
	}
	var p struct {
		PathWithNamespace string `json:"path_with_namespace"`
		Permissions       struct {
			ProjectAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"project_access"`
			GroupAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"group_access"`
		}
	}
	if err := gitlabGet(ctx, host, token, "/projects/"+url.PathEscape(path), &p); err != nil {
		return fmt.Errorf("the token can't read %s: %w", path, err)
	}
	level := 0
	if a := p.Permissions.ProjectAccess; a != nil && a.AccessLevel > level {
		level = a.AccessLevel
	}
	if a := p.Permissions.GroupAccess; a != nil && a.AccessLevel > level {
		level = a.AccessLevel
	}
	if level < gitlabDeveloper {
		fmt.Printf("Note: the token reads %s but can't push (role below Developer).\n", p.PathWithNamespace)
	}
	fmt.Printf("Token of %s, reaches %s.\n", user.Username, p.PathWithNamespace)
	return nil
}

// otherGitLabProjects returns up to three projects besides want that the
// token's user is a member of. A project access token's bot user is a member
// of its own project only.
func otherGitLabProjects(ctx context.Context, host, token, want string) ([]string, error) {
	var projects []struct {
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := gitlabGet(ctx, host, token, "/projects?membership=true&simple=true&per_page=4", &projects); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range projects {
		if !strings.EqualFold(p.PathWithNamespace, want) && len(out) < 3 {
			out = append(out, p.PathWithNamespace)
		}
	}
	return out, nil
}

func gitlabGet(ctx context.Context, host, token, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitlabBase(host)+"/api/v4"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(res.Body).Decode(v)
	case http.StatusUnauthorized:
		return errors.New("GitLab rejects the token (401): wrong, expired or revoked")
	case http.StatusForbidden:
		return errors.New("GitLab says 403: the token lacks the api scope")
	case http.StatusNotFound:
		return errors.New("GitLab says 404: the token has no access to it")
	}
	return fmt.Errorf("GitLab says %d", res.StatusCode)
}
