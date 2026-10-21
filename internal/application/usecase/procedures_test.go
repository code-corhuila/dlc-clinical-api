package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

type fakeProcedures struct {
	items   map[string]model.Procedure
	idem    map[string]out.IdempotencyRecord
	sources map[string]bool
	extras  []out.RecordedExtra
	outbox  []out.OutboxEvent
}

func (r *fakeProcedures) FindIdempotency(_ context.Context, a, o, k string) (out.IdempotencyRecord, bool, error) {
	rec, ok := r.idem[scope(a, o, k)]
	return rec, ok, nil
}

func (r *fakeProcedures) GetProcedure(_ context.Context, id string) (model.Procedure, error) {
	p, ok := r.items[id]
	if !ok {
		return model.Procedure{}, out.ErrNotFound
	}
	return p, nil
}

func (r *fakeProcedures) CompleteProcedure(_ context.Context, p model.Procedure, from int, ex []out.RecordedExtra, idem out.IdempotencyRecord, ev []out.OutboxEvent) error {
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	if r.items[p.ID].Version != from {
		return out.ErrVersionConflict
	}
	for _, e := range ex {
		if r.sources[e.SourceRecordID] {
			return out.ErrDuplicateSource
		}
	}
	for _, e := range ex {
		r.sources[e.SourceRecordID] = true
	}
	r.items[p.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = p, idem
	r.extras, r.outbox = append(r.extras, ex...), append(r.outbox, ev...)
	return nil
}

type fakeAppointments struct {
	inAttention bool
	err         error
}

func (a *fakeAppointments) InAttention(context.Context, string, string) (bool, error) {
	return a.inAttention, a.err
}

type procedureFixture struct {
	treatmentFixture
	procs     *fakeProcedures
	apts      *fakeAppointments
	svc       *ProcedureService
	procedure string
	treatment string
}

func setupProcedures(t *testing.T, start bool) procedureFixture {
	tf := setupTreatments(t)
	planned, err := tf.plan(dentist, "pat-1", "pf-plan-01")
	if err != nil {
		t.Fatal(err)
	}
	if start {
		if _, err := tf.svc.Start(context.Background(), in.StartTreatmentCommand{Actor: dentist, TreatmentID: planned.Treatment.ID, ExpectedVersion: 1, IdempotencyKey: "pf-start-01", RequestHash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	procs := &fakeProcedures{items: map[string]model.Procedure{}, idem: map[string]out.IdempotencyRecord{}, sources: map[string]bool{}}
	for _, p := range planned.Treatment.Procedures {
		procs.items[p.ID] = p
	}
	pf := procedureFixture{treatmentFixture: tf, procs: procs, apts: &fakeAppointments{inAttention: true},
		procedure: planned.Treatment.Procedures[0].ID, treatment: planned.Treatment.ID}
	pf.svc = &ProcedureService{Procedures: procs, Treatments: tf.repo, Care: tf.care, Patients: tf.patients, Appointments: pf.apts, Clock: tf.svc.Clock, IDs: tf.svc.IDs}
	return pf
}

func extra(src string, typ model.ExtraType) model.ExtraInput {
	return model.ExtraInput{SourceRecordID: src, Type: typ, Code: "X-1", Quantity: "2", Description: "Resina", ClinicalReason: "Fractura"}
}

func (pf procedureFixture) complete(actor in.Actor, key string, mut ...func(*in.CompleteProcedureCommand)) (in.ProcedureCompletionResult, error) {
	cmd := in.CompleteProcedureCommand{Actor: actor, ProcedureID: pf.procedure, ExpectedVersion: 1, IdempotencyKey: key, RequestHash: "h-" + key, CorrelationID: "corr-p",
		MaterialsUsed:          []model.ExtraInput{extra("src-m1", model.ExtraAdditionalMaterial)},
		AdditionalRequirements: []model.ExtraInput{extra("src-r1", model.ExtraComplexityAdjustment)}}
	for _, m := range mut {
		m(&cmd)
	}
	return pf.svc.Complete(context.Background(), cmd)
}

func TestCompleteProcedureRecordsProgressExtrasAndEvent(t *testing.T) {
	pf := setupProcedures(t, true)
	res, err := pf.complete(dentist, "k-done-01")
	if err != nil {
		t.Fatal(err)
	}
	p := res.Procedure
	if p.Status != model.StatusCompleted || p.Version != 2 || p.CompletedBy != "dentist-1" || p.CompletedAt.IsZero() {
		t.Fatalf("procedure = %+v", p)
	}
	if len(pf.procs.extras) != 2 || pf.procs.extras[0].ProcedureID != p.ID || pf.procs.extras[0].AppointmentID != p.AppointmentID || pf.procs.extras[1].AuthorID != "dentist-1" {
		t.Fatalf("extras = %+v", pf.procs.extras)
	}
	ev := pf.procs.outbox[0]
	items := ev.Payload["additionalItems"].([]map[string]any)
	if ev.EventType != "ProcedureCompleted" || ev.AggregateID != pf.treatment || ev.Payload["procedureId"] != p.ID || ev.Payload["patientId"] != "pat-1" ||
		ev.Payload["procedureCode"] != "OPER-01" || len(items) != 2 || items[0]["quantity"] != "2" || items[0]["sourceRecordId"] != "src-m1" {
		t.Fatalf("event = %+v", ev)
	}
	if _, leaked := items[0]["description"]; leaked {
		t.Fatal("the event carries only the v1 additional item fields")
	}
	for _, money := range []string{"price", "unitPrice", "total", "amount"} {
		if _, ok := ev.Payload[money]; ok {
			t.Fatalf("money field %q in a clinical event", money)
		}
	}
}

func TestCompleteProcedureIsExactlyOnce(t *testing.T) {
	pf := setupProcedures(t, true)
	first, _ := pf.complete(dentist, "k-once-01")
	again, err := pf.complete(dentist, "k-once-01")
	if err != nil || !again.Replayed || again.Procedure.Version != first.Procedure.Version || len(pf.procs.outbox) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}
	_, err = pf.complete(dentist, "k-once-02", func(c *in.CompleteProcedureCommand) { c.ExpectedVersion = 2 })
	if kindOf(t, err) != model.KindConflict || err.(*model.Error).Code != "CARE_STATE_CONFLICT" {
		t.Fatalf("a second completion with another key must conflict, got %v", err)
	}
	if _, err := pf.complete(dentist, "k-once-01", func(c *in.CompleteProcedureCommand) { c.RequestHash = "other" }); kindOf(t, err) != model.KindConflict {
		t.Fatalf("got %v", err)
	}
}

func TestCompleteProcedureRules(t *testing.T) {
	cases := map[string]struct {
		start bool
		actor in.Actor
		prep  func(procedureFixture)
		mut   func(*in.CompleteProcedureCommand)
		want  model.Kind
		code  string
	}{
		"assistant":             {true, assistant, nil, nil, model.KindForbidden, "FORBIDDEN"},
		"unassigned dentist":    {true, other, nil, nil, model.KindNotFound, "NOT_FOUND"},
		"missing procedure":     {true, dentist, nil, func(c *in.CompleteProcedureCommand) { c.ProcedureID = "nope" }, model.KindNotFound, "NOT_FOUND"},
		"treatment not started": {false, dentist, nil, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
		"not in attention":      {true, dentist, func(pf procedureFixture) { pf.apts.inAttention = false }, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
		"inactive patient":      {true, dentist, func(pf procedureFixture) { pf.patients.inactive["pat-1"] = true }, nil, model.KindRule, "BUSINESS_RULE_VIOLATION"},
		"stale version":         {true, dentist, nil, func(c *in.CompleteProcedureCommand) { c.ExpectedVersion = 7 }, model.KindStaleVersion, "STALE_VERSION"},
		"invalid extra":         {true, dentist, nil, func(c *in.CompleteProcedureCommand) { c.MaterialsUsed[0].Quantity = "0" }, model.KindInvalidInput, "VALIDATION_ERROR"},
		"repeated source":       {true, dentist, nil, func(c *in.CompleteProcedureCommand) { c.AdditionalRequirements[0].SourceRecordID = "src-m1" }, model.KindInvalidInput, "VALIDATION_ERROR"},
		"source already used":   {true, dentist, func(pf procedureFixture) { pf.procs.sources["src-m1"] = true }, nil, model.KindConflict, "SOURCE_ALREADY_RECORDED"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pf := setupProcedures(t, c.start)
			if c.prep != nil {
				c.prep(pf)
			}
			var muts []func(*in.CompleteProcedureCommand)
			if c.mut != nil {
				muts = append(muts, c.mut)
			}
			_, err := pf.complete(c.actor, "k-rule-01", muts...)
			if kindOf(t, err) != c.want || err.(*model.Error).Code != c.code {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestCompleteProcedureFailsClosed(t *testing.T) {
	pf := setupProcedures(t, true)
	pf.apts.err = out.ErrUnavailable
	if _, err := pf.complete(dentist, "k-down-01"); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("appointment authority unavailable must fail closed, got %v", err)
	}
	if len(pf.procs.outbox) != 0 {
		t.Fatal("nothing may be committed when authority is unknown")
	}
}
