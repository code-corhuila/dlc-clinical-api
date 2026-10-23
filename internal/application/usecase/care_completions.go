package usecase

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// Operation ids of clinical-service.yaml, used to scope idempotency keys.
const (
	OpDeclareCareCompletion = "ClinicalDeclareCareCompleted"
	OpRetryCareClosure      = "ClinicalRetrycareclosure"
)

// CareCompletionService implements in.CareCompletions.
type CareCompletionService struct {
	Records      out.RecordRepository
	Entries      out.EntryRepository
	Closures     out.ClosureRepository
	Care         out.CareAuthority
	Patients     out.PatientDirectory
	Appointments out.AppointmentAuthority
	Billing      out.BillingAuthority
	Intents      out.IntentStore
	Admissions   out.AdmissionStore
	Clock        out.Clock
	IDs          out.IDGenerator
}

var _ in.CareCompletions = (*CareCompletionService)(nil)

// Declare records the assigned Dentist's declaration that the encounter's
// clinical work and Billing extras are saved (CLN-009). It freezes the source
// set server-side, opens one closure per encounter (ADR-014) and publishes
// ClinicalCareCompleted; it never finalizes the appointment (APT-009).
func (s *CareCompletionService) Declare(ctx context.Context, cmd in.DeclareCareCompletionCommand) (in.CareClosureResult, error) {
	if res, done, err := s.replay(ctx, cmd); done || err != nil {
		return res, err
	}
	if !slices.Contains(cmd.Actor.Roles, RoleDentist) {
		return in.CareClosureResult{}, forbidden()
	}
	rec, err := s.writableRecord(ctx, cmd.Actor, cmd.RecordID)
	if err != nil {
		return in.CareClosureResult{}, err
	}
	if rec.Version != cmd.ExpectedVersion {
		return in.CareClosureResult{}, &model.Error{Kind: model.KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
	}
	cons, err := s.Entries.GetEntry(ctx, cmd.ConsultationID)
	if err != nil && !errors.Is(err, out.ErrNotFound) {
		return in.CareClosureResult{}, err
	}
	if err != nil || cons.Kind != model.KindConsultation || cons.RecordID != rec.ID || (cons.AppointmentID != "" && cons.AppointmentID != cmd.AppointmentID) {
		return in.CareClosureResult{}, ruleViolation("consultationId must be a consultation of this record and encounter")
	}
	if _, declared, err := s.Closures.FindClosureByEncounter(ctx, rec.ID, cmd.ConsultationID); err != nil {
		return in.CareClosureResult{}, err
	} else if declared {
		return in.CareClosureResult{}, careStateConflict()
	}
	return s.close(ctx, cmd, rec)
}

// close runs the ADR-017 §6 closing sequence. The DECLARATION intent and the
// local freeze are saved first, so a crash at any later step is reconciled
// from Clinical's own data: kept if the declaration committed, else released.
func (s *CareCompletionService) close(ctx context.Context, cmd in.DeclareCareCompletionCommand, rec model.ClinicalRecord) (in.CareClosureResult, error) {
	now := s.Clock.Now()
	d := declaration{svc: s, intent: out.WriteIntent{OperationID: s.IDs.NewID(), Kind: out.IntentDeclaration, PatientID: rec.PatientID,
		Encounter: out.EncounterKey{RecordID: rec.ID, ConsultationID: cmd.ConsultationID}, State: out.IntentPending, CreatedAt: now, UpdatedAt: now}}
	pending, err := s.Admissions.BeginDeclaration(ctx, d.intent)
	switch {
	case errors.Is(err, out.ErrEncounterFrozen):
		return in.CareClosureResult{}, careStateConflict()
	case err != nil:
		return in.CareClosureResult{}, err
	case pending > 0:
		d.reject(ctx)
		return in.CareClosureResult{}, careStateConflict()
	}
	if _, err := attendingEncounter(ctx, s.Appointments, cmd.AppointmentID, rec.PatientID, cmd.Actor.ID); err != nil {
		d.reject(ctx)
		return in.CareClosureResult{}, err
	}
	charges, err := d.freezeCharges(ctx)
	if err != nil {
		d.reject(ctx)
		return in.CareClosureResult{}, err
	}
	tagged := out.WithWriteTag(ctx, out.WriteTag{OperationID: d.intent.OperationID, Complete: true})
	closure, err := s.commitDeclaration(tagged, cmd, rec, charges)
	if err != nil {
		if committed := d.reject(ctx); committed {
			return in.CareClosureResult{}, err // the declaration holds; a retry replays it
		}
		if errors.Is(err, out.ErrDuplicateIdempotency) {
			res, _, rerr := s.replay(ctx, cmd)
			return res, rerr
		}
		return in.CareClosureResult{}, err
	}
	_ = s.Intents.FinishIntent(ctx, d.intent.OperationID, out.IntentCompleted)
	return in.CareClosureResult{Closure: closure}, nil
}

// declaration is one attempt of the closing sequence.
type declaration struct {
	svc          *CareCompletionService
	intent       out.WriteIntent
	billingAsked bool // Billing may hold a freeze for this operationId
}

// freezeCharges freezes the encounter's manual charges in Billing (§6.3).
// Unsettled writes reject the declaration with 409.
func (d *declaration) freezeCharges(ctx context.Context) ([]string, error) {
	d.billingAsked = true
	k := d.intent.Encounter
	settlement, err := d.svc.Billing.FreezeManualCharges(ctx, k.RecordID, k.ConsultationID, d.intent.OperationID)
	if err != nil {
		return nil, unavailable("billing authority", err)
	}
	if !settlement.Settled || settlement.PendingWrites > 0 {
		return nil, careStateConflict()
	}
	return settlement.ChargeIDs, nil
}

// reject fences the intent, then releases both freezes and aborts it (§6.5).
// It reports committed=true, and changes nothing, when the declaration did
// commit. Billing never releases on its own, so an unconfirmed release keeps
// everything PENDING for the reconciler instead of reopening the encounter.
func (d *declaration) reject(ctx context.Context) (committed bool) {
	k, op := d.intent.Encounter, d.intent.OperationID
	committed, err := d.svc.Intents.FenceIntent(ctx, op)
	if err != nil || committed {
		if committed {
			_ = d.svc.Intents.FinishIntent(ctx, op, out.IntentCompleted)
		}
		return committed
	}
	if d.billingAsked && d.svc.Billing.ReleaseManualChargeFreeze(ctx, k.RecordID, k.ConsultationID, op) != nil {
		return false
	}
	if d.svc.Admissions.Unfreeze(ctx, k, op) != nil {
		return false
	}
	_ = d.svc.Intents.FinishIntent(ctx, op, out.IntentAborted)
	return false
}

// commitDeclaration commits the closure with the frozen source set and its
// ClinicalCareCompleted event in one transaction.
func (s *CareCompletionService) commitDeclaration(ctx context.Context, cmd in.DeclareCareCompletionCommand, rec model.ClinicalRecord, charges []string) (model.CareClosure, error) {
	procs, err := s.Closures.CompletedProcedureIDs(ctx, rec.ID, cmd.AppointmentID)
	if err != nil {
		return model.CareClosure{}, err
	}

	now := s.Clock.Now()
	closure, err := model.DeclareCareCompletion(model.DeclarationInput{
		ClosureID: s.IDs.NewID(), RecordID: rec.ID, ConsultationID: cmd.ConsultationID, AppointmentID: cmd.AppointmentID,
		PatientID: rec.PatientID, DentistID: cmd.Actor.ID, ProcedureIDs: procs, ManualChargeIDs: charges,
		ClinicalVersion: rec.Version + 1, CorrelationID: cmd.CorrelationID, At: now,
	})
	if err != nil {
		return model.CareClosure{}, err
	}
	event := out.OutboxEvent{
		EventID: s.IDs.NewID(), EventType: "ClinicalCareCompleted", EventVersion: 1, AggregateID: rec.ID, AggregateType: "ClinicalRecord",
		CorrelationID: cmd.CorrelationID, CausationID: cmd.CorrelationID, ActorID: cmd.Actor.ID, OccurredAt: now,
		Payload: map[string]any{"clinicalRecordId": rec.ID, "consultationId": closure.ConsultationID, "appointmentId": closure.AppointmentID,
			"patientId": closure.PatientID, "dentistId": closure.DentistID, "procedureIds": closure.ProcedureIDs,
			"manualChargeIds": closure.ManualChargeIDs, "completedAt": now.UTC().Format(time.RFC3339), "clinicalVersion": closure.ClinicalVersion},
	}
	idem := out.IdempotencyRecord{ActorID: cmd.Actor.ID, Operation: OpDeclareCareCompletion, Key: cmd.IdempotencyKey, RequestHash: cmd.RequestHash,
		ResourceID: closure.ID, ResponseStatus: statusCreated, CreatedAt: now, ExpiresAt: now.Add(idempotencyRetention)}
	switch err := s.Closures.DeclareCareCompletion(ctx, closure, rec.Version, idem, []out.OutboxEvent{event}); {
	case errors.Is(err, out.ErrVersionConflict):
		return model.CareClosure{}, &model.Error{Kind: model.KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
	case errors.Is(err, out.ErrClosureExists):
		return model.CareClosure{}, careStateConflict()
	case err != nil:
		return model.CareClosure{}, err
	}
	return closure, nil
}

// GetClosure returns an encounter closure; out of scope looks missing.
func (s *CareCompletionService) GetClosure(ctx context.Context, actor in.Actor, id string) (model.CareClosure, error) {
	if err := mayUseClinical(actor, accessRead); err != nil {
		return model.CareClosure{}, err
	}
	c, err := s.Closures.GetClosure(ctx, id)
	if errors.Is(err, out.ErrNotFound) {
		return model.CareClosure{}, notFound()
	}
	if err != nil {
		return model.CareClosure{}, err
	}
	switch err := authorizePatient(ctx, s.Care, actor, c.PatientID, accessRead); {
	case errors.Is(err, out.ErrUnavailable):
		return model.CareClosure{}, err
	case err != nil:
		return model.CareClosure{}, notFound()
	}
	return c, nil
}

// writableRecord loads a record the Dentist may write for an active patient.
func (s *CareCompletionService) writableRecord(ctx context.Context, actor in.Actor, id string) (model.ClinicalRecord, error) {
	entries := &EntryService{Records: s.Records, Care: s.Care, Patients: s.Patients}
	return entries.writableRecord(ctx, actor, id)
}

func (s *CareCompletionService) replay(ctx context.Context, cmd in.DeclareCareCompletionCommand) (in.CareClosureResult, bool, error) {
	saved, found, err := s.Closures.FindIdempotency(ctx, cmd.Actor.ID, OpDeclareCareCompletion, cmd.IdempotencyKey)
	if err != nil || !found {
		return in.CareClosureResult{}, false, err
	}
	if saved.RequestHash != cmd.RequestHash {
		return in.CareClosureResult{}, true, &model.Error{Kind: model.KindConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "the idempotency key was already used with a different request"}
	}
	c, err := s.GetClosure(ctx, cmd.Actor, saved.ResourceID)
	if err != nil {
		return in.CareClosureResult{}, true, err
	}
	return in.CareClosureResult{Closure: c, Replayed: true}, true, nil
}

// statusAccepted is the stored outcome of a resumed closure (202 in the contract).
const statusAccepted = 202

// Retry resumes the same pending or failed closure after an authorized
// correction. It never undoes or duplicates the committed clinical work or
// charges: the original declaration event is retransmitted, not recreated.
func (s *CareCompletionService) Retry(ctx context.Context, cmd in.RetryClosureCommand) (in.CareClosureResult, error) {
	if !textWithinReason(cmd.Reason) {
		return in.CareClosureResult{}, &model.Error{Kind: model.KindInvalidInput, Code: "VALIDATION_ERROR", Message: "must have 1 to 1000 characters", Field: "reason"}
	}
	saved, found, err := s.Closures.FindIdempotency(ctx, cmd.Actor.ID, OpRetryCareClosure, cmd.IdempotencyKey)
	if err != nil {
		return in.CareClosureResult{}, err
	}
	if found {
		if saved.RequestHash != cmd.RequestHash {
			return in.CareClosureResult{}, &model.Error{Kind: model.KindConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "the idempotency key was already used with a different request"}
		}
		c, err := s.writableClosure(ctx, cmd.Actor, saved.ResourceID)
		return in.CareClosureResult{Closure: c, Replayed: true}, err
	}
	c, err := s.writableClosure(ctx, cmd.Actor, cmd.ClosureID)
	if err != nil {
		return in.CareClosureResult{}, err
	}
	now := s.Clock.Now()
	from := c.Version
	if err := c.Retry(cmd.ExpectedVersion, now); err != nil {
		return in.CareClosureResult{}, err
	}
	idem := out.IdempotencyRecord{ActorID: cmd.Actor.ID, Operation: OpRetryCareClosure, Key: cmd.IdempotencyKey, RequestHash: cmd.RequestHash,
		ResourceID: c.ID, ResponseStatus: statusAccepted, CreatedAt: now, ExpiresAt: now.Add(idempotencyRetention)}
	switch err := s.Closures.RetryClosure(ctx, c, from, idem); {
	case errors.Is(err, out.ErrVersionConflict):
		return in.CareClosureResult{}, &model.Error{Kind: model.KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
	case errors.Is(err, out.ErrDuplicateIdempotency):
		return s.Retry(ctx, cmd)
	case err != nil:
		return in.CareClosureResult{}, err
	}
	return in.CareClosureResult{Closure: c}, nil
}

func textWithinReason(r string) bool { n := len([]rune(r)); return n >= 1 && n <= 1000 }

// writableClosure loads a closure the actor may act on (Dentist assigned or
// clinically authorized Administrator); out of scope looks missing.
func (s *CareCompletionService) writableClosure(ctx context.Context, actor in.Actor, id string) (model.CareClosure, error) {
	if err := mayUseClinical(actor, accessWrite); err != nil {
		return model.CareClosure{}, err
	}
	c, err := s.Closures.GetClosure(ctx, id)
	if errors.Is(err, out.ErrNotFound) {
		return model.CareClosure{}, notFound()
	}
	if err != nil {
		return model.CareClosure{}, err
	}
	switch err := authorizePatient(ctx, s.Care, actor, c.PatientID, accessWrite); {
	case errors.Is(err, out.ErrUnavailable):
		return model.CareClosure{}, err
	case err != nil:
		return model.CareClosure{}, notFound()
	}
	return c, nil
}
