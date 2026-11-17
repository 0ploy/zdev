// Package run loads and resolves project jobs defined in .zdev/run/*.toml,
// the files behind `zdev run <file> [job] [args]`.
package run

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ServiceHost is the service value for jobs that run on the host.
const ServiceHost = "host"

// Output modes.
const (
	OutputShow  = "show"
	OutputQuiet = "quiet"
)

// Dir returns the directory holding job files for a project.
func Dir(projectDir string) string {
	return filepath.Join(projectDir, ".zdev", "run")
}

// File is one .zdev/run/<name>.toml file.
type File struct {
	Name        string
	Path        string
	Description string
	Aliases     []string
	// Hidden is only ever set on single-job files.
	Hidden bool
	// Single is true for a file with a top-level run/script; it then has
	// exactly one job, whose Name is empty.
	Single bool
	// Jobs in the order they appear in the file.
	Jobs []*Job
}

// Job returns the job with this name or alias, or nil.
func (f *File) Job(name string) *Job {
	for _, j := range f.Jobs {
		if j.Name == name {
			return j
		}
	}
	for _, j := range f.Jobs {
		if slices.Contains(j.Aliases, name) {
			return j
		}
	}
	return nil
}

// Job is one runnable job, with file-level defaults already applied.
type Job struct {
	File        *File
	Name        string // empty for the job of a single-job file
	Description string
	Run         string
	Script      string
	Needs       []string
	Service     string
	Workdir     string
	User        string
	Env         map[string]string
	EnvFile     string
	Output      string
	Confirm     string
	Hidden      bool
	Aliases     []string
}

// ID is the job's name as typed on the command line: "test" or "deploy all".
func (j *Job) ID() string {
	if j.Name == "" {
		return j.File.Name
	}
	return j.File.Name + " " + j.Name
}

// Set is every job file of a project.
type Set struct {
	// Files that loaded and validated, sorted by name.
	Files []*File
	// Broken maps a file name to why it can't be used. One broken file never
	// takes the others down, except files whose jobs depend on it.
	Broken map[string]error
	// Warnings are non-fatal: unknown keys, so a job file written for a newer
	// zdev still loads.
	Warnings []string

	byName map[string]*File // names and aliases of Files
}

// Options configures Load.
type Options struct {
	// Services are the project's service names. Nil skips the check that a
	// job's service exists (e.g. when the project config doesn't load).
	Services []string
}

// FileError lists everything wrong with one job file.
type FileError struct {
	File     string
	Problems []string
}

func (e *FileError) Error() string {
	if len(e.Problems) == 1 {
		return e.File + ": " + e.Problems[0]
	}
	return e.File + ":\n  " + strings.Join(e.Problems, "\n  ")
}

var (
	nameRe        = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]*$`)
	envKeyRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholderRe = regexp.MustCompile(`\{(all|[0-9]+)\}`)
)

// rawJob mirrors the keys a job accepts. Pointers distinguish "unset"
// (inherit the file-level value) from an explicit empty value.
type rawJob struct {
	Description *string           `toml:"description"`
	Run         *string           `toml:"run"`
	Script      *string           `toml:"script"`
	Needs       []string          `toml:"needs"`
	Service     *string           `toml:"service"`
	Workdir     *string           `toml:"workdir"`
	User        *string           `toml:"user"`
	Env         map[string]string `toml:"env"`
	EnvFile     *string           `toml:"env_file"`
	Output      *string           `toml:"output"`
	Confirm     *string           `toml:"confirm"`
	Hidden      *bool             `toml:"hidden"`
	Aliases     []string          `toml:"aliases"`
}

type rawFile struct {
	rawJob
	Jobs map[string]rawJob `toml:"jobs"`
}

// Load reads every job file in .zdev/run/. A missing directory is an empty
// set. The error is only for a directory that can't be read; problems in a
// file land in Set.Broken.
func Load(projectDir string, opts Options) (*Set, error) {
	s := &Set{Broken: map[string]error{}}

	dir := Dir(projectDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			s.index()
			return s, nil
		}
		return nil, err
	}

	for _, e := range entries {
		fn := e.Name()
		// Dotfiles are editor droppings (.#deploy.toml), never job files.
		if e.IsDir() || strings.HasPrefix(fn, ".") || !strings.HasSuffix(fn, ".toml") {
			continue
		}
		name := strings.TrimSuffix(fn, ".toml")
		path := filepath.Join(dir, fn)
		if !nameRe.MatchString(name) {
			s.Broken[name] = &FileError{File: fn, Problems: []string{
				"invalid file name, use lowercase letters, digits, - and _ (not starting with -)"}}
			continue
		}
		f, warnings, err := loadFile(name, path, opts)
		s.Warnings = append(s.Warnings, warnings...)
		if err != nil {
			s.Broken[name] = err
			continue
		}
		s.Files = append(s.Files, f)
	}

	s.checkAliases()
	s.index()
	s.checkNeeds()
	return s, nil
}

func loadFile(name, path string, opts Options) (*File, []string, error) {
	fn := filepath.Base(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, &FileError{File: fn, Problems: []string{err.Error()}}
	}

	var raw rawFile
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, nil, &FileError{File: fn, Problems: []string{err.Error()}}
	}

	var warnings []string
	for _, k := range md.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("%s: unknown key %q ignored (written for a newer zdev?)", fn, k.String()))
	}

	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	f := &File{Name: name, Path: path}
	top := raw.rawJob
	hasTopScript := top.Run != nil || top.Script != nil
	hasJobs := md.IsDefined("jobs")

	switch {
	case hasTopScript && hasJobs:
		add("has both a top-level run/script and [jobs.*]; use one or the other")
	case !hasTopScript && len(raw.Jobs) == 0:
		add("defines no job: add a top-level run or script, or [jobs.<name>] tables")
	}
	if len(problems) > 0 {
		return nil, warnings, &FileError{File: fn, Problems: problems}
	}

	f.Description = deref(top.Description)
	f.Aliases = top.Aliases
	for _, a := range f.Aliases {
		if !nameRe.MatchString(a) {
			add("aliases: invalid name %q", a)
		}
	}

	if hasTopScript {
		f.Single = true
		f.Hidden = deref(top.Hidden)
		j := buildJob(f, "", rawJob{}, top)
		j.Aliases = nil // aliases of a single-job file name the file
		problems = append(problems, validateJob(j, "", opts)...)
		f.Jobs = []*Job{j}
	} else {
		if len(top.Needs) > 0 {
			add("needs is only allowed on a job ([jobs.<name>])")
		}
		if top.Hidden != nil {
			add("hidden is only allowed on a job ([jobs.<name>])")
		}
		for _, jn := range jobOrder(md, raw.Jobs) {
			loc := "jobs." + jn
			if !nameRe.MatchString(jn) {
				add("%s: invalid job name, use lowercase letters, digits, - and _ (not starting with -)", loc)
				continue
			}
			j := buildJob(f, jn, top, raw.Jobs[jn])
			problems = append(problems, validateJob(j, loc+": ", opts)...)
			f.Jobs = append(f.Jobs, j)
		}
		problems = append(problems, checkJobAliases(f)...)
	}

	if len(problems) > 0 {
		return nil, warnings, &FileError{File: fn, Problems: problems}
	}
	return f, warnings, nil
}

// jobOrder returns job names in file order, so listings follow the author.
func jobOrder(md toml.MetaData, jobs map[string]rawJob) []string {
	var order []string
	seen := map[string]bool{}
	for _, k := range md.Keys() {
		if len(k) >= 2 && k[0] == "jobs" && !seen[k[1]] {
			seen[k[1]] = true
			order = append(order, k[1])
		}
	}
	// Defensive: anything Keys() didn't report, sorted at the end.
	var rest []string
	for jn := range jobs {
		if !seen[jn] {
			rest = append(rest, jn)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

// buildJob applies file-level defaults (def) under the job's own keys (r).
// description and aliases are never inherited: at file level they describe
// and name the file.
func buildJob(f *File, name string, def, r rawJob) *Job {
	pick := func(job, file *string) string {
		if job != nil {
			return *job
		}
		return deref(file)
	}
	j := &Job{
		File:        f,
		Name:        name,
		Description: deref(r.Description),
		Run:         deref(r.Run),
		Script:      deref(r.Script),
		Needs:       r.Needs,
		Service:     pick(r.Service, def.Service),
		Workdir:     pick(r.Workdir, def.Workdir),
		User:        pick(r.User, def.User),
		EnvFile:     pick(r.EnvFile, def.EnvFile),
		Output:      pick(r.Output, def.Output),
		Confirm:     pick(r.Confirm, def.Confirm),
		Hidden:      deref(r.Hidden),
		Aliases:     r.Aliases,
	}
	if j.Service == "" {
		j.Service = ServiceHost
	}
	if j.Output == "" {
		j.Output = OutputShow
	}
	if len(def.Env) > 0 || len(r.Env) > 0 {
		j.Env = map[string]string{}
		maps.Copy(j.Env, def.Env)
		maps.Copy(j.Env, r.Env)
	}
	return j
}

func validateJob(j *Job, loc string, opts Options) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, loc+fmt.Sprintf(format, args...)) }

	switch {
	case j.Run != "" && j.Script != "":
		add("has both run and script; use one")
	case strings.TrimSpace(j.Run) == "" && strings.TrimSpace(j.Script) == "" && len(j.Needs) == 0:
		add("needs a run or script (or needs, for a job that only runs dependencies)")
	}

	if j.Output != OutputShow && j.Output != OutputQuiet {
		add("output must be %q or %q, got %q", OutputShow, OutputQuiet, j.Output)
	}

	if j.Service == ServiceHost {
		if j.User != "" {
			add("user only applies to container jobs (service = \"<name>\"); on the host, use sudo in the script")
		}
	} else if opts.Services != nil && !slices.Contains(opts.Services, j.Service) {
		add("service %q is not defined in .zdev/config.yaml (services: %s)", j.Service, strings.Join(sortedCopy(opts.Services), ", "))
	}

	for k, v := range j.Env {
		if !envKeyRe.MatchString(k) {
			add("env: invalid variable name %q", k)
		}
		if err := checkPlaceholders(v); err != nil {
			add("env.%s: %v", k, err)
		}
	}
	for field, v := range map[string]string{"workdir": j.Workdir, "script": j.Script, "confirm": j.Confirm} {
		if err := checkPlaceholders(v); err != nil {
			add("%s: %v", field, err)
		}
	}
	for _, n := range j.Needs {
		if strings.TrimSpace(n) == "" {
			add("needs: empty entry")
		} else if err := checkPlaceholders(n); err != nil {
			add("needs %q: %v", n, err)
		}
	}
	for _, a := range j.Aliases {
		if !nameRe.MatchString(a) {
			add("aliases: invalid name %q", a)
		}
	}
	sort.Strings(problems) // map iteration above must not reorder output
	return problems
}

// checkJobAliases rejects job aliases that collide with a job name or another alias.
func checkJobAliases(f *File) []string {
	var problems []string
	owner := map[string]string{}
	for _, j := range f.Jobs {
		owner[j.Name] = j.Name
	}
	for _, j := range f.Jobs {
		for _, a := range j.Aliases {
			if other, ok := owner[a]; ok {
				problems = append(problems, fmt.Sprintf("jobs.%s: alias %q collides with job %q", j.Name, a, other))
				continue
			}
			owner[a] = j.Name
		}
	}
	return problems
}

// checkAliases moves files whose aliases collide with another file's name or
// alias to Broken. Names always win over aliases; between aliases, the file
// that sorts first keeps it.
func (s *Set) checkAliases() {
	owner := map[string]string{}
	for _, f := range s.Files {
		owner[f.Name] = f.Name
	}
	for name := range s.Broken {
		owner[name] = name // a broken file still owns its name
	}
	var keep []*File
	for _, f := range s.Files {
		var problems []string
		for _, a := range f.Aliases {
			if other, ok := owner[a]; ok && other != f.Name {
				problems = append(problems, fmt.Sprintf("alias %q collides with %s", a, describeOwner(a, other)))
			}
		}
		if len(problems) > 0 {
			s.Broken[f.Name] = &FileError{File: filepath.Base(f.Path), Problems: problems}
			continue
		}
		for _, a := range f.Aliases {
			owner[a] = f.Name
		}
		keep = append(keep, f)
	}
	s.Files = keep
}

func describeOwner(alias, owner string) string {
	if alias == owner {
		return "file " + owner + ".toml"
	}
	return "an alias of " + owner + ".toml"
}

func (s *Set) index() {
	s.byName = map[string]*File{}
	for _, f := range s.Files {
		s.byName[f.Name] = f
	}
	for _, f := range s.Files {
		for _, a := range f.Aliases {
			s.byName[a] = f
		}
	}
}

// checkNeeds validates every dependency (target exists, isn't broken, no
// placeholder in the target, no cycle). A file with a bad dependency becomes
// broken, which can in turn break files depending on it, so this repeats
// until nothing changes.
func (s *Set) checkNeeds() {
	for {
		changed := false
		for _, f := range s.Files {
			var problems []string
			for _, j := range f.Jobs {
				for _, n := range j.Needs {
					if _, _, err := s.resolveNeed(j, n); err != nil {
						problems = append(problems, fmt.Sprintf("%sneeds %q: %v", jobLoc(j), n, err))
					}
				}
			}
			if len(problems) > 0 {
				s.markBroken(f, problems)
				changed = true
				break // Files changed under us; start over
			}
		}
		if changed {
			continue
		}
		if cycle := s.findCycle(); cycle != nil {
			ids := make([]string, len(cycle))
			for i, j := range cycle {
				ids[i] = j.ID()
			}
			msg := "dependency cycle: " + strings.Join(ids, " -> ")
			for _, j := range cycle {
				if _, done := s.Broken[j.File.Name]; !done {
					s.Broken[j.File.Name] = &FileError{File: filepath.Base(j.File.Path), Problems: []string{msg}}
				}
			}
			s.dropBroken()
			continue
		}
		return
	}
}

func jobLoc(j *Job) string {
	if j.Name == "" {
		return ""
	}
	return "jobs." + j.Name + ": "
}

func (s *Set) markBroken(f *File, problems []string) {
	s.Broken[f.Name] = &FileError{File: filepath.Base(f.Path), Problems: problems}
	s.dropBroken()
}

func (s *Set) dropBroken() {
	var keep []*File
	for _, f := range s.Files {
		if _, broken := s.Broken[f.Name]; !broken {
			keep = append(keep, f)
		}
	}
	s.Files = keep
	s.index()
}

// findCycle returns the jobs of one dependency cycle (first job repeated at
// the end), or nil.
func (s *Set) findCycle() []*Job {
	const (
		unvisited = iota
		active
		done
	)
	state := map[*Job]int{}
	var stack []*Job
	var cycle []*Job

	var visit func(j *Job) bool
	visit = func(j *Job) bool {
		state[j] = active
		stack = append(stack, j)
		for _, n := range j.Needs {
			dep, _, err := s.resolveNeed(j, n)
			if err != nil {
				continue // already reported by checkNeeds
			}
			switch state[dep] {
			case active:
				i := slices.Index(stack, dep)
				cycle = append(slices.Clone(stack[i:]), dep)
				return true
			case unvisited:
				if visit(dep) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[j] = done
		return false
	}

	for _, f := range s.Files {
		for _, j := range f.Jobs {
			if state[j] == unvisited && visit(j) {
				return cycle
			}
		}
	}
	return nil
}

// resolveNeed resolves one needs entry of job `from`. A bare name is a job in
// the same file first; otherwise the entry reads like a command line
// ("db reset", "build {1}"). The returned arguments may still contain
// placeholders.
func (s *Set) resolveNeed(from *Job, entry string) (*Job, []string, error) {
	words := strings.Fields(entry)
	if len(words) == 0 {
		return nil, nil, fmt.Errorf("empty entry")
	}
	if hasPlaceholder(words[0]) {
		return nil, nil, fmt.Errorf("the file or job name can't be a placeholder")
	}
	if !from.File.Single {
		if j := from.File.Job(words[0]); j != nil {
			if j == from {
				return nil, nil, fmt.Errorf("a job can't depend on itself")
			}
			return j, words[1:], nil
		}
	}
	if f := s.byName[words[0]]; f != nil && !f.Single && len(words) > 1 && hasPlaceholder(words[1]) {
		return nil, nil, fmt.Errorf("the job name can't be a placeholder")
	}
	t, err := s.Resolve(words)
	if err != nil {
		return nil, nil, err
	}
	if t.Job == nil {
		return nil, nil, fmt.Errorf("%s.toml has several jobs, name one (%q)", t.File.Name, t.File.Name+" <job>")
	}
	if t.Job == from {
		return nil, nil, fmt.Errorf("a job can't depend on itself")
	}
	return t.Job, t.Args, nil
}

func checkPlaceholders(s string) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if m[1] == "all" {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n == 0 || m[1][0] == '0' {
			return fmt.Errorf("invalid placeholder %s, arguments are {1}, {2}, ... or {all}", m[0])
		}
	}
	return nil
}

func hasPlaceholder(s string) bool {
	return placeholderRe.MatchString(s)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func sortedCopy(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}
