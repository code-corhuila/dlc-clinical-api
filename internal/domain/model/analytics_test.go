package model

import (
	"testing"
	"time"
)

var (
	bogota, _ = time.LoadLocation("America/Bogota")
	t9        = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
)

func fact(status AppointmentStatus, version int, at time.Time) AppointmentFact {
	return AppointmentFact{AppointmentID: "apt-1", DentistID: "den-1", StartAt: t9, Status: status, AppointmentVersion: version,
		SourceEventID: "ev-old", SourceEventType: "AppointmentScheduled", SourceOccurredAt: at}
}

func TestAppointmentFactNeverMovesBackwards(t *testing.T) {
	stored := fact(AppointmentCancelled, 0, t9.Add(time.Hour))
	if stored.Supersedes(fact(AppointmentScheduled, 0, t9)) == false {
		t.Fatal("a later fact supersedes an earlier one")
	}
	if fact(AppointmentScheduled, 0, t9).Supersedes(stored) {
		t.Fatal("an older event never overwrites a newer state")
	}
	newer, older := fact(AppointmentScheduled, 3, t9), fact(AppointmentScheduled, 2, t9.Add(time.Hour))
	if !newer.Supersedes(older) || older.Supersedes(newer) {
		t.Fatal("the appointment version wins over the event time when both carry one")
	}
}

func TestAppointmentFactCountsOnlyLiveAppointments(t *testing.T) {
	for status, counted := range map[AppointmentStatus]bool{
		AppointmentScheduled: true, AppointmentFinalized: true, AppointmentCancelled: false, AppointmentNoShow: false,
	} {
		if fact(status, 0, t9).Counted() != counted {
			t.Errorf("%s counted = %v", status, !counted)
		}
	}
}

func TestClinicCalendarUsesTheClinicTimezone(t *testing.T) {
	// 03:00 UTC on the 24th is still the 23rd in Bogota (UTC-5).
	late := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	if got := ClinicDay(late, bogota); got != "2026-10-23" {
		t.Fatalf("day = %s", got)
	}
	if got := ClinicMonth(time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC), bogota); got != "2026-10" {
		t.Fatalf("month = %s", got)
	}
	monday, sunday := ClinicWeek(time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC), bogota)
	if monday.Format("2006-01-02") != "2026-10-19" || sunday.Format("2006-01-02") != "2026-10-25" {
		t.Fatalf("week = %s..%s", monday, sunday)
	}
}

func TestCopMoneyIsExact(t *testing.T) {
	for raw, cents := range map[string]int64{"150000.00": 15000000, "0.01": 1, "999999999999.99": 99999999999999} {
		got, err := ParseCOP(raw)
		if err != nil || got != cents || FormatCOP(got) != raw {
			t.Errorf("%s -> %d %v -> %s", raw, got, err, FormatCOP(got))
		}
	}
	for _, bad := range []string{"150000", "1.5", "-1.00", "1e5", "", "1000000000000.00"} {
		if _, err := ParseCOP(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestChangePercentIsRoundedAndNeedsAReference(t *testing.T) {
	if p, ok := ChangePercent(15000000, 10000000); !ok || p != 50 {
		t.Fatalf("got %v %v", p, ok)
	}
	if p, ok := ChangePercent(10000000, 30000000); !ok || p != -66.7 {
		t.Fatalf("rounded to one decimal: %v", p)
	}
	if _, ok := ChangePercent(10000000, 0); ok {
		t.Fatal("no previous month, no percentage (PD-CLN003-04)")
	}
}
