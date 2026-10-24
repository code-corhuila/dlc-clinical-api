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

// OpOpenRecord scopes idempotency keys to the OpenAPI operation.
const OpOpenRecord = "ClinicalOpenclinicalrecord"

// statusCreated is the stored outcome of a first, successful creation.
const statusCreated = 201

// idempotencyRetention keeps retry records at least seven days (design-decisions.md).
const idempotencyRetention = 7 * 24 * time.Hour

// RecordService implements in.ClinicalRecords.
type RecordService struct {
	Repo     out.RecordRepository
	Care     out.CareAuthority
	Patients out.PatientDirectory
	Clock    out.Clock
	IDs      out.IDGenerator
}

var _ in.ClinicalRecords = (*RecordService)(nil)

// Open creates the patient's single record, idempotently by actor and key.
func (s *RecordService) Open(ctx context.Context, cmd in.OpenRecordCommand) (in.OpenRecordResult, error) {
	if cmd.PatientID == "" {
		return in.OpenRecordResult{}, &model.Error{Kind: model.KindInvalidInput, Code: "VALIDATION_ERROR", Message: "required", Field: "patientId"}
	}
	if res, done, err := s.replay(ctx, cmd); done || err != nil {
		return res, err
	}
	if err := authorizePatient(ctx, s.Care, cmd.Actor, cmd.PatientID, accessWrite); err != nil {
		return in.OpenRecordResult{}, err
	}
	active, err := s.Patients.IsActive(ctx, cmd.PatientID)
	if err != nil {
		return in.OpenRecordResult{}, unavailable("patient directory", err)
	}
	if !active {
		return in.OpenRecordResult{}, &model.Error{Kind: model.KindRule, Code: "BUSINESS_RULE_VIOLATION", Message: "only active patients can receive new clinical records"}
	}

	now := s.Clock.Now()
	rec, err := model.OpenRecord(s.IDs.NewID(), cmd.PatientID, cmd.Actor.ID, now)
	if err != nil {
		return in.OpenRecordResult{}, err
	}
	idem := out.IdempotencyRecord{
		ActorID: cmd.Actor.ID, Operation: OpOpenRecord, Key: cmd.IdempotencyKey, RequestHash: cmd.RequestHash,
		ResourceID: rec.ID, ResponseStatus: statusCreated, CreatedAt: now, ExpiresAt: now.Add(idempotencyRetention),
	}
	switch err := s.Repo.CreateRecord(ctx, rec, idem); {
	case errors.Is(err, out.ErrDuplicateIdempotency):
		// A concurrent retry stored the key first: answer with its result.
		res, _, rerr := s.replay(ctx, cmd)
		return res, rerr
	case errors.Is(err, out.ErrRecordExists):
		return in.OpenRecordResult{}, &model.Error{Kind: model.KindConflict, Code: "RECORD_ALREADY_EXISTS", Message: "the patient already has a clinical record"}
	case err != nil:
		return in.OpenRecordResult{}, err
	}
	return in.OpenRecordResult{Record: rec}, nil
}

// replay returns the stored result of a reused key after re-checking the
// actor's current authorization; a different payload is a conflict.
func (s *RecordService) replay(ctx context.Context, cmd in.OpenRecordCommand) (in.OpenRecordResult, bool, error) {
	saved, found, err := s.Repo.FindIdempotency(ctx, cmd.Actor.ID, OpOpenRecord, cmd.IdempotencyKey)
	if err != nil || !found {
		return in.OpenRecordResult{}, false, err
	}
	if saved.RequestHash != cmd.RequestHash {
		return in.OpenRecordResult{}, true, &model.Error{Kind: model.KindConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "the idempotency key was already used with a different request"}
	}
	rec, err := s.Repo.GetRecord(ctx, saved.ResourceID)
	if err != nil {
		return in.OpenRecordResult{}, true, err
	}
	if err := authorizePatient(ctx, s.Care, cmd.Actor, rec.PatientID, accessWrite); err != nil {
		return in.OpenRecordResult{}, true, err
	}
	return in.OpenRecordResult{Record: rec, Replayed: true}, true, nil
}

// List filters by authorization before counting: a Dentist only ever sees,
// and counts, the patients currently assigned by Appointments.
func (s *RecordService) List(ctx context.Context, q in.ListRecordsQuery) (in.Page[model.ClinicalRecord], error) {
	page, limit, err := pageWindow(q.Page)
	if err != nil {
		return in.Page[model.ClinicalRecord]{}, err
	}
	if err := mayUseClinical(q.Actor, accessRead); err != nil {
		return in.Page[model.ClinicalRecord]{}, err
	}
	var scope []string
	if !clinicalAdmin(q.Actor, accessRead) {
		assigned, err := s.Care.AssignedPatients(ctx, q.Actor.ID)
		if err != nil {
			return in.Page[model.ClinicalRecord]{}, unavailable("care authority", err)
		}
		scope = append([]string{}, assigned...)
	}
	if q.PatientID != "" {
		if scope != nil && !slices.Contains(scope, q.PatientID) {
			return in.Page[model.ClinicalRecord]{Items: []model.ClinicalRecord{}, Page: page, Limit: limit}, nil
		}
		scope = []string{q.PatientID}
	}
	items, total, err := s.Repo.ListRecords(ctx, out.RecordFilter{PatientIDs: scope, Offset: (page - 1) * limit, Limit: limit})
	if err != nil {
		return in.Page[model.ClinicalRecord]{}, err
	}
	if items == nil {
		items = []model.ClinicalRecord{}
	}
	return in.Page[model.ClinicalRecord]{Items: items, Page: page, Limit: limit, Total: total}, nil
}

// Get returns a record; an out-of-scope record is indistinguishable from a
// missing one so identifiers cannot be enumerated (authentication.md).
func (s *RecordService) Get(ctx context.Context, actor in.Actor, id string) (model.ClinicalRecord, error) {
	if err := mayUseClinical(actor, accessRead); err != nil {
		return model.ClinicalRecord{}, err
	}
	rec, err := s.Repo.GetRecord(ctx, id)
	if errors.Is(err, out.ErrNotFound) {
		return model.ClinicalRecord{}, notFound()
	}
	if err != nil {
		return model.ClinicalRecord{}, err
	}
	switch err := authorizePatient(ctx, s.Care, actor, rec.PatientID, accessRead); {
	case errors.Is(err, out.ErrUnavailable):
		return model.ClinicalRecord{}, err
	case err != nil:
		return model.ClinicalRecord{}, notFound()
	}
	return rec, nil
}
