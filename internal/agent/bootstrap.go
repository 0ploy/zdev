package agent

// Bootstrap mode (PLAN_2 section 1): a repository without .zdev/ has no
// service image yet, so the agent runs in a small image of zdev's own (bash,
// GNU tools, git) with the repository as a plain bind and no project network,
// to write the zdev setup. The developer then runs `zdev start` and continues
// with `zdev claude --continue` in the project's own image.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

const bootstrapDockerfile = "FROM " + config.AlpineImage + `
RUN apk add --no-cache bash coreutils findutils grep sed gawk diffutils patch less \
      git openssh-client curl wget jq yq-go ripgrep python3 tzdata ca-certificates
`

// BootstrapImage is the bootstrap image's tag: built locally, named by the
// hash of its Dockerfile.
func BootstrapImage() string {
	sum := sha256.Sum256([]byte(bootstrapDockerfile))
	return "zdev-agent-bootstrap:" + hex.EncodeToString(sum[:])[:12]
}

// EnsureBootstrapImage builds the bootstrap image when it isn't there.
func EnsureBootstrapImage(ctx context.Context, d Docker) error {
	if _, err := d.ImageID(ctx, BootstrapImage()); err == nil {
		return nil
	}
	if err := d.Stream(ctx, strings.NewReader(bootstrapDockerfile), nil, nil, "build", "-q", "-t", BootstrapImage(), "-"); err != nil {
		return fmt.Errorf("building the bootstrap image: %w", err)
	}
	removeOtherTags(ctx, d, BootstrapImage())
	return nil
}

// BootstrapPrompt is the system prompt of a bootstrap session of agent
// (its name, and its zdev command: claude, pi).
func BootstrapPrompt(agent, command, dir string, info *ImageInfo) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("# Where you run\n\n")
	w("You are the only %s of this developer: there is none on their machine, and their machine is out of your reach. ", agent)
	w("You run in a sandbox container for the repository at %s, which has no zdev setup yet (no .zdev/config.yaml). ", dir)
	w("Your job here is usually to create it: zdev runs the project's services in containers, and once it is set up, sessions run in the project's own image with its toolchain.\n\n")
	w("- This is zdev's small bootstrap image (Alpine: bash, git, GNU tools, curl, jq, ripgrep, python3); the project's own toolchain is not installed.\n")
	w("- zdev commands don't work in this session: there is no project yet. `zdev --help` and the zdev skill describe the configuration.\n")
	w("- When .zdev/config.yaml is written, ask the developer to run `zdev start` on their machine, then `zdev %s --continue` to continue this conversation in the project's own image.\n\n", command)
	w("# Git\n\n`.git/hooks` and `.git/config` are read-only: commits and branches work; for pushes, remotes and git config, ask the developer.\n\n")
	w("# Network\n\nThe internet is reachable. The developer's machine and private networks are not.\n")
	_ = info
	return b.String()
}
