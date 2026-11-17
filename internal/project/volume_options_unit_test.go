package project

import (
	"testing"

	"github.com/0ploy/zdev/internal/runtime"
)

// TestParseVolumeMount_StripsMode: the target used to keep ":ro", so with
// Mutagen a read-only bind got the container path "/app:ro" (sync session
// beta, sync volume target) and the stale-mount check never matched it.
func TestParseVolumeMount_StripsMode(t *testing.T) {
	for _, vol := range []string{"./src:/app:ro", "./src:/app:ro,z", "./src:/app"} {
		if _, target, _ := parseVolumeMount(vol); target != "/app" {
			t.Errorf("parseVolumeMount(%q) target = %q, want /app", vol, target)
		}
	}
}

func TestVolumeMount_KeepsModeForDocker(t *testing.T) {
	tests := []struct {
		vol  string
		want string
	}{
		{"./src:/app", "./src:/app"},
		{"./src:/app:ro", "./src:/app:ro"},
		{"./src:/app:rw", "./src:/app"},
		{"./src:/app:ro,z", "./src:/app:ro,z"},
		{"./src:/app:cached", "./src:/app:cached"},
	}
	for _, tt := range tests {
		source, target, _ := parseVolumeMount(tt.vol)
		if got := volumeMount(tt.vol, source, target).Arg(); got != tt.want {
			t.Errorf("volumeMount(%q).Arg() = %q, want %q", tt.vol, got, tt.want)
		}
	}
}

// TestGetMutagenSyncMounts_SkipsReadOnlyBinds: Mutagen writes into the
// container through the mount, so a read-only directory bind stays native.
func TestGetMutagenSyncMounts_SkipsReadOnlyBinds(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock, []string{"src", "conf"}, []string{"./src:/app", "./conf:/etc/app:ro"})

	mounts := p.GetMutagenSyncMounts()
	if len(mounts) != 1 || mounts[0].ContainerPath != "/app" {
		t.Fatalf("sync mounts = %+v, want only /app", mounts)
	}

	cfg := p.buildContainerConfig("app", p.Config.Services["app"], true, NewMutagenMounts(mounts))
	for _, v := range cfg.Volumes {
		if v.Target == "/etc/app" && !v.ReadOnly {
			t.Errorf("read-only bind lost its :ro: %+v", v)
		}
	}
}
