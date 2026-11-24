package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/state"
)

// routerShortAlias is the short name project containers have always used
// to reach the router on their network.
const routerShortAlias = "router"

// docsHostPrefix + domain is the docs page the router serves from its file
// provider (see config.EnsureDocsConfig); not in the shared service registry.
const docsHostPrefix = "docs.shared."

// routerAliases returns the network aliases the router should carry on
// every project network: its short name, the web UI host of every shared
// service and the docs page, and the routed hostnames of every registered
// project that fall under the zdev domain.
//
// This is what makes *.<domain> work inside containers. The public DNS
// record answers 127.0.0.1, which in a container is the container itself;
// a network alias on the router takes precedence in Docker's embedded DNS,
// so <project>.<domain> resolves to the router and the request takes the
// same path as in the browser. Wildcards can't be expressed as aliases -
// only hostnames zdev knows from config are covered.
//
// Only hosts under the zdev domain: routing.domain accepts any hostname,
// and an alias applies to EVERY project's containers, which also trust the
// zdev CA. An outside host (a production domain routed locally, or a
// malicious config routing api.stripe.com) would silently take over that
// domain for all other projects - while the browser still reaches the real
// site. Outside hosts keep working from the host, as before.
//
// Sorted, so callers can compare alias sets and pass a stable order.
func (m *Manager) routerAliases() ([]string, error) {
	seen := map[string]bool{routerShortAlias: true}
	aliases := []string{routerShortAlias}
	add := func(host string) {
		if host != "" && !seen[host] {
			seen[host] = true
			aliases = append(aliases, host)
		}
	}

	if m.cfg == nil || m.cfg.Domain == "" {
		return aliases, nil
	}
	domain := strings.ToLower(m.cfg.Domain)
	for _, def := range AllSharedServices() {
		if def.HasWebUI() {
			add(def.Subdomain + "." + domain)
		}
	}
	add(docsHostPrefix + domain)

	stateMgr, err := state.DefaultManager()
	if err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}
	hosts, err := stateMgr.GetAllRoutedHosts()
	if err != nil {
		return nil, fmt.Errorf("failed to read routed hosts from state: %w", err)
	}
	for _, host := range hosts {
		if host = strings.ToLower(host); strings.HasSuffix(host, "."+domain) {
			add(host)
		}
	}

	sort.Strings(aliases)
	return aliases, nil
}

// ConnectRouter attaches the router to a project network with the full
// alias set, then brings every other project network the router is on up
// to date.
//
// Docker can't change the aliases of an existing endpoint, so a network
// whose alias set is out of date gets a disconnect + reconnect. That drops
// in-flight requests routed to that project for a moment, so it only
// happens when the set actually changed (a project with new hostnames
// started) - a restart of a known project touches nothing.
//
// Serialized across zdev processes: each run computes the alias set once,
// so two concurrent starts could otherwise finish in the wrong order and
// put back an older set, losing the other project's hostnames.
func (m *Manager) ConnectRouter(ctx context.Context, projectNetwork string) error {
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return m.connectRouter(ctx, projectNetwork)
	}
	return stateMgr.WithRouterLock(func() error { return m.connectRouter(ctx, projectNetwork) })
}

func (m *Manager) connectRouter(ctx context.Context, projectNetwork string) error {
	status, err := m.RouterStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Running {
		return fmt.Errorf("router is not running (start with: zdev services start)")
	}

	desired, err := m.routerAliases()
	if err != nil {
		// Without state, still attach so the project routes; aliases catch
		// up on the next start.
		fmt.Printf("Warning: %v; router hostnames not updated\n", err)
		desired = []string{routerShortAlias}
	}

	attached, err := m.runtime.GetContainerNetworks(ctx, RouterContainerName)
	if err != nil {
		return fmt.Errorf("failed to inspect router networks: %w", err)
	}

	current, isAttached := attached[projectNetwork]
	if err := m.ensureRouterAliases(ctx, projectNetwork, current, isAttached, desired); err != nil {
		return err
	}

	for _, network := range sortedKeys(attached) {
		if network == SharedNetworkName || network == projectNetwork {
			continue
		}
		if err := m.ensureRouterAliases(ctx, network, attached[network], true, desired); err != nil {
			fmt.Printf("Warning: %v\n", err)
		}
	}
	return nil
}

// ensureRouterAliases makes the router's endpoint on one network carry
// exactly the desired aliases. When a reconnect fails it falls back to the
// previous aliases, so a failure never leaves the project without routing.
func (m *Manager) ensureRouterAliases(ctx context.Context, network string, current []string, isAttached bool, desired []string) error {
	if !isAttached {
		if err := m.runtime.NetworkConnect(ctx, network, RouterContainerName, desired...); err != nil {
			if isAlreadyConnectedErr(err) {
				return nil
			}
			return fmt.Errorf("failed to connect router to network %s: %w", network, err)
		}
		return nil
	}

	if sameStringSet(managedAliases(current), desired) {
		return nil
	}

	fmt.Printf("Updating router hostnames on %s...\n", network)
	if err := m.runtime.NetworkDisconnect(ctx, network, RouterContainerName); err != nil {
		return fmt.Errorf("failed to update router hostnames on %s: %w", network, err)
	}
	if err := m.runtime.NetworkConnect(ctx, network, RouterContainerName, desired...); err != nil {
		if isAlreadyConnectedErr(err) {
			// Another zdev process reconnected it in between.
			return nil
		}
		if restoreErr := m.runtime.NetworkConnect(ctx, network, RouterContainerName, current...); restoreErr != nil && !isAlreadyConnectedErr(restoreErr) {
			return fmt.Errorf("router lost network %s while updating hostnames: %w (restore: %v)", network, err, restoreErr)
		}
		return fmt.Errorf("failed to update router hostnames on %s, kept the old ones: %w", network, err)
	}
	return nil
}

// managedAliases drops aliases zdev didn't set. Docker before API 1.44
// (Docker 25) lists the container's short ID under Aliases too; compared
// raw, the sets would never match and every start would reconnect the
// router on every project network. zdev's aliases are "router" and
// hostnames; a short ID has no dot.
func managedAliases(aliases []string) []string {
	out := make([]string, 0, len(aliases))
	for _, a := range aliases {
		if a == routerShortAlias || strings.Contains(a, ".") {
			out = append(out, a)
		}
	}
	return out
}

// isAlreadyConnectedErr matches the daemon's error for connecting an
// endpoint that already exists.
func isAlreadyConnectedErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "already connected")
}

// sameStringSet reports whether a and b hold the same strings, ignoring
// order and duplicates.
func sameStringSet(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	other := make(map[string]bool, len(b))
	for _, s := range b {
		if !set[s] {
			return false
		}
		other[s] = true
	}
	return len(set) == len(other)
}
