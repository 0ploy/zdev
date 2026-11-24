package state

import (
	"path/filepath"
	"testing"
)

func TestHostsRecorded(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.yaml"))

	// Started by this zdev, routes nothing: recorded, never re-read.
	_ = mgr.RegisterProjectWithRouting("plain", "/plain", nil, nil, nil)
	// Registered without hosts (rename, older code paths): pending.
	_ = mgr.RegisterProject("legacy", "/legacy")

	pending, err := mgr.ProjectsWithoutHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending["legacy"] != "/legacy" {
		t.Fatalf("pending = %v, want only legacy", pending)
	}

	if err := mgr.SetProjectHosts("legacy", []string{"legacy.0ploy.dev"}); err != nil {
		t.Fatal(err)
	}
	entry, _ := mgr.GetProject("legacy")
	if !entry.HostsRecorded || len(entry.Hosts) != 1 || entry.Path != "/legacy" {
		t.Errorf("legacy = %+v, want recorded host and path kept", entry)
	}

	// Unregistered in the meantime: not recreated.
	if err := mgr.SetProjectHosts("deleted", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := mgr.GetProject("deleted"); e != nil {
		t.Errorf("SetProjectHosts created an entry: %+v", e)
	}
}
