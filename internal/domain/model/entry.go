package model

import "time"

// EntryKind is the type of an authored clinical entry.
type EntryKind string

const (
	KindConsultation EntryKind = "CONSULTATION"
	KindDiagnosis    EntryKind = "DIAGNOSIS"
	KindEvolution    EntryKind = "EVOLUTION"
	KindAntecedent   EntryKind = "ANTECEDENT"
	KindAllergy      EntryKind = "ALLERGY"
)

func (k EntryKind) valid() bool {
	switch k {
	case KindConsultation, KindDiagnosis, KindEvolution, KindAntecedent, KindAllergy:
		return true
	}
	return false
}

// Entry is append-only (CLN-004, CLN-005): a correction is a new entry linked
// through AmendsEntryID; the original is never overwritten or deleted.
type Entry struct {
	ID              string
	RecordID        string
	Kind            EntryKind
	Text            string
	Code            string
	AppointmentID   string
	ConsultationID  string
	AuthorID        string
	AmendsEntryID   string
	AmendmentReason string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Version         int
}

// EntryInput carries a new entry; AuthorID and At are set by the server.
type EntryInput struct {
	ID, RecordID   string
	Kind           EntryKind
	Text, Code     string
	AppointmentID  string
	ConsultationID string
	AuthorID       string
	At             time.Time
}

// NewEntry validates and builds an entry. A diagnosis must carry the explicit
// consultationId supplied by the caller; it is never inferred (CLN-008).
func NewEntry(in EntryInput) (Entry, error) {
	switch {
	case !in.Kind.valid():
		return Entry{}, invalid("kind", "must be CONSULTATION, DIAGNOSIS, EVOLUTION, ANTECEDENT or ALLERGY")
	case !textWithin(in.Text, 1, 10000):
		return Entry{}, invalid("text", "must have 1 to 10000 characters")
	case in.Code != "" && !textWithin(in.Code, 1, 80):
		return Entry{}, invalid("code", "must have 1 to 80 characters")
	case in.Kind == KindDiagnosis && in.ConsultationID == "":
		return Entry{}, invalid("consultationId", "required for DIAGNOSIS")
	case in.AuthorID == "":
		return Entry{}, invalid("authorId", "required")
	}
	return Entry{
		ID: in.ID, RecordID: in.RecordID, Kind: in.Kind, Text: in.Text, Code: in.Code,
		AppointmentID: in.AppointmentID, ConsultationID: in.ConsultationID, AuthorID: in.AuthorID,
		CreatedAt: in.At, UpdatedAt: in.At, Version: 1,
	}, nil
}

// CheckDiagnosisConsultation verifies that the referenced consultation exists
// in the same record and encounter as the diagnosis.
func CheckDiagnosisConsultation(diag EntryInput, cons Entry) error {
	switch {
	case cons.ID != diag.ConsultationID:
		return rule("consultationId does not match the referenced consultation")
	case cons.Kind != KindConsultation:
		return rule("consultationId must reference a CONSULTATION entry")
	case cons.RecordID != diag.RecordID:
		return rule("the consultation belongs to another clinical record")
	case cons.AppointmentID != "" && diag.AppointmentID != "" && cons.AppointmentID != diag.AppointmentID:
		return rule("the consultation belongs to another encounter")
	}
	return nil
}

// Amendment is a correction request for an existing entry.
type Amendment struct {
	ID, Text, Reason string
	ExpectedVersion  int
	AuthorID         string
	At               time.Time
}

// Amend returns a new entry that corrects e, preserving e unchanged.
func (e Entry) Amend(a Amendment) (Entry, error) {
	switch {
	case !textWithin(a.Text, 1, 10000):
		return Entry{}, invalid("text", "must have 1 to 10000 characters")
	case !textWithin(a.Reason, 1, 1000):
		return Entry{}, invalid("reason", "must have 1 to 1000 characters")
	case a.AuthorID == "":
		return Entry{}, invalid("authorId", "required")
	case a.ExpectedVersion != e.Version:
		return Entry{}, staleVersion()
	}
	return Entry{
		ID: a.ID, RecordID: e.RecordID, Kind: e.Kind, Text: a.Text, Code: e.Code,
		AppointmentID: e.AppointmentID, ConsultationID: e.ConsultationID, AuthorID: a.AuthorID,
		AmendsEntryID: e.ID, AmendmentReason: a.Reason, CreatedAt: a.At, UpdatedAt: a.At, Version: 1,
	}, nil
}
