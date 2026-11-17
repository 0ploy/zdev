package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/runtime"
)

// Runner executes a Plan.
type Runner struct {
	ProjectDir  string
	ProjectName string
	Runtime     runtime.Runtime

	// HostEnv is added to the process environment for host jobs
	// (PROJECTNAME, PROJECTPATH, ZDEV_CALLER_DIR, ...).
	HostEnv map[string]string
	// ContainerEnv is passed to container jobs. Host paths mean nothing in
	// a container, so the caller keeps it to PROJECTNAME and ZDEV_DOMAIN.
	ContainerEnv map[string]string

	// Verbose forces output = "show" for every job.
	Verbose bool
	// Yes answers every confirm prompt with yes.
	Yes bool
	// Plain disables colors in zdev's own lines and in the print helpers
	// (exported to jobs as ZDEV_PLAIN=1).
	Plain bool
	// Color enables colors in zdev's own lines (stdout is a color terminal).
	Color bool
	// Terminal is true when stdin and stdout are both terminals: confirm
	// prompts are possible and container jobs get a TTY.
	Terminal bool

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// preludeSeps caches, per host shell, what goes between the prelude and
	// the body (see preludeSep).
	preludeSeps map[string]string
}

// JobError is a job that exited non-zero (or was interrupted). The CLI exits
// with Code and prints nothing more: the runner already reported it.
type JobError struct {
	Job  string
	Code int
	Err  error
}

func (e *JobError) Error() string {
	return failure(e.Job, e.Err)
}

// failure describes a failed job: its exit code, or the reason it couldn't
// start (missing interpreter, script not executable, docker error).
func failure(job string, err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("%s failed (exit %d)", job, exitCode(err))
	}
	return fmt.Sprintf("%s failed: %v", job, err)
}

func (e *JobError) Unwrap() error { return e.Err }

// ErrAborted is returned when the user answers no to a confirm prompt.
var ErrAborted = errors.New("aborted")

// Run executes the plan's steps in order and stops at the first failure.
func (r *Runner) Run(ctx context.Context, p *Plan) error {
	if err := r.confirm(p); err != nil {
		return err
	}
	if err := r.checkContainers(ctx, p); err != nil {
		return err
	}

	// The terminal delivers Ctrl-C to the whole process group, so the job
	// gets it directly; zdev only has to survive it and not start anything
	// after. SIGTERM is aimed at zdev alone, so it cancels the running job.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var interrupted atomic.Bool
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		for {
			select {
			case sig := <-sigs:
				interrupted.Store(true)
				if sig == syscall.SIGTERM {
					cancel()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	multi := len(p.Steps) > 1
	for _, st := range p.Steps {
		if interrupted.Load() {
			return &JobError{Job: st.Job.ID(), Code: 130, Err: ErrAborted}
		}
		if st.OnlyNeeds() {
			continue // its dependencies ran before it; nothing else to do
		}
		var err error
		if st.Job.Output == OutputQuiet && !r.Verbose {
			err = r.runQuiet(ctx, st)
		} else {
			if multi {
				r.printStep(r.Stdout, st.Job.ID())
			}
			err = r.execStep(ctx, st, r.Stdin, r.Stdout, r.Stderr, true)
			if err != nil {
				r.printLine(r.Stderr, colorRed, "✗ "+failure(st.Job.ID(), err))
			}
		}
		if err != nil {
			code := exitCode(err)
			if interrupted.Load() && code != 130 {
				code = 130
			}
			return &JobError{Job: st.Job.ID(), Code: code, Err: err}
		}
	}
	return nil
}

// confirm asks every confirm prompt of the plan before anything runs, so a
// "no" never leaves a half-done run behind.
func (r *Runner) confirm(p *Plan) error {
	for _, st := range p.Steps {
		if st.Confirm == "" || r.Yes {
			continue
		}
		if !r.Terminal {
			return fmt.Errorf("%s asks %q; pass --yes to run it without a terminal", st.Job.ID(), st.Confirm)
		}
		fmt.Fprintf(r.Stderr, "%s [y/N] ", st.Confirm)
		answer := strings.ToLower(strings.TrimSpace(readLine(r.Stdin)))
		if answer != "y" && answer != "yes" {
			return ErrAborted
		}
	}
	return nil
}

// readLine reads up to a newline one byte at a time, so nothing meant for
// the job is swallowed by a read buffer.
func readLine(in io.Reader) string {
	var sb strings.Builder
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			sb.WriteByte(b[0])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}

// checkContainers fails before anything runs when a container job's service
// isn't running, instead of half-way through.
func (r *Runner) checkContainers(ctx context.Context, p *Plan) error {
	checked := map[string]bool{}
	for _, st := range p.Steps {
		svc := st.Job.Service
		if svc == ServiceHost || checked[svc] {
			continue
		}
		checked[svc] = true
		running, err := r.Runtime.IsContainerRunning(ctx, r.containerName(svc))
		if err != nil {
			return fmt.Errorf("checking service %s: %w", svc, err)
		}
		if !running {
			return fmt.Errorf("%s runs in service %s, which is not running; start the project first (zdev start)", st.Job.ID(), svc)
		}
	}
	return nil
}

func (r *Runner) containerName(service string) string {
	return runtime.ScopedName(service, r.ProjectName)
}

// runQuiet captures the job's output. Marker lines (▶ ⚠ ✗) still reach the
// terminal as they come; everything else only on failure.
func (r *Runner) runQuiet(ctx context.Context, st Step) error {
	var buf bytes.Buffer
	w := &markerWriter{buf: &buf, emit: func(kind byte, msg string) { r.printMarker(kind, msg) }}
	start := time.Now()
	err := r.execStep(ctx, st, nil, w, w, false)
	w.flush()
	if err != nil {
		r.printLine(r.Stderr, colorRed, "✗ "+failure(st.Job.ID(), err))
		// Marker lines were already shown live; the dump is everything else.
		for _, line := range strings.SplitAfter(buf.String(), "\n") {
			if line == "" {
				continue
			}
			if _, _, ok := parseMarker(line); ok {
				continue
			}
			io.WriteString(r.Stderr, line)
			if !strings.HasSuffix(line, "\n") {
				fmt.Fprintln(r.Stderr)
			}
		}
		return err
	}
	r.printLine(r.Stdout, colorGreen, fmt.Sprintf("✓ %s (%s)", st.Job.ID(), formatDuration(time.Since(start))))
	return nil
}

// execStep runs one step with the given streams. show is false in quiet
// mode: no stdin, no TTY.
func (r *Runner) execStep(ctx context.Context, st Step, stdin io.Reader, stdout, stderr io.Writer, show bool) error {
	env := map[string]string{}
	if st.Job.Service == ServiceHost {
		maps.Copy(env, r.HostEnv)
	} else {
		maps.Copy(env, r.ContainerEnv)
	}
	maps.Copy(env, st.Env)
	if r.Plain {
		env["ZDEV_PLAIN"] = "1"
	}
	if !show {
		// Captured output: step drops its blank-line padding, zdev adds it
		// when it shows the marker live.
		env["ZDEV_QUIET"] = "1"
	}

	if st.Job.Service == ServiceHost {
		return r.execHost(ctx, st, env, stdin, stdout, stderr)
	}
	return r.execContainer(ctx, st, env, runtime.ExecStreamOptions{
		User:        st.Job.User,
		Workdir:     st.Workdir,
		Env:         env,
		Interactive: show && stdin != nil,
		TTY:         show && r.Terminal,
		Stdin:       stdin,
		Stdout:      stdout,
		Stderr:      stderr,
	})
}

func (r *Runner) execHost(ctx context.Context, st Step, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) error {
	var argv []string
	switch {
	case st.Script != nil:
		argv = append(append([]string{}, st.Script...), st.Args...)
	default:
		sb := parseShebang(st.Job.Run)
		switch {
		case sb == nil:
			body := r.preludeSep(ctx, []string{"bash"}) + st.Job.Run
			argv = append([]string{"bash", "-c", prelude + body, st.Job.ID()}, st.Args...)
		case sb.shell:
			body := r.preludeSep(ctx, sb.argv) + st.Job.Run
			argv = append(append(append([]string{}, sb.argv...), "-c", prelude+body, st.Job.ID()), st.Args...)
		default:
			f, err := os.CreateTemp("", "zdev-run-*")
			if err != nil {
				return err
			}
			defer os.Remove(f.Name())
			_, werr := f.WriteString(st.Job.Run)
			cerr := f.Close()
			if err := errors.Join(werr, cerr, os.Chmod(f.Name(), 0o700)); err != nil {
				return err
			}
			argv = append([]string{f.Name()}, st.Args...)
		}
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = st.Workdir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// On cancel (SIGTERM to zdev) give the job a chance to clean up before
	// the default SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if err != nil && st.Script != nil && errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%s is not executable (chmod +x %s)", st.Script[0], filepath.Base(st.Script[0]))
	}
	return err
}

// preludeSep returns what goes between the one-line prelude and the body.
// Normally nothing: the body continues on the prelude's line, so line
// numbers in shell errors match the job file. bash 3.x (the one macOS
// ships) reports `bash -c` line numbers one too low, so there the body
// starts on a new line instead. Probed once per interpreter.
func (r *Runner) preludeSep(ctx context.Context, shell []string) string {
	key := strings.Join(shell, " ")
	if sep, ok := r.preludeSeps[key]; ok {
		return sep
	}
	sep := ""
	argv := append(append([]string{}, shell...), "-c", `echo "${BASH_VERSINFO[0]:-0}"`)
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if v := strings.TrimSpace(string(out)); err == nil && (v == "1" || v == "2" || v == "3") {
		sep = "\n"
	}
	if r.preludeSeps == nil {
		r.preludeSeps = map[string]string{}
	}
	r.preludeSeps[key] = sep
	return sep
}

// containerLauncher runs a shell body in a container. $0 is the interpreter
// ("auto" for a body without shebang: bash where the image has it, sh
// otherwise, e.g. alpine), $1 the prelude, $2 the body, then the job id ($0
// of the script) and the arguments. busybox ash reports `sh -c` line
// numbers one too low, so there the body starts on a new line (see
// preludeSep for the same bash 3.x quirk on the host). The interpreter is
// left unquoted on purpose: a shebang like "/bin/sh -x" splits into words.
const containerLauncher = `i=$0 p=$1 b=$2; shift 2
if [ "$i" = auto ]; then
  if command -v bash >/dev/null 2>&1; then exec bash -c "$p$b" "$@"; fi
  i=sh
fi
case "${i%% *}" in sh|ash|*/sh|*/ash) if [ -n "${BB_ASH_VERSION:-}" ]; then p="$p
"; fi ;; esac
exec $i -c "$p$b" "$@"`

func (r *Runner) execContainer(ctx context.Context, st Step, env map[string]string, opts runtime.ExecStreamOptions) error {
	container := r.containerName(st.Job.Service)
	var argv []string
	switch {
	case st.Script != nil:
		argv = append(append([]string{}, st.Script...), st.Args...)
	default:
		sb := parseShebang(st.Job.Run)
		switch {
		case sb == nil:
			argv = append([]string{"sh", "-c", containerLauncher, "auto", prelude, st.Job.Run, st.Job.ID()}, st.Args...)
		case sb.shell:
			argv = append([]string{"sh", "-c", containerLauncher, strings.Join(sb.argv, " "), prelude, st.Job.Run, st.Job.ID()}, st.Args...)
		default:
			// Other interpreters need a file. Write it in a first exec (body
			// on stdin), run it in a second one so the job keeps the real
			// stdin, and remove it afterwards.
			var path bytes.Buffer
			var errOut bytes.Buffer
			write := runtime.ExecStreamOptions{
				User:   opts.User,
				Stdin:  strings.NewReader(st.Job.Run),
				Stdout: &path,
				Stderr: &errOut,
			}
			write.Interactive = true
			err := r.Runtime.ExecStream(ctx, container, []string{"sh", "-c",
				`umask 077; f=$(mktemp "${TMPDIR:-/tmp}/zdev-run.XXXXXX") && cat > "$f" && chmod 700 "$f" && printf %s "$f"`}, write)
			if err != nil {
				return fmt.Errorf("%s: writing the script into %s: %w %s", st.Job.ID(), st.Job.Service, err, strings.TrimSpace(errOut.String()))
			}
			argv = append([]string{"sh", "-c", `f=$0; "$f" "$@"; rc=$?; rm -f "$f"; exit $rc`, strings.TrimSpace(path.String())}, st.Args...)
		}
	}
	return r.Runtime.ExecStream(ctx, container, argv, opts)
}

type shebang struct {
	argv  []string
	shell bool // bash/sh family: run with -c and the print-helper prelude
}

var shells = map[string]bool{"bash": true, "sh": true, "dash": true, "ash": true, "zsh": true}

// parseShebang returns nil for a body without a shebang line.
func parseShebang(body string) *shebang {
	first, _, _ := strings.Cut(body, "\n")
	if !strings.HasPrefix(first, "#!") {
		return nil
	}
	argv := strings.Fields(strings.TrimPrefix(first, "#!"))
	if len(argv) == 0 {
		return nil
	}
	name := filepath.Base(argv[0])
	if name == "env" && len(argv) == 2 {
		name = argv[1]
	} else if name == "env" {
		name = "" // env with flags (-S ...): leave it to the kernel
	}
	return &shebang{argv: argv, shell: shells[name]}
}

// prelude makes a shell body stop at the first failing line (like just) and
// defines the print helpers. It is one line and the body continues on the
// same line, so line numbers in shell error messages match the job file
// (containers are assumed to have bash 4+; for the host see preludeSep).
// A shebang on the body's first line becomes a comment.
const prelude = `set -e; (set -o pipefail) 2>/dev/null && set -o pipefail; ` +
	`__zdev_c() { [ -t "$1" ] && [ -z "${ZDEV_PLAIN:-}" ] && [ -z "${NO_COLOR:-}" ]; }; ` +
	`step() { if [ -n "${ZDEV_QUIET:-}" ]; then printf '▶ %s\n' "$*"; elif __zdev_c 1; then printf '\n\n\033[1;36m▶ %s\033[0m\n\n' "$*"; else printf '\n\n▶ %s\n\n' "$*"; fi; }; ` +
	`warn() { if __zdev_c 2; then printf '\033[33m⚠ %s\033[0m\n' "$*" >&2; else printf '⚠ %s\n' "$*" >&2; fi; }; ` +
	`fail() { if __zdev_c 2; then printf '\033[31m✗ %s\033[0m\n' "$*" >&2; else printf '✗ %s\n' "$*" >&2; fi; exit 1; }; `

// exitCode maps a job error to a process exit code: the child's own code,
// 128+signal when it was killed, 1 for anything else.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	return 1
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
