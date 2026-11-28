package agent

// The host fence (PLAN_2 section 7): firewall rules in the agent container's
// network namespace, installed from outside by a short-lived helper that joins
// the namespace with NET_ADMIN and exits. The agent never holds NET_ADMIN, so
// it can't remove them (spike 4). Blocked: the Docker host's addresses, link-
// local and the private ranges except the project's own networks, and the
// addresses of services that hold the Docker socket.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

const fenceDockerfile = "FROM " + config.AlpineImage + "\nRUN apk add --no-cache iptables ip6tables\n"

// FenceImage is the helper image's tag: built locally, named by the hash of
// its Dockerfile so a change builds a new one.
func FenceImage() string {
	sum := sha256.Sum256([]byte(fenceDockerfile))
	return "zdev-agent-fence:" + hex.EncodeToString(sum[:])[:12]
}

// EnsureFenceImage builds the helper image when it isn't there.
func EnsureFenceImage(ctx context.Context, d Docker) error {
	if _, err := d.ImageID(ctx, FenceImage()); err == nil {
		return nil
	}
	if err := d.Stream(ctx, strings.NewReader(fenceDockerfile), nil, nil, "build", "-q", "-t", FenceImage(), "-"); err != nil {
		return fmt.Errorf("building the fence helper image: %w", err)
	}
	removeOtherTags(ctx, d, FenceImage())
	return nil
}

// Fence describes the rules for one agent container.
type Fence struct {
	AllowSubnets []string // the project's networks
	HostPorts    []int    // host ports the agent may reach (agent.host_access)
	BlockIPs     []string // services holding the Docker socket
	// ProxyOnly rejects everything not allowed above: the internet only
	// through the egress proxy (loopback stays open for it and Docker's DNS).
	ProxyOnly bool
}

// Script is the helper's shell script (iptables v4 and v6, REJECT so tools
// fail fast instead of timing out). The rules go into a new chain that
// replaces the previous one in a single jump swap, so applying again (after
// a lifecycle command gave a socket service a new address) never leaves the
// container unfenced in between.
func (f Fence) Script() string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("swap() { t=$1\n  $t -N zdev-new 2>/dev/null || $t -F zdev-new\n}\n")
	b.WriteString("commit() { t=$1\n  $t -I OUTPUT 1 -j zdev-new\n  while $t -D OUTPUT -j zdev 2>/dev/null; do :; done\n  $t -F zdev 2>/dev/null || true\n  $t -X zdev 2>/dev/null || true\n  $t -E zdev-new zdev\n}\n")
	b.WriteString("swap iptables\nr() { iptables -A zdev-new \"$@\"; }\n")
	if f.ProxyOnly {
		b.WriteString("r -o lo -j ACCEPT\n")
	}
	for _, ip := range f.BlockIPs {
		fmt.Fprintf(&b, "r -d %s -j REJECT\n", ip)
	}
	b.WriteString("for h in host.docker.internal gateway.docker.internal host.orb.internal host.lima.internal; do\n")
	b.WriteString("  for ip in $(getent ahostsv4 $h 2>/dev/null | awk '{print $1}' | sort -u); do\n")
	for _, p := range f.HostPorts {
		fmt.Fprintf(&b, "    r -d $ip -p tcp --dport %d -j ACCEPT\n", p)
	}
	b.WriteString("    r -d $ip -j REJECT\n  done\ndone\n")
	for _, s := range f.AllowSubnets {
		fmt.Fprintf(&b, "r -d %s -j ACCEPT\n", s)
	}
	for _, s := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16"} {
		fmt.Fprintf(&b, "r -d %s -j REJECT\n", s)
	}
	if f.ProxyOnly {
		b.WriteString("r -j REJECT\n")
	}
	b.WriteString("commit iptables\n")
	b.WriteString("if swap ip6tables 2>/dev/null; then\n")
	if f.ProxyOnly {
		b.WriteString("  ip6tables -A zdev-new -o lo -j ACCEPT\n")
	}
	b.WriteString("  for s in fc00::/7 fe80::/10; do ip6tables -A zdev-new -d $s -j REJECT; done\n")
	if f.ProxyOnly {
		b.WriteString("  ip6tables -A zdev-new -j REJECT\n")
	}
	b.WriteString("  commit ip6tables\nfi\n")
	return b.String()
}

// Apply installs the rules in the running container's network namespace.
func (f Fence) Apply(ctx context.Context, d Docker, container string) error {
	if err := EnsureFenceImage(ctx, d); err != nil {
		return err
	}
	_, err := d.Output(ctx, "run", "--rm", "--network", "container:"+container, "--cap-add", "NET_ADMIN",
		"--entrypoint", "sh", FenceImage(), "-c", f.Script())
	if err != nil {
		return fmt.Errorf("installing the host fence: %w", err)
	}
	return nil
}
