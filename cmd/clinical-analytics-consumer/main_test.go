package main

import (
	"slices"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/in/messaging"
)

// The relay publishes mandatory messages: a Clinical fact whose catalogued
// consumer is Analytics must have a queue bound, or it is retried until FAILED.
func TestAnalyticsBindsTheClinicalFactsItConsumes(t *testing.T) {
	for _, event := range []string{"DiagnosisRecorded", "TreatmentPlanned", "ClinicalCareCompleted"} {
		if !slices.Contains(sources, messaging.Binding{Exchange: "clinical.events", RoutingKey: event}) {
			t.Errorf("analytics queue must bind clinical.events %s (event-catalog.md)", event)
		}
	}
}
