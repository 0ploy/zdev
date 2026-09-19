package services

import (
	"context"
	"sort"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// mockManager returns a Manager backed by a MockRuntime, plus the mock.
func mockManager(t *testing.T) (*Manager, *runtime.MockRuntime) {
	t.Helper()
	mock := runtime.NewMockRuntime()
	mgr := &Manager{
		cfg: &config.GlobalConfig{
			Domain: "0ploy.dev",
			Shared: config.SharedConfig{
				Mail:   config.MailConfig{Image: "axllent/mailpit:latest"},
				Router: config.RouterConfig{Image: config.RouterImage},
			},
		},
		runtime: mock,
	}
	return mgr, mock
}

// attach wires up a pre-existing shared container in the mock: on the shared
// network plus the given project networks, each with the "mail" alias.
func attach(mock *runtime.MockRuntime, container string, staleHash string, projectNetworks ...string) {
	mock.ContainersExist[container] = true
	mock.ContainersRunning[container] = true
	mock.ImagesExist["axllent/mailpit:latest"] = true
	mock.NetworksExist[SharedNetworkName] = true
	mock.ContainerLabels[container] = map[string]string{runtime.ConfigHashLabel: staleHash}
	mock.ContainerNetworks[container] = map[string][]string{SharedNetworkName: nil}
	for _, network := range projectNetworks {
		mock.NetworksExist[network] = true
		mock.ContainerNetworks[container][network] = []string{"mail"}
	}
}

// TestStartService_PreservesProjectNetworksAcrossRecreate is the regression
// test for "router detached again - another project's restart takes it with
// them".
//
// Shared services are created on zdev_shared and joined to each project's
// network afterwards. Those endpoints exist only in the Docker daemon, so a
// config-drift recreate used to silently drop every project except the one
// whose lifecycle triggered the recreate.
func TestStartService_PreservesProjectNetworksAcrossRecreate(t *testing.T) {
	mgr, mock := mockManager(t)
	attach(mock, MailContainerName, "stale-hash", "alpha.zdev", "beta.zdev")

	if err := mgr.StartMail(context.Background()); err != nil {
		t.Fatalf("StartMail: %v", err)
	}

	if mock.CallCount("RemoveContainer") != 1 {
		t.Fatalf("expected a recreate, got %d RemoveContainer calls", mock.CallCount("RemoveContainer"))
	}

	got := mock.ContainerNetworks[MailContainerName]
	for _, network := range []string{"alpha.zdev", "beta.zdev"} {
		aliases, ok := got[network]
		if !ok {
			t.Errorf("container lost network %s across recreate (attached: %v)", network, sortedNetworkNames(got))
			continue
		}
		if len(aliases) != 1 || aliases[0] != "mail" {
			t.Errorf("network %s reattached without its aliases: got %v, want [mail]", network, aliases)
		}
	}
}

// TestStartService_SkipsNetworksThatNoLongerExist covers a project torn down
// while the shared service was stopped: its network is gone and must not be
// resurrected, but the surviving project must still be reattached.
func TestStartService_SkipsNetworksThatNoLongerExist(t *testing.T) {
	mgr, mock := mockManager(t)
	attach(mock, MailContainerName, "stale-hash", "alpha.zdev", "gone.zdev")
	mock.NetworksExist["gone.zdev"] = false

	if err := mgr.StartMail(context.Background()); err != nil {
		t.Fatalf("StartMail: %v", err)
	}

	got := mock.ContainerNetworks[MailContainerName]
	if _, ok := got["gone.zdev"]; ok {
		t.Error("reattached to a network that no longer exists")
	}
	if _, ok := got["alpha.zdev"]; !ok {
		t.Errorf("surviving project network not reattached (attached: %v)", sortedNetworkNames(got))
	}
}

// TestStartService_NoDriftLeavesNetworksAlone: when the hash matches there is
// no recreate, so nothing should touch the network topology.
func TestStartService_NoDriftLeavesNetworksAlone(t *testing.T) {
	mgr, mock := mockManager(t)
	expected := MailContainerConfig(MailServiceConfig{Image: "axllent/mailpit:latest", Domain: "0ploy.dev"})
	attach(mock, MailContainerName, expected.Labels[runtime.ConfigHashLabel], "alpha.zdev")

	if err := mgr.StartMail(context.Background()); err != nil {
		t.Fatalf("StartMail: %v", err)
	}

	if mock.CallCount("RemoveContainer") != 0 {
		t.Error("recreated a container whose config hash matched")
	}
	if mock.CallCount("NetworkConnect") != 0 {
		t.Error("touched networks without a recreate")
	}
}

// TestSnapshotProjectNetworks_ExcludesSharedNetwork: zdev_shared comes back
// from the container config on create, so it must not be in the snapshot.
func TestSnapshotProjectNetworks_ExcludesSharedNetwork(t *testing.T) {
	mgr, mock := mockManager(t)
	attach(mock, MailContainerName, "stale-hash", "alpha.zdev")

	snapshot := mgr.snapshotProjectNetworks(context.Background(), MailContainerName)
	if _, ok := snapshot[SharedNetworkName]; ok {
		t.Errorf("snapshot must exclude %s, got %v", SharedNetworkName, sortedNetworkNames(snapshot))
	}
	if _, ok := snapshot["alpha.zdev"]; !ok {
		t.Errorf("snapshot missing project network, got %v", sortedNetworkNames(snapshot))
	}
}

// TestBuildRouterContainerConfig_PortOrderInsensitive guards the root cause:
// ports become order-sensitive --entrypoints Command entries and
// ComputeConfigHash hashes Command in order, so an unsorted port list from
// any caller would produce a hash that never matches the compare path's -
// permanent drift, and a router recreate on every single project start.
func TestBuildRouterContainerConfig_PortOrderInsensitive(t *testing.T) {
	t.Setenv("ZDEV_HOME", t.TempDir())
	mgr, _ := mockManager(t)

	sorted := mgr.buildRouterContainerConfig([]int{3100, 7280, 8123, 9428, 33306}, []int{514, 9000})
	shuffled := mgr.buildRouterContainerConfig([]int{9428, 33306, 3100, 8123, 7280}, []int{9000, 514})

	if sorted.Labels[runtime.ConfigHashLabel] != shuffled.Labels[runtime.ConfigHashLabel] {
		t.Errorf("router config hash depends on port ORDER, not the port SET:\n sorted:   %s\n shuffled: %s",
			sorted.Labels[runtime.ConfigHashLabel], shuffled.Labels[runtime.ConfigHashLabel])
	}
}

func sortedNetworkNames(networks map[string][]string) []string {
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
