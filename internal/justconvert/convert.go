// Package justconvert turns a .zdev/commands/<name>.just file into a
// .zdev/run/<name>.toml job file. It reads just's own JSON dump
// (`just --dump --dump-format json`) instead of parsing just syntax, converts
// what it can translate faithfully and refuses the whole file otherwise.
//
// It exists only while just is deprecated and goes away with it.
package justconvert

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Result of converting one justfile.
type Result struct {
	// TOML is the job file content; empty when Problems is non-empty.
	TOML string
	// Notes are behavior changes worth reviewing in the converted file.
	Notes []string
	// Problems are constructs the converter can't translate safely. Any
	// problem means the file is not converted.
	Problems []string
	// Echoed is true when just would have printed command lines before
	// running them (lines without @); the converted job doesn't.
	Echoed bool
}

type dump struct {
	Aliases     map[string]alias      `json:"aliases"`
	Assignments map[string]assignment `json:"assignments"`
	First       string                `json:"first"`
	Doc         *string               `json:"doc"`
	Modules     map[string]any        `json:"modules"`
	Recipes     map[string]recipe     `json:"recipes"`
	Settings    map[string]any        `json:"settings"`
}

type assignment struct {
	Export bool `json:"export"`
	Value  any  `json:"value"`
}

type alias struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

type recipe struct {
	Attributes   []any        `json:"attributes"`
	Body         [][]any      `json:"body"`
	Dependencies []dependency `json:"dependencies"`
	Doc          *string      `json:"doc"`
	Name         string       `json:"name"`
	Parameters   []parameter  `json:"parameters"`
	Priors       int          `json:"priors"`
	Private      bool         `json:"private"`
	Quiet        bool         `json:"quiet"`
	Shebang      bool         `json:"shebang"`
}

type dependency struct {
	Arguments []any  `json:"arguments"`
	Recipe    string `json:"recipe"`
}

type parameter struct {
	Default any    `json:"default"`
	Export  bool   `json:"export"`
	Kind    string `json:"kind"` // singular, plus, star
	Name    string `json:"name"`
	Long    any    `json:"long"`
	Short   any    `json:"short"`
	Pattern any    `json:"pattern"`
}

// job is one converted recipe.
type job struct {
	name        string
	description string
	confirm     string
	hidden      bool
	workdir     string
	needs       []string
	aliases     []string
	run         string
}

var (
	jobNameRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]*$`)
	osAttrRe  = regexp.MustCompile(`(?m)^\s*\[[^\]\n]*\b(linux|macos|unix|windows|openbsd|freebsd|netbsd|dragonfly|android|ios)\b[^\]\n]*\]`)
	// Default values and dependency arguments are inserted into shell code
	// (${1:-value}) or a needs word: only allow characters that need no
	// quoting in either.
	safeValueRe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]*$`)
	envNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	zdevStepRe  = regexp.MustCompile(`^(\s*)zdev step\b`)
)

// Settings that change nothing for a converted job (or only how just itself
// behaves). Any other setting with a non-default value is refused.
var harmlessSettings = map[string]bool{
	"positional_arguments":      true, // jobs always get positional arguments
	"quiet":                     true, // jobs never echo
	"ignore_comments":           true, // comments are comments in the shell too
	"fallback":                  true,
	"allow_duplicate_recipes":   true,
	"allow_duplicate_variables": true,
	"no_exit_message":           true,
	"unstable":                  true,
	"lazy":                      true,
	"windows_powershell":        true,
	"windows_shell":             true,
}

// Convert converts one justfile. name is the file name without .just (the
// zdev command), dumpJSON the output of `just --dump --dump-format json` and
// source the justfile text (used only for recipe order, which the JSON dump
// doesn't keep).
func Convert(name string, dumpJSON, source []byte) (*Result, error) {
	var d dump
	if err := json.Unmarshal(dumpJSON, &d); err != nil {
		return nil, fmt.Errorf("reading just's dump: %w", err)
	}
	res := &Result{}
	problem := func(format string, args ...any) { res.Problems = append(res.Problems, fmt.Sprintf(format, args...)) }

	if len(d.Modules) > 0 {
		problem("uses modules (mod)")
	}
	// Variables holding a plain string are inlined: just substitutes their
	// text, so pasting the text is exactly what it did.
	vars := map[string]string{}
	for _, k := range sortedKeys(d.Assignments) {
		a := d.Assignments[k]
		v, ok := a.Value.(string)
		switch {
		case a.Export:
			problem("variable %s is exported as an env var: move it to env in the job file", k)
		case !ok:
			problem("variable %s is an expression: inline it by hand", k)
		default:
			vars[k] = v
		}
	}
	for _, k := range sortedKeys(d.Settings) {
		v := d.Settings[k]
		if harmlessSettings[k] || isDefaultSetting(v) {
			continue
		}
		problem("uses set %s", strings.ReplaceAll(k, "_", "-"))
	}
	if len(d.Recipes) == 0 {
		problem("has no recipes")
	}
	// just leaves recipes for other platforms out of the dump: converting
	// would silently drop them.
	if m := osAttrRe.FindSubmatch(source); m != nil {
		problem("uses OS attributes (%s): just hides recipes for other platforms from the converter, convert by hand", strings.TrimSpace(string(m[0])))
	}
	positional := boolSetting(d.Settings, "positional_arguments")

	order := recipeOrder(d.Recipes, source)
	single := len(order) == 1
	var jobs []*job
	for _, rn := range order {
		r := d.Recipes[rn]
		jobID := name
		if !single {
			jobID = name + " " + rn
		}
		j, notes, problems := convertRecipe(r, jobID, vars, positional)
		for _, p := range problems {
			problem("recipe %s: %s", rn, p)
		}
		for _, n := range notes {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %s", jobID, n))
		}
		if !r.Shebang && !r.Quiet && !boolSetting(d.Settings, "quiet") && echoes(r) {
			res.Echoed = true
		}
		if j != nil {
			jobs = append(jobs, j)
		}
	}

	// Aliases: job aliases in a multi-job file. In a single-job file there
	// is no job to alias (`zdev run <file>` already runs it).
	for _, an := range sortedKeys(d.Aliases) {
		a := d.Aliases[an]
		if single {
			res.Notes = append(res.Notes, fmt.Sprintf("alias %s dropped: %s has a single job, run it as zdev run %s", an, name, name))
			continue
		}
		if !jobNameRe.MatchString(an) {
			problem("alias %s: invalid name for zdev (lowercase letters, digits, - and _)", an)
			continue
		}
		for _, j := range jobs {
			if j.name == a.Target {
				j.aliases = append(j.aliases, an)
			}
		}
	}

	if single && len(order) == 1 && order[0] != name && order[0] != "default" {
		res.Notes = append(res.Notes, fmt.Sprintf("the only recipe, %s, is now the whole command: `zdev %s %s` becomes `zdev run %s` (a trailing %s would be passed as an argument)", order[0], name, order[0], name, order[0]))
	}
	if !single && len(order) > 0 {
		if d.First != "" {
			res.Notes = append(res.Notes, fmt.Sprintf("bare `zdev %s` ran recipe %s; there is no default job any more: zdev run %s %s", name, d.First, name, d.First))
		}
		if _, ok := d.Recipes[name]; ok {
			res.Notes = append(res.Notes, fmt.Sprintf("`zdev %s <args>` ran recipe %s with the arguments; now: zdev run %s %s <args> (consider renaming the job)", name, name, name, name))
		}
	}

	res.Problems = dedupe(res.Problems)
	if len(res.Problems) > 0 {
		return res, nil
	}
	desc := ""
	if d.Doc != nil {
		desc = *d.Doc
	}
	res.TOML = render(name, desc, single, jobs)
	return res, nil
}

func convertRecipe(r recipe, jobID string, vars map[string]string, positional bool) (*job, []string, []string) {
	var notes, problems []string
	j := &job{name: r.Name, hidden: r.Private}
	if r.Doc != nil {
		j.description = *r.Doc
	}
	if !jobNameRe.MatchString(r.Name) {
		problems = append(problems, "invalid job name for zdev (lowercase letters, digits, - and _)")
	}

	for _, a := range r.Attributes {
		switch v := a.(type) {
		case string:
			switch v {
			case "private":
				j.hidden = true
			case "positional-arguments":
				positional = true
			case "no-exit-message", "exit-message", "no-quiet":
			case "confirm":
				j.confirm = fmt.Sprintf("Run %s?", jobID)
			case "parallel":
				notes = append(notes, "dependencies ran in parallel ([parallel]); they now run one after another")
			default:
				problems = append(problems, fmt.Sprintf("attribute [%s] has no zdev equivalent", v))
			}
		case map[string]any:
			for k, val := range v {
				s, _ := val.(string)
				switch k {
				case "confirm":
					j.confirm = s
					if s == "" {
						j.confirm = fmt.Sprintf("Run %s?", jobID)
					}
				case "doc":
					j.description = s
				case "group", "metadata", "extension":
				case "working-directory":
					j.workdir = s
				default:
					problems = append(problems, fmt.Sprintf("attribute [%s] has no zdev equivalent", k))
				}
			}
		default:
			problems = append(problems, fmt.Sprintf("unrecognized attribute %v", a))
		}
	}

	params, pp := convertParams(r.Parameters)
	problems = append(problems, pp...)

	for i, dep := range r.Dependencies {
		if i >= r.Priors {
			problems = append(problems, fmt.Sprintf("dependency %s runs after the recipe (&&); zdev only runs dependencies before", dep.Recipe))
			continue
		}
		words := []string{dep.Recipe}
		for _, arg := range dep.Arguments {
			w, err := depArgument(arg, params, vars)
			if err != nil {
				problems = append(problems, fmt.Sprintf("dependency %s: %v", dep.Recipe, err))
				continue
			}
			words = append(words, w)
		}
		j.needs = append(j.needs, strings.Join(words, " "))
	}

	body, bn, bp := convertBody(r, params, vars, jobID, positional)
	notes = append(notes, bn...)
	problems = append(problems, bp...)
	j.run = body
	return j, notes, problems
}

type param struct {
	index      int // 1-based position
	kind       string
	defaultVal string
	hasDefault bool
}

func convertParams(ps []parameter) (map[string]param, []string) {
	var problems []string
	params := map[string]param{}
	for i, p := range ps {
		pr := param{index: i + 1, kind: p.Kind}
		if p.Export {
			problems = append(problems, fmt.Sprintf("parameter $%s is exported as an env var", p.Name))
		}
		if p.Long != nil || p.Short != nil {
			problems = append(problems, fmt.Sprintf("parameter %s is a named option (--%s)", p.Name, p.Name))
		}
		switch dv := p.Default.(type) {
		case nil:
		case string:
			if !safeValueRe.MatchString(dv) {
				problems = append(problems, fmt.Sprintf("parameter %s: default %q needs quoting, convert it by hand", p.Name, dv))
			}
			pr.defaultVal, pr.hasDefault = dv, true
		default:
			problems = append(problems, fmt.Sprintf("parameter %s: default is an expression", p.Name))
		}
		params[p.Name] = pr
	}
	return params, problems
}

// depArgument converts one dependency argument to a needs word: a literal
// or a parameter reference ({N} / {all}).
func depArgument(arg any, params map[string]param, vars map[string]string) (string, error) {
	switch v := arg.(type) {
	case string:
		if v == "" || !safeValueRe.MatchString(v) {
			return "", fmt.Errorf("argument %q needs quoting, which needs entries can't express", v)
		}
		return v, nil
	case []any:
		if len(v) == 2 && v[0] == "variable" {
			name, _ := v[1].(string)
			p, ok := params[name]
			if !ok {
				if v, isVar := vars[name]; isVar {
					return depArgument(v, params, vars)
				}
				return "", fmt.Errorf("argument uses variable %s", name)
			}
			if p.kind == "singular" {
				if p.hasDefault {
					return "", fmt.Errorf("argument %s has a default, which needs entries can't express", name)
				}
				return fmt.Sprintf("{%d}", p.index), nil
			}
			if p.index == 1 {
				return "{all}", nil
			}
			return "", fmt.Errorf("argument passes variadic %s that doesn't start at the first argument", name)
		}
	}
	return "", fmt.Errorf("argument is an expression")
}

func convertBody(r recipe, params map[string]param, vars map[string]string, jobID string, positional bool) (string, []string, []string) {
	var notes, problems []string
	var lines []string

	interp := ""
	shellBody := true
	bashBody := true // linewise bodies run in bash
	if r.Shebang && len(r.Body) > 0 {
		interp = joinText(r.Body[0])
		shellBody, bashBody = shebangKind(interp)
	}

	q := &quoteState{}
	continuing := false
	ignoreGroup := false
	for li, frags := range r.Body {
		var sb strings.Builder
		first := true
		for fi, f := range frags {
			switch v := f.(type) {
			case string:
				text := v
				if first && !r.Shebang && !continuing {
					text, ignoreGroup = stripPrefixes(text)
					trimmed := strings.TrimSpace(text)
					if strings.HasPrefix(trimmed, "cd ") || strings.HasPrefix(trimmed, "export ") {
						word := strings.Fields(trimmed)[0]
						notes = append(notes, fmt.Sprintf("line %d `%s` now affects the following lines (just ran each line in its own shell)", li+1, word))
					}
				}
				if first && shellBody {
					text = zdevStepRe.ReplaceAllString(text, "${1}step")
				}
				sb.WriteString(text)
				q.feed(text)
			case []any:
				if !shellBody {
					problems = append(problems, fmt.Sprintf("interpolates {{...}} into a %s script", strings.TrimPrefix(interp, "#!")))
					continue
				}
				expr, ok := unwrapInterpolation(v)
				if !ok {
					problems = append(problems, "unrecognized interpolation")
					continue
				}
				standalone := q.unquoted() && atWordStart(sb.String()) && atWordEnd(frags, fi)
				s, err := convertExpr(expr, params, vars, standalone, bashBody)
				if err != nil {
					problems = append(problems, err.Error())
					continue
				}
				sb.WriteString(s)
			}
			first = false
		}
		line := sb.String()
		continuing = strings.HasSuffix(line, "\\")
		if ignoreGroup && !continuing {
			line += " || true"
			ignoreGroup = false
		}
		lines = append(lines, line)
	}

	// just refused to run a recipe without its required arguments. The
	// check goes in front of the first body line (after a shebang) instead
	// of on its own line, so line numbers in shell errors still match the
	// original recipe.
	var preamble []string
	if shellBody {
		if check := argCheck(params, jobID); check != "" {
			preamble = append(preamble, check)
		}
		// With positional arguments, just passes parameter defaults as $1,
		// $2, ...: fill them in so the body sees the same values.
		if positional {
			if fill := fillDefaults(params); fill != "" {
				preamble = append(preamble, fill)
			}
		}
	} else if positional && hasDefaults(params) {
		problems = append(problems, fmt.Sprintf("parameter defaults reach the %s script as arguments ([positional-arguments]); convert by hand", strings.TrimPrefix(interp, "#!")))
	}
	if len(preamble) > 0 {
		at := 0
		if r.Shebang {
			at = 1
		}
		pre := strings.Join(preamble, "; ")
		if at < len(lines) {
			lines[at] = pre + "; " + lines[at]
		} else {
			lines = append(lines, pre)
		}
	}
	if strings.TrimSpace(strings.Join(lines, "")) == "" {
		return "", notes, problems // a recipe that only runs its dependencies
	}
	return strings.Join(lines, "\n") + "\n", notes, problems
}

// shebangKind reports whether a shebang runs a shell (gets the prelude and
// {{}} conversion) and whether that shell is bash.
func shebangKind(line string) (shell, bash bool) {
	argv := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(argv) == 0 {
		return false, false
	}
	name := argv[0][strings.LastIndex(argv[0], "/")+1:]
	if name == "env" && len(argv) == 2 {
		name = argv[1]
	}
	switch name {
	case "bash":
		return true, true
	case "sh", "dash", "ash", "zsh":
		return true, false
	}
	return false, false
}

// stripPrefixes removes just's @ (don't echo) and - (ignore failure) line
// prefixes, reporting whether - was present.
func stripPrefixes(s string) (string, bool) {
	ignore := false
	for len(s) > 0 && (s[0] == '@' || s[0] == '-') {
		if s[0] == '-' {
			ignore = true
		}
		s = s[1:]
	}
	return s, ignore
}

func echoes(r recipe) bool {
	for _, frags := range r.Body {
		if len(frags) == 0 {
			continue
		}
		s, ok := frags[0].(string)
		if !ok || (!strings.HasPrefix(s, "@") && !strings.HasPrefix(s, "-@")) {
			if ok && strings.TrimSpace(s) == "" && len(frags) == 1 {
				continue
			}
			return true
		}
	}
	return false
}

func unwrapInterpolation(v []any) ([]any, bool) {
	if len(v) == 1 {
		if inner, ok := v[0].([]any); ok {
			return inner, true
		}
	}
	return nil, false
}

// convertExpr turns a {{...}} expression into shell. A parameter becomes its
// positional argument: quoted when it stands alone as an unquoted word (an
// argument with spaces now stays one word, which is what the author meant),
// raw otherwise (inside quotes or glued to other text, matching just's text
// substitution).
func convertExpr(expr []any, params map[string]param, vars map[string]string, standalone, bash bool) (string, error) {
	if len(expr) == 0 {
		return "", fmt.Errorf("empty interpolation")
	}
	kind, _ := expr[0].(string)
	switch kind {
	case "variable":
		name, _ := expr[1].(string)
		p, ok := params[name]
		if !ok {
			// Parameters shadow variables, as in just.
			if v, isVar := vars[name]; isVar {
				return v, nil
			}
			return "", fmt.Errorf("uses variable %s", name)
		}
		var s string
		switch {
		case p.kind != "singular" && p.index == 1:
			if standalone {
				return `"$@"`, nil
			}
			s = "$*"
		case p.kind != "singular":
			if !bash {
				return "", fmt.Errorf("variadic %s doesn't start at the first argument; needs bash", name)
			}
			if standalone {
				return fmt.Sprintf(`"${@:%d}"`, p.index), nil
			}
			s = fmt.Sprintf("${*:%d}", p.index)
		case p.hasDefault:
			s = fmt.Sprintf("${%d:-%s}", p.index, p.defaultVal)
		default:
			s = fmt.Sprintf("${%d}", p.index)
		}
		if standalone {
			return `"` + s + `"`, nil
		}
		return s, nil
	case "call":
		fn, _ := expr[1].(string)
		var s string
		switch fn {
		case "invocation_directory", "invocation_directory_native":
			s = "${ZDEV_CALLER_DIR}"
		case "justfile_directory", "source_directory":
			s = "${PROJECTPATH}/.zdev/commands"
		case "env_var", "env":
			if len(expr) == 3 {
				if v, ok := expr[2].(string); ok && envNameRe.MatchString(v) {
					s = "${" + v + "}"
				}
			}
		}
		if s == "" {
			return "", fmt.Errorf("uses the just function %s()", fn)
		}
		if standalone {
			return `"` + s + `"`, nil
		}
		return s, nil
	}
	return "", fmt.Errorf("uses a just expression (%s)", kind)
}

// sortedParams returns parameters in position order.
func sortedParams(params map[string]param) []param {
	var ps []param
	for _, p := range params {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, k int) bool { return ps[i].index < ps[k].index })
	return ps
}

func hasDefaults(params map[string]param) bool {
	for _, p := range params {
		if p.hasDefault {
			return true
		}
	}
	return false
}

// fillDefaults rebuilds the positional arguments with defaults filled in, in
// POSIX sh: save the singular parameters, shift them off, set them back in
// front of the rest.
func fillDefaults(params map[string]param) string {
	if !hasDefaults(params) {
		return ""
	}
	var saves, refs []string
	n := 0
	for _, p := range sortedParams(params) {
		if p.kind != "singular" {
			break
		}
		n = p.index
		v := fmt.Sprintf("${%d}", p.index)
		if p.hasDefault {
			v = fmt.Sprintf("${%d:-%s}", p.index, p.defaultVal)
		}
		saves = append(saves, fmt.Sprintf("__zdev_%d=%s", p.index, v))
		refs = append(refs, fmt.Sprintf(`"$__zdev_%d"`, p.index))
	}
	return fmt.Sprintf(`%s; if [ $# -ge %d ]; then shift %d; else shift $#; fi; set -- %s "$@"`,
		strings.Join(saves, "; "), n, n, strings.Join(refs, " "))
}

// argCheck reproduces just's "missing arguments" error.
func argCheck(params map[string]param, jobID string) string {
	type named struct {
		name string
		p    param
	}
	var ps []named
	for n, p := range params {
		ps = append(ps, named{n, p})
	}
	sort.Slice(ps, func(i, k int) bool { return ps[i].p.index < ps[k].p.index })

	required := 0
	var usage []string
	for _, x := range ps {
		switch {
		case x.p.kind == "plus":
			required++
			usage = append(usage, "<"+x.name+">...")
		case x.p.kind == "star":
			usage = append(usage, "["+x.name+"...]")
		case x.p.hasDefault:
			usage = append(usage, "["+x.name+"]")
		default:
			required++
			usage = append(usage, "<"+x.name+">")
		}
	}
	if required == 0 {
		return ""
	}
	return fmt.Sprintf(`if [ $# -lt %d ]; then fail "usage: zdev run %s %s"; fi`, required, jobID, strings.Join(usage, " "))
}

// quoteState tracks whether a position in shell text is inside quotes.
type quoteState struct {
	single, double, escape bool
}

func (q *quoteState) feed(s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q.escape:
			q.escape = false
		case q.single:
			if c == '\'' {
				q.single = false
			}
		case c == '\\':
			q.escape = true
		case q.double:
			if c == '"' {
				q.double = false
			}
		case c == '"':
			q.double = true
		case c == '\'':
			q.single = true
		}
	}
}

func (q *quoteState) unquoted() bool { return !q.single && !q.double }

func atWordStart(before string) bool {
	if before == "" {
		return true
	}
	c := before[len(before)-1]
	return c == ' ' || c == '\t' || c == '(' || c == ';' || c == '|' || c == '&'
}

func atWordEnd(frags []any, i int) bool {
	if i+1 >= len(frags) {
		return true
	}
	next, ok := frags[i+1].(string)
	if !ok || next == "" {
		return false
	}
	c := next[0]
	return c == ' ' || c == '\t' || c == ';' || c == '|' || c == '&' || c == ')'
}

func joinText(frags []any) string {
	var sb strings.Builder
	for _, f := range frags {
		if s, ok := f.(string); ok {
			sb.WriteString(s)
		}
	}
	return sb.String()
}

// isDefaultSetting reports whether a dumped setting holds just's default:
// null, false, or an empty list (newer just versions dump list-valued
// settings such as dotenv_command as [] when unset).
func isDefaultSetting(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case []any:
		return len(x) == 0
	}
	return false
}

func boolSetting(settings map[string]any, key string) bool {
	v, _ := settings[key].(bool)
	return v
}

// recipeOrder returns recipe names in source order. The JSON dump is a map,
// so positions come from the recipe header lines in the source; anything not
// found sorts last, alphabetically.
func recipeOrder(recipes map[string]recipe, source []byte) []string {
	pos := map[string]int{}
	lines := strings.Split(string(source), "\n")
	for name := range recipes {
		re := regexp.MustCompile(`^@?` + regexp.QuoteMeta(name) + `(\s|:|$)`)
		pos[name] = len(lines) + 1
		for i, l := range lines {
			if re.MatchString(l) {
				pos[name] = i
				break
			}
		}
	}
	names := sortedKeys(recipes)
	sort.SliceStable(names, func(i, k int) bool { return pos[names[i]] < pos[names[k]] })
	return names
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func render(name, description string, single bool, jobs []*job) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Converted from .zdev/commands/%s.just by `zdev run --convert-just`.\n", name)
	if single {
		j := jobs[0]
		if j.description == "" {
			j.description = description
		}
		writeJob(&sb, j, true)
		return sb.String()
	}
	if description != "" {
		fmt.Fprintf(&sb, "description = %s\n", tomlString(description))
	}
	for _, j := range jobs {
		fmt.Fprintf(&sb, "\n[jobs.%s]\n", j.name)
		writeJob(&sb, j, false)
	}
	return sb.String()
}

func writeJob(sb *strings.Builder, j *job, single bool) {
	if j.description != "" {
		fmt.Fprintf(sb, "description = %s\n", tomlString(j.description))
	}
	if len(j.aliases) > 0 && !single {
		fmt.Fprintf(sb, "aliases = %s\n", tomlList(j.aliases))
	}
	if j.hidden {
		sb.WriteString("hidden = true\n")
	}
	if len(j.needs) > 0 {
		fmt.Fprintf(sb, "needs = %s\n", tomlList(j.needs))
	}
	if j.workdir != "" {
		fmt.Fprintf(sb, "workdir = %s\n", tomlString(j.workdir))
	}
	if j.confirm != "" {
		fmt.Fprintf(sb, "confirm = %s\n", tomlString(j.confirm))
	}
	if j.run != "" {
		fmt.Fprintf(sb, "run = %s\n", tomlMultiline(j.run))
	}
}

// tomlString writes a TOML basic string. strconv.Quote would emit \x
// escapes, which TOML doesn't have.
func tomlString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&sb, `\u%04X`, r)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func tomlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// tomlMultiline writes a body verbatim as a ”' literal string, or as an
// escaped """ string in the rare case the body itself contains ”'.
func tomlMultiline(body string) string {
	if !strings.Contains(body, "'''") {
		return "'''\n" + body + "'''"
	}
	esc := strings.ReplaceAll(body, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"""`, `""\"`)
	return `"""` + "\n" + esc + `"""`
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
