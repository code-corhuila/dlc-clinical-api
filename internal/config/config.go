// Package config reads the service settings and declares every runtime limit
// explicitly (Norma 5.3.10): an undeclared default means "no limit" in Go.
package config

import (
	"fmt"
	"strconv"
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
	MongoURI          string
	MongoDatabase     string
	MongoMaxPoolSize  int           // connection pool size
	MongoTimeout      time.Duration // connect, pool wait and per-operation deadline
	PatientsURL       string        // patients-service /internal/v1 base; empty fails closed
	AppointmentsURL   string        // appointments-service /internal/v1 base; empty fails closed
	ServiceToken      string        // service credential issued by identity, never versioned
	ServiceTokenFile  string
	InternalTimeout   time.Duration // bound of each owner call
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
	if err := loadMongo(lookup, &cfg); err != nil {
		return Config{}, err
	}
	if err := loadOwners(lookup, &cfg); err != nil {
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

// loadMongo requires the Clinical database URI (credential of clinical_app,
// never the administrator) and declares pool size and deadlines explicitly.
func loadMongo(lookup func(string) (string, bool), cfg *Config) error {
	cfg.MongoURI, _ = lookup("MONGO_URI")
	if cfg.MongoURI == "" {
		return fmt.Errorf("MONGO_URI is required")
	}
	cfg.MongoDatabase, cfg.MongoMaxPoolSize, cfg.MongoTimeout = "clinical", 10, 5*time.Second
	if v, ok := lookup("MONGO_DATABASE"); ok {
		if v == "" {
			return fmt.Errorf("MONGO_DATABASE must not be empty")
		}
		cfg.MongoDatabase = v
	}
	if v, ok := lookup("MONGO_MAX_POOL_SIZE"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return fmt.Errorf("MONGO_MAX_POOL_SIZE must be an integer from 1 to 100")
		}
		cfg.MongoMaxPoolSize = n
	}
	if v, ok := lookup("MONGO_TIMEOUT"); ok {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("MONGO_TIMEOUT must be a positive duration")
		}
		cfg.MongoTimeout = d
	}
	return nil
}

// loadOwners reads the optional private owner contracts. When any owner is
// configured, exactly one service-credential source is required.
func loadOwners(lookup func(string) (string, bool), cfg *Config) error {
	cfg.PatientsURL, _ = lookup("PATIENTS_INTERNAL_URL")
	cfg.AppointmentsURL, _ = lookup("APPOINTMENTS_INTERNAL_URL")
	cfg.ServiceToken, _ = lookup("SERVICE_TOKEN")
	cfg.ServiceTokenFile, _ = lookup("SERVICE_TOKEN_FILE")
	timeout, err := positiveDuration(lookup, "INTERNAL_TIMEOUT", 3*time.Second)
	if err != nil {
		return err
	}
	cfg.InternalTimeout = timeout
	if (cfg.PatientsURL != "" || cfg.AppointmentsURL != "") && (cfg.ServiceToken == "") == (cfg.ServiceTokenFile == "") {
		return fmt.Errorf("set exactly one of SERVICE_TOKEN or SERVICE_TOKEN_FILE when an internal owner URL is configured")
	}
	return nil
}
