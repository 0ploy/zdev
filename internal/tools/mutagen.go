package tools

import (
	"context"
	"os/exec"

	"github.com/0ploy/zdev/internal/config"
)

// MutagenTool returns the ToolInfo for mutagen
func MutagenTool() ToolInfo {
	return ToolInfo{
		ToolRelease:  config.Mutagen,
		BinaryName:   "mutagen",
		ArchiveType:  "tar.gz",
		ExtraFiles:   []string{"mutagen-agents.tar.gz"}, // Required for container sync
		VersionArgs:  []string{"version"},
		BeforeUpdate: stopMutagenDaemon,
	}
}

// stopMutagenDaemon stops the running daemon with the binary that started
// it: a newer client refuses to talk to an older daemon. Sync sessions are
// kept on disk, and the next sync use starts the new daemon, which resumes
// them. Best effort: no daemon running is fine.
func stopMutagenDaemon(ctx context.Context, oldPath string) {
	_ = exec.CommandContext(ctx, oldPath, "daemon", "stop").Run()
}
