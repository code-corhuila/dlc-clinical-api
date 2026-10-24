package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func analyticsRouter(t *testing.T, built bool) http.Handler {
	t.Helper()
	store := persistence.NewMemory()
	if built {
		ctx := context.Background()
		at := testNow.Add(-time.Hour)
		_ = store.Project(ctx, out.Projection{Event: out.ProcessedEvent{EventID: "e1", EventType: "AppointmentScheduled", OccurredAt: at},
			Fact: &model.AppointmentFact{AppointmentID: "a1", DentistID: testSubject, StartAt: testNow, Status: model.AppointmentScheduled,
				SourceEventID: "e1", SourceEventType: "AppointmentScheduled", SourceOccurredAt: at}})
		_ = store.Project(ctx, out.Projection{Event: out.ProcessedEvent{EventID: "e2", EventType: "PaymentRecorded", OccurredAt: at},
			Revenue: &out.RevenueIncrement{Month: model.ClinicMonth(testNow, time.UTC), Cents: 15000050}})
	}
	svc := &usecase.DashboardService{Analytics: store, Clock: &clock{t: testNow}, Location: time.UTC}
	return NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Routes: AnalyticsRoutes(svc)})
}

func TestDashboardRouteFollowsTheContract(t *testing.T) {
	h := analyticsRouter(t, true)
	admin := tokenFor(t, testSubject, "ADMINISTRATOR")
	rec := do(h, http.MethodGet, "/api/v1/clinical-analytics/dashboard", admin, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	today := body["todayAppointments"].(map[string]any)
	week := body["weeklyActivity"].([]any)
	revenue := body["monthlyRevenue"].(map[string]any)
	if today["count"] != float64(1) || today["deltaVsYesterday"] != float64(1) || len(week) != 7 || revenue["amount"] != "150000.50" ||
		!rfc3339UTC.MatchString(body["asOf"].(string)) || len(body) != 4 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if _, has := revenue["changePercent"]; has {
		t.Fatal("no previous month: changePercent is omitted")
	}
	if day := week[0].(map[string]any); len(day["day"].(string)) != 10 {
		t.Fatalf("days are calendar dates: %v", day)
	}

	rec = do(h, http.MethodGet, "/api/v1/clinical-analytics/dashboard", tokenFor(t, testSubject, "DENTIST"), nil, nil)
	var dentist map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &dentist)
	if _, has := dentist["monthlyRevenue"]; rec.Code != http.StatusOK || has {
		t.Fatalf("the Dentist never receives revenue: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardRouteFailures(t *testing.T) {
	rec := do(analyticsRouter(t, false), http.MethodGet, "/api/v1/clinical-analytics/dashboard", tokenFor(t, testSubject, "ADMINISTRATOR"), nil, nil)
	if rec.Code != http.StatusServiceUnavailable || decodeEnvelope(t, rec).Error != "SERVICE_UNAVAILABLE" {
		t.Fatalf("no projection yet: %d %s", rec.Code, rec.Body.String())
	}
	h := analyticsRouter(t, true)
	if rec := do(h, http.MethodGet, "/api/v1/clinical-analytics/dashboard", "", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/v1/clinical-analytics/dashboard", tokenFor(t, testSubject, "PATIENT"), nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}
