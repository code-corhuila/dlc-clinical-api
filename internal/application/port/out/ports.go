// Package out declares what the Clinical service needs from the outside:
// persistence, time, identifiers and the owners of patient and care authority.
package out

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var (
	// ErrNotFound means the resource does not exist.
	ErrNotFound = errors.New("not found")
	// ErrRecordExists means the patient already has a clinical record.
	ErrRecordExists = errors.New("clinical record already exists")
	// ErrDuplicateIdempotency means another request stored the same key first.
	ErrDuplicateIdempotency = errors.New("idempotency key already stored")
	// ErrUnavailable means an authority could not be consulted; callers fail closed.
	ErrUnavailable = errors.New("dependency unavailable")
)

// Clock returns the server time; author and time never come from the client.
type Clock interface{ Now() time.Time }

// IDGenerator returns new UUIDs.
type IDGenerator interface{ NewID() string }

// IdempotencyRecord is the stored result of a creating request (Norma 5.3.8).
type IdempotencyRecord struct {
	ActorID        string
	Operation      string
	Key            string
	RequestHash    string
	ResourceID     string
	ResponseStatus int
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// RecordFilter bounds a listing. A nil PatientIDs means every patient.
type RecordFilter struct {
	PatientIDs []string
	Offset     int
	Limit      int
}

// RecordRepository persists clinical records. CreateRecord writes the record
// and its idempotency record in one transaction.
type RecordRepository interface {
	FindIdempotency(ctx context.Context, actorID, operation, key string) (IdempotencyRecord, bool, error)
	CreateRecord(ctx context.Context, rec model.ClinicalRecord, idem IdempotencyRecord) error
	GetRecord(ctx context.Context, id string) (model.ClinicalRecord, error)
	// ListRecords returns oldest first and the total matching the filter.
	ListRecords(ctx context.Context, f RecordFilter) ([]model.ClinicalRecord, int, error)
}

// CareAuthority answers, from Appointments, whether a dentist currently cares
// for a patient. A stale projection is never enough for a write.
type CareAuthority interface {
	IsAssigned(ctx context.Context, patientID, dentistID string) (bool, error)
	AssignedPatients(ctx context.Context, dentistID string) ([]string, error)
}

// PatientDirectory answers, from Patients, whether a patient is active.
type PatientDirectory interface {
	IsActive(ctx context.Context, patientID string) (bool, error)
}
