package state

import (
	"path/filepath"
	"testing"
)

// TestGetAllRoutingPorts_Sorted is the regression test for the router
// detaching from every project on an unrelated project's start.
//
// The ports become order-sensitive `--entrypoints.tcp-<n>` Command entries on
// the router, and runtime.ComputeConfigHash hashes Command in order. This
// function used to return Go map iteration order, so the hash of the created
// container never matched the hash StartRouter's compare path computed from
// the sorted union - drift on every call, a recreate on every call, and every
// other project's network dropped with it.
func TestGetAllRoutingPorts_Sorted(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.yaml"))

	if err := mgr.RegisterProjectWithRouting("beta", "/tmp/beta", []int{8123, 9428, 3100, 7280}, []int{9000}); err != nil {
		t.Fatalf("register beta: %v", err)
	}
	if err := mgr.RegisterProjectWithRouting("alpha", "/tmp/alpha", []int{33306}, []int{514}); err != nil {
		t.Fatalf("register alpha: %v", err)
	}

	wantTCP := []int{3100, 7280, 8123, 9428, 33306}
	wantUDP := []int{514, 9000}

	// Repeat: map iteration order is randomized per range, so a single pass
	// could pass by luck.
	for i := 0; i < 20; i++ {
		tcp, udp, err := mgr.GetAllRoutingPorts()
		if err != nil {
			t.Fatalf("GetAllRoutingPorts: %v", err)
		}
		assertIntsEqual(t, "tcp", tcp, wantTCP)
		assertIntsEqual(t, "udp", udp, wantUDP)
	}
}

func assertIntsEqual(t *testing.T, label string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s ports: got %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s ports not sorted: got %v, want %v", label, got, want)
		}
	}
}
