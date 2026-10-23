package config

import (
	"testing"
	"time"
)

func reconcilerEnv(values map[string]string) func(string) (string, bool) {
	full := map[string]string{"MONGO_URI": "mongodb://db:27017/?replicaSet=rs0", "APPOINTMENTS_INTERNAL_URL": "http://appointments:8080",
		"BILLING_INTERNAL_URL": "http://billing:8080", "SERVICE_TOKEN": "svc"}
	for k, v := range values {
		full[k] = v
	}
	return func(k string) (string, bool) {
		v, ok := full[k]
		if ok && v == "\x00" {
			return "", false
		}
		return v, ok
	}
}

func TestLoadReconcilerDeclaresEveryLimit(t *testing.T) {
	cfg, err := LoadReconciler(reconcilerEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Reconciler{MongoURI: "mongodb://db:27017/?replicaSet=rs0", MongoDatabase: "clinical", MongoMaxPoolSize: 10, MongoTimeout: 5 * time.Second,
		AppointmentsURL: "http://appointments:8080", BillingURL: "http://billing:8080", ServiceToken: "svc", InternalTimeout: 3 * time.Second,
		Interval: 30 * time.Second, RunTimeout: 30 * time.Second, MinAge: 2 * time.Minute, BatchSize: 50, ShutdownTimeout: 20 * time.Second}
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadReconcilerRejectsUnusableSettings(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"no appointments owner": {"APPOINTMENTS_INTERNAL_URL": "\x00"},
		"no billing owner":      {"BILLING_INTERNAL_URL": "\x00"},
		"no service token":      {"SERVICE_TOKEN": "\x00"},
		"zero min age":          {"RECONCILE_MIN_AGE": "0s"},
		"huge batch":            {"RECONCILE_BATCH_SIZE": "100000"},
		"no run bound":          {"RECONCILE_RUN_TIMEOUT": "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadReconciler(reconcilerEnv(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
