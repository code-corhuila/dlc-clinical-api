package in

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// RecordEntryCommand appends an authored entry to a clinical record.
// CorrelationID travels into the outbox events the entry produces.
type RecordEntryCommand struct {
	Actor          Actor
	RecordID       string
	Kind           model.EntryKind
	Text           string
	Code           string
	AppointmentID  string
	ConsultationID string
	IdempotencyKey string
	RequestHash    string
	CorrelationID  string
}

// AmendEntryCommand appends a linked correction; the original never changes.
type AmendEntryCommand struct {
	Actor           Actor
	EntryID         string
	Text            string
	Reason          string
	ExpectedVersion int
	IdempotencyKey  string
	RequestHash     string
}

// EntryResult tells whether the entry was created now or replayed.
type EntryResult struct {
	Entry    model.Entry
	Replayed bool
}

// ClinicalEntries are the HU-CLN-001 entry operations.
type ClinicalEntries interface {
	Record(ctx context.Context, cmd RecordEntryCommand) (EntryResult, error)
	Amend(ctx context.Context, cmd AmendEntryCommand) (EntryResult, error)
	List(ctx context.Context, actor Actor, recordID string, page PageRequest) (Page[model.Entry], error)
}
