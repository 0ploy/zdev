package cmd

import (
	"context"
	"errors"
	"testing"
)

func TestIsZdevNetwork(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"scale-shop.zdev", true},
		{"zdev_link_shop-billing", true},
		{"bridge", false},
		{"host", false},
		{"none", false},
		{"some-compose-project_default", false},
		{"zdev", false},
	}

	for _, tt := range tests {
		if got := isZdevNetwork(tt.name); got != tt.want {
			t.Errorf("isZdevNetwork(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// fakeLister answers ListContainers from a fixed network -> containers map.
type fakeLister struct {
	byNetwork map[string][]string
	err       error
}

func (f fakeLister) ListContainers(_ context.Context, filter string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	const prefix = "network="
	if len(filter) > len(prefix) {
		return f.byNetwork[filter[len(prefix):]], nil
	}
	return nil, nil
}

func TestSelectOrphanNetworks(t *testing.T) {
	networks := []string{
		"bridge",               // not ours
		"live.zdev",            // owned by a registered project
		"zdev_link_pair",       // owned by a link in state
		"deleted-project.zdev", // nothing left that owns it
		"stopped-project.zdev", // still has a stopped container pinned to it
		"emptied-project.zdev", // only attached to a container being removed
	}
	known := map[string]bool{"live.zdev": true, "zdev_link_pair": true}
	removing := map[string]bool{"app.emptied-project.zdev": true}

	docker := fakeLister{byNetwork: map[string][]string{
		"stopped-project.zdev": {"app.stopped-project.zdev"},
		"emptied-project.zdev": {"app.emptied-project.zdev"},
	}}

	got, err := selectOrphanNetworks(context.Background(), docker, networks, known, removing)
	if err != nil {
		t.Fatalf("selectOrphanNetworks returned %v", err)
	}

	want := map[string]bool{"deleted-project.zdev": true, "emptied-project.zdev": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("%s was selected for removal but should have been kept", name)
		}
	}
}

// TestSelectOrphanNetworks_KeepsNetworkOfStoppedContainer is the safety case
// worth stating on its own: a stopped container pins its network by ID, so
// removing the network leaves it unstartable until it is recreated.
func TestSelectOrphanNetworks_KeepsNetworkOfStoppedContainer(t *testing.T) {
	docker := fakeLister{byNetwork: map[string][]string{
		"sw-test10.zdev": {"app.sw-test10.zdev", "db.sw-test10.zdev"},
	}}

	got, err := selectOrphanNetworks(context.Background(), docker,
		[]string{"sw-test10.zdev"}, map[string]bool{}, map[string]bool{})
	if err != nil {
		t.Fatalf("selectOrphanNetworks returned %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want the network kept while containers still reference it", got)
	}
}

func TestSelectOrphanNetworks_SurfacesListErrors(t *testing.T) {
	docker := fakeLister{err: errors.New("docker daemon unreachable")}

	if _, err := selectOrphanNetworks(context.Background(), docker,
		[]string{"orphan.zdev"}, map[string]bool{}, map[string]bool{}); err == nil {
		t.Fatal("expected the lookup failure to surface instead of pruning blind")
	}
}

func TestBelongsToAnyProject(t *testing.T) {
	unreadable := map[string]bool{"zeroploy": true}

	tests := []struct {
		resource string
		want     bool
	}{
		{"app.zeroploy.zdev", true},
		{"mysql.zeroploy.zdev", true},
		{"sync.app.zeroploy.zdev", true},
		{"app.other.zdev", false},
		{"zeroploy.zdev", false}, // the network, handled separately
		{"app.zeroploy-staging.zdev", false},
	}

	for _, tt := range tests {
		if got := belongsToAnyProject(tt.resource, unreadable); got != tt.want {
			t.Errorf("belongsToAnyProject(%q) = %v, want %v", tt.resource, got, tt.want)
		}
	}
}
