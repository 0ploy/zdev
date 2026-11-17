package run

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"
)

// Step is one job of a run with its arguments and every placeholder resolved.
type Step struct {
	Job  *Job
	Args []string
	// Workdir: for host jobs an absolute path (project root by default); for
	// container jobs the path as written, empty meaning the service's own.
	Workdir string
	// Env is the job's own env_file + env, substituted. Not the inherited
	// process environment.
	Env map[string]string
	// Script is the substituted `script` command line (path + words), nil
	// for run jobs.
	Script  []string
	Confirm string
}

// OnlyNeeds reports a job that only runs its dependencies.
func (st Step) OnlyNeeds() bool {
	return st.Script == nil && strings.TrimSpace(st.Job.Run) == ""
}

// Plan is the ordered list of steps a `zdev run` executes: dependencies
// first, depth first, each (job, arguments) once; the target is last.
type Plan struct {
	Steps []Step
}

// Plan resolves dependencies and placeholders for a target. Nothing runs and
// nothing outside the project is read, so `zdev run --show` uses it as is.
func (s *Set) Plan(t Target, projectDir string) (*Plan, error) {
	if t.Job == nil {
		return nil, fmt.Errorf("%s has several jobs, name one%s", t.File.Name, available(t.File.visibleJobNames()))
	}
	p := &Plan{}
	seen := map[string]bool{}

	var visit func(j *Job, args []string) error
	visit = func(j *Job, args []string) error {
		key := j.ID() + "\x00" + strings.Join(args, "\x00")
		if seen[key] {
			return nil
		}
		seen[key] = true

		for _, n := range j.Needs {
			if err := missingArg(fmt.Sprintf("%s: needs %q", j.ID(), n), n, args); err != nil {
				return err
			}
			dep, depArgs, err := s.resolveNeed(j, n)
			if err != nil {
				return fmt.Errorf("%s: needs %q: %w", j.ID(), n, err)
			}
			var resolved []string
			for _, a := range depArgs {
				resolved = append(resolved, substituteWords(a, args)...)
			}
			if err := visit(dep, resolved); err != nil {
				return err
			}
		}

		step, err := buildStep(j, args, projectDir)
		if err != nil {
			return err
		}
		p.Steps = append(p.Steps, step)
		return nil
	}

	if err := visit(t.Job, t.Args); err != nil {
		return nil, err
	}
	return p, nil
}

func buildStep(j *Job, args []string, projectDir string) (Step, error) {
	st := Step{Job: j, Args: args, Confirm: substitute(j.Confirm, args)}

	if err := missingArg(j.ID()+": workdir", j.Workdir, args); err != nil {
		return st, err
	}
	wd := substitute(j.Workdir, args)
	if j.Service == ServiceHost {
		switch {
		case wd == "":
			wd = projectDir
		case !filepath.IsAbs(wd):
			wd = filepath.Join(projectDir, wd)
		}
	}
	st.Workdir = wd

	if j.EnvFile != "" || len(j.Env) > 0 {
		st.Env = map[string]string{}
	}
	if j.EnvFile != "" {
		path := j.EnvFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectDir, path)
		}
		fileEnv, err := readDotenv(path)
		if err != nil {
			return st, fmt.Errorf("%s: env_file: %w", j.ID(), err)
		}
		maps.Copy(st.Env, fileEnv)
	}
	for k, v := range j.Env {
		st.Env[k] = substitute(v, args)
	}

	if j.Script != "" {
		st.Script = substituteWords(j.Script, args)
		if len(st.Script) == 0 || st.Script[0] == "" {
			return st, fmt.Errorf("%s: script resolves to an empty path", j.ID())
		}
		if j.Service == ServiceHost && !filepath.IsAbs(st.Script[0]) {
			st.Script[0] = filepath.Join(projectDir, st.Script[0])
		}
	}
	return st, nil
}
