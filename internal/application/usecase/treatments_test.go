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

type fakeTreatments struct {
	items    map[string]model.Treatment
	idem     map[string]out.IdempotencyRecord
	outbox   []out.OutboxEvent
	conflict bool
}

func (r *fakeTreatments) FindIdempotency(_ context.Context, a, o, k string) (out.IdempotencyRecord, bool, error) {
	rec, ok := r.idem[scope(a, o, k)]
	return rec, ok, nil
}

func (r *fakeTreatments) CreateTreatment(_ context.Context, t model.Treatment, idem out.IdempotencyRecord, ev []out.OutboxEvent) error {
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	r.items[t.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = t, idem
	r.outbox = append(r.outbox, ev...)
	return nil
}

func (r *fakeTreatments) UpdateTreatment(_ context.Context, t model.Treatment, from int, idem out.IdempotencyRecord) error {
	if r.conflict || r.items[t.ID].Version != from {
		return out.ErrVersionConflict
	}
	r.items[t.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = t, idem
	return nil
}

func (r *fakeTreatments) GetTreatment(_ context.Context, id string) (model.Treatment, error) {
	t, ok := r.items[id]
	if !ok {
		return model.Treatment{}, out.ErrNotFound
	}
	return t, nil
}

func (r *fakeTreatments) ListTreatments(_ context.Context, f out.RecordFilter) ([]model.Treatment, int, error) {
	var all []model.Treatment
	for _, t := range r.items {
		if f.PatientIDs == nil || contains(f.PatientIDs, t.PatientID) {
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if f.Offset >= len(all) {
		return []model.Treatment{}, len(all), nil
	}
	return all[f.Offset:min(f.Offset+f.Limit, len(all))], len(all), nil
}

type treatmentFixture struct {
	entryFixture
	repo *fakeTreatments
	svc  *TreatmentService
	diag string // diagnosis on pat-1's record
}

func setupTreatments(t *testing.T) treatmentFixture {
	ef := setupEntries(t)
	repo := &fakeTreatments{items: map[string]model.Treatment{}, idem: map[string]out.IdempotencyRecord{}}
	tf := treatmentFixture{entryFixture: ef, repo: repo}
	tf.svc = &TreatmentService{Records: ef.repo, Entries: ef.entries, Treatments: repo, Care: ef.care, Patients: ef.patients, Clock: ef.svc.Clock, IDs: ef.svc.IDs}
	cons, _ := ef.record(dentist, ef.rec1, model.KindConsultation, "t-cons-01")
	diag, err := ef.record(dentist, ef.rec1, model.KindDiagnosis, "t-diag-01", func(c *in.RecordEntryCommand) { c.ConsultationID = cons.Entry.ID })
	if err != nil {
		t.Fatal(err)
	}
	tf.diag = diag.Entry.ID
	ef.entries.outbox = nil
	return tf
}

func (tf treatmentFixture) plan(actor in.Actor, patient, key string, mut ...func(*in.PlanTreatmentCommand)) (in.TreatmentResult, error) {
	cmd := in.PlanTreatmentCommand{Actor: actor, PatientID: patient, DiagnosisID: tf.diag, IdempotencyKey: key, RequestHash: "h-" + key, CorrelationID: "corr-t",
		Procedures: []in.PlannedProcedureInput{{Code: "OPER-01", AppointmentID: "apt-1"}, {Code: "RX-01", AppointmentID: "apt-1"}}}
	for _, m := range mut {
		m(&cmd)
	}
	return tf.svc.Plan(context.Background(), cmd)
}

func TestPlanTreatmentPublishesTreatmentPlanned(t *testing.T) {
	tf := setupTreatments(t)
	res, err := tf.plan(dentist, "pat-1", "k-plan-01")
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Treatment
	if tr.Status != model.StatusPlanned || tr.RecordID != tf.rec1 || len(tr.Procedures) != 2 || tr.Procedures[0].ID == "" || tr.CreatedBy != "dentist-1" {
		t.Fatalf("treatment = %+v", tr)
	}
	if len(tf.repo.outbox) != 1 {
		t.Fatalf("outbox = %+v", tf.repo.outbox)
	}
	ev := tf.repo.outbox[0]
	if ev.EventType != "TreatmentPlanned" || ev.AggregateID != tr.ID || ev.CorrelationID != "corr-t" ||
		ev.Payload["treatmentId"] != tr.ID || ev.Payload["patientId"] != "pat-1" || ev.Payload["dentistId"] != "dentist-1" || ev.Payload["procedureCount"] != 2 || len(ev.Payload) != 5 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestPlanTreatmentRules(t *testing.T) {
	cases := map[string]struct {
		actor   in.Actor
		patient string
		mut     func(*in.PlanTreatmentCommand)
		prep    func(treatmentFixture)
		want    model.Kind
	}{
		"assistant":          {assistant, "pat-1", nil, nil, model.KindForbidden},
		"unassigned dentist": {other, "pat-1", nil, nil, model.KindForbidden},
		"inactive patient":   {dentist, "pat-1", nil, func(tf treatmentFixture) { tf.patients.inactive["pat-1"] = true }, model.KindRule},
		"patient no record":  {dentist, "pat-2", func(c *in.PlanTreatmentCommand) { c.DiagnosisID = ""; c.ClinicalReason = "Control" }, nil, model.KindRule},
		"diagnosis missing":  {dentist, "pat-1", func(c *in.PlanTreatmentCommand) { c.DiagnosisID = "nope" }, nil, model.KindRule},
		"no procedures":      {dentist, "pat-1", func(c *in.PlanTreatmentCommand) { c.Procedures = nil }, nil, model.KindInvalidInput},
		"no reason nor diag": {dentist, "pat-1", func(c *in.PlanTreatmentCommand) { c.DiagnosisID = "" }, nil, model.KindInvalidInput},
		"missing patient":    {dentist, "", nil, nil, model.KindInvalidInput},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tf := setupTreatments(t)
			if c.prep != nil {
				c.prep(tf)
			}
			var muts []func(*in.PlanTreatmentCommand)
			if c.mut != nil {
				muts = append(muts, c.mut)
			}
			if _, err := tf.plan(c.actor, c.patient, "k-rule-01", muts...); kindOf(t, err) != c.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPlanTreatmentDiagnosisMustBeADiagnosisOfTheRecord(t *testing.T) {
	tf := setupTreatments(t)
	foreignCons, _ := tf.record(admin, tf.rec3, model.KindConsultation, "t-fc-01")
	foreign, _ := tf.record(admin, tf.rec3, model.KindDiagnosis, "t-fd-01", func(c *in.RecordEntryCommand) { c.ConsultationID = foreignCons.Entry.ID })
	evo, _ := tf.record(dentist, tf.rec1, model.KindEvolution, "t-evo-01")
	for name, id := range map[string]string{"other record": foreign.Entry.ID, "not a diagnosis": evo.Entry.ID} {
		t.Run(name, func(t *testing.T) {
			_, err := tf.plan(dentist, "pat-1", "k-d-"+name, func(c *in.PlanTreatmentCommand) { c.DiagnosisID = id })
			if kindOf(t, err) != model.KindRule {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPlanTreatmentIdempotencyAndFailClosed(t *testing.T) {
	tf := setupTreatments(t)
	first, _ := tf.plan(dentist, "pat-1", "k-same-01")
	again, err := tf.plan(dentist, "pat-1", "k-same-01")
	if err != nil || !again.Replayed || again.Treatment.ID != first.Treatment.ID || len(tf.repo.items) != 1 || len(tf.repo.outbox) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}
	if _, err := tf.plan(dentist, "pat-1", "k-same-01", func(c *in.PlanTreatmentCommand) { c.RequestHash = "x" }); kindOf(t, err) != model.KindConflict {
		t.Fatalf("got %v", err)
	}
	tf.care.err = out.ErrUnavailable
	if _, err := tf.plan(dentist, "pat-1", "k-down-01"); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestStartTreatment(t *testing.T) {
	tf := setupTreatments(t)
	planned, _ := tf.plan(dentist, "pat-1", "k-plan-01")
	start := func(version int, key string) (in.TreatmentResult, error) {
		return tf.svc.Start(context.Background(), in.StartTreatmentCommand{Actor: dentist, TreatmentID: planned.Treatment.ID, ExpectedVersion: version, IdempotencyKey: key, RequestHash: "h"})
	}
	res, err := start(1, "k-start-01")
	if err != nil || res.Treatment.Status != model.StatusInProgress || res.Treatment.Version != 2 {
		t.Fatalf("start = %+v %v", res, err)
	}
	replay, err := start(1, "k-start-01")
	if err != nil || !replay.Replayed || replay.Treatment.Status != model.StatusInProgress {
		t.Fatalf("a retried start returns the saved result: %+v %v", replay, err)
	}
	if _, err := start(2, "k-start-02"); kindOf(t, err) != model.KindTransition {
		t.Fatalf("got %v", err)
	}
	if _, err := start(1, "k-start-03"); kindOf(t, err) != model.KindStaleVersion {
		t.Fatalf("got %v", err)
	}
	fresh, _ := tf.plan(dentist, "pat-1", "k-plan-02")
	tf.repo.conflict = true
	_, err = tf.svc.Start(context.Background(), in.StartTreatmentCommand{Actor: dentist, TreatmentID: fresh.Treatment.ID, ExpectedVersion: 1, IdempotencyKey: "k-start-04", RequestHash: "h"})
	if kindOf(t, err) != model.KindStaleVersion {
		t.Fatalf("a concurrent write must surface as STALE_VERSION, got %v", err)
	}
}

func TestListAndGetTreatmentsAreScoped(t *testing.T) {
	tf := setupTreatments(t)
	mine, _ := tf.plan(dentist, "pat-1", "k-mine-01")
	foreignCons, _ := tf.record(admin, tf.rec3, model.KindConsultation, "t-fc-02")
	foreignDiag, _ := tf.record(admin, tf.rec3, model.KindDiagnosis, "t-fd-02", func(c *in.RecordEntryCommand) { c.ConsultationID = foreignCons.Entry.ID })
	foreign, err := tf.plan(admin, "pat-3", "k-foreign-01", func(c *in.PlanTreatmentCommand) { c.DiagnosisID = foreignDiag.Entry.ID })
	if err != nil {
		t.Fatal(err)
	}
	page, err := tf.svc.List(context.Background(), in.ListTreatmentsQuery{Actor: dentist})
	if err != nil || page.Total != 1 || page.Items[0].ID != mine.Treatment.ID {
		t.Fatalf("page = %+v %v", page, err)
	}
	if _, err := tf.svc.Get(context.Background(), dentist, foreign.Treatment.ID); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	if got, err := tf.svc.Get(context.Background(), admin, foreign.Treatment.ID); err != nil || got.ID != foreign.Treatment.ID {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := tf.svc.List(context.Background(), in.ListTreatmentsQuery{Actor: assistant}); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("got %v", err)
	}
}
