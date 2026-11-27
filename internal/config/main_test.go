package config

import (
	"os"
	"testing"
)

// TestMain drops the variables a zdev claude sandbox sets: with them, project
// names resolve to the host's directory (buildVariables) and the tests, which
// load projects from temp dirs, would fail when run in a sandbox.
func TestMain(m *testing.M) {
	os.Unsetenv("ZDEV_SANDBOX")
	os.Unsetenv("ZDEV_HOST_PROJECTDIR")
	os.Exit(m.Run())
}
