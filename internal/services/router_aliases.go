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

// routerAliases returns the network aliases the router should carry on
// every project network: its short name, the web UI host of every shared
// service, and the routed hostnames of every registered project.
//
// This is what makes *.<domain> work inside containers. The public DNS
// record answers 127.0.0.1, which in a container is the container itself;
// a network alias on the router takes precedence in Docker's embedded DNS,
// so <project>.<domain> resolves to the router and the request takes the
// same path as in the browser. Wildcards can't be expressed as aliases -
// only hostnames zdev knows from config are covered.
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

	if m.cfg != nil && m.cfg.Domain != "" {
		for _, def := range AllSharedServices() {
			if def.HasWebUI() {
				add(def.Subdomain + "." + m.cfg.Domain)
			}
		}
	}

	stateMgr, err := state.DefaultManager()
	if err != nil {
		return nil, fmt.Errorf("failed to load state: %w", err)
	}
	hosts, err := stateMgr.GetAllRoutedHosts()
	if err != nil {
		return nil, fmt.Errorf("failed to read routed hosts from state: %w", err)
	}
	for _, host := range hosts {
		add(host)
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
func (m *Manager) ConnectRouter(ctx context.Context, projectNetwork string) error {
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

	if sameStringSet(current, desired) {
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
