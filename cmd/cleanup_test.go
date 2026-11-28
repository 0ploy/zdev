package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
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

// The volumes of a project whose directory is missing (an unmounted drive)
// are its databases: kept, per project, and never offered as orphans
// (review 2026-11-28, M4).
func TestSplitStaleVolumes(t *testing.T) {
	remove, kept := splitStaleVolumes(
		[]string{"db_data.shop.zdev", "sync.app.shop.zdev", "old.blog.zdev", "cache.other.zdev"},
		[]string{"shop", "gone"})
	if strings.Join(remove, ",") != "old.blog.zdev,cache.other.zdev" {
		t.Errorf("remove = %v", remove)
	}
	if strings.Join(kept["shop"], ",") != "db_data.shop.zdev,sync.app.shop.zdev" || len(kept["gone"]) != 0 {
		t.Errorf("kept = %v", kept)
	}
}

// cleanupDocker answers planCleanup's listings and records apply's removals.
type cleanupDocker struct {
	containers []string            // all, as `docker ps -a --filter label=zdev.project`
	byNetwork  map[string][]string // network -> containers attached
	volumes    []string
	networks   []string
	removed    []string
	failVolume string
}

func (d *cleanupDocker) ListContainers(_ context.Context, filter string) ([]string, error) {
	if n, ok := strings.CutPrefix(filter, "network="); ok {
		return d.byNetwork[n], nil
	}
	return d.containers, nil
}

func (d *cleanupDocker) ListVolumes(context.Context, string) ([]runtime.Volume, error) {
	var out []runtime.Volume
	for _, v := range d.volumes {
		out = append(out, runtime.Volume{Name: v})
	}
	return out, nil
}

func (d *cleanupDocker) ListNetworks(context.Context, string) ([]string, error) {
	return d.networks, nil
}

func (d *cleanupDocker) ForceRemoveContainer(_ context.Context, name string) error {
	d.removed = append(d.removed, "container "+name)
	return nil
}

func (d *cleanupDocker) RemoveNetwork(_ context.Context, name string) error {
	d.removed = append(d.removed, "network "+name)
	return nil
}

func (d *cleanupDocker) RemoveVolume(_ context.Context, name string) error {
	if name == d.failVolume {
		return errors.New("volume is in use")
	}
	d.removed = append(d.removed, "volume "+name)
	return nil
}

type cleanupState struct{ unregistered, unlinked []string }

func (s *cleanupState) UnregisterProject(name string) error {
	s.unregistered = append(s.unregistered, name)
	return nil
}

func (s *cleanupState) RemoveProjectFromLinks(name string) error {
	s.unlinked = append(s.unlinked, name)
	return nil
}

// cleanupWorld is a registry with one live project (shop), one whose config
// doesn't parse (broken), one on an unmounted drive that has a database
// (ext) and one deleted for good (gone, nothing left but its network), plus
// what Docker holds for them and for a project no longer registered at all.
func cleanupWorld(t *testing.T) (map[string]state.ProjectEntry, map[string]state.LinkEntry, *cleanupDocker) {
	t.Helper()
	t.Setenv("ZDEV_HOME", t.TempDir())
	write := func(dir, cfg string) {
		if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shop, broken := t.TempDir(), t.TempDir()
	write(shop, "name: shop\nservices:\n  app:\n    image: alpine\n    volumes:\n      - db_data:/var/lib/db\n")
	write(broken, "name: broken\nnot_a_field: true\n")
	projects := map[string]state.ProjectEntry{
		"shop":   {Path: shop},
		"broken": {Path: broken},
		"ext":    {Path: filepath.Join(t.TempDir(), "unmounted", "ext")},
		"gone":   {Path: filepath.Join(t.TempDir(), "deleted", "gone")},
	}
	links := map[string]state.LinkEntry{"pair": {Network: "zdev_link_pair"}}
	d := &cleanupDocker{
		containers: []string{"app.shop.zdev", "old.shop.zdev", "app.broken.zdev", "app.ext.zdev"},
		byNetwork: map[string][]string{
			"broken.zdev": {"app.broken.zdev"},
			"ext.zdev":    {"app.ext.zdev"},
		},
		volumes:  []string{"db_data.shop.zdev", "cache.shop.zdev", "db.broken.zdev", "db.ext.zdev", "db.unregistered.zdev"},
		networks: []string{"bridge", "shop.zdev", "broken.zdev", "ext.zdev", "gone.zdev", "zdev_link_pair"},
	}
	return projects, links, d
}

func projectNames(ps []cleanupProject) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.name)
	}
	return strings.Join(out, ",")
}

// What a cleanup removes and keeps, end to end: the live config's resources
// stay, an unreadable project keeps everything with its name, a missing
// directory keeps its volumes and registration unless --purge-missing, and
// the rest goes.
func TestPlanCleanup(t *testing.T) {
	projects, links, d := cleanupWorld(t)
	plan, err := planCleanup(context.Background(), projects, links, d, false)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"unreadable":  {projectNames(plan.unreadable), "broken"},
		"keptMissing": {projectNames(plan.keptMissing), "ext"},
		"keptVolumes": {strings.Join(plan.keptVolumes["ext"], ","), "db.ext.zdev"},
		"unregister":  {projectNames(plan.unregister), "gone"},
		"containers":  {strings.Join(plan.containers, ","), "app.ext.zdev,old.shop.zdev"},
		"volumes":     {strings.Join(plan.volumes, ","), "cache.shop.zdev,db.unregistered.zdev"},
		// ext's network goes with its only container; broken's stays.
		"networks": {strings.Join(plan.networks, ","), "ext.zdev,gone.zdev"},
	}
	for what, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", what, c[0], c[1])
		}
	}

	plan, err = planCleanup(context.Background(), projects, links, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if projectNames(plan.keptMissing) != "" || projectNames(plan.unregister) != "ext,gone" ||
		strings.Join(plan.volumes, ",") != "cache.shop.zdev,db.ext.zdev,db.unregistered.zdev" {
		t.Errorf("--purge-missing: kept %q, unregister %q, volumes %v", projectNames(plan.keptMissing), projectNames(plan.unregister), plan.volumes)
	}
}

// print says what is kept and why before what goes, and reports whether
// there is anything to do.
func TestCleanupPlanPrint(t *testing.T) {
	projects, links, d := cleanupWorld(t)
	plan, _ := planCleanup(context.Background(), projects, links, d, false)
	var b strings.Builder
	if !plan.print(&b) {
		t.Fatal("print reported nothing to do")
	}
	out := b.String()
	order := []string{
		"Warning: not inspecting broken",
		"Projects whose directory is missing (1)", "  - ext (", "): db.ext.zdev", "--purge-missing",
		"Stale project registrations (1)", "  - gone (",
		"Orphaned containers (2)", "Orphaned volumes (2)", "Orphaned networks (2)",
	}
	at := 0
	for _, s := range order {
		i := strings.Index(out[at:], s)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", s, out)
		}
		at += i + len(s)
	}

	b.Reset()
	if (cleanupPlan{keptMissing: plan.keptMissing, keptVolumes: plan.keptVolumes}).print(&b) || !strings.Contains(b.String(), "Nothing to clean up.") {
		t.Errorf("a plan that only keeps things:\n%s", b.String())
	}
}

// apply removes containers before networks (a network goes once nothing
// uses it) and keeps going when one step fails.
func TestCleanupPlanApply(t *testing.T) {
	projects, links, d := cleanupWorld(t)
	plan, _ := planCleanup(context.Background(), projects, links, d, false)
	d.failVolume = "cache.shop.zdev"
	reg := &cleanupState{}
	var b strings.Builder
	plan.apply(context.Background(), &b, reg, d)
	if strings.Join(reg.unregistered, ",") != "gone" || strings.Join(reg.unlinked, ",") != "gone" {
		t.Errorf("unregistered %v, unlinked %v", reg.unregistered, reg.unlinked)
	}
	want := "container app.ext.zdev,container old.shop.zdev,network ext.zdev,network gone.zdev,volume db.unregistered.zdev"
	if got := strings.Join(d.removed, ","); got != want {
		t.Errorf("removed %s, want %s", got, want)
	}
	if !strings.Contains(b.String(), "Removed 1 volume(s), 1 failed") {
		t.Errorf("summary missing:\n%s", b.String())
	}
}
