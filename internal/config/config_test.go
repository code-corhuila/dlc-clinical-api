package config

import (
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) (string, bool) {
	full := map[string]string{"JWT_ISSUER": "iss", "JWT_AUDIENCE": "clinical-service", "JWT_PUBLIC_KEY": "pem", "MONGO_URI": "mongodb://db:27017/?replicaSet=rs0"}
	for k, v := range values {
		full[k] = v
	}
	values = full
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
		JWTIssuer:         "iss",
		JWTAudience:       "clinical-service",
		JWTPublicKey:      "pem",
		MongoURI:          "mongodb://db:27017/?replicaSet=rs0",
		MongoDatabase:     "clinical",
		MongoMaxPoolSize:  10,
		MongoTimeout:      5 * time.Second,
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

func TestLoadRequiresIdentityConfiguration(t *testing.T) {
	for _, key := range []string{"JWT_ISSUER", "JWT_AUDIENCE"} {
		values := map[string]string{key: ""}
		if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s must be required, got %v", key, err)
		}
	}
}

func TestLoadRequiresExactlyOnePublicKeySource(t *testing.T) {
	none := map[string]string{"JWT_PUBLIC_KEY": ""}
	if _, err := Load(env(none)); err == nil {
		t.Fatal("a public key source is required")
	}
	both := map[string]string{"JWT_PUBLIC_KEY_FILE": "/keys/jwt.pub"}
	if _, err := Load(env(both)); err == nil {
		t.Fatal("JWT_PUBLIC_KEY and JWT_PUBLIC_KEY_FILE are mutually exclusive")
	}
	file := map[string]string{"JWT_PUBLIC_KEY": "", "JWT_PUBLIC_KEY_FILE": "/keys/jwt.pub"}
	cfg, err := Load(env(file))
	if err != nil || cfg.JWTPublicKeyFile != "/keys/jwt.pub" {
		t.Fatalf("file source not accepted: %+v %v", cfg, err)
	}
}

func TestLoadMongoSettings(t *testing.T) {
	if _, err := Load(env(map[string]string{"MONGO_URI": ""})); err == nil || !strings.Contains(err.Error(), "MONGO_URI") {
		t.Fatalf("MONGO_URI must be required, got %v", err)
	}
	cfg, err := Load(env(map[string]string{"MONGO_DATABASE": "clinical_qa", "MONGO_MAX_POOL_SIZE": "25", "MONGO_TIMEOUT": "3s"}))
	if err != nil || cfg.MongoDatabase != "clinical_qa" || cfg.MongoMaxPoolSize != 25 || cfg.MongoTimeout != 3*time.Second {
		t.Fatalf("got %+v %v", cfg, err)
	}
	for _, bad := range []map[string]string{{"MONGO_MAX_POOL_SIZE": "0"}, {"MONGO_MAX_POOL_SIZE": "x"}, {"MONGO_MAX_POOL_SIZE": "500"}, {"MONGO_TIMEOUT": "0s"}, {"MONGO_DATABASE": ""}} {
		if _, err := Load(env(bad)); err == nil {
			t.Fatalf("%v must be rejected", bad)
		}
	}
}
