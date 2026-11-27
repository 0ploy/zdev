package runtime

import (
	"strings"
	"testing"
)

func TestParseImageEntrypoint(t *testing.T) {
	e, c, id, err := parseImageEntrypoint("[\"docker-php-entrypoint\"]\n[\"php-fpm\"]\nsha256:abc\n")
	if err != nil || strings.Join(e, " ") != "docker-php-entrypoint" || strings.Join(c, " ") != "php-fpm" || id != "sha256:abc" {
		t.Errorf("got %q %q %q %v", e, c, id, err)
	}
	// Docker prints null for an unset ENTRYPOINT or CMD.
	e, c, _, err = parseImageEntrypoint("null\n[\"nginx\",\"-g\",\"daemon off;\"]\nsha256:def")
	if err != nil || e != nil || len(c) != 3 {
		t.Errorf("null entrypoint: %q %q %v", e, c, err)
	}
	if _, _, _, err := parseImageEntrypoint("garbage"); err == nil {
		t.Error("garbage accepted")
	}
}
