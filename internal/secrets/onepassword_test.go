package secrets

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const testEnvID = "b7qmzx3kfpwj4hn2c6t8vydl5a"

func TestValidateRef(t *testing.T) {
	valid := []string{
		"op-env://API_KEY",
		"op-env://DB_PASSWORD",
		"op-env://lower_case1",
	}
	for _, ref := range valid {
		if err := ValidateRef(ref); err != nil {
			t.Errorf("ValidateRef(%q) = %v, want nil", ref, err)
		}
	}

	invalid := []string{
		"op-env://",
		"op-env://KEY WITH SPACE",
		"op-env://KEY=VALUE",
		"op-env://vault/item/field",
		"op-env://KE\nY",
		"API_KEY",
	}
	for _, ref := range invalid {
		if err := ValidateRef(ref); err == nil {
			t.Errorf("ValidateRef(%q) = nil, want error", ref)
		}
	}
}

func TestKey(t *testing.T) {
	if got := Key("op-env://API_KEY"); got != "API_KEY" {
		t.Errorf("Key = %q, want API_KEY", got)
	}
}

func TestParseEnvironmentOutput(t *testing.T) {
	out := "PLAIN=hello\n" +
		"WITH_EQUALS=a=b=c\n" +
		"EMPTY=\n" +
		"URL=mysql://dev:dev@localhost:3306/db\n" +
		"DUPLICATE=first\n" +
		"DUPLICATE=last\n" +
		"garbage line without separator\n"

	vars := parseEnvironmentOutput(out)

	want := map[string]string{
		"PLAIN":       "hello",
		"WITH_EQUALS": "a=b=c",
		"EMPTY":       "",
		"URL":         "mysql://dev:dev@localhost:3306/db",
		"DUPLICATE":   "last",
	}
	if len(vars) != len(want) {
		t.Errorf("parsed %d vars, want %d: %v", len(vars), len(want), vars)
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("vars[%q] = %q, want %q", k, vars[k], v)
		}
	}
}

// testCLI builds an OnePasswordCLI with all seams stubbed. Tests override
// individual fields.
func testCLI() *OnePasswordCLI {
	o := NewOnePasswordCLI()
	o.lookPath = func(name string) (string, bool) {
		if name == "op" {
			return "/usr/local/bin/op", true
		}
		return "", false
	}
	o.isInteractive = func() bool { return false }
	o.confirm = func(string) bool { return false }
	return o
}

func stubRead(output string) func(context.Context, string, string, []string) (string, string, error) {
	return func(context.Context, string, string, []string) (string, string, error) {
		return output, "", nil
	}
}

func TestResolve_SelectsRequestedKeys(t *testing.T) {
	o := testCLI()
	readCalls := 0
	inner := stubRead("API_KEY=value-a\nDB_PASS=value-b\nUNUSED=nope\n")
	o.runRead = func(ctx context.Context, binary, envID string, env []string) (string, string, error) {
		readCalls++
		if envID != testEnvID {
			t.Errorf("envID = %q, want %q", envID, testEnvID)
		}
		return inner(ctx, binary, envID, env)
	}

	got, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY", "DB_PASS", "API_KEY"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["API_KEY"] != "value-a" || got["DB_PASS"] != "value-b" {
		t.Errorf("unexpected values: %v", got)
	}
	if _, ok := got["UNUSED"]; ok {
		t.Error("unrequested key must not be returned")
	}
	if readCalls != 1 {
		t.Errorf("read called %d times, want 1", readCalls)
	}
}

func TestResolve_CacheAvoidsSecondInvocation(t *testing.T) {
	o := testCLI()
	readCalls := 0
	inner := stubRead("API_KEY=value-a\nOTHER=value-b\n")
	o.runRead = func(ctx context.Context, binary, envID string, env []string) (string, string, error) {
		readCalls++
		return inner(ctx, binary, envID, env)
	}

	if _, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"}); err != nil {
		t.Fatalf("Resolve #1: %v", err)
	}
	// Different key, same environment: must come from cache.
	got, err := o.Resolve(context.Background(), testEnvID, []string{"OTHER"})
	if err != nil {
		t.Fatalf("Resolve #2: %v", err)
	}
	if got["OTHER"] != "value-b" {
		t.Errorf("got %v", got)
	}
	if readCalls != 1 {
		t.Errorf("read called %d times, want 1 (cached per environment)", readCalls)
	}
}

func TestResolve_MissingKeyError(t *testing.T) {
	o := testCLI()
	o.runRead = stubRead("PRESENT=yes\n")

	_, err := o.Resolve(context.Background(), testEnvID, []string{"PRESENT", "ABSENT"})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "ABSENT") || !strings.Contains(err.Error(), testEnvID) {
		t.Errorf("error should name the missing key and environment: %v", err)
	}
	if strings.Contains(err.Error(), "yes") {
		t.Errorf("error must not leak resolved values: %v", err)
	}
}

func TestEnsureBinary_MissingNonInteractive(t *testing.T) {
	o := testCLI()
	o.lookPath = func(string) (string, bool) { return "", false }

	_, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err == nil {
		t.Fatal("expected error when op is missing")
	}
	for _, want := range []string{"not found in PATH", "brew install 1password-cli@beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
}

func TestEnsureBinary_InstallOfferAccepted(t *testing.T) {
	o := testCLI()
	installed := false
	o.lookPath = func(name string) (string, bool) {
		switch name {
		case "brew":
			return "/opt/homebrew/bin/brew", true
		case "op":
			if installed {
				return "/opt/homebrew/bin/op", true
			}
		}
		return "", false
	}
	o.isInteractive = func() bool { return true }
	prompted := false
	o.confirm = func(string) bool { prompted = true; return true }
	o.runInstall = func(context.Context) error { installed = true; return nil }
	o.runRead = stubRead("API_KEY=val\n")

	got, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err != nil {
		t.Fatalf("Resolve after install: %v", err)
	}
	if !prompted || !installed {
		t.Errorf("prompted=%v installed=%v, want both true", prompted, installed)
	}
	if got["API_KEY"] != "val" {
		t.Errorf("got %v", got)
	}
}

func TestEnsureBinary_InstallOfferDeclined(t *testing.T) {
	o := testCLI()
	o.lookPath = func(name string) (string, bool) {
		if name == "brew" {
			return "/opt/homebrew/bin/brew", true
		}
		return "", false
	}
	o.isInteractive = func() bool { return true }
	o.confirm = func(string) bool { return false }
	o.runInstall = func(context.Context) error {
		t.Fatal("install must not run when declined")
		return nil
	}

	if _, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"}); err == nil {
		t.Error("expected error after declined install")
	}
}

func TestEnsureBinary_NoPromptWithoutBrew(t *testing.T) {
	o := testCLI()
	o.lookPath = func(string) (string, bool) { return "", false }
	o.isInteractive = func() bool { return true }
	o.confirm = func(string) bool {
		t.Fatal("must not prompt when brew is unavailable")
		return false
	}

	if _, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"}); err == nil {
		t.Error("expected error")
	}
}

func TestRead_AuthRecoveryRetriesOnce(t *testing.T) {
	o := testCLI()
	o.isInteractive = func() bool { return true }
	o.confirm = func(string) bool { return true }
	o.listAccounts = func(context.Context, string) ([]opAccount, error) {
		return []opAccount{{UserUUID: "USERID"}}, nil
	}
	signinCalls := 0
	o.runSignin = func(context.Context, string) (string, error) {
		signinCalls++
		return "session-token", nil
	}

	readCalls := 0
	var envOnRetry []string
	o.runRead = func(ctx context.Context, binary, envID string, env []string) (string, string, error) {
		readCalls++
		if readCalls == 1 {
			return "", "[ERROR] you are not currently signed in", fmt.Errorf("exit status 1")
		}
		envOnRetry = env
		return "API_KEY=val\n", "", nil
	}

	got, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got["API_KEY"] != "val" {
		t.Errorf("got %v", got)
	}
	if signinCalls != 1 || readCalls != 2 {
		t.Errorf("signinCalls=%d readCalls=%d, want 1 and 2", signinCalls, readCalls)
	}
	if len(envOnRetry) != 1 || envOnRetry[0] != "OP_SESSION_USERID=session-token" {
		t.Errorf("retry env = %v, want captured session token", envOnRetry)
	}
}

func TestRead_AuthRecoverySecondFailureSurfaces(t *testing.T) {
	o := testCLI()
	o.isInteractive = func() bool { return true }
	o.confirm = func(string) bool { return true }
	o.listAccounts = func(context.Context, string) ([]opAccount, error) {
		return []opAccount{{UserUUID: "USERID"}}, nil
	}
	o.runSignin = func(context.Context, string) (string, error) { return "", nil }

	readCalls := 0
	o.runRead = func(context.Context, string, string, []string) (string, string, error) {
		readCalls++
		return "", "[ERROR] you are not currently signed in", fmt.Errorf("exit status 1")
	}

	_, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err == nil {
		t.Fatal("expected error")
	}
	if readCalls != 2 {
		t.Errorf("readCalls=%d, want exactly 2 (single retry)", readCalls)
	}
	if !strings.Contains(err.Error(), "op signin") {
		t.Errorf("auth error should carry signin hint, got: %v", err)
	}
}

func TestRead_NonAuthErrorNoSigninOffer(t *testing.T) {
	o := testCLI()
	o.isInteractive = func() bool { return true }
	o.confirm = func(string) bool {
		t.Fatal("must not offer signin for non-auth errors")
		return false
	}

	readCalls := 0
	o.runRead = func(context.Context, string, string, []string) (string, string, error) {
		readCalls++
		return "", `[ERROR] environment "bogus" not found`, fmt.Errorf("exit status 1")
	}

	_, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err == nil {
		t.Fatal("expected error")
	}
	if readCalls != 1 {
		t.Errorf("readCalls=%d, want 1 (no retry)", readCalls)
	}
	if strings.Contains(err.Error(), "op signin") {
		t.Errorf("non-auth error should not carry signin hint: %v", err)
	}
}

func TestRead_AuthErrorNonInteractiveNoPrompt(t *testing.T) {
	o := testCLI()
	o.isInteractive = func() bool { return false }
	o.confirm = func(string) bool {
		t.Fatal("must not prompt when non-interactive")
		return false
	}
	o.runRead = func(context.Context, string, string, []string) (string, string, error) {
		return "", "[ERROR] no accounts configured for use with 1Password CLI", fmt.Errorf("exit status 1")
	}

	_, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "OP_SERVICE_ACCOUNT_TOKEN") {
		t.Errorf("non-interactive auth error should mention service accounts: %v", err)
	}
}

func TestRead_StableCLIWithoutEnvironmentsHint(t *testing.T) {
	o := testCLI()
	o.runRead = func(context.Context, string, string, []string) (string, string, error) {
		return "", `[ERROR] unknown command "environment" for "op"`, fmt.Errorf("exit status 1")
	}

	_, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "1password-cli@beta") {
		t.Errorf("stable-CLI error should point at the beta build: %v", err)
	}
}

func TestRead_AccountAddWhenNoAccounts(t *testing.T) {
	o := testCLI()
	o.isInteractive = func() bool { return true }
	o.confirm = func(string) bool { return true }
	accountAdded := false
	o.listAccounts = func(context.Context, string) ([]opAccount, error) {
		if accountAdded {
			return []opAccount{{UserUUID: "NEWUSER"}}, nil
		}
		return nil, nil
	}
	o.runAccountAdd = func(context.Context, string) error { accountAdded = true; return nil }
	o.runSignin = func(context.Context, string) (string, error) { return "tok", nil }

	readCalls := 0
	o.runRead = func(context.Context, string, string, []string) (string, string, error) {
		readCalls++
		if readCalls == 1 {
			return "", "[ERROR] no accounts configured for use with 1Password CLI", fmt.Errorf("exit status 1")
		}
		return "API_KEY=val\n", "", nil
	}

	got, err := o.Resolve(context.Background(), testEnvID, []string{"API_KEY"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !accountAdded {
		t.Error("account add should have run")
	}
	if got["API_KEY"] != "val" {
		t.Errorf("got %v", got)
	}
}

func TestHashValues(t *testing.T) {
	a := HashValues(map[string]string{"API_KEY": "one", "DB_PASS": "two"})
	b := HashValues(map[string]string{"DB_PASS": "two", "API_KEY": "one"})
	if a != b {
		t.Error("hash should be order-independent")
	}
	c := HashValues(map[string]string{"API_KEY": "one", "DB_PASS": "rotated"})
	if a == c {
		t.Error("hash should change when a value changes")
	}
}
