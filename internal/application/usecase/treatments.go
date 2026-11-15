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
	OpPlanTreatment  = "ClinicalPlantreatment"
	OpStartTreatment = "ClinicalStarttreatment"
)

// TreatmentService implements in.Treatments.
type TreatmentService struct {
	Records    out.RecordRepository
	Entries    out.EntryRepository
	Treatments out.TreatmentRepository
	Closures   out.EncounterClosures
	Care       out.CareAuthority
	Patients   out.PatientDirectory
	Guard      *GuardedWrites
	Clock      out.Clock
	IDs        out.IDGenerator
}

var _ in.Treatments = (*TreatmentService)(nil)

func ruleViolation(msg string) error {
	return &model.Error{Kind: model.KindRule, Code: "BUSINESS_RULE_VIOLATION", Message: msg}
}

// Plan creates a treatment for the patient's record and publishes TreatmentPlanned.
func (s *TreatmentService) Plan(ctx context.Context, cmd in.PlanTreatmentCommand) (in.TreatmentResult, error) {
	if cmd.PatientID == "" {
		return in.TreatmentResult{}, &model.Error{Kind: model.KindInvalidInput, Code: "VALIDATION_ERROR", Message: "required", Field: "patientId"}
	}
	if res, done, err := s.replay(ctx, cmd.Actor, OpPlanTreatment, cmd.IdempotencyKey, cmd.RequestHash); done || err != nil {
		return res, err
	}
	if err := authorizePatient(ctx, s.Care, cmd.Actor, cmd.PatientID, accessWrite); err != nil {
		return in.TreatmentResult{}, err
	}
	if err := s.requireActive(ctx, cmd.PatientID); err != nil {
		return in.TreatmentResult{}, err
	}
	appointments := make([]string, 0, len(cmd.Procedures))
	for _, p := range cmd.Procedures {
		appointments = append(appointments, p.AppointmentID)
	}
	if err := ensureOpen(ctx, s.Closures, appointments...); err != nil {
		return in.TreatmentResult{}, err
	}
	records, _, err := s.Records.ListRecords(ctx, out.RecordFilter{PatientIDs: []string{cmd.PatientID}, Limit: 1})
	if err != nil {
		return in.TreatmentResult{}, err
	}
	if len(records) == 0 {
		return in.TreatmentResult{}, ruleViolation("the patient has no clinical record")
	}
	rec := records[0]
	if cmd.DiagnosisID != "" {
		diag, err := s.Entries.GetEntry(ctx, cmd.DiagnosisID)
		if err != nil && !errors.Is(err, out.ErrNotFound) {
			return in.TreatmentResult{}, err
		}
		if err != nil || diag.Kind != model.KindDiagnosis || diag.RecordID != rec.ID {
			return in.TreatmentResult{}, ruleViolation("diagnosisId must reference a diagnosis of the patient's record")
		}
	}
	procs := make([]model.PlannedProcedure, len(cmd.Procedures))
	for i, p := range cmd.Procedures {
		procs[i] = model.PlannedProcedure{ID: s.IDs.NewID(), Code: p.Code, AppointmentID: p.AppointmentID}
	}
	now := s.Clock.Now()
	t, err := model.PlanTreatment(model.TreatmentInput{
		ID: s.IDs.NewID(), RecordID: rec.ID, PatientID: cmd.PatientID, DiagnosisID: cmd.DiagnosisID,
		ClinicalReason: cmd.ClinicalReason, Procedures: procs, AuthorID: cmd.Actor.ID, At: now,
	})
	if err != nil {
		return in.TreatmentResult{}, err
	}
	// TreatmentPlanned v1: identifiers and a count only, never the clinical reason.
	event := out.OutboxEvent{
		EventID: s.IDs.NewID(), EventType: "TreatmentPlanned", EventVersion: 1, AggregateID: t.ID, AggregateType: "Treatment",
		CorrelationID: cmd.CorrelationID, CausationID: cmd.CorrelationID, ActorID: cmd.Actor.ID, OccurredAt: now,
		Payload: map[string]any{"treatmentId": t.ID, "patientId": t.PatientID, "dentistId": cmd.Actor.ID,
			"procedureCount": len(t.Procedures), "plannedAt": now.UTC().Format(time.RFC3339)},
	}
	idem := s.idempotency(cmd.Actor, OpPlanTreatment, cmd.IdempotencyKey, cmd.RequestHash, t.ID, statusCreated, now)
	err = s.Guard.Run(ctx, GuardedWrite{Actor: cmd.Actor, PatientID: cmd.PatientID,
		Commit: func(tagged context.Context) error {
			return s.Treatments.CreateTreatment(tagged, t, idem, []out.OutboxEvent{event})
		}})
	if errors.Is(err, out.ErrDuplicateIdempotency) {
		res, _, rerr := s.replay(ctx, cmd.Actor, OpPlanTreatment, cmd.IdempotencyKey, cmd.RequestHash)
		return res, rerr
	}
	if err != nil {
		return in.TreatmentResult{}, err
	}
	return in.TreatmentResult{Treatment: t}, nil
}

// Start moves PLANNED to IN_PROGRESS with optimistic concurrency.
func (s *TreatmentService) Start(ctx context.Context, cmd in.StartTreatmentCommand) (in.TreatmentResult, error) {
	if res, done, err := s.replay(ctx, cmd.Actor, OpStartTreatment, cmd.IdempotencyKey, cmd.RequestHash); done || err != nil {
		return res, err
	}
	t, err := s.Get(ctx, cmd.Actor, cmd.TreatmentID)
	if err != nil {
		return in.TreatmentResult{}, err
	}
	if err := mayUseClinical(cmd.Actor, accessWrite); err != nil {
		return in.TreatmentResult{}, err
	}
	if err := s.requireActive(ctx, t.PatientID); err != nil {
		return in.TreatmentResult{}, err
	}
	now := s.Clock.Now()
	if err := t.Start(cmd.ExpectedVersion, now); err != nil {
		return in.TreatmentResult{}, err
	}
	idem := s.idempotency(cmd.Actor, OpStartTreatment, cmd.IdempotencyKey, cmd.RequestHash, t.ID, statusOK, now)
	switch err := s.Treatments.UpdateTreatment(ctx, t, cmd.ExpectedVersion, idem); {
	case errors.Is(err, out.ErrVersionConflict):
		return in.TreatmentResult{}, &model.Error{Kind: model.KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
	case errors.Is(err, out.ErrDuplicateIdempotency):
		res, _, rerr := s.replay(ctx, cmd.Actor, OpStartTreatment, cmd.IdempotencyKey, cmd.RequestHash)
		return res, rerr
	case err != nil:
		return in.TreatmentResult{}, err
	}
	return in.TreatmentResult{Treatment: t}, nil
}

// List shows only treatments of patients the actor may read, counted after filtering.
func (s *TreatmentService) List(ctx context.Context, q in.ListTreatmentsQuery) (in.Page[model.Treatment], error) {
	page, limit, err := pageWindow(q.Page)
	if err != nil {
		return in.Page[model.Treatment]{}, err
	}
	scope, empty, err := visiblePatients(ctx, s.Care, q.Actor, q.PatientID)
	if err != nil || empty {
		return in.Page[model.Treatment]{Items: []model.Treatment{}, Page: page, Limit: limit}, err
	}
	items, total, err := s.Treatments.ListTreatments(ctx, out.RecordFilter{PatientIDs: scope, Offset: (page - 1) * limit, Limit: limit})
	if err != nil {
		return in.Page[model.Treatment]{}, err
	}
	return in.Page[model.Treatment]{Items: items, Page: page, Limit: limit, Total: total}, nil
}

// Get returns a treatment; out of scope is indistinguishable from missing.
func (s *TreatmentService) Get(ctx context.Context, actor in.Actor, id string) (model.Treatment, error) {
	if err := mayUseClinical(actor, accessRead); err != nil {
		return model.Treatment{}, err
	}
	t, err := s.Treatments.GetTreatment(ctx, id)
	if errors.Is(err, out.ErrNotFound) {
		return model.Treatment{}, notFound()
	}
	if err != nil {
		return model.Treatment{}, err
	}
	switch err := authorizePatient(ctx, s.Care, actor, t.PatientID, accessRead); {
	case errors.Is(err, out.ErrUnavailable):
		return model.Treatment{}, err
	case err != nil:
		return model.Treatment{}, notFound()
	}
	return t, nil
}

func (s *TreatmentService) requireActive(ctx context.Context, patientID string) error {
	active, err := s.Patients.IsActive(ctx, patientID)
	if err != nil {
		return unavailable("patient directory", err)
	}
	if !active {
		return ruleViolation("only active patients can receive new treatments")
	}
	return nil
}

func (s *TreatmentService) idempotency(a in.Actor, op, key, hash, resource string, status int, now time.Time) out.IdempotencyRecord {
	return out.IdempotencyRecord{ActorID: a.ID, Operation: op, Key: key, RequestHash: hash, ResourceID: resource,
		ResponseStatus: status, CreatedAt: now, ExpiresAt: now.Add(idempotencyRetention)}
}

// replay returns the current treatment for a reused key after re-checking
// write authorization; a different payload is a conflict.
func (s *TreatmentService) replay(ctx context.Context, actor in.Actor, op, key, hash string) (in.TreatmentResult, bool, error) {
	saved, found, err := s.Treatments.FindIdempotency(ctx, actor.ID, op, key)
	if err != nil || !found {
		return in.TreatmentResult{}, false, err
	}
	if saved.RequestHash != hash {
		return in.TreatmentResult{}, true, &model.Error{Kind: model.KindConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "the idempotency key was already used with a different request"}
	}
	t, err := s.Treatments.GetTreatment(ctx, saved.ResourceID)
	if err != nil {
		return in.TreatmentResult{}, true, err
	}
	if err := authorizePatient(ctx, s.Care, actor, t.PatientID, accessWrite); err != nil {
		return in.TreatmentResult{}, true, err
	}
	return in.TreatmentResult{Treatment: t, Replayed: true}, true, nil
}

// visiblePatients returns the patient scope of a listing: nil for every
// patient, or the assigned ones; empty is true when the filter is out of scope.
func visiblePatients(ctx context.Context, care out.CareAuthority, actor in.Actor, patientID string) ([]string, bool, error) {
	if err := mayUseClinical(actor, accessRead); err != nil {
		return nil, false, err
	}
	var scope []string
	if !clinicalAdmin(actor, accessRead) {
		assigned, err := care.AssignedPatients(ctx, actor.ID)
		if err != nil {
			return nil, false, unavailable("care authority", err)
		}
		scope = append([]string{}, assigned...)
	}
	if patientID == "" {
		return scope, false, nil
	}
	if scope != nil && !slices.Contains(scope, patientID) {
		return nil, true, nil
	}
	return []string{patientID}, false, nil
}
