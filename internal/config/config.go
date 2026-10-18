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
	JWTIssuer         string
	JWTAudience       string
	JWTPublicKey      string // PEM, also accepted on one line with literal "\n"
	JWTPublicKeyFile  string
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
	if err := loadIdentity(lookup, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadIdentity requires the issuer, audience and exactly one public key source.
// The service never holds a private or shared key.
func loadIdentity(lookup func(string) (string, bool), cfg *Config) error {
	cfg.JWTIssuer, _ = lookup("JWT_ISSUER")
	cfg.JWTAudience, _ = lookup("JWT_AUDIENCE")
	cfg.JWTPublicKey, _ = lookup("JWT_PUBLIC_KEY")
	cfg.JWTPublicKeyFile, _ = lookup("JWT_PUBLIC_KEY_FILE")
	switch {
	case cfg.JWTIssuer == "":
		return fmt.Errorf("JWT_ISSUER is required")
	case cfg.JWTAudience == "":
		return fmt.Errorf("JWT_AUDIENCE is required")
	case (cfg.JWTPublicKey == "") == (cfg.JWTPublicKeyFile == ""):
		return fmt.Errorf("set exactly one of JWT_PUBLIC_KEY or JWT_PUBLIC_KEY_FILE")
	}
	return nil
}
