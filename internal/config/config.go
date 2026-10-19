// Package config reads the service settings and declares every runtime limit
// explicitly (Norma 5.3.10): an undeclared default means "no limit" in Go.
package config

import (
	"fmt"
	"time"
)

// Config holds the validated process settings.
type Config struct {
	HTTPAddr          string
	AppEnv            string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
}

var environments = map[string]bool{"dev": true, "qa": true, "main": true}

// Load builds a Config from lookup (os.LookupEnv in production), applying the
// declared limits for unset variables and rejecting invalid ones.
func Load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		HTTPAddr:       ":8080",
		AppEnv:         "dev",
		MaxHeaderBytes: 1 << 20,
		MaxBodyBytes:   1 << 20,
	}
	if v, ok := lookup("HTTP_ADDR"); ok {
		if v == "" {
			return Config{}, fmt.Errorf("HTTP_ADDR must not be empty")
		}
		cfg.HTTPAddr = v
	}
	if v, ok := lookup("APP_ENV"); ok {
		if !environments[v] {
			return Config{}, fmt.Errorf("APP_ENV must be dev, qa or main, got %q", v)
		}
		cfg.AppEnv = v
	}

	durations := []struct {
		key    string
		target *time.Duration
		value  time.Duration
	}{
		{"READ_HEADER_TIMEOUT", &cfg.ReadHeaderTimeout, 5 * time.Second},
		{"READ_TIMEOUT", &cfg.ReadTimeout, 10 * time.Second},
		{"WRITE_TIMEOUT", &cfg.WriteTimeout, 15 * time.Second},
		{"IDLE_TIMEOUT", &cfg.IdleTimeout, 60 * time.Second},
		{"SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout, 20 * time.Second},
	}
	for _, d := range durations {
		*d.target = d.value
		v, ok := lookup(d.key)
		if !ok {
			continue
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid duration %q", d.key, v)
		}
		if parsed <= 0 {
			return Config{}, fmt.Errorf("%s must be positive; zero means unlimited", d.key)
		}
		*d.target = parsed
	}
	return cfg, nil
}
