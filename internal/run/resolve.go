package run

import (
	"fmt"
	"strings"
)

// Target is what a command line resolves to.
type Target struct {
	File *File
	// Job is nil when a multi-job file was named without a job: the caller
	// lists the file's jobs, it never runs a default.
	Job  *Job
	Args []string
}

// Resolve maps `zdev run` arguments to a job. The first token names a file
// (or file alias). In a single-job file every further token is an argument;
// in a multi-job file the second token must name a job, so adding a job can
// never change what an existing command line means.
func (s *Set) Resolve(tokens []string) (Target, error) {
	if len(tokens) == 0 {
		return Target{}, fmt.Errorf("no command given")
	}
	name := tokens[0]
	f := s.byName[name]
	if f == nil {
		if err, broken := s.Broken[name]; broken {
			return Target{}, err
		}
		return Target{}, fmt.Errorf("unknown command %q%s", name, available(s.visibleFileNames()))
	}
	if f.Single {
		return Target{File: f, Job: f.Jobs[0], Args: tokens[1:]}, nil
	}
	if len(tokens) == 1 {
		return Target{File: f}, nil
	}
	j := f.Job(tokens[1])
	if j == nil {
		return Target{}, fmt.Errorf("%s has no job %q%s", f.Name, tokens[1], available(f.visibleJobNames()))
	}
	return Target{File: f, Job: j, Args: tokens[2:]}, nil
}

func (s *Set) visibleFileNames() []string {
	var names []string
	for _, f := range s.Files {
		if !f.Hidden {
			names = append(names, f.Name)
		}
	}
	return names
}

func (f *File) visibleJobNames() []string {
	var names []string
	for _, j := range f.Jobs {
		if !j.Hidden {
			names = append(names, j.Name)
		}
	}
	return names
}

func available(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (available: " + strings.Join(names, ", ") + ")"
}
