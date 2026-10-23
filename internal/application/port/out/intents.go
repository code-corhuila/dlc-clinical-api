package out

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrEncounterFrozen means the encounter stopped admitting writes because
	// a care-completions declaration froze it (ADR-017 §5).
	ErrEncounterFrozen = errors.New("encounter frozen for care completion")
	// ErrIntentSettled means the write intent is no longer pending, or its
	// admission is not ADMITTED, so the tagged write must not commit.
	ErrIntentSettled = errors.New("write intent already settled")
)

// Write intent kinds and states (Clinical data model, write_intents).
const (
	IntentGuardedWrite = "GUARDED_WRITE"
	IntentDeclaration  = "DECLARATION"

	IntentPending   = "PENDING"
	IntentCompleted = "COMPLETED"
	IntentAborted   = "ABORTED"
)

// EncounterKey identifies one encounter: a consultation of a clinical record.
type EncounterKey struct {
	RecordID       string
	ConsultationID string
}

// WriteIntent is persisted before any guard or freeze call, so a lost owner
// response or a crash can be reconciled from Clinical's own data. Committed
// is set in the same transaction as the write it announces.
type WriteIntent struct {
	OperationID string
	Kind        string
	PatientID   string
	Encounter   EncounterKey
	GuardID     string
	Committed   bool
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IntentStore keeps write intents. They never expire by time.
type IntentStore interface {
	SaveIntent(ctx context.Context, w WriteIntent) error
	AttachGuard(ctx context.Context, operationID, guardID string) error
	// FinishIntent moves a PENDING intent to COMPLETED or ABORTED; finishing
	// it again with the same state is a no-op.
	FinishIntent(ctx context.Context, operationID, state string) error
	GetIntent(ctx context.Context, operationID string) (WriteIntent, error)
	// PendingIntents returns up to limit PENDING intents, oldest first.
	PendingIntents(ctx context.Context, limit int) ([]WriteIntent, error)
}

// AdmissionStore keeps the owner-local admission state of each encounter.
type AdmissionStore interface {
	// Admit records an ADMITTED write, or answers ErrEncounterFrozen.
	Admit(ctx context.Context, key EncounterKey, operationID string) error
	// AbortAdmission ends an ADMITTED write that will never commit.
	AbortAdmission(ctx context.Context, key EncounterKey, operationID string) error
	// BeginDeclaration saves the DECLARATION intent and freezes its encounter in
	// one transaction, returning how many admissions are still ADMITTED. A
	// freeze held by another declaration answers ErrEncounterFrozen.
	BeginDeclaration(ctx context.Context, w WriteIntent) (pendingAdmissions int, err error)
	// Unfreeze reopens the encounter only if operationID holds the freeze.
	Unfreeze(ctx context.Context, key EncounterKey, operationID string) error
}

// WriteTag binds a repository write to its intent: inside the write's own
// transaction the adapter marks the intent committed (and COMPLETED when
// Complete is set) and the encounter admission COMMITTED, or fails the whole
// write with ErrIntentSettled. It travels in the context like a transaction
// boundary, so every write port keeps its signature.
type WriteTag struct {
	OperationID string
	Encounter   *EncounterKey
	Complete    bool
}

type writeTagKey struct{}

// WithWriteTag returns a context whose repository writes apply tag.
func WithWriteTag(ctx context.Context, tag WriteTag) context.Context {
	return context.WithValue(ctx, writeTagKey{}, tag)
}

// WriteTagFrom returns the tag of a guarded write, if any.
func WriteTagFrom(ctx context.Context) (WriteTag, bool) {
	tag, ok := ctx.Value(writeTagKey{}).(WriteTag)
	return tag, ok
}
