package persistence

import (
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// UUIDs: the analytics validators enforce them.
const (
	anaApt1 = "a0a0a0a0-0000-4000-8000-000000000001"
	anaApt2 = "a0a0a0a0-0000-4000-8000-000000000002"
	anaApt3 = "a0a0a0a0-0000-4000-8000-000000000003"
	anaDen1 = "d0d0d0d0-0000-4000-8000-000000000001"
	anaDen2 = "d0d0d0d0-0000-4000-8000-000000000002"
)

func anaEvent(n int) string { return "e0e0e0e0-0000-4000-8000-00000000000" + string(rune('0'+n)) }

func cleanAnalytics(t *testing.T, m *Mongo) {
	t.Helper()
	for _, c := range []string{"analytics_appointment_facts", "analytics_weekly_activity", "analytics_monthly_revenue", "analytics_processed_events"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMongoAnalyticsProjectsAndReads(t *testing.T) {
	m := newTestMongo(t)
	cleanAnalytics(t, m)
	day := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	if _, built, err := m.LatestEvent(ctx); err != nil || built {
		t.Fatalf("no projection before the first event: %v %v", built, err)
	}
	project := func(n int, p out.Projection) error {
		p.Event = out.ProcessedEvent{EventID: anaEvent(n), EventType: "AppointmentScheduled", OccurredAt: day.Add(time.Duration(n) * time.Minute)}
		return m.Project(ctx, p)
	}
	fact := func(n int, apt, dentist string, status model.AppointmentStatus) *model.AppointmentFact {
		return &model.AppointmentFact{AppointmentID: apt, DentistID: dentist, StartAt: day, Status: status,
			SourceEventID: anaEvent(n), SourceEventType: "AppointmentScheduled", SourceOccurredAt: day}
	}
	for n, err := range map[int]error{
		1: project(1, out.Projection{Fact: fact(1, anaApt1, anaDen1, model.AppointmentScheduled)}),
		2: project(2, out.Projection{Fact: fact(2, anaApt2, anaDen2, model.AppointmentScheduled)}),
		3: project(3, out.Projection{Fact: fact(3, anaApt3, anaDen1, model.AppointmentCancelled)}),
		4: project(4, out.Projection{Activity: &out.ActivityIncrement{Day: "2026-10-23", DentistID: anaDen1}}),
		5: project(5, out.Projection{Activity: &out.ActivityIncrement{Day: "2026-10-23", DentistID: anaDen1}}),
		6: project(6, out.Projection{Revenue: &out.RevenueIncrement{Month: "2026-10", Cents: 15000050}}),
		7: project(7, out.Projection{Revenue: &out.RevenueIncrement{Month: "2026-10", Cents: 1000}}),
	} {
		if err != nil {
			t.Fatalf("event %d rejected by the real schema: %v", n, err)
		}
	}
	if err := project(7, out.Projection{Revenue: &out.RevenueIncrement{Month: "2026-10", Cents: 1000}}); !errors.Is(err, out.ErrDuplicateEvent) {
		t.Fatalf("got %v", err)
	}
	from, to := day.Add(-15*time.Hour), day.Add(9*time.Hour)
	if n, err := m.CountAppointments(ctx, from, to, ""); err != nil || n != 2 {
		t.Fatalf("clinic count = %d %v", n, err)
	}
	if n, _ := m.CountAppointments(ctx, from, to, anaDen1); n != 1 {
		t.Fatalf("dentist count = %d", n)
	}
	if act, err := m.ActivityBetween(ctx, "2026-10-19", "2026-10-25", anaDen1); err != nil || act["2026-10-23"] != 2 {
		t.Fatalf("activity = %v %v", act, err)
	}
	if cents, ok, err := m.CollectedRevenue(ctx, "2026-10"); err != nil || !ok || cents != 15001050 {
		t.Fatalf("revenue is exact: %d %v %v", cents, ok, err)
	}
	if _, ok, _ := m.CollectedRevenue(ctx, "2026-09"); ok {
		t.Fatal("a month without payments has no revenue")
	}
	if latest, built, _ := m.LatestEvent(ctx); !built || !latest.Equal(day.Add(7*time.Minute)) {
		t.Fatalf("latest = %s", latest)
	}

	stale := fact(8, anaApt1, anaDen1, model.AppointmentCancelled)
	if err := project(8, out.Projection{Fact: stale, ExpectedSourceEventID: anaEvent(9)}); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("a replace from a stale source is a conflict: %v", err)
	}
	if _, built, _ := m.LatestEvent(ctx); !built {
		t.Fatal("unchanged")
	}
	if f, _, _ := m.FindAppointmentFact(ctx, anaApt1); f.Status != model.AppointmentScheduled {
		t.Fatalf("a rejected projection leaves no trace: %+v", f)
	}
	if err := project(8, out.Projection{Fact: stale, ExpectedSourceEventID: anaEvent(1)}); err != nil {
		t.Fatal(err)
	}
	if f, ok, _ := m.FindAppointmentFact(ctx, anaApt1); !ok || f.Status != model.AppointmentCancelled || f.SourceEventID != anaEvent(8) || !f.StartAt.Equal(day) {
		t.Fatalf("fact = %+v", f)
	}
}
