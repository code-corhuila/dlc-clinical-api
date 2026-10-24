package config

import "testing"

func TestLoadAnalyticsConsumerUsesItsOwnQueueAndTheClinicTimezone(t *testing.T) {
	cfg, err := LoadAnalyticsConsumer(relayEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Queue != "clinical.analytics" || cfg.ClinicTimezone.String() != "America/Bogota" || cfg.DeliveryLimit != 10 {
		t.Fatalf("got %+v", cfg)
	}
	cfg, err = LoadAnalyticsConsumer(relayEnv(map[string]string{"ANALYTICS_QUEUE": "clinical.analytics.qa", "CLINIC_TIMEZONE": "UTC"}))
	if err != nil || cfg.Queue != "clinical.analytics.qa" || cfg.ClinicTimezone.String() != "UTC" {
		t.Fatalf("got %+v %v", cfg, err)
	}
	for name, values := range map[string]map[string]string{
		"unknown timezone": {"CLINIC_TIMEZONE": "Mars/Base"},
		"empty queue":      {"ANALYTICS_QUEUE": ""},
		"no broker":        {"RABBITMQ_URL": ""},
	} {
		if _, err := LoadAnalyticsConsumer(relayEnv(values)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
