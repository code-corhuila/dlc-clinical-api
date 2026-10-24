package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func analyticsFact(id, dentist string, start time.Time, status model.AppointmentStatus, eventID string) *model.AppointmentFact {
	return &model.AppointmentFact{AppointmentID: id, DentistID: dentist, StartAt: start, Status: status,
		SourceEventID: eventID, SourceEventType: "AppointmentScheduled", SourceOccurredAt: start}
}

func TestMemoryAnalyticsProjectsAndReads(t *testing.T) {
	m := NewMemory()
	day := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	if _, built, _ := m.LatestEvent(ctx); built {
		t.Fatal("no projection before the first event")
	}
	project := func(id string, p out.Projection) error {
		p.Event = out.ProcessedEvent{EventID: id, EventType: "X", OccurredAt: day}
		return m.Project(ctx, p)
	}
	_ = project("e1", out.Projection{Fact: analyticsFact("a1", "d1", day, model.AppointmentScheduled, "e1")})
	_ = project("e2", out.Projection{Fact: analyticsFact("a2", "d2", day, model.AppointmentScheduled, "e2")})
	_ = project("e3", out.Projection{Fact: analyticsFact("a3", "d1", day, model.AppointmentCancelled, "e3")})
	_ = project("e4", out.Projection{Activity: &out.ActivityIncrement{Day: "2026-10-23", DentistID: "d1"}})
	_ = project("e5", out.Projection{Activity: &out.ActivityIncrement{Day: "2026-10-23", DentistID: "d2"}})
	_ = project("e6", out.Projection{Revenue: &out.RevenueIncrement{Month: "2026-10", Cents: 1500}})
	if err := project("e6", out.Projection{Revenue: &out.RevenueIncrement{Month: "2026-10", Cents: 1500}}); !errors.Is(err, out.ErrDuplicateEvent) {
		t.Fatalf("got %v", err)
	}
	from, to := day.Add(-15*time.Hour), day.Add(9*time.Hour)
	if n, _ := m.CountAppointments(ctx, from, to, ""); n != 2 {
		t.Fatalf("clinic count = %d (cancelled excluded)", n)
	}
	if n, _ := m.CountAppointments(ctx, from, to, "d1"); n != 1 {
		t.Fatalf("dentist count = %d", n)
	}
	if act, _ := m.ActivityBetween(ctx, "2026-10-19", "2026-10-25", ""); act["2026-10-23"] != 2 {
		t.Fatalf("clinic activity = %v", act)
	}
	if act, _ := m.ActivityBetween(ctx, "2026-10-19", "2026-10-25", "d2"); act["2026-10-23"] != 1 {
		t.Fatalf("dentist activity = %v", act)
	}
	if cents, ok, _ := m.CollectedRevenue(ctx, "2026-10"); !ok || cents != 1500 {
		t.Fatalf("revenue = %d %v", cents, ok)
	}
	if latest, built, _ := m.LatestEvent(ctx); !built || !latest.Equal(day) {
		t.Fatalf("latest = %s %v", latest, built)
	}
	if err := project("e7", out.Projection{Fact: analyticsFact("a1", "d1", day, model.AppointmentCancelled, "e7"), ExpectedSourceEventID: "stale"}); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("a fact replaced from another source is a conflict: %v", err)
	}
	if f, ok, _ := m.FindAppointmentFact(ctx, "a1"); !ok || f.Status != model.AppointmentScheduled {
		t.Fatalf("a rejected projection changes nothing: %+v", f)
	}
}
