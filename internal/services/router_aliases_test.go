package services

import (
	"context"
	"reflect"
	"testing"

	"github.com/0ploy/zdev/internal/state"
)

// registerHosts writes projects with routed hosts into a temp state file.
func registerHosts(t *testing.T, projects map[string][]string) {
	t.Helper()
	t.Setenv("ZDEV_HOME", t.TempDir())
	stateMgr, err := state.DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	for name, hosts := range projects {
		if err := stateMgr.RegisterProjectWithRouting(name, "/tmp/"+name, nil, nil, hosts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRouterAliases_SharedUIsAndProjectHosts(t *testing.T) {
	registerHosts(t, map[string][]string{
		"alpha": {"alpha.0ploy.dev"},
		"beta":  {"beta.0ploy.dev", "alpha.0ploy.dev"},
	})
	mgr, _ := mockManager(t)

	got, err := mgr.routerAliases()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"router": true, "alpha.0ploy.dev": true, "beta.0ploy.dev": true, "router.shared.0ploy.dev": true, "mail.shared.0ploy.dev": true}
	set := map[string]bool{}
	for _, a := range got {
		if set[a] {
			t.Errorf("duplicate alias %q in %v", a, got)
		}
		set[a] = true
	}
	for a := range want {
		if !set[a] {
			t.Errorf("missing alias %q in %v", a, got)
		}
	}
	if set["."+mgr.cfg.Domain] {
		t.Errorf("service without web UI (DNS) produced an alias: %v", got)
	}
}

// Project B starts while A runs: B's network gets the full set, and A's
// network is reconnected so A's containers resolve B's hostname too.
func TestConnectRouter_UpdatesOtherProjectNetworks(t *testing.T) {
	registerHosts(t, map[string][]string{
		"alpha": {"alpha.0ploy.dev"},
		"beta":  {"beta.0ploy.dev"},
	})
	mgr, mock := mockManager(t)
	attach(mock, RouterContainerName, "hash", "alpha.zdev")
	mock.ContainerNetworks[RouterContainerName]["alpha.zdev"] = []string{"router", "alpha.0ploy.dev"}
	mock.NetworksExist["beta.zdev"] = true

	if err := mgr.ConnectRouter(context.Background(), "beta.zdev"); err != nil {
		t.Fatalf("ConnectRouter: %v", err)
	}

	desired, _ := mgr.routerAliases()
	for _, network := range []string{"alpha.zdev", "beta.zdev"} {
		if got := mock.ContainerNetworks[RouterContainerName][network]; !reflect.DeepEqual(got, desired) {
			t.Errorf("%s aliases = %v, want %v", network, got, desired)
		}
	}
	if _, ok := mock.ContainerNetworks[RouterContainerName][SharedNetworkName]; !ok {
		t.Error("router lost zdev_shared")
	}
	if n := mock.CallCount("NetworkDisconnect"); n != 1 {
		t.Errorf("NetworkDisconnect called %d times, want 1 (alpha only)", n)
	}
}

// A restart of a known project must not reconnect anything: that would drop
// in-flight requests for no reason.
func TestConnectRouter_UpToDateTouchesNothing(t *testing.T) {
	registerHosts(t, map[string][]string{"alpha": {"alpha.0ploy.dev"}})
	mgr, mock := mockManager(t)
	desired, err := mgr.routerAliases()
	if err != nil {
		t.Fatal(err)
	}
	attach(mock, RouterContainerName, "hash", "alpha.zdev")
	// Same set, different order.
	reversed := make([]string, len(desired))
	for i, a := range desired {
		reversed[len(desired)-1-i] = a
	}
	mock.ContainerNetworks[RouterContainerName]["alpha.zdev"] = reversed

	if err := mgr.ConnectRouter(context.Background(), "alpha.zdev"); err != nil {
		t.Fatalf("ConnectRouter: %v", err)
	}
	if n := mock.CallCount("NetworkDisconnect") + mock.CallCount("NetworkConnect"); n != 0 {
		t.Errorf("%d connect/disconnect calls, want 0", n)
	}
}

// A router recreate reattaches with aliases derived from state, not the
// stale snapshot.
func TestRestoreProjectNetworks_RouterGetsFreshAliases(t *testing.T) {
	registerHosts(t, map[string][]string{"alpha": {"alpha.0ploy.dev"}, "beta": {"beta.0ploy.dev"}})
	mgr, mock := mockManager(t)
	mock.NetworksExist["alpha.zdev"] = true

	mgr.restoreProjectNetworks(context.Background(), RouterContainerName, map[string][]string{"alpha.zdev": {"router"}})

	desired, _ := mgr.routerAliases()
	if got := mock.ContainerNetworks[RouterContainerName]["alpha.zdev"]; !reflect.DeepEqual(got, desired) {
		t.Errorf("restored aliases = %v, want %v", got, desired)
	}
}

func TestSameStringSet(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"a"}, false},
		{[]string{"a", "a"}, []string{"a"}, true},
	}
	for _, c := range cases {
		if got := sameStringSet(c.a, c.b); got != c.want {
			t.Errorf("sameStringSet(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
