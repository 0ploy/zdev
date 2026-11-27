package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeGitLab answers the API calls the setup makes. projects is what
// /projects?membership=true returns; level is the token's role on the project.
func fakeGitLab(t *testing.T, token string, level int, projects []string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v4/version" && r.Header.Get("PRIVATE-TOKEN") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
			return
		}
		if r.Header.Get("PRIVATE-TOKEN") != token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
			return
		}
		switch {
		case r.URL.Path == "/api/v4/user":
			_, _ = w.Write([]byte(`{"username":"project_7_bot_abc"}`))
		case r.URL.EscapedPath() == "/api/v4/projects/team%2Fapp":
			_, _ = w.Write([]byte(`{"path_with_namespace":"team/app","permissions":{"project_access":{"access_level":` + strconv.Itoa(level) + `},"group_access":null}}`))
		case r.URL.Path == "/api/v4/projects" && r.URL.Query().Get("membership") == "true":
			var b strings.Builder
			b.WriteString("[")
			for i, p := range projects {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`{"path_with_namespace":"` + p + `"}`)
			}
			b.WriteString("]")
			_, _ = w.Write([]byte(b.String()))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Project Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	old := gitlabBase
	gitlabBase = func(string) string { return srv.URL }
	t.Cleanup(func() { gitlabBase = old })
}

func TestIsGitLab(t *testing.T) {
	fakeGitLab(t, "tok", 30, nil)
	if !isGitLab(context.Background(), "git.example.com") {
		t.Error("an instance answering GitLab's anonymous 401 was not recognized")
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "<html>not found</html>", http.StatusNotFound)
	}))
	defer other.Close()
	gitlabBase = func(string) string { return other.URL }
	if isGitLab(context.Background(), "git.example.com") {
		t.Error("a non-GitLab server was taken for GitLab")
	}
	if !isGitLab(context.Background(), "gitlab.com") {
		t.Error("gitlab.com needs no probe")
	}
}

func TestCheckGitLabToken(t *testing.T) {
	fakeGitLab(t, "glpat-good", 30, []string{"team/app"})
	ctx := context.Background()
	if err := checkGitLabToken(ctx, "git.example.com", "glpat-good", "team/app"); err != nil {
		t.Errorf("a Developer token on the project: %v", err)
	}
	if err := checkGitLabToken(ctx, "git.example.com", "glpat-bad", "team/app"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a wrong token: %v", err)
	}
	if err := checkGitLabToken(ctx, "git.example.com", "glpat-good", "team/other"); err == nil || !strings.Contains(err.Error(), "can't read team/other") {
		t.Errorf("a project the token can't open: %v", err)
	}
}

func TestOtherGitLabProjects(t *testing.T) {
	fakeGitLab(t, "glpat-project", 30, []string{"team/app"})
	if others, err := otherGitLabProjects(context.Background(), "git.example.com", "glpat-project", "team/app"); err != nil || len(others) != 0 {
		t.Errorf("a project access token reports others: %v, %v", others, err)
	}

	fakeGitLab(t, "glpat-personal", 50, []string{"team/app", "team/web", "ops/infra"})
	others, err := otherGitLabProjects(context.Background(), "git.example.com", "glpat-personal", "team/app")
	if err != nil || strings.Join(others, ",") != "team/web,ops/infra" {
		t.Errorf("a personal token's other projects = %v, %v", others, err)
	}
}
