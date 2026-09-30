// Package config loads runtime configuration at the composition root.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config contains only process-level configuration. Domain code never reads the environment.
type Config struct {
	HTTPAddr        string
	AppEnv          string
	ShutdownTimeout time.Duration
}

// Load reads the variables needed by the bootstrap HTTP server. Database and JWT settings are
// intentionally declared in .env.example but are not consumed until their adapters are implemented.
func Load() (Config, error) {
	shutdownTimeout, err := time.ParseDuration(value("SHUTDOWN_TIMEOUT", "20s"))
	if err != nil || shutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration")
	}

	return Config{
		HTTPAddr:        value("HTTP_ADDR", ":8080"),
		AppEnv:          value("APP_ENV", "local"),
		ShutdownTimeout: shutdownTimeout,
	}, nil
}

func value(key, fallback string) string {
	if configured := os.Getenv(key); configured != "" {
		return configured
	}
	return fallback
}
