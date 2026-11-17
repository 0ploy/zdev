package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMember(t *testing.T) {
	tests := []struct {
		input   string
		project string
		service string
	}{
		{"myproject", "myproject", ""},
		{"myproject.app", "myproject", "app"},
		{"sec-scan-decoder.app", "sec-scan-decoder", "app"},
		{"redis-debug.worker", "redis-debug", "worker"},
		{"simple", "simple", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			m := ParseMember(tt.input)
			if m.Project != tt.project {
				t.Errorf("Project: got %q, want %q", m.Project, tt.project)
			}
			if m.Service != tt.service {
				t.Errorf("Service: got %q, want %q", m.Service, tt.service)
			}
		})
	}
}

func TestLinkMemberString(t *testing.T) {
	tests := []struct {
		member LinkMember
		want   string
	}{
		{LinkMember{Project: "myproject"}, "myproject"},
		{LinkMember{Project: "myproject", Service: "app"}, "myproject.app"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.member.String()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateLinkName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"backend-mesh", false},
		{"test_net", false},
		{"MyLink123", false},
		{"", true},
		{"has space", true},
		{"has.dot", true},
		{"has/slash", true},
		{"special!", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLinkName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLinkName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestLinkNetworkName(t *testing.T) {
	got := LinkNetworkName("test-net")
	want := "zdev_link_test-net"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "zdev-link-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	return NewManager(filepath.Join(tmpDir, "state.yaml"))
}

func TestCreateLink(t *testing.T) {
	mgr := newTestManager(t)

	// Create a link
	if err := mgr.CreateLink("test-net"); err != nil {
		t.Fatalf("CreateLink failed: %v", err)
	}

	// Verify it exists
	link, err := mgr.GetLink("test-net")
	if err != nil {
		t.Fatalf("GetLink failed: %v", err)
	}
	if link == nil {
		t.Fatal("expected link to exist")
	}
	if link.Network != "zdev_link_test-net" {
		t.Errorf("network: got %q, want %q", link.Network, "zdev_link_test-net")
	}
	if len(link.Members) != 0 {
		t.Errorf("expected no members, got %d", len(link.Members))
	}

	// Creating duplicate should fail
	if err := mgr.CreateLink("test-net"); err == nil {
		t.Fatal("expected error creating duplicate link")
	}
}

func TestDeleteLink(t *testing.T) {
	mgr := newTestManager(t)

	_ = mgr.CreateLink("test-net")

	if err := mgr.DeleteLink("test-net"); err != nil {
		t.Fatalf("DeleteLink failed: %v", err)
	}

	link, err := mgr.GetLink("test-net")
	if err != nil {
		t.Fatalf("GetLink failed: %v", err)
	}
	if link != nil {
		t.Fatal("expected link to be deleted")
	}

	// Deleting non-existent should fail
	if err := mgr.DeleteLink("nonexistent"); err == nil {
		t.Fatal("expected error deleting non-existent link")
	}
}

func TestAddLinkMembers(t *testing.T) {
	mgr := newTestManager(t)

	_ = mgr.CreateLink("test-net")

	// Add members
	members := []LinkMember{
		{Project: "sec-scan"},
		{Project: "redis-debug", Service: "app"},
	}
	if err := mgr.AddLinkMembers("test-net", members); err != nil {
		t.Fatalf("AddLinkMembers failed: %v", err)
	}

	link, _ := mgr.GetLink("test-net")
	if len(link.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(link.Members))
	}

	// Adding duplicate should not create duplicates
	if err := mgr.AddLinkMembers("test-net", []LinkMember{{Project: "sec-scan"}}); err != nil {
		t.Fatalf("AddLinkMembers failed: %v", err)
	}

	link, _ = mgr.GetLink("test-net")
	if len(link.Members) != 2 {
		t.Errorf("expected 2 members after adding duplicate, got %d", len(link.Members))
	}

	// Adding to non-existent link should fail
	if err := mgr.AddLinkMembers("nonexistent", members); err == nil {
		t.Fatal("expected error adding to non-existent link")
	}
}

func TestRemoveLinkMembers(t *testing.T) {
	mgr := newTestManager(t)

	_ = mgr.CreateLink("test-net")
	_ = mgr.AddLinkMembers("test-net", []LinkMember{
		{Project: "sec-scan"},
		{Project: "redis-debug", Service: "app"},
		{Project: "shopware"},
	})

	// Remove one member
	if err := mgr.RemoveLinkMembers("test-net", []LinkMember{{Project: "sec-scan"}}); err != nil {
		t.Fatalf("RemoveLinkMembers failed: %v", err)
	}

	link, _ := mgr.GetLink("test-net")
	if len(link.Members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(link.Members))
	}

	// Verify the right one was removed
	for _, m := range link.Members {
		if m.Project == "sec-scan" && m.Service == "" {
			t.Error("sec-scan should have been removed")
		}
	}

	// Removing from non-existent link should fail
	if err := mgr.RemoveLinkMembers("nonexistent", []LinkMember{{Project: "sec-scan"}}); err == nil {
		t.Fatal("expected error removing from non-existent link")
	}
}

func TestListLinks(t *testing.T) {
	mgr := newTestManager(t)

	// Empty initially
	links, err := mgr.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks failed: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}

	// Create two links
	_ = mgr.CreateLink("net-a")
	_ = mgr.CreateLink("net-b")

	links, err = mgr.ListLinks()
	if err != nil {
		t.Fatalf("ListLinks failed: %v", err)
	}
	if len(links) != 2 {
		t.Errorf("expected 2 links, got %d", len(links))
	}
}

func TestGetLinksForProject(t *testing.T) {
	mgr := newTestManager(t)

	_ = mgr.CreateLink("net-a")
	_ = mgr.CreateLink("net-b")
	_ = mgr.AddLinkMembers("net-a", []LinkMember{
		{Project: "sec-scan"},
		{Project: "decoder"},
	})
	_ = mgr.AddLinkMembers("net-b", []LinkMember{
		{Project: "shopware"},
		{Project: "decoder"},
	})

	// sec-scan is only in net-a
	links, err := mgr.GetLinksForProject("sec-scan")
	if err != nil {
		t.Fatalf("GetLinksForProject failed: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("expected 1 link for sec-scan, got %d", len(links))
	}
	if _, ok := links["net-a"]; !ok {
		t.Error("expected net-a for sec-scan")
	}

	// decoder is in both
	links, err = mgr.GetLinksForProject("decoder")
	if err != nil {
		t.Fatalf("GetLinksForProject failed: %v", err)
	}
	if len(links) != 2 {
		t.Errorf("expected 2 links for decoder, got %d", len(links))
	}

	// unknown project returns empty
	links, err = mgr.GetLinksForProject("unknown")
	if err != nil {
		t.Fatalf("GetLinksForProject failed: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links for unknown, got %d", len(links))
	}
}

func TestRenameProject(t *testing.T) {
	mgr := newTestManager(t)

	_ = mgr.RegisterProject("old-name", "/some/path")

	// Rename
	if err := mgr.RenameProject("old-name", "new-name"); err != nil {
		t.Fatalf("RenameProject failed: %v", err)
	}

	// Old name gone
	old, _ := mgr.GetProject("old-name")
	if old != nil {
		t.Error("old project should be removed")
	}

	// New name exists with same path
	newProj, _ := mgr.GetProject("new-name")
	if newProj == nil {
		t.Fatal("new project should exist")
	}
	if newProj.Path != "/some/path" {
		t.Errorf("path: got %q, want %q", newProj.Path, "/some/path")
	}
}

func TestRenameProjectUpdatesLinks(t *testing.T) {
	mgr := newTestManager(t)

	_ = mgr.RegisterProject("proj-a", "/a")
	_ = mgr.RegisterProject("proj-b", "/b")
	_ = mgr.CreateLink("test-net")
	_ = mgr.AddLinkMembers("test-net", []LinkMember{
		{Project: "proj-a"},
		{Project: "proj-b", Service: "app"},
	})

	// Rename proj-a
	if err := mgr.RenameProject("proj-a", "proj-a-new"); err != nil {
		t.Fatalf("RenameProject failed: %v", err)
	}

	// Link should reference new name
	link, _ := mgr.GetLink("test-net")
	if link == nil {
		t.Fatal("link should still exist")
	}

	foundNew := false
	foundOld := false
	for _, m := range link.Members {
		if m.Project == "proj-a-new" {
			foundNew = true
		}
		if m.Project == "proj-a" {
			foundOld = true
		}
	}
	if !foundNew {
		t.Error("link should reference proj-a-new")
	}
	if foundOld {
		t.Error("link should not reference proj-a anymore")
	}

	// proj-b should be unchanged
	foundB := false
	for _, m := range link.Members {
		if m.Project == "proj-b" && m.Service == "app" {
			foundB = true
		}
	}
	if !foundB {
		t.Error("proj-b.app should be unchanged in the link")
	}
}

func TestRenameProjectErrors(t *testing.T) {
	mgr := newTestManager(t)

	// Rename non-existent
	if err := mgr.RenameProject("nonexistent", "new"); err == nil {
		t.Error("expected error renaming non-existent project")
	}

	// Rename to existing name
	_ = mgr.RegisterProject("proj-a", "/a")
	_ = mgr.RegisterProject("proj-b", "/b")
	if err := mgr.RenameProject("proj-a", "proj-b"); err == nil {
		t.Error("expected error renaming to existing project name")
	}
}

func TestLinksDoNotAffectProjects(t *testing.T) {
	mgr := newTestManager(t)

	// Register a project and create a link
	_ = mgr.RegisterProject("myproject", "/some/path")
	_ = mgr.CreateLink("test-net")
	_ = mgr.AddLinkMembers("test-net", []LinkMember{{Project: "myproject"}})

	// Verify project is still there
	proj, err := mgr.GetProject("myproject")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if proj == nil {
		t.Fatal("expected project to exist")
	}

	// Delete link, project should remain
	_ = mgr.DeleteLink("test-net")

	proj, err = mgr.GetProject("myproject")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if proj == nil {
		t.Fatal("expected project to still exist after link deletion")
	}
}

func TestRemoveProjectFromLinks(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.CreateLink("test-net")
	_ = mgr.AddLinkMembers("test-net", []LinkMember{
		{Project: "proj-a"},
		{Project: "proj-a", Service: "worker"},
		{Project: "proj-b", Service: "app"},
	})

	if err := mgr.RemoveProjectFromLinks("proj-a"); err != nil {
		t.Fatalf("RemoveProjectFromLinks: %v", err)
	}

	link, _ := mgr.GetLink("test-net")
	if link == nil {
		t.Fatal("link should still exist")
	}
	if len(link.Members) != 1 || link.Members[0].Project != "proj-b" {
		t.Errorf("members = %+v, want only proj-b", link.Members)
	}
}
