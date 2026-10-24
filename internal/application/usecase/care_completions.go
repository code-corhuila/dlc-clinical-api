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

// OpDeclareCareCompletion scopes idempotency keys to the OpenAPI operation.
const OpDeclareCareCompletion = "ClinicalDeclareCareCompleted"

// CareCompletionService implements in.CareCompletions.
type CareCompletionService struct {
	Records      out.RecordRepository
	Entries      out.EntryRepository
	Closures     out.ClosureRepository
	Care         out.CareAuthority
	Patients     out.PatientDirectory
	Appointments out.AppointmentAuthority
	Billing      out.BillingAuthority
	Gate         out.EncounterGate
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
	attending, err := s.Appointments.InAttention(ctx, cmd.AppointmentID, rec.PatientID)
	if err != nil {
		return in.CareClosureResult{}, unavailable("appointment authority", err)
	}
	if !attending {
		return in.CareClosureResult{}, careStateConflict()
	}
	if err := s.Gate.Quiesce(ctx, rec.ID, cmd.ConsultationID, cmd.AppointmentID); err != nil {
		return in.CareClosureResult{}, unavailable("encounter write gate", err)
	}
	procs, err := s.Closures.CompletedProcedureIDs(ctx, rec.ID, cmd.AppointmentID)
	if err != nil {
		return in.CareClosureResult{}, err
	}
	charges, err := s.Billing.EncounterManualCharges(ctx, rec.ID, cmd.ConsultationID, cmd.AppointmentID)
	if err != nil {
		return in.CareClosureResult{}, unavailable("billing authority", err)
	}

	now := s.Clock.Now()
	closure, err := model.DeclareCareCompletion(model.DeclarationInput{
		ClosureID: s.IDs.NewID(), RecordID: rec.ID, ConsultationID: cmd.ConsultationID, AppointmentID: cmd.AppointmentID,
		PatientID: rec.PatientID, DentistID: cmd.Actor.ID, ProcedureIDs: procs, ManualChargeIDs: charges,
		ClinicalVersion: rec.Version + 1, CorrelationID: cmd.CorrelationID, At: now,
	})
	if err != nil {
		return in.CareClosureResult{}, err
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
		return in.CareClosureResult{}, &model.Error{Kind: model.KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
	case errors.Is(err, out.ErrClosureExists):
		return in.CareClosureResult{}, careStateConflict()
	case errors.Is(err, out.ErrDuplicateIdempotency):
		res, _, rerr := s.replay(ctx, cmd)
		return res, rerr
	case err != nil:
		return in.CareClosureResult{}, err
	}
	return in.CareClosureResult{Closure: closure}, nil
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
