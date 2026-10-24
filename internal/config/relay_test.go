package config

import (
	"strings"
	"testing"
	"time"
)

func relayEnv(values map[string]string) func(string) (string, bool) {
	full := map[string]string{"MONGO_URI": "mongodb://db:27017/?replicaSet=rs0", "RABBITMQ_URL": "amqp://broker:5672/"}
	for k, v := range values {
		full[k] = v
	}
	return func(k string) (string, bool) { v, ok := full[k]; return v, ok }
}

func TestLoadRelayDeclaresEveryLimit(t *testing.T) {
	cfg, err := LoadRelay(relayEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Relay{
		MongoURI: "mongodb://db:27017/?replicaSet=rs0", MongoDatabase: "clinical", MongoMaxPoolSize: 10, MongoTimeout: 5 * time.Second,
		BrokerURL: "amqp://broker:5672/", Exchange: "clinical.events", PublishTimeout: 5 * time.Second,
		BatchSize: 100, Interval: 5 * time.Second, RunTimeout: 30 * time.Second, MaxAttempts: 20, BaseDelay: time.Second, MaxDelay: 5 * time.Minute,
		ShutdownTimeout: 20 * time.Second,
	}
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadRelayRejectsMissingOrUnboundedValues(t *testing.T) {
	cases := map[string]map[string]string{
		"no broker":      {"RABBITMQ_URL": ""},
		"no mongo":       {"MONGO_URI": ""},
		"zero batch":     {"RELAY_BATCH_SIZE": "0"},
		"huge batch":     {"RELAY_BATCH_SIZE": "100000"},
		"zero attempts":  {"RELAY_MAX_ATTEMPTS": "0"},
		"no run limit":   {"RELAY_RUN_TIMEOUT": "0s"},
		"base above max": {"RELAY_BASE_DELAY": "10m"},
		"bad interval":   {"RELAY_INTERVAL": "often"},
		"empty exchange": {"RELAY_EXCHANGE": ""},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadRelay(relayEnv(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	_, err := LoadRelay(relayEnv(map[string]string{"RELAY_BATCH_SIZE": "x"}))
	if err == nil || !strings.Contains(err.Error(), "RELAY_BATCH_SIZE") {
		t.Fatalf("the error names the variable, got %v", err)
	}
}
