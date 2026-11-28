package project

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
)

// agentFieldClass classifies every project config field for the agent
// policy (PLAN_2 section 10). A new field fails TestAgentPolicyClassifiesFields
// until it is classified here, and a field that reaches outside containers
// needs a rule in CheckAgentPolicy before it can be:
//
//	safe:     stays inside containers or zdev's own handling; applied freely
//	checked:  CheckAgentPolicy has a rule for it
//	pinned:   can't change from a sandbox at all (checkAgentProject)
var agentFieldClass = map[string]string{
	"version":            "safe",
	"name":               "pinned",
	"domain":             "checked", // routed hosts
	"info":               "safe",
	"auto_open_at_start": "safe", // not for sandbox requests (cmd/start.go)
	"variables":          "safe", // substituted before the check; host env reads are checked (HostEnv)
	"environment":        "checked",
	"shared.router":      "safe",
	"shared.mail":        "safe",
	"shared.db":          "safe",
	"shared.redis":       "safe",
	"shared.logs":        "safe",
	"mutagen.ignore":     "safe", // the .git guards come after it (agentGitGuards)

	"services.*.image":                  "safe",
	"services.*.dockerfile":             "checked",
	"services.*.routing.protocol":       "safe",
	"services.*.routing.port":           "safe",
	"services.*.routing.host_port":      "checked",
	"services.*.routing.domain":         "checked",
	"services.*.working_dir":            "safe",
	"services.*.volumes":                "checked",
	"services.*.environment":            "checked", // op-env:// references
	"services.*.op-env":                 "checked",
	"services.*.command":                "safe",
	"services.*.labels":                 "checked", // traefik.*
	"services.*.register_to_dbui":       "safe",
	"services.*.mutagen.user":           "safe",
	"services.*.mutagen.group":          "safe",
	"services.*.mutagen.file_mode":      "safe",
	"services.*.mutagen.directory_mode": "safe",
	"services.*.mutagen.no_sync":        "checked", // a native bind of the root exposes .git
}

// configFieldPaths lists the yaml paths of a config struct's leaf fields;
// map values with struct elements get a "*" segment.
func configFieldPaths(t reflect.Type, prefix string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		path := prefix + name
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			configFieldPaths(ft, path+".", out)
		case ft.Kind() == reflect.Map && derefKind(ft.Elem()) == reflect.Struct:
			configFieldPaths(ft.Elem(), path+".*.", out)
		default:
			*out = append(*out, path)
		}
	}
}

func derefKind(t reflect.Type) reflect.Kind {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind()
}

func TestAgentPolicyClassifiesFields(t *testing.T) {
	var paths []string
	configFieldPaths(reflect.TypeOf(config.ProjectConfig{}), "", &paths)
	sort.Strings(paths)
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
		class, ok := agentFieldClass[p]
		if !ok {
			t.Errorf("config field %s is not classified for the agent policy: add it to agentFieldClass (and a rule to CheckAgentPolicy if it reaches outside containers)", p)
			continue
		}
		if class != "safe" && class != "checked" && class != "pinned" {
			t.Errorf("%s: unknown class %q", p, class)
		}
	}
	for p := range agentFieldClass {
		if !seen[p] {
			t.Errorf("agentFieldClass lists %s, which is no config field", p)
		}
	}
}
