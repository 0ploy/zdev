package services

import (
	"github.com/0ploy/zdev/internal/runtime"
)

// RedisInsightsContainerName is the name of the Redis Insights container
const RedisInsightsContainerName = "zdev_redis"

// RedisInsightsServiceConfig holds configuration for the Redis Insights container
type RedisInsightsServiceConfig struct {
	Image      string
	Domain     string
	TLSEnabled bool
}

// RedisInsightsContainerConfig returns the container configuration for Redis Insights
func RedisInsightsContainerConfig(cfg RedisInsightsServiceConfig) runtime.ContainerConfig {
	return webUIContainerConfig(webUIConfig{
		ContainerName: RedisInsightsContainerName,
		Service:       "redis-insights",
		Subdomain:     "redis",
		Alias:         "redis-insights",
		Port:          "5540",
		Image:         cfg.Image,
		Domain:        cfg.Domain,
		TLSEnabled:    cfg.TLSEnabled,
	})
}
