package run

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	colorRed    = "31"
	colorGreen  = "32"
	colorYellow = "33"
	colorStep   = "1;36" // bold cyan, like ui.StatusStep
)

// Marker line prefixes: the print helpers' plain output and the protocol any
// language can print. The line format IS the protocol.
const (
	markerStep = '>'
	markerWarn = '!'
	markerFail = 'x'
)

var markerPrefixes = []struct {
	prefix string
	kind   byte
}{
	{"▶ ", markerStep},
	{"⚠ ", markerWarn},
	{"✗ ", markerFail},
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// parseMarker reports whether a line is a marker line, ignoring color codes.
func parseMarker(line string) (byte, string, bool) {
	plain := strings.TrimSpace(ansiRe.ReplaceAllString(line, ""))
	for _, m := range markerPrefixes {
		if rest, ok := strings.CutPrefix(plain, m.prefix); ok {
			return m.kind, strings.TrimSpace(rest), true
		}
	}
	return 0, "", false
}

// markerWriter captures everything into buf and calls emit for each complete
// marker line as it arrives. exec.Cmd serializes writes when stdout and
// stderr share one comparable writer, so no locking is needed.
type markerWriter struct {
	buf     *bytes.Buffer
	pending []byte
	emit    func(kind byte, msg string)
}

func (w *markerWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.scan(string(w.pending[:i]))
		w.pending = w.pending[i+1:]
	}
	return len(p), nil
}

// flush handles a last line without a trailing newline.
func (w *markerWriter) flush() {
	if len(w.pending) > 0 {
		w.scan(string(w.pending))
		w.pending = nil
	}
}

func (w *markerWriter) scan(line string) {
	if kind, msg, ok := parseMarker(line); ok {
		w.emit(kind, msg)
	}
}

func (r *Runner) printMarker(kind byte, msg string) {
	switch kind {
	case markerStep:
		r.printStep(r.Stdout, msg)
	case markerWarn:
		r.printLine(r.Stderr, colorYellow, "⚠ "+msg)
	case markerFail:
		r.printLine(r.Stderr, colorRed, "✗ "+msg)
	}
}

// printStep matches ui.StatusStep: two blank lines, the marker, one blank line.
func (r *Runner) printStep(w io.Writer, msg string) {
	fmt.Fprint(w, "\n\n")
	r.printLine(w, colorStep, "▶ "+msg)
	fmt.Fprintln(w)
}

func (r *Runner) printLine(w io.Writer, color, text string) {
	if r.Color && !r.Plain {
		fmt.Fprintf(w, "\x1b[%sm%s\x1b[0m\n", color, text)
		return
	}
	fmt.Fprintln(w, text)
}
