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
		HTTPAddr:                ":8080",
		AppEnv:                  "dev",
		ReadHeaderTimeout:       5 * time.Second,
		ReadTimeout:             10 * time.Second,
		WriteTimeout:            15 * time.Second,
		IdleTimeout:             60 * time.Second,
		ShutdownTimeout:         20 * time.Second,
		MaxHeaderBytes:          1 << 20,
		MaxBodyBytes:            1 << 20,
		JWTIssuer:               "iss",
		JWTAudience:             "clinical-service",
		JWTPublicKey:            "pem",
		MongoURI:                "mongodb://db:27017/?replicaSet=rs0",
		MongoDatabase:           "clinical",
		MongoMaxPoolSize:        10,
		MongoTimeout:            5 * time.Second,
		InternalTimeout:         3 * time.Second,
		ClinicalReadPermission:  "clinical:read",
		ClinicalWritePermission: "clinical:write",
	}
	if cfg.ClinicTimezone == nil || cfg.ClinicTimezone.String() != "America/Bogota" {
		t.Fatalf("clinic timezone = %v", cfg.ClinicTimezone)
	}
	want.ClinicTimezone = cfg.ClinicTimezone
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"HTTP_ADDR":             ":9090",
		"APP_ENV":               "qa",
		"AUTH_INTERNAL_URL":     "http://auth",
		"SERVICE_TOKEN":         "svc",
		"SHUTDOWN_TIMEOUT":      "30s",
		"JWT_PUBLIC_KEY":        "",
		"JWT_JWKS_URL":          "http://auth/jwks",
		"PATIENTS_INTERNAL_URL": "http://patients", "APPOINTMENTS_INTERNAL_URL": "http://appointments", "BILLING_INTERNAL_URL": "http://billing",
		"WRITE_TIMEOUT": "20s",
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

// Auth rotates keys with a kid (authentication.md): outside dev the service
// reads the JWKS; a single configured key is a dev convenience.
func TestLoadJWKSSource(t *testing.T) {
	cfg, err := Load(env(map[string]string{"JWT_PUBLIC_KEY": "", "JWT_JWKS_URL": "http://auth:8080/api/v1/auth/jwks"}))
	if err != nil || cfg.JWTJWKSURL != "http://auth:8080/api/v1/auth/jwks" || cfg.JWKSMinRefresh != 30*time.Second {
		t.Fatalf("got %+v %v", cfg, err)
	}
	if _, err := Load(env(map[string]string{"JWT_JWKS_URL": "http://auth/jwks"})); err == nil {
		t.Fatal("JWT_JWKS_URL and JWT_PUBLIC_KEY are mutually exclusive")
	}
	qa := map[string]string{"APP_ENV": "qa", "SERVICE_TOKEN": "svc", "AUTH_INTERNAL_URL": "http://auth",
		"PATIENTS_INTERNAL_URL": "http://patients", "APPOINTMENTS_INTERNAL_URL": "http://appointments", "BILLING_INTERNAL_URL": "http://billing"}
	if _, err := Load(env(qa)); err == nil || !strings.Contains(err.Error(), "JWT_JWKS_URL") {
		t.Fatalf("qa with a static key must be refused, got %v", err)
	}
	if _, err := Load(env(map[string]string{"JWT_PUBLIC_KEY": "", "JWT_JWKS_URL": "http://a/jwks", "JWKS_MIN_REFRESH": "0s"})); err == nil {
		t.Fatal("JWKS_MIN_REFRESH must be positive")
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

func TestLoadInternalOwners(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || cfg.PatientsURL != "" || cfg.AppointmentsURL != "" || cfg.InternalTimeout != 3*time.Second {
		t.Fatalf("owners are optional and fail closed when absent: %+v %v", cfg, err)
	}
	cfg, err = Load(env(map[string]string{"PATIENTS_INTERNAL_URL": "http://patients:8080", "APPOINTMENTS_INTERNAL_URL": "http://appointments:8080",
		"SERVICE_TOKEN": "svc", "INTERNAL_TIMEOUT": "2s"}))
	if err != nil || cfg.PatientsURL != "http://patients:8080" || cfg.AppointmentsURL != "http://appointments:8080" || cfg.ServiceToken != "svc" || cfg.InternalTimeout != 2*time.Second {
		t.Fatalf("got %+v %v", cfg, err)
	}
	for name, bad := range map[string]map[string]string{
		"owner without token": {"PATIENTS_INTERNAL_URL": "http://patients:8080"},
		"two token sources":   {"PATIENTS_INTERNAL_URL": "http://p", "SERVICE_TOKEN": "a", "SERVICE_TOKEN_FILE": "/run/secrets/t"},
		"no call bound":       {"PATIENTS_INTERNAL_URL": "http://p", "SERVICE_TOKEN": "a", "INTERNAL_TIMEOUT": "0s"},
	} {
		if _, err := Load(env(bad)); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// Outside dev every owner is required: a missing one would start a "healthy"
// service that answers 503 to every write.
func TestLoadRequiresEveryOwnerOutsideDev(t *testing.T) {
	all := map[string]string{"APP_ENV": "qa", "SERVICE_TOKEN": "svc", "AUTH_INTERNAL_URL": "http://auth", "JWT_PUBLIC_KEY": "", "JWT_JWKS_URL": "http://auth/jwks",
		"PATIENTS_INTERNAL_URL": "http://patients", "APPOINTMENTS_INTERNAL_URL": "http://appointments", "BILLING_INTERNAL_URL": "http://billing"}
	if _, err := Load(env(all)); err != nil {
		t.Fatalf("complete qa settings rejected: %v", err)
	}
	for _, key := range []string{"PATIENTS_INTERNAL_URL", "APPOINTMENTS_INTERNAL_URL", "BILLING_INTERNAL_URL"} {
		partial := map[string]string{}
		for k, v := range all {
			if k != key {
				partial[k] = v
			}
		}
		_, err := Load(env(partial))
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("qa without %s must fail naming it, got %v", key, err)
		}
	}
}

func TestLoadAuthSessionValidation(t *testing.T) {
	cfg, err := Load(env(map[string]string{"AUTH_INTERNAL_URL": "http://auth:8080", "SERVICE_TOKEN": "svc"}))
	if err != nil || cfg.AuthURL != "http://auth:8080" {
		t.Fatalf("got %+v %v", cfg, err)
	}
	for _, e := range []string{"qa", "main"} {
		if _, err := Load(env(map[string]string{"APP_ENV": e})); err == nil {
			t.Fatalf("%s must not run without revocation checks", e)
		}
	}
	if _, err := Load(env(map[string]string{"AUTH_INTERNAL_URL": "http://auth"})); err == nil {
		t.Fatal("auth URL needs a service token")
	}
	if _, err := Load(env(map[string]string{"BILLING_INTERNAL_URL": "http://billing"})); err == nil {
		t.Fatal("billing URL needs a service token")
	}
	if cfg, err := Load(env(map[string]string{"BILLING_INTERNAL_URL": "http://billing", "SERVICE_TOKEN": "svc"})); err != nil || cfg.BillingURL != "http://billing" {
		t.Fatalf("got %+v %v", cfg, err)
	}
}

func TestLoadClinicalPermissionNames(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || cfg.ClinicalReadPermission != "clinical:read" || cfg.ClinicalWritePermission != "clinical:write" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg, err = Load(env(map[string]string{"CLINICAL_READ_PERMISSION": "clinical.read", "CLINICAL_WRITE_PERMISSION": "clinical.write"}))
	if err != nil || cfg.ClinicalReadPermission != "clinical.read" || cfg.ClinicalWritePermission != "clinical.write" {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
	if _, err := Load(env(map[string]string{"CLINICAL_WRITE_PERMISSION": ""})); err == nil {
		t.Fatal("an empty permission name must be rejected")
	}
}
