package mutagen

import (
	"strings"
	"testing"
)

func TestParseConflicts(t *testing.T) {
	data := `[{"name":"zdev-shop-app","conflicts":[
	  {"root":".git/index","alphaChanges":[{"path":".git/index","old":{},"new":{}}],"betaChanges":[{"path":".git/index"}]},
	  {"root":"src","alphaChanges":[{"path":"src/a.php"}],"betaChanges":[{"path":"src/b.php"}]}]}]`
	got, err := parseConflicts([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != ".git/index,src/a.php,src/b.php" {
		t.Errorf("parseConflicts = %v", got)
	}
	if got, _ := parseConflicts([]byte(`[{"name":"x"}]`)); len(got) != 0 {
		t.Errorf("no conflicts: %v", got)
	}
}
