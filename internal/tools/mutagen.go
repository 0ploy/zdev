package tools

import (
	"fmt"

	"github.com/0ploy/zdev/internal/config"
)

// MutagenTool returns the ToolInfo for mutagen
func MutagenTool() ToolInfo {
	return ToolInfo{
		Name:        "mutagen",
		Version:     config.MutagenVersion,
		URLTemplate: config.MutagenURLTemplate,
		BinaryName:  "mutagen",
		ArchiveType: "tar.gz",
		URLBuilder:  mutagenURLBuilder,
		ExtraFiles:  []string{"mutagen-agents.tar.gz"}, // Required for container sync
		Checksums: map[string]string{
			"darwin/amd64": "7d06f7d8fcfe90bc7e55cc834a2f2f20c2e0af9ea9bc35911fc4341ad56a9bbf",
			"darwin/arm64": "6f810416d9e5fc4fd5e18431146f8b3c5a2056ba5a24f76c1e66da86eb3257e2",
			"linux/amd64":  "7735286c778cc438418209f24d03a64f3a0151c8065ef0fe079cfaf093af6f8f",
			"linux/arm64":  "bcba735aebf8cbc11da9b3742118a665599ac697fa06bc5751cac8dcd540db8a",
		},
	}
}

// mutagenURLBuilder constructs the download URL for mutagen
// URL pattern: https://github.com/mutagen-io/mutagen/releases/download/v{version}/mutagen_{os}_{arch}_v{version}.tar.gz
func mutagenURLBuilder(template, version, goos, goarch string) string {
	// Mutagen uses standard os/arch naming (darwin, linux, amd64, arm64)
	return fmt.Sprintf(template, version, goos, goarch, version)
}
