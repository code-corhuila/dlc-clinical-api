package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// Operation ids of clinical-service.yaml, used to scope idempotency keys.
const (
	OpRecordEntry = "ClinicalRecordclinicalentry"
	OpAmendEntry  = "ClinicalAmendclinicalentry"
)

// EntryService implements in.ClinicalEntries.
type EntryService struct {
	Records  out.RecordRepository
	Entries  out.EntryRepository
	Closures out.EncounterClosures
	Care     out.CareAuthority
	Patients out.PatientDirectory
	Guard    *GuardedWrites
	Clock    out.Clock
	IDs      out.IDGenerator
}

var _ in.ClinicalEntries = (*EntryService)(nil)

// Record appends an authored entry and, for consultations and diagnoses,
// its outbox event in the same write.
func (s *EntryService) Record(ctx context.Context, cmd in.RecordEntryCommand) (in.EntryResult, error) {
	if res, done, err := s.replay(ctx, cmd.Actor, OpRecordEntry, cmd.IdempotencyKey, cmd.RequestHash); done || err != nil {
		return res, err
	}
	rec, err := s.writableRecord(ctx, cmd.Actor, cmd.RecordID)
	if err != nil {
		return in.EntryResult{}, err
	}
	if err := ensureOpen(ctx, s.Closures, cmd.AppointmentID); err != nil {
		return in.EntryResult{}, err
	}
	now := s.Clock.Now()
	input := model.EntryInput{
		ID: s.IDs.NewID(), RecordID: rec.ID, Kind: cmd.Kind, Text: cmd.Text, Code: cmd.Code,
		AppointmentID: cmd.AppointmentID, ConsultationID: cmd.ConsultationID, AuthorID: cmd.Actor.ID, At: now,
	}
	entry, err := model.NewEntry(input)
	if err != nil {
		return in.EntryResult{}, err
	}
	if entry.Kind == model.KindDiagnosis {
		cons, err := s.Entries.GetEntry(ctx, cmd.ConsultationID)
		if errors.Is(err, out.ErrNotFound) {
			return in.EntryResult{}, &model.Error{Kind: model.KindRule, Code: "BUSINESS_RULE_VIOLATION", Message: "consultationId does not reference a consultation of this record"}
		}
		if err != nil {
			return in.EntryResult{}, err
		}
		if err := model.CheckDiagnosisConsultation(input, cons); err != nil {
			return in.EntryResult{}, err
		}
	}
	return s.create(ctx, cmd.Actor, rec, OpRecordEntry, cmd.IdempotencyKey, cmd.RequestHash, entry, s.events(cmd, rec, entry, now))
}

// Amend appends a correction linked to the original entry (CLN-004, CLN-005).
func (s *EntryService) Amend(ctx context.Context, cmd in.AmendEntryCommand) (in.EntryResult, error) {
	if res, done, err := s.replay(ctx, cmd.Actor, OpAmendEntry, cmd.IdempotencyKey, cmd.RequestHash); done || err != nil {
		return res, err
	}
	if err := mayUseClinical(cmd.Actor, accessWrite); err != nil {
		return in.EntryResult{}, err
	}
	orig, err := s.Entries.GetEntry(ctx, cmd.EntryID)
	if errors.Is(err, out.ErrNotFound) {
		return in.EntryResult{}, notFound()
	}
	if err != nil {
		return in.EntryResult{}, err
	}
	rec, err := s.writableRecord(ctx, cmd.Actor, orig.RecordID)
	if err != nil {
		return in.EntryResult{}, err
	}
	if err := ensureOpen(ctx, s.Closures, orig.AppointmentID); err != nil {
		return in.EntryResult{}, err
	}
	fix, err := orig.Amend(model.Amendment{ID: s.IDs.NewID(), Text: cmd.Text, Reason: cmd.Reason, ExpectedVersion: cmd.ExpectedVersion, AuthorID: cmd.Actor.ID, At: s.Clock.Now()})
	if err != nil {
		return in.EntryResult{}, err
	}
	return s.create(ctx, cmd.Actor, rec, OpAmendEntry, cmd.IdempotencyKey, cmd.RequestHash, fix, nil)
}

// List authorizes the parent record before reading or counting its entries.
func (s *EntryService) List(ctx context.Context, actor in.Actor, recordID string, p in.PageRequest) (in.Page[model.Entry], error) {
	page, limit, err := pageWindow(p)
	if err != nil {
		return in.Page[model.Entry]{}, err
	}
	if _, err := s.record(ctx, actor, recordID, accessRead); err != nil {
		return in.Page[model.Entry]{}, err
	}
	items, total, err := s.Entries.ListEntries(ctx, recordID, (page-1)*limit, limit)
	if err != nil {
		return in.Page[model.Entry]{}, err
	}
	return in.Page[model.Entry]{Items: items, Page: page, Limit: limit, Total: total}, nil
}

// record loads a record the actor may access; out of scope looks missing.
func (s *EntryService) record(ctx context.Context, actor in.Actor, id string, mode access) (model.ClinicalRecord, error) {
	if err := mayUseClinical(actor, mode); err != nil {
		return model.ClinicalRecord{}, err
	}
	rec, err := s.Records.GetRecord(ctx, id)
	if errors.Is(err, out.ErrNotFound) {
		return model.ClinicalRecord{}, notFound()
	}
	if err != nil {
		return model.ClinicalRecord{}, err
	}
	switch err := authorizePatient(ctx, s.Care, actor, rec.PatientID, mode); {
	case errors.Is(err, out.ErrUnavailable):
		return model.ClinicalRecord{}, err
	case err != nil:
		return model.ClinicalRecord{}, notFound()
	}
	return rec, nil
}

// writableRecord also requires an active patient (PAT-002).
func (s *EntryService) writableRecord(ctx context.Context, actor in.Actor, id string) (model.ClinicalRecord, error) {
	rec, err := s.record(ctx, actor, id, accessWrite)
	if err != nil {
		return rec, err
	}
	active, err := s.Patients.IsActive(ctx, rec.PatientID)
	if err != nil {
		return model.ClinicalRecord{}, unavailable("patient directory", err)
	}
	if !active {
		return model.ClinicalRecord{}, &model.Error{Kind: model.KindRule, Code: "BUSINESS_RULE_VIOLATION", Message: "only active patients can receive new clinical entries"}
	}
	return rec, nil
}

func (s *EntryService) replay(ctx context.Context, actor in.Actor, op, key, hash string) (in.EntryResult, bool, error) {
	saved, found, err := s.Entries.FindIdempotency(ctx, actor.ID, op, key)
	if err != nil || !found {
		return in.EntryResult{}, false, err
	}
	if saved.RequestHash != hash {
		return in.EntryResult{}, true, &model.Error{Kind: model.KindConflict, Code: "IDEMPOTENCY_CONFLICT", Message: "the idempotency key was already used with a different request"}
	}
	entry, err := s.Entries.GetEntry(ctx, saved.ResourceID)
	if err != nil {
		return in.EntryResult{}, true, err
	}
	if _, err := s.record(ctx, actor, entry.RecordID, accessWrite); err != nil {
		return in.EntryResult{}, true, err
	}
	return in.EntryResult{Entry: entry, Replayed: true}, true, nil
}

// create appends the entry under the patient guard. A diagnosis is admitted
// to the encounter of its consultation and an appointment entry to the
// encounter of that appointment's consultation (ADR-017 §5).
func (s *EntryService) create(ctx context.Context, actor in.Actor, rec model.ClinicalRecord, op, key, hash string, e model.Entry, events []out.OutboxEvent) (in.EntryResult, error) {
	idem := out.IdempotencyRecord{
		ActorID: actor.ID, Operation: op, Key: key, RequestHash: hash, ResourceID: e.ID,
		ResponseStatus: statusCreated, CreatedAt: e.CreatedAt, ExpiresAt: e.CreatedAt.Add(idempotencyRetention),
	}
	var encounter *out.EncounterKey
	switch {
	case e.ConsultationID != "":
		encounter = &out.EncounterKey{RecordID: rec.ID, ConsultationID: e.ConsultationID}
	case e.Kind != model.KindConsultation:
		var err error
		if encounter, err = appointmentEncounter(ctx, s.Entries, rec.ID, e.AppointmentID); err != nil {
			return in.EntryResult{}, err
		}
	}
	err := s.Guard.Run(ctx, GuardedWrite{Actor: actor, PatientID: rec.PatientID, Encounter: encounter,
		Commit: func(tagged context.Context) error { return s.Entries.CreateEntry(tagged, e, idem, events) }})
	if errors.Is(err, out.ErrDuplicateIdempotency) {
		res, _, rerr := s.replay(ctx, actor, op, key, hash)
		return res, rerr
	}
	if err != nil {
		return in.EntryResult{}, err
	}
	return in.EntryResult{Entry: e}, nil
}

// events builds the minimized facts of a consultation or diagnosis; no
// clinical narrative ever enters a general-purpose event.
func (s *EntryService) events(cmd in.RecordEntryCommand, rec model.ClinicalRecord, e model.Entry, at time.Time) []out.OutboxEvent {
	payload := map[string]any{"patientId": rec.PatientID, "dentistId": e.AuthorID, "recordedAt": at.UTC().Format(time.RFC3339)}
	var eventType string
	switch e.Kind {
	case model.KindConsultation:
		eventType, payload["consultationId"] = "ConsultationRecorded", e.ID
		if e.AppointmentID != "" {
			payload["appointmentId"] = e.AppointmentID
		}
	case model.KindDiagnosis:
		eventType, payload["diagnosisId"], payload["consultationId"] = "DiagnosisRecorded", e.ID, e.ConsultationID
		if e.Code != "" {
			payload["diagnosisCode"] = e.Code
		}
	default:
		return nil
	}
	return []out.OutboxEvent{{
		EventID: s.IDs.NewID(), EventType: eventType, EventVersion: 1, AggregateID: rec.ID, AggregateType: "ClinicalRecord",
		CorrelationID: cmd.CorrelationID, CausationID: cmd.CorrelationID, ActorID: cmd.Actor.ID, Payload: payload, OccurredAt: at,
	}}
}
