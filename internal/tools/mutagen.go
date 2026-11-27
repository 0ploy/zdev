package tools

import "github.com/0ploy/zdev/internal/config"

// MutagenTool returns the ToolInfo for mutagen
func MutagenTool() ToolInfo {
	return ToolInfo{
		ToolRelease: config.Mutagen,
		BinaryName:  "mutagen",
		ArchiveType: "tar.gz",
		ExtraFiles:  []string{"mutagen-agents.tar.gz"}, // Required for container sync
	}
}
