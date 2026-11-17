package run

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// readDotenv parses a dotenv file: KEY=VALUE lines, optional `export `
// prefix, # comments and blank lines, values optionally wrapped in single or
// double quotes. No variable expansion and no multi-line values: env_file is
// a convenience for the common .env, not a dotenv implementation.
func readDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	env := map[string]string{}
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyRe.MatchString(key) {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i]) // inline comment on an unquoted value
		}
		env[key] = value
	}
	return env, sc.Err()
}
