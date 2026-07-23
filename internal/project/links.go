package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

// connectLinks connects this project's containers to any link networks it belongs to
func (p *Project) connectLinks(ctx context.Context) {
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return
	}

	links, err := stateMgr.GetLinksForProject(p.Config.Name)
	if err != nil || len(links) == 0 {
		return
	}

	for linkName, entry := range links {
		networkExists, err := p.Runtime.NetworkExists(ctx, entry.Network)
		if err != nil || !networkExists {
			continue
		}

		containers := p.linkContainers(entry)
		for _, containerName := range containers {
			if err := p.Runtime.NetworkConnect(ctx, entry.Network, containerName); err != nil {
				errStr := strings.ToLower(err.Error())
				if !strings.Contains(errStr, "already exists") && !strings.Contains(errStr, "already connected") {
					fmt.Printf("Warning: failed to connect %s to link %q: %v\n", containerName, linkName, err)
				}
			} else {
				fmt.Printf("Connected %s to link %q\n", containerName, linkName)
			}
		}
	}
}

// disconnectLinks disconnects this project's containers from all link networks
func (p *Project) disconnectLinks(ctx context.Context) {
	stateMgr, err := state.DefaultManager()
	if err != nil {
		fmt.Printf("Warning: failed to load link state during disconnect: %v\n", err)
		return
	}

	links, err := stateMgr.GetLinksForProject(p.Config.Name)
	if err != nil {
		fmt.Printf("Warning: failed to load project links during disconnect: %v\n", err)
		return
	}
	if len(links) == 0 {
		return
	}

	for linkName, entry := range links {
		containers := p.linkContainers(entry)
		for _, containerName := range containers {
			if err := p.Runtime.NetworkDisconnect(ctx, entry.Network, containerName); err != nil {
				fmt.Printf("Warning: failed to disconnect %s from link %q: %v\n", containerName, linkName, err)
			}
		}
	}
}

// linkContainers returns the container names for this project within a link entry
func (p *Project) linkContainers(entry state.LinkEntry) []string {
	var containers []string
	for _, member := range entry.Members {
		if member.Project != p.Config.Name {
			continue
		}

		if member.Service != "" {
			containers = append(containers, p.ContainerName(member.Service))
		} else {
			for svcName := range p.Config.Services {
				containers = append(containers, p.ContainerName(svcName))
			}
		}
	}
	return containers
}

// DisconnectProjectFromLinks disconnects containers by project name without loading the project config.
// Used by the link delete command when the project config may not be available.
func DisconnectProjectFromLinks(ctx context.Context, rt runtime.Runtime, link *state.LinkEntry, projectName string, stateMgr *state.Manager) {
	for _, member := range link.Members {
		if member.Project != projectName {
			continue
		}

		if member.Service != "" {
			_ = rt.NetworkDisconnect(ctx, link.Network, ContainerNameFor(member.Service, projectName))
			continue
		}

		// Whole project - need to load config to find service names
		proj, err := stateMgr.GetProject(projectName)
		if err != nil || proj == nil {
			continue
		}

		p, err := LoadFromDir(proj.Path)
		if err != nil {
			continue
		}

		for svcName := range p.Config.Services {
			_ = rt.NetworkDisconnect(ctx, link.Network, ContainerNameFor(svcName, projectName))
		}
	}
}
