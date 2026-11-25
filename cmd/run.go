package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/0ploy/zdev/internal/agent"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/run"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	runShow    bool
	runJSON    bool
	runYes     bool
	runVerbose bool
	runConvert bool
)

var runCmd = &cobra.Command{
	Use:   "run [flags] [<command> [job] [args...]]",
	Short: "Run a project command from .zdev/run/",
	Long: `Run a project command defined in .zdev/run/<command>.toml.

  zdev run                       list all commands
  zdev run deploy                list the jobs of deploy.toml
  zdev run deploy all prod       run job "all" with argument "prod"
  zdev run test --filter Foo     single-job file: everything after the name is an argument

zdev's own flags go before the command name; everything after it reaches the
job verbatim, including --help.`,
	Args:              cobra.ArbitraryArgs,
	ValidArgsFunction: completeRun,
	RunE:              runRun,
}

func init() {
	runCmd.Flags().BoolVar(&runShow, "show", false, "Print what would run (dependencies, service, workdir, script) without running it")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "List all commands and jobs as JSON")
	runCmd.Flags().BoolVarP(&runYes, "yes", "y", false, "Answer yes to confirm prompts")
	runCmd.Flags().BoolVarP(&runVerbose, "verbose", "v", false, "Show the output of quiet jobs")
	runCmd.Flags().BoolVar(&runConvert, "convert-just", false, "Convert .zdev/commands/<command>.just files (all without a name) to .zdev/run/ job files")
	runCmd.Flags().SetInterspersed(false) // everything after the command name belongs to the job
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	dir, err := config.FindProjectDir()
	if err != nil {
		return err
	}
	if runConvert {
		ctx := context.Background()
		dump, err := defaultJustDumper(ctx)
		if err != nil {
			return err
		}
		return convertJustfiles(ctx, os.Stdout, dir, args, dump)
	}
	return runInProject(dir, args)
}

// runInProject is `zdev run <args>` for a project directory (also used by
// `zdev create --auto-setup`). Job failures exit the process with the job's
// exit code.
func runInProject(dir string, args []string) error {
	proj, err := project.LoadFromDir(dir)
	if err != nil {
		return err
	}
	var services []string
	for name := range proj.Config.Services {
		services = append(services, name)
	}
	set, err := run.Load(dir, run.Options{Services: services})
	if err != nil {
		return err
	}

	if runJSON {
		if len(args) > 0 {
			return fmt.Errorf("--json lists everything and takes no command")
		}
		return printRunJSON(os.Stdout, set)
	}
	for _, w := range set.Warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
	if len(args) == 0 {
		printRunList(os.Stdout, set)
		return nil
	}

	target, err := set.Resolve(args)
	if err != nil {
		return err
	}
	if target.Job == nil {
		printRunFile(os.Stdout, target.File)
		return nil
	}
	plan, err := set.Plan(target, dir)
	if err != nil {
		return err
	}
	if runShow {
		printRunPlan(os.Stdout, args, plan)
		return nil
	}

	ctx := context.Background()
	// In an agent sandbox, container steps go through the relay to the host,
	// and host steps run here, in the sandbox, never on the developer's machine.
	var rt run.ContainerRuntime = proj.Runtime
	if inSandbox() {
		rt = agent.RelayRuntime{Socket: os.Getenv("ZDEV_RELAY")}
	} else if planUsesContainers(plan) {
		if err := requireDocker(ctx); err != nil {
			return err
		}
	}

	plain := false
	if cfg, err := config.LoadGlobalConfig(); err == nil && cfg != nil {
		plain = ui.PlainMode(cfg.Terminal.Plain)
	}
	callerDir, _ := os.Getwd()
	hostEnv := proj.RunEnv()
	hostEnv["ZDEV_CALLER_DIR"] = callerDir

	runner := &run.Runner{
		ProjectDir:  dir,
		ProjectName: proj.Config.Name,
		Runtime:     rt,
		HostEnv:     hostEnv,
		ContainerEnv: map[string]string{
			"PROJECTNAME": proj.Config.Name,
			"ZDEV_DOMAIN": config.GetZdevDomain(),
		},
		Verbose:  runVerbose,
		Yes:      runYes,
		Plain:    plain,
		Color:    !plain && ui.SupportsColors(),
		Terminal: term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())),
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}

	err = runner.Run(ctx, plan)
	// A failing job already reported itself; exit with its code like a shell
	// would, without cobra's "Error: ..." on top.
	var jobErr *run.JobError
	if errors.As(err, &jobErr) {
		os.Exit(jobErr.Code)
	}
	if errors.Is(err, run.ErrAborted) {
		fmt.Fprintln(os.Stderr, "Aborted.")
		os.Exit(1)
	}
	return err
}

func planUsesContainers(p *run.Plan) bool {
	for _, st := range p.Steps {
		if st.Job.Service != run.ServiceHost {
			return true
		}
	}
	return false
}

// names joins a name with its aliases: "test, t".
func names(name string, aliases []string) string {
	return strings.Join(append([]string{name}, aliases...), ", ")
}

func printRunList(w io.Writer, set *run.Set) {
	visible := 0
	for _, f := range set.Files {
		if !f.Hidden {
			visible++
		}
	}
	if visible == 0 && len(set.Broken) == 0 {
		fmt.Fprintln(w, "No project commands. Add one as .zdev/run/<name>.toml.")
		return
	}

	fmt.Fprintln(w, "Project commands (zdev run <command> [job] [args]):")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, f := range set.Files {
		if f.Hidden {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\n", names(f.Name, f.Aliases), f.Description)
		if f.Single {
			continue
		}
		for _, j := range f.Jobs {
			if !j.Hidden {
				fmt.Fprintf(tw, "    %s\t%s\n", names(j.Name, j.Aliases), j.Description)
			}
		}
	}
	tw.Flush()
	printBroken(w, set)
}

func printBroken(w io.Writer, set *run.Set) {
	if len(set.Broken) == 0 {
		return
	}
	var keys []string
	for k := range set.Broken {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Broken (fix these to use them):")
	for _, k := range keys {
		for _, line := range strings.Split(set.Broken[k].Error(), "\n") {
			fmt.Fprintln(w, "  "+line)
		}
	}
}

func printRunFile(w io.Writer, f *run.File) {
	if f.Description != "" {
		fmt.Fprintf(w, "%s: %s\n\n", f.Name, f.Description)
	}
	fmt.Fprintf(w, "Jobs (zdev run %s <job> [args]):\n\n", f.Name)
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, j := range f.Jobs {
		if !j.Hidden {
			fmt.Fprintf(tw, "  %s\t%s\n", names(j.Name, j.Aliases), j.Description)
		}
	}
	tw.Flush()
}

func printRunPlan(w io.Writer, args []string, p *run.Plan) {
	fmt.Fprintf(w, "zdev run %s\n", shellJoin(args))
	for i, st := range p.Steps {
		j := st.Job
		where := j.Service
		if j.User != "" {
			where += ", user " + j.User
		}
		fmt.Fprintf(w, "\n%d. %s  (%s, output %s)\n", i+1, j.ID(), where, j.Output)
		if len(st.Args) > 0 {
			fmt.Fprintf(w, "   args     %s\n", shellJoin(st.Args))
		}
		workdir := st.Workdir
		if workdir == "" {
			workdir = "(service default)"
		}
		fmt.Fprintf(w, "   workdir  %s\n", workdir)
		if len(st.Env) > 0 {
			var keys []string
			for k := range st.Env {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			// Keys only: env_file values may be secrets.
			fmt.Fprintf(w, "   env      %s\n", strings.Join(keys, " "))
		}
		if st.Confirm != "" {
			fmt.Fprintf(w, "   confirm  %q\n", st.Confirm)
		}
		if st.Script != nil {
			fmt.Fprintf(w, "   script   %s\n", shellJoin(append(append([]string{}, st.Script...), st.Args...)))
			continue
		}
		if st.OnlyNeeds() {
			fmt.Fprintln(w, "   (only runs its dependencies)")
			continue
		}
		fmt.Fprintln(w, "   run")
		for _, line := range strings.Split(strings.TrimRight(j.Run, "\n"), "\n") {
			fmt.Fprintf(w, "     | %s\n", line)
		}
	}
}

// shellJoin quotes arguments that need it, for display only.
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

type runJSONJob struct {
	Name        string   `json:"name"`
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Service     string   `json:"service"`
	User        string   `json:"user,omitempty"`
	Output      string   `json:"output"`
	Needs       []string `json:"needs,omitempty"`
	Confirm     string   `json:"confirm,omitempty"`
	Hidden      bool     `json:"hidden,omitempty"`
}

type runJSONFile struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Aliases     []string     `json:"aliases,omitempty"`
	Single      bool         `json:"single"`
	Hidden      bool         `json:"hidden,omitempty"`
	Jobs        []runJSONJob `json:"jobs"`
}

type runJSONBroken struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type runJSONOutput struct {
	Files    []runJSONFile   `json:"files"`
	Broken   []runJSONBroken `json:"broken"`
	Warnings []string        `json:"warnings"`
}

func printRunJSON(w io.Writer, set *run.Set) error {
	out := runJSONOutput{Files: []runJSONFile{}, Broken: []runJSONBroken{}, Warnings: set.Warnings}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, f := range set.Files {
		jf := runJSONFile{Name: f.Name, Description: f.Description, Aliases: f.Aliases, Single: f.Single, Hidden: f.Hidden}
		for _, j := range f.Jobs {
			jf.Jobs = append(jf.Jobs, runJSONJob{
				Name: j.Name, ID: j.ID(), Description: j.Description, Aliases: j.Aliases,
				Service: j.Service, User: j.User, Output: j.Output, Needs: j.Needs,
				Confirm: j.Confirm, Hidden: j.Hidden,
			})
		}
		out.Files = append(out.Files, jf)
	}
	var keys []string
	for k := range set.Broken {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out.Broken = append(out.Broken, runJSONBroken{File: k + ".toml", Error: set.Broken[k].Error()})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// completeRun completes command names, then job names of multi-job files.
// Arguments after that are the job's business: fall back to file completion.
func completeRun(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 1 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	dir, err := config.FindProjectDir()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	set, err := run.Load(dir, run.Options{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	if len(args) == 0 {
		for _, f := range set.Files {
			if !f.Hidden {
				out = append(out, f.Name+"\t"+f.Description)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	target, err := set.Resolve(args[:1])
	if err != nil || target.File.Single {
		return nil, cobra.ShellCompDirectiveDefault
	}
	for _, j := range target.File.Jobs {
		if !j.Hidden {
			out = append(out, j.Name+"\t"+j.Description)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
