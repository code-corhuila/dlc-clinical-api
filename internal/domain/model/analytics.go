package model

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// AppointmentStatus is the analytics view of an appointment (HU-CLN-003):
// enough to count today's appointments, nothing operational.
type AppointmentStatus string

const (
	AppointmentScheduled AppointmentStatus = "SCHEDULED"
	AppointmentCancelled AppointmentStatus = "CANCELADA"
	AppointmentNoShow    AppointmentStatus = "NO_ASISTIO"
	AppointmentFinalized AppointmentStatus = "FINALIZADA"
)

// AppointmentFact is the latest known state of one appointment, rebuilt from
// Appointments events. It holds no patient identity and has no authority.
type AppointmentFact struct {
	AppointmentID      string
	DentistID          string
	StartAt            time.Time
	Status             AppointmentStatus
	AppointmentVersion int // 0 when the source event carries no version
	SourceEventID      string
	SourceEventType    string
	SourceOccurredAt   time.Time
}

// Supersedes reports whether f is at least as recent as other, so that an
// out-of-order delivery never moves the projection backwards. The appointment
// version decides when both carry one; otherwise the event time does.
func (f AppointmentFact) Supersedes(other AppointmentFact) bool {
	if f.AppointmentVersion > 0 && other.AppointmentVersion > 0 && f.AppointmentVersion != other.AppointmentVersion {
		return f.AppointmentVersion > other.AppointmentVersion
	}
	return !f.SourceOccurredAt.Before(other.SourceOccurredAt)
}

// Counted excludes CANCELADA and NO_ASISTIO from today's appointments.
func (f AppointmentFact) Counted() bool {
	return f.Status == AppointmentScheduled || f.Status == AppointmentFinalized
}

// ClinicDay is the calendar day of t in the clinic timezone (YYYY-MM-DD).
func ClinicDay(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// ClinicMonth is the calendar month of t in the clinic timezone (YYYY-MM).
func ClinicMonth(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01") }

// ClinicWeek returns the Monday and Sunday (at midnight, clinic timezone) of t's week.
func ClinicWeek(t time.Time, loc *time.Location) (monday, sunday time.Time) {
	local := t.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	offset := (int(day.Weekday()) + 6) % 7 // Monday = 0
	monday = day.AddDate(0, 0, -offset)
	return monday, monday.AddDate(0, 0, 6)
}

var copAmount = regexp.MustCompile(`^\d{1,12}\.\d{2}$`)

var errCOP = errors.New("amount must be an exact COP decimal with two places")

// ParseCOP reads an exact COP decimal (shared Money schema) into cents.
func ParseCOP(raw string) (int64, error) {
	if !copAmount.MatchString(raw) {
		return 0, errCOP
	}
	units, err := strconv.ParseInt(raw[:len(raw)-3], 10, 64)
	if err != nil {
		return 0, errCOP
	}
	cents, _ := strconv.ParseInt(raw[len(raw)-2:], 10, 64)
	return units*100 + cents, nil
}

// FormatCOP writes cents as an exact COP decimal.
func FormatCOP(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// ChangePercent is the change against the previous period rounded to one
// decimal; without a previous amount there is no reference (PD-CLN003-04).
func ChangePercent(current, previous int64) (float64, bool) {
	if previous <= 0 {
		return 0, false
	}
	return math.Round(float64(current-previous)/float64(previous)*1000) / 10, true
}
