package config

import (
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestLoadAppliesDeclaredLimitsWhenUnset(t *testing.T) {
	cfg, err := Load(env(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		HTTPAddr:          ":8080",
		AppEnv:            "dev",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   20 * time.Second,
		MaxHeaderBytes:    1 << 20,
		MaxBodyBytes:      1 << 20,
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"HTTP_ADDR":        ":9090",
		"APP_ENV":          "qa",
		"SHUTDOWN_TIMEOUT": "30s",
		"WRITE_TIMEOUT":    "20s",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.AppEnv != "qa" || cfg.ShutdownTimeout != 30*time.Second || cfg.WriteTimeout != 20*time.Second {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"unparseable duration": {"WRITE_TIMEOUT": "soon"},
		"zero means unlimited": {"IDLE_TIMEOUT": "0s"},
		"negative duration":    {"READ_HEADER_TIMEOUT": "-1s"},
		"unknown environment":  {"APP_ENV": "prod"},
		"empty address":        {"HTTP_ADDR": ""},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadErrorNamesTheVariable(t *testing.T) {
	_, err := Load(env(map[string]string{"IDLE_TIMEOUT": "0s"}))
	if err == nil || !strings.Contains(err.Error(), "IDLE_TIMEOUT") {
		t.Fatalf("error should name the variable, got %v", err)
	}
}
