package usecase

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

type fakeEntries struct {
	entries map[string]model.Entry
	idem    map[string]out.IdempotencyRecord
	outbox  []out.OutboxEvent
}

func (r *fakeEntries) FindIdempotency(_ context.Context, a, o, k string) (out.IdempotencyRecord, bool, error) {
	rec, ok := r.idem[scope(a, o, k)]
	return rec, ok, nil
}

func (r *fakeEntries) CreateEntry(_ context.Context, e model.Entry, idem out.IdempotencyRecord, ev []out.OutboxEvent) error {
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	r.entries[e.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = e, idem
	r.outbox = append(r.outbox, ev...)
	return nil
}

func (r *fakeEntries) GetEntry(_ context.Context, id string) (model.Entry, error) {
	e, ok := r.entries[id]
	if !ok {
		return model.Entry{}, out.ErrNotFound
	}
	return e, nil
}

func (r *fakeEntries) ListEntries(_ context.Context, recordID string, offset, limit int) ([]model.Entry, int, error) {
	var all []model.Entry
	for _, e := range r.entries {
		if e.RecordID == recordID {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if offset >= len(all) {
		return []model.Entry{}, len(all), nil
	}
	return all[offset:min(offset+limit, len(all))], len(all), nil
}

type entryFixture struct {
	fixture
	entries    *fakeEntries
	encounters *fakeEncounters
	svc        *EntryService
	rec1       string // record of pat-1 (assigned to dentist-1)
	rec3       string // record of pat-3 (not assigned)
}

func setupEntries(t *testing.T) entryFixture {
	f := setup()
	e := &fakeEntries{entries: map[string]model.Entry{}, idem: map[string]out.IdempotencyRecord{}}
	ef := entryFixture{fixture: f, entries: e}
	ef.encounters = &fakeEncounters{closed: map[string]bool{}}
	ef.svc = &EntryService{Records: f.repo, Entries: e, Closures: ef.encounters, Care: f.care, Patients: f.patients, Guard: f.svc.Guard, Clock: f.svc.Clock, IDs: f.svc.IDs}
	r1, err := open(f, admin, "pat-1", "rec-key-1", "h")
	r3, err3 := open(f, admin, "pat-3", "rec-key-3", "h")
	if err != nil || err3 != nil {
		t.Fatal(err, err3)
	}
	ef.rec1, ef.rec3 = r1.Record.ID, r3.Record.ID
	return ef
}

func (ef entryFixture) record(actor in.Actor, rec string, kind model.EntryKind, key string, mut ...func(*in.RecordEntryCommand)) (in.EntryResult, error) {
	cmd := in.RecordEntryCommand{Actor: actor, RecordID: rec, Kind: kind, Text: "nota", AppointmentID: "apt-1", IdempotencyKey: key, RequestHash: "h-" + key, CorrelationID: "corr-1"}
	for _, m := range mut {
		m(&cmd)
	}
	return ef.svc.Record(context.Background(), cmd)
}

func TestRecordEvolutionKeepsServerAuthor(t *testing.T) {
	ef := setupEntries(t)
	res, err := ef.record(dentist, ef.rec1, model.KindEvolution, "k-evo-01")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.AuthorID != "dentist-1" || res.Entry.RecordID != ef.rec1 || res.Entry.Version != 1 || len(ef.entries.outbox) != 0 {
		t.Fatalf("entry = %+v, outbox = %d", res.Entry, len(ef.entries.outbox))
	}
}

func TestConsultationAndDiagnosisPublishEvents(t *testing.T) {
	ef := setupEntries(t)
	cons, err := ef.record(dentist, ef.rec1, model.KindConsultation, "k-cons-01")
	if err != nil {
		t.Fatal(err)
	}
	diag, err := ef.record(dentist, ef.rec1, model.KindDiagnosis, "k-diag-01", func(c *in.RecordEntryCommand) {
		c.ConsultationID, c.Code = cons.Entry.ID, "K02.1"
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ef.entries.outbox) != 2 {
		t.Fatalf("outbox = %+v", ef.entries.outbox)
	}
	c, d := ef.entries.outbox[0], ef.entries.outbox[1]
	if c.EventType != "ConsultationRecorded" || c.EventVersion != 1 || c.AggregateID != ef.rec1 || c.CorrelationID != "corr-1" ||
		c.Payload["consultationId"] != cons.Entry.ID || c.Payload["patientId"] != "pat-1" || c.Payload["dentistId"] != "dentist-1" || c.Payload["appointmentId"] != "apt-1" {
		t.Fatalf("consultation event = %+v", c)
	}
	if d.EventType != "DiagnosisRecorded" || d.Payload["diagnosisId"] != diag.Entry.ID || d.Payload["consultationId"] != cons.Entry.ID || d.Payload["diagnosisCode"] != "K02.1" {
		t.Fatalf("diagnosis event = %+v", d)
	}
	if _, ok := c.Payload["text"]; ok {
		t.Fatal("clinical narrative must never enter events")
	}
}

func TestDiagnosisRequiresConsultationOfSameRecord(t *testing.T) {
	ef := setupEntries(t)
	foreign, _ := ef.record(admin, ef.rec3, model.KindConsultation, "k-cons-03")
	for name, id := range map[string]string{"other record": foreign.Entry.ID, "missing": "nope"} {
		t.Run(name, func(t *testing.T) {
			_, err := ef.record(dentist, ef.rec1, model.KindDiagnosis, "k-diag-"+name, func(c *in.RecordEntryCommand) { c.ConsultationID = id })
			if kindOf(t, err) != model.KindRule {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRecordEntryAccessRules(t *testing.T) {
	cases := map[string]struct {
		actor in.Actor
		rec   func(entryFixture) string
		prep  func(entryFixture)
		want  model.Kind
	}{
		"assistant":          {assistant, func(ef entryFixture) string { return ef.rec1 }, nil, model.KindForbidden},
		"unassigned dentist": {dentist, func(ef entryFixture) string { return ef.rec3 }, nil, model.KindNotFound},
		"missing record":     {dentist, func(entryFixture) string { return "nope" }, nil, model.KindNotFound},
		"inactive patient":   {dentist, func(ef entryFixture) string { return ef.rec1 }, func(ef entryFixture) { ef.patients.inactive["pat-1"] = true }, model.KindRule},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ef := setupEntries(t)
			if c.prep != nil {
				c.prep(ef)
			}
			if _, err := ef.record(c.actor, c.rec(ef), model.KindEvolution, "k-rule-01"); kindOf(t, err) != c.want {
				t.Fatalf("got %v", err)
			}
		})
	}
	ef := setupEntries(t)
	ef.care.err = out.ErrUnavailable
	if _, err := ef.record(dentist, ef.rec1, model.KindEvolution, "k-down-01"); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("must fail closed, got %v", err)
	}
}

func TestRecordEntryIdempotency(t *testing.T) {
	ef := setupEntries(t)
	first, _ := ef.record(dentist, ef.rec1, model.KindConsultation, "k-same-01")
	again, err := ef.record(dentist, ef.rec1, model.KindConsultation, "k-same-01")
	if err != nil || !again.Replayed || again.Entry.ID != first.Entry.ID || len(ef.entries.entries) != 1 || len(ef.entries.outbox) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}
	_, err = ef.record(dentist, ef.rec1, model.KindConsultation, "k-same-01", func(c *in.RecordEntryCommand) { c.RequestHash = "other" })
	if kindOf(t, err) != model.KindConflict {
		t.Fatalf("got %v", err)
	}
}

func TestAmendEntryPreservesOriginal(t *testing.T) {
	ef := setupEntries(t)
	orig, _ := ef.record(dentist, ef.rec1, model.KindConsultation, "k-orig-01")
	amend := func(version int, key string) (in.EntryResult, error) {
		return ef.svc.Amend(context.Background(), in.AmendEntryCommand{Actor: dentist, EntryID: orig.Entry.ID, Text: "corregida", Reason: "pieza", ExpectedVersion: version, IdempotencyKey: key, RequestHash: "h"})
	}
	res, err := amend(1, "k-amend-01")
	if err != nil || res.Entry.AmendsEntryID != orig.Entry.ID || res.Entry.AuthorID != "dentist-1" || ef.entries.entries[orig.Entry.ID].Text != "nota" {
		t.Fatalf("amendment = %+v %v", res.Entry, err)
	}
	if _, err := amend(2, "k-amend-02"); kindOf(t, err) != model.KindStaleVersion {
		t.Fatalf("got %v", err)
	}
	if _, err := ef.svc.Amend(context.Background(), in.AmendEntryCommand{Actor: dentist, EntryID: "nope", Text: "x", Reason: "r", ExpectedVersion: 1, IdempotencyKey: "k-amend-03"}); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
}

func TestListEntriesIsScopedAndPaged(t *testing.T) {
	ef := setupEntries(t)
	for _, k := range []string{"k-l-01", "k-l-02", "k-l-03"} {
		_, _ = ef.record(dentist, ef.rec1, model.KindEvolution, k)
	}
	page, err := ef.svc.List(context.Background(), dentist, ef.rec1, in.PageRequest{Limit: 2})
	if err != nil || page.Total != 3 || len(page.Items) != 2 || !page.Items[0].CreatedAt.After(page.Items[1].CreatedAt) {
		t.Fatalf("page = %+v %v", page, err)
	}
	if _, err := ef.svc.List(context.Background(), dentist, ef.rec3, in.PageRequest{}); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("parent authorization first, got %v", err)
	}
	if _, err := ef.svc.List(context.Background(), dentist, ef.rec1, in.PageRequest{Limit: 101}); kindOf(t, err) != model.KindInvalidInput {
		t.Fatalf("got %v", err)
	}
}
