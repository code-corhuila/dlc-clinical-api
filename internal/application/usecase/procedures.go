package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// OpCompleteProcedure scopes idempotency keys to the OpenAPI operation.
const OpCompleteProcedure = "ClinicalCompleteprocedure"

// ProcedureService implements in.ProcedureCompletions.
type ProcedureService struct {
	Procedures   out.ProcedureRepository
	Treatments   out.TreatmentRepository
	Closures     out.EncounterClosures
	Care         out.CareAuthority
	Patients     out.PatientDirectory
	Appointments out.AppointmentAuthority
	Clock        out.Clock
	IDs          out.IDGenerator
}

var _ in.ProcedureCompletions = (*ProcedureService)(nil)

func careStateConflict() error {
	return &model.Error{Kind: model.KindConflict, Code: "CARE_STATE_CONFLICT", Message: "the current care state does not allow procedure completion"}
}

// Complete records one procedure's progress, its justified extras and
// ProcedureCompleted atomically. It creates no closure and never completes
// the appointment (ADR-014, APT-009); exactly one business effect per procedure.
func (s *ProcedureService) Complete(ctx context.Context, cmd in.CompleteProcedureCommand) (in.ProcedureCompletionResult, error) {
	if res, done, err := s.replay(ctx, cmd); done || err != nil {
		return res, err
	}
	p, err := s.writableProcedure(ctx, cmd.Actor, cmd.ProcedureID)
	if err != nil {
		return in.ProcedureCompletionResult{}, err
	}
	if err := ensureOpen(ctx, s.Closures, p.AppointmentID); err != nil {
		return in.ProcedureCompletionResult{}, err
	}
	t, err := s.Treatments.GetTreatment(ctx, p.TreatmentID)
	if err != nil {
		return in.ProcedureCompletionResult{}, err
	}
	if t.Status != model.StatusInProgress {
		return in.ProcedureCompletionResult{}, careStateConflict()
	}
	extras, err := validExtras(append(append([]model.ExtraInput{}, cmd.MaterialsUsed...), cmd.AdditionalRequirements...))
	if err != nil {
		return in.ProcedureCompletionResult{}, err
	}
	if _, err := attendingEncounter(ctx, s.Appointments, p.AppointmentID, p.PatientID, ""); err != nil {
		return in.ProcedureCompletionResult{}, err
	}

	now := s.Clock.Now()
	from := p.Version
	if err := p.Complete(cmd.ExpectedVersion, cmd.Actor.ID, now); err != nil {
		return in.ProcedureCompletionResult{}, err
	}
	recorded := make([]out.RecordedExtra, len(extras))
	items := make([]map[string]any, len(extras))
	for i, e := range extras {
		recorded[i] = out.RecordedExtra{Extra: e, ProcedureID: p.ID, AppointmentID: p.AppointmentID, AuthorID: cmd.Actor.ID, CreatedAt: now}
		items[i] = map[string]any{"type": string(e.Type), "quantity": e.Quantity, "clinicalReason": e.ClinicalReason, "sourceRecordId": e.SourceRecordID}
		if e.Code != "" {
			items[i]["code"] = e.Code
		}
	}
	event := out.OutboxEvent{
		EventID: s.IDs.NewID(), EventType: "ProcedureCompleted", EventVersion: 1, AggregateID: p.TreatmentID, AggregateType: "Treatment",
		CorrelationID: cmd.CorrelationID, CausationID: cmd.CorrelationID, ActorID: cmd.Actor.ID, OccurredAt: now,
		Payload: map[string]any{"procedureId": p.ID, "treatmentId": p.TreatmentID, "patientId": p.PatientID, "appointmentId": p.AppointmentID,
			"procedureCode": p.Code, "additionalItems": items, "completedAt": now.UTC().Format(time.RFC3339)},
	}
	idem := out.IdempotencyRecord{ActorID: cmd.Actor.ID, Operation: OpCompleteProcedure, Key: cmd.IdempotencyKey, RequestHash: cmd.RequestHash,
		ResourceID: p.ID, ResponseStatus: statusOK, CreatedAt: now, ExpiresAt: now.Add(idempotencyRetention)}
	switch err := s.Procedures.CompleteProcedure(ctx, p, from, recorded, idem, []out.OutboxEvent{event}); {
	case errors.Is(err, out.ErrVersionConflict):
		return in.ProcedureCompletionResult{}, &model.Error{Kind: model.KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
	case errors.Is(err, out.ErrDuplicateSource):
		return in.ProcedureCompletionResult{}, &model.Error{Kind: model.KindConflict, Code: "SOURCE_ALREADY_RECORDED", Message: "a sourceRecordId of this request was already recorded"}
	case errors.Is(err, out.ErrDuplicateIdempotency):
		res, _, rerr := s.replay(ctx, cmd)
		return res, rerr
	case err != nil:
		return in.ProcedureCompletionResult{}, err
	}
	return in.ProcedureCompletionResult{Procedure: p}, nil
}

// validExtras validates every extra and rejects a repeated source in one request.
func validExtras(inputs []model.ExtraInput) ([]model.Extra, error) {
	seen := map[string]bool{}
	extras := make([]model.Extra, 0, len(inputs))
	for _, in := range inputs {
		e, err := model.NewExtra(in)
		if err != nil {
			return nil, err
		}
		if seen[e.SourceRecordID] {
			return nil, &model.Error{Kind: model.KindInvalidInput, Code: "VALIDATION_ERROR", Message: "must be unique within the request", Field: "sourceRecordId"}
		}
		seen[e.SourceRecordID] = true
		extras = append(extras, e)
	}
	return extras, nil
}

// writableProcedure loads a procedure the actor may write for an active
// patient; an out-of-scope procedure is indistinguishable from a missing one.
func (s *ProcedureService) writableProcedure(ctx context.Context, actor in.Actor, id string) (model.Procedure, error) {
	if err := mayUseClinical(actor, accessWrite); err != nil {
		return model.Procedure{}, err
	}
	p, err := s.Procedures.GetProcedure(ctx, id)
	if errors.Is(err, out.ErrNotFound) {
		return model.Procedure{}, notFound()
	}
	if err != nil {
		return model.Procedure{}, err
	}
	switch err := authorizePatient(ctx, s.Care, actor, p.PatientID, accessWrite); {
	case errors.Is(err, out.ErrUnavailable):
		return model.Procedure{}, err
	case err != nil:
		return model.Procedure{}, notFound()
	}
	active, err := s.Patients.IsActive(ctx, p.PatientID)
	if err != nil {
		return model.Procedure{}, unavailable("patient directory", err)
	}
	if !active {
		return model.Procedure{}, ruleViolation("only active patients can receive new clinical work")
	}
	return p, nil
}

func (s *ProcedureService) replay(ctx context.Context, cmd in.CompleteProcedureCommand) (in.ProcedureCompletionResult, bool, error) {
	saved, found, err := s.Procedures.FindIdempotency(ctx, cmd.Actor.ID, OpCompleteProcedure, cmd.IdempotencyKey)
	if err != nil || !found {
		return in.ProcedureCompletionResult{}, false, err
	}
	if saved.RequestHash != cmd.RequestHash {
		return in.ProcedureCompletionResult{}, true, &model.Error{Kind: model.KindConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "the idempotency key was already used with a different request"}
	}
	p, err := s.writableProcedure(ctx, cmd.Actor, saved.ResourceID)
	if err != nil {
		return in.ProcedureCompletionResult{}, true, err
	}
	return in.ProcedureCompletionResult{Procedure: p, Replayed: true}, true, nil
}
