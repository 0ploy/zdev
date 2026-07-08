package ui

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// PlainMode returns true if terminal features should be disabled.
// Checks ZDEV_PLAIN env var first, then falls back to config.
func PlainMode(configPlain bool) bool {
	if env := os.Getenv("ZDEV_PLAIN"); env != "" {
		return env == "1" || strings.ToLower(env) == "true"
	}
	return configPlain
}

// IsTerminal returns true if stdout is a terminal.
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// SupportsHyperlinks returns true if the terminal likely supports OSC 8 hyperlinks.
// Detection is based on known terminals and the COLORTERM heuristic.
func SupportsHyperlinks() bool {
	if !IsTerminal() {
		return false
	}

	// Check for known terminals that support OSC 8
	termProgram := os.Getenv("TERM_PROGRAM")
	switch termProgram {
	case "iTerm.app", "WezTerm", "Hyper", "Tabby", "vscode":
		return true
	case "Apple_Terminal":
		// Apple Terminal does NOT support OSC 8
		return false
	}

	// Check for other known terminals via their specific env vars
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if os.Getenv("ALACRITTY_WINDOW_ID") != "" {
		return true
	}
	if os.Getenv("WT_SESSION") != "" {
		// Windows Terminal
		return true
	}
	if os.Getenv("KONSOLE_VERSION") != "" {
		return true
	}
	if os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return true
	}

	// VTE-based terminals (GNOME Terminal, etc.) version 0.50+
	if vteVersion := os.Getenv("VTE_VERSION"); vteVersion != "" {
		if v, err := strconv.Atoi(vteVersion); err == nil && v >= 5000 {
			return true
		}
	}

	// Heuristic: terminals that support truecolor are usually modern enough for hyperlinks
	colorterm := os.Getenv("COLORTERM")
	if colorterm == "truecolor" || colorterm == "24bit" {
		return true
	}

	return false
}

// SupportsColors returns true if the terminal supports colors.
func SupportsColors() bool {
	if !IsTerminal() {
		return false
	}

	// Check TERM for basic color support
	termEnv := os.Getenv("TERM")
	if termEnv == "dumb" || termEnv == "" {
		return false
	}

	return true
}

// Hyperlink returns a clickable hyperlink if supported, otherwise just the URL.
// If text is empty, the URL is used as the display text.
func Hyperlink(url, text string, plainMode bool) string {
	if text == "" {
		text = url
	}

	if plainMode || !SupportsHyperlinks() {
		return text
	}

	// OSC 8 hyperlink format: \e]8;;URL\e\\TEXT\e]8;;\e\\
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}

// Color applies ANSI color codes if supported.
func Color(text, color string, plainMode bool) string {
	if plainMode || !SupportsColors() {
		return text
	}

	var code string
	switch color {
	case "green":
		code = "\x1b[32m"
	case "red":
		code = "\x1b[31m"
	case "yellow":
		code = "\x1b[33m"
	case "blue":
		code = "\x1b[34m"
	case "cyan":
		code = "\x1b[36m"
	case "dim":
		code = "\x1b[2m" // dim/faint text
	default:
		return text
	}

	return code + text + "\x1b[0m"
}

// StatusStep prints a visually distinct framework status message to stdout.
// Used during multi-step flows (setup, start) to make zdev's own progress
// markers stand out against verbose nested command output. Leads with two
// blank lines, the full line in bold cyan, and a trailing blank line so
// the header sits apart from the command output that follows.
func StatusStep(message string, plainMode bool) {
	fmt.Println()
	fmt.Println()
	if plainMode || !SupportsColors() {
		fmt.Printf("▶ %s\n", message)
	} else {
		// Bold + cyan in one escape so the whole line reads as a single visual unit.
		fmt.Printf("\x1b[1;36m▶ %s\x1b[0m\n", message)
	}
	fmt.Println()
}

// StatusColor returns colored status text based on the status value.
func StatusColor(status string, plainMode bool) string {
	if plainMode || !SupportsColors() {
		return status
	}

	switch status {
	case "running":
		return Color(status, "green", false)
	case "stopped", "not created":
		return Color(status, "red", false)
	case "unknown", "not implemented":
		return Color(status, "yellow", false)
	default:
		return status
	}
}

// HyperlinkKeyHint returns a hint about which key to hold to click links.
// Returns empty string in plain mode or when hyperlinks aren't supported.
// The hint is dimmed to be visible but not distracting.
func HyperlinkKeyHint(plainMode bool) string {
	if plainMode || !SupportsHyperlinks() {
		return ""
	}

	var hint string
	if runtime.GOOS == "darwin" {
		hint = "(Cmd+click to open)"
	} else {
		hint = "(Ctrl+click to open)"
	}
	return Color(hint, "dim", false)
}
