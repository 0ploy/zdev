package run

import (
	"fmt"
	"strconv"
	"strings"
)

// substitute replaces {N} and {all} in a string field (env values, workdir,
// confirm). {all} joins the arguments with spaces; a missing argument is
// empty.
func substitute(s string, args []string) string {
	return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		key := m[1 : len(m)-1]
		if key == "all" {
			return strings.Join(args, " ")
		}
		n, _ := strconv.Atoi(key) // validated at load time
		if n >= 1 && n <= len(args) {
			return args[n-1]
		}
		return ""
	})
}

// substituteWords splits a word-list field (needs, script) on whitespace and
// substitutes each word. A word that is exactly {all} expands to one word
// per argument, so arguments with spaces survive intact.
func substituteWords(s string, args []string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		if w == "{all}" {
			out = append(out, args...)
			continue
		}
		out = append(out, substitute(w, args))
	}
	return out
}

// missingArg returns an error naming the first placeholder in s that points
// past the given arguments, for fields where an empty value would do harm.
func missingArg(field, s string, args []string) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if m[1] == "all" {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n > len(args) {
			return fmt.Errorf("%s uses %s but only %d argument(s) were given", field, m[0], len(args))
		}
	}
	return nil
}
