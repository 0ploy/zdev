package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

// DBUIContainerName is the name of the Adminer container
const DBUIContainerName = "zdev_db"

// DBUIServiceConfig holds configuration for the database UI container
type DBUIServiceConfig struct {
	Image         string
	Domain        string
	TLSEnabled    bool
	AdminerCfgDir string // Path to the Adminer config directory (for servers.php)
}

// DBUIContainerConfig returns the container configuration for Adminer
func DBUIContainerConfig(cfg DBUIServiceConfig) runtime.ContainerConfig {
	var volumes []runtime.VolumeMount

	// Mount the servers.php file for the login-servers plugin
	if cfg.AdminerCfgDir != "" {
		volumes = append(volumes, runtime.VolumeMount{
			Source:   cfg.AdminerCfgDir + "/servers.php",
			Target:   "/var/www/html/plugins-enabled/login-servers.php",
			ReadOnly: true,
		})
	}

	return webUIContainerConfig(webUIConfig{
		ContainerName: DBUIContainerName,
		Service:       "db",
		Subdomain:     "db",
		Alias:         "adminer",
		Port:          "8080",
		Image:         cfg.Image,
		Domain:        cfg.Domain,
		TLSEnabled:    cfg.TLSEnabled,
		Volumes:       volumes,
		Env: map[string]string{
			// Default to no specific database type - user selects in UI
			"ADMINER_DEFAULT_SERVER": "",
		},
	})
}

// GetAdminerConfigDir returns the path to the Adminer config directory
func GetAdminerConfigDir() (string, error) {
	return filepath.Join(config.GetZdevHome(), "adminer"), nil
}

// EnsureAdminerConfig creates the Adminer config directory and servers.php file
func EnsureAdminerConfig(ctx context.Context) (string, error) {
	adminerDir, err := GetAdminerConfigDir()
	if err != nil {
		return "", err
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(adminerDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create adminer config directory: %w", err)
	}

	// Generate servers.php
	if err := UpdateAdminerServers(ctx); err != nil {
		return "", err
	}

	return adminerDir, nil
}

// UpdateAdminerServers regenerates the servers.php file based on running projects
func UpdateAdminerServers(ctx context.Context) error {
	adminerDir, err := GetAdminerConfigDir()
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(adminerDir, 0755); err != nil {
		return fmt.Errorf("failed to create adminer config directory: %w", err)
	}

	// Get all database servers from registered projects
	servers, err := getDBServersFromProjects(ctx)
	if err != nil {
		return err
	}

	// Generate PHP content
	content := generateServersPHP(servers)

	// Write to file
	serversPath := filepath.Join(adminerDir, "servers.php")
	if err := os.WriteFile(serversPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write servers.php: %w", err)
	}

	return nil
}

// DBServer represents a database server for Adminer
type DBServer struct {
	Hostname    string // Full container name (e.g., db.myproject.zdev)
	ProjectName string // Project name (e.g., myproject)
	ServiceName string // Service name (e.g., db)
	DBType      string // Database type hint (e.g., mysql, postgres)
}

// getDBServersFromProjects discovers database services from all registered projects
func getDBServersFromProjects(ctx context.Context) ([]DBServer, error) {
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return nil, err
	}

	projects, err := stateMgr.ListProjects()
	if err != nil {
		return nil, err
	}
	var servers []DBServer

	for _, proj := range projects {
		// Load project config
		projCfg, err := config.LoadProject(proj.Path)
		if err != nil {
			continue // Skip projects that can't be loaded
		}

		// Check if project uses shared db
		if !projCfg.Shared.DBUI {
			continue
		}

		// Find database services (explicit opt-in via register_to_dbui, or auto-detect by name/image)
		for serviceName, svc := range projCfg.Services {
			if svc.RegisterToDBUI || isDBServiceByNameOrImage(serviceName, svc.Image) {
				servers = append(servers, DBServer{
					Hostname:    fmt.Sprintf("%s.%s.zdev", serviceName, projCfg.Name),
					ProjectName: projCfg.Name,
					ServiceName: serviceName,
					DBType:      detectDBType(svc.Image),
				})
			}
		}
	}

	return servers, nil
}

// IsDBServiceByName checks if a service name suggests it's a database service.
// Only includes databases supported by Adminer (MySQL, MariaDB, PostgreSQL, SQLite).
func IsDBServiceByName(name string) bool {
	dbNames := []string{"db", "database", "mysql", "mariadb", "postgres", "postgresql", "sqlite"}
	nameLower := strings.ToLower(name)
	for _, dbName := range dbNames {
		if nameLower == dbName || strings.HasPrefix(nameLower, dbName+"-") || strings.HasSuffix(nameLower, "-"+dbName) {
			return true
		}
	}
	return false
}

// isDBServiceByNameOrImage checks if a service is a database by name or image
// Only includes databases supported by Adminer (MySQL, MariaDB, PostgreSQL, SQLite)
func isDBServiceByNameOrImage(name, image string) bool {
	if IsDBServiceByName(name) {
		return true
	}

	// Check by image
	imageLower := strings.ToLower(image)
	dbImages := []string{"mysql", "mariadb", "postgres", "sqlite"}
	for _, dbImage := range dbImages {
		if strings.Contains(imageLower, dbImage) {
			return true
		}
	}

	return false
}

// detectDBType tries to detect the database type from the image name
// Only includes databases supported by Adminer
func detectDBType(image string) string {
	imageLower := strings.ToLower(image)

	switch {
	case strings.Contains(imageLower, "mysql"):
		return "mysql"
	case strings.Contains(imageLower, "mariadb"):
		return "mysql" // MariaDB uses MySQL driver in Adminer
	case strings.Contains(imageLower, "postgres"):
		return "pgsql"
	case strings.Contains(imageLower, "sqlite"):
		return "sqlite"
	default:
		return ""
	}
}

// generateServersPHP creates the PHP content for Adminer's login-servers plugin
func generateServersPHP(servers []DBServer) string {
	var sb strings.Builder
	sb.WriteString("<?php\n")
	sb.WriteString("// Auto-generated by zdev - do not edit manually\n")
	sb.WriteString("// This file provides a list of database servers for Adminer\n\n")
	sb.WriteString("require_once('plugins/login-servers.php');\n\n")
	sb.WriteString("return new AdminerLoginServers([\n")

	for _, server := range servers {
		// Label shown in dropdown (alias without zdev_ prefix)
		label := fmt.Sprintf("%s_%s", server.ProjectName, server.ServiceName)

		// Map DBType to Adminer driver name
		driver := dbTypeToAdminerDriver(server.DBType)

		sb.WriteString(fmt.Sprintf("    '%s' => [\n", label))
		sb.WriteString(fmt.Sprintf("        'server' => '%s',\n", server.Hostname))
		sb.WriteString(fmt.Sprintf("        'driver' => '%s',\n", driver))
		sb.WriteString("    ],\n")
	}

	sb.WriteString("]);\n")
	return sb.String()
}

// dbTypeToAdminerDriver maps our DB type to Adminer's driver name
func dbTypeToAdminerDriver(dbType string) string {
	switch dbType {
	case "mysql":
		return "server" // MySQL driver is called 'server' in Adminer
	case "pgsql":
		return "pgsql"
	case "sqlite":
		return "sqlite"
	default:
		return "server" // Default to MySQL
	}
}
