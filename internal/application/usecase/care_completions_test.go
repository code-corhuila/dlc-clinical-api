package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

type fakeClosures struct {
	records   *fakeRepo
	items     map[string]model.CareClosure
	idem      map[string]out.IdempotencyRecord
	outbox    []out.OutboxEvent
	completed []string
	retried   int
}

func (r *fakeClosures) FindIdempotency(_ context.Context, a, o, k string) (out.IdempotencyRecord, bool, error) {
	rec, ok := r.idem[scope(a, o, k)]
	return rec, ok, nil
}

func (r *fakeClosures) GetClosure(_ context.Context, id string) (model.CareClosure, error) {
	c, ok := r.items[id]
	if !ok {
		return model.CareClosure{}, out.ErrNotFound
	}
	return c, nil
}

func (r *fakeClosures) FindClosureByEncounter(_ context.Context, rec, cons string) (model.CareClosure, bool, error) {
	for _, c := range r.items {
		if c.RecordID == rec && c.ConsultationID == cons {
			return c, true, nil
		}
	}
	return model.CareClosure{}, false, nil
}

func (r *fakeClosures) CompletedProcedureIDs(context.Context, string, string) ([]string, error) {
	return r.completed, nil
}

func (r *fakeClosures) DeclareCareCompletion(_ context.Context, c model.CareClosure, from int, idem out.IdempotencyRecord, ev []out.OutboxEvent) error {
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	rec := r.records.records[c.RecordID]
	if rec.Version != from {
		return out.ErrVersionConflict
	}
	rec.Version = c.ClinicalVersion
	r.records.records[c.RecordID] = rec
	r.items[c.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = c, idem
	r.outbox = append(r.outbox, ev...)
	return nil
}

type fakeBilling struct {
	charges []string
	err     error
}

func (b *fakeBilling) EncounterManualCharges(context.Context, string, string, string) ([]string, error) {
	return b.charges, b.err
}

type fakeGate struct{ err error }

func (g *fakeGate) Quiesce(context.Context, string, string, string) error { return g.err }

type closureFixture struct {
	entryFixture
	closures     *fakeClosures
	billing      *fakeBilling
	gate         *fakeGate
	appointments *fakeAppointments
	svc          *CareCompletionService
	consultation string
}

func setupClosures(t *testing.T) closureFixture {
	ef := setupEntries(t)
	cons, err := ef.record(dentist, ef.rec1, model.KindConsultation, "cc-cons-01")
	if err != nil {
		t.Fatal(err)
	}
	cf := closureFixture{entryFixture: ef, consultation: cons.Entry.ID,
		closures:     &fakeClosures{records: ef.repo, items: map[string]model.CareClosure{}, idem: map[string]out.IdempotencyRecord{}, completed: []string{"proc-2", "proc-1"}},
		billing:      &fakeBilling{charges: []string{"charge-1"}},
		gate:         &fakeGate{},
		appointments: &fakeAppointments{inAttention: true}}
	cf.svc = &CareCompletionService{Records: ef.repo, Entries: ef.entries, Closures: cf.closures, Care: ef.care, Patients: ef.patients,
		Appointments: cf.appointments, Billing: cf.billing, Gate: cf.gate, Clock: ef.svc.Clock, IDs: ef.svc.IDs}
	return cf
}

func (cf closureFixture) declare(actor in.Actor, key string, mut ...func(*in.DeclareCareCompletionCommand)) (in.CareClosureResult, error) {
	cmd := in.DeclareCareCompletionCommand{Actor: actor, RecordID: cf.rec1, ConsultationID: cf.consultation, AppointmentID: "apt-1",
		ExpectedVersion: 1, IdempotencyKey: key, RequestHash: "h-" + key, CorrelationID: "corr-cc"}
	for _, m := range mut {
		m(&cmd)
	}
	return cf.svc.Declare(context.Background(), cmd)
}

func TestDeclareCareCompletionCreatesOneClosurePerEncounter(t *testing.T) {
	cf := setupClosures(t)
	res, err := cf.declare(dentist, "k-decl-01")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Closure
	if c.Status != model.ClosureAwaitingFinalization || c.DentistID != "dentist-1" || c.PatientID != "pat-1" || c.ClinicalVersion != 2 ||
		len(c.ProcedureIDs) != 2 || c.ProcedureIDs[0] != "proc-1" || len(c.ManualChargeIDs) != 1 {
		t.Fatalf("closure = %+v", c)
	}
	if cf.repo.records[cf.rec1].Version != 2 {
		t.Fatal("the declaration raises the clinical record version")
	}
	ev := cf.closures.outbox[0]
	if ev.EventType != "ClinicalCareCompleted" || ev.AggregateType != "ClinicalRecord" || ev.AggregateID != cf.rec1 ||
		ev.Payload["consultationId"] != cf.consultation || ev.Payload["clinicalVersion"] != 2 || len(ev.Payload) != 9 {
		t.Fatalf("event = %+v", ev)
	}
	again, err := cf.declare(dentist, "k-decl-01")
	if err != nil || !again.Replayed || again.Closure.ID != c.ID || len(cf.closures.outbox) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}
	_, err = cf.declare(dentist, "k-decl-02", func(c *in.DeclareCareCompletionCommand) { c.ExpectedVersion = 2 })
	if kindOf(t, err) != model.KindConflict || err.(*model.Error).Code != "CARE_STATE_CONFLICT" {
		t.Fatalf("a second declaration of the encounter must conflict, got %v", err)
	}
}

func TestDeclareCareCompletionRules(t *testing.T) {
	cases := map[string]struct {
		actor in.Actor
		prep  func(closureFixture)
		mut   func(*in.DeclareCareCompletionCommand)
		want  model.Kind
		code  string
	}{
		"administrator":      {admin, nil, nil, model.KindForbidden, "FORBIDDEN"},
		"assistant":          {assistant, nil, nil, model.KindForbidden, "FORBIDDEN"},
		"unassigned dentist": {other, nil, nil, model.KindNotFound, "NOT_FOUND"},
		"stale record":       {dentist, nil, func(c *in.DeclareCareCompletionCommand) { c.ExpectedVersion = 9 }, model.KindStaleVersion, "STALE_VERSION"},
		"not a consultation": {dentist, nil, func(c *in.DeclareCareCompletionCommand) { c.ConsultationID = "nope" }, model.KindRule, "BUSINESS_RULE_VIOLATION"},
		"other encounter":    {dentist, nil, func(c *in.DeclareCareCompletionCommand) { c.AppointmentID = "apt-9" }, model.KindRule, "BUSINESS_RULE_VIOLATION"},
		"not in attention":   {dentist, func(cf closureFixture) { cf.appointments.inAttention = false }, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
		"inactive patient":   {dentist, func(cf closureFixture) { cf.patients.inactive["pat-1"] = true }, nil, model.KindRule, "BUSINESS_RULE_VIOLATION"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			if c.prep != nil {
				c.prep(cf)
			}
			var muts []func(*in.DeclareCareCompletionCommand)
			if c.mut != nil {
				muts = append(muts, c.mut)
			}
			_, err := cf.declare(c.actor, "k-rule-01", muts...)
			if kindOf(t, err) != c.want || err.(*model.Error).Code != c.code {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestDeclarationFailsClosedWithoutGateOrBilling(t *testing.T) {
	for name, prep := range map[string]func(closureFixture){
		"write gate unverified": func(cf closureFixture) { cf.gate.err = out.ErrUnavailable },
		"billing unavailable":   func(cf closureFixture) { cf.billing.err = out.ErrUnavailable },
		"appointments down":     func(cf closureFixture) { cf.appointments.err = out.ErrUnavailable },
	} {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			prep(cf)
			if _, err := cf.declare(dentist, "k-down-01"); !errors.Is(err, out.ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
			if len(cf.closures.items) != 0 || len(cf.closures.outbox) != 0 {
				t.Fatal("no declaration may be committed when an owner cannot confirm")
			}
		})
	}
}

func TestGetClosureIsScoped(t *testing.T) {
	cf := setupClosures(t)
	res, _ := cf.declare(dentist, "k-decl-01")
	if got, err := cf.svc.GetClosure(context.Background(), dentist, res.Closure.ID); err != nil || got.ID != res.Closure.ID {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := cf.svc.GetClosure(context.Background(), other, res.Closure.ID); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := cf.svc.GetClosure(context.Background(), dentist, "missing"); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := cf.svc.GetClosure(context.Background(), assistant, res.Closure.ID); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("got %v", err)
	}
}

func (r *fakeClosures) RetryClosure(_ context.Context, c model.CareClosure, from int, idem out.IdempotencyRecord) error {
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	if r.items[c.ID].Version != from {
		return out.ErrVersionConflict
	}
	r.items[c.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = c, idem
	r.retried++
	return nil
}

func (cf closureFixture) failClosure(t *testing.T) model.CareClosure {
	t.Helper()
	res, err := cf.declare(dentist, "k-decl-01")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Closure
	c.MarkFailed("billing intervention required", c.UpdatedAt)
	cf.closures.items[c.ID] = c
	return c
}

func (cf closureFixture) retry(actor in.Actor, id string, version int, key string) (in.CareClosureResult, error) {
	return cf.svc.Retry(context.Background(), in.RetryClosureCommand{Actor: actor, ClosureID: id, Reason: "precio corregido en Billing", ExpectedVersion: version, IdempotencyKey: key, RequestHash: "h-" + key})
}

func TestRetryClosureResumesTheSameProcess(t *testing.T) {
	cf := setupClosures(t)
	failed := cf.failClosure(t)
	res, err := cf.retry(dentist, failed.ID, 2, "k-retry-01")
	if err != nil || res.Closure.Status != model.ClosureAwaitingFinalization || res.Closure.FailureReason != "" || res.Closure.Version != 3 || cf.closures.retried != 1 {
		t.Fatalf("retry = %+v %v", res, err)
	}
	if again, err := cf.retry(dentist, failed.ID, 2, "k-retry-01"); err != nil || !again.Replayed || cf.closures.retried != 1 {
		t.Fatalf("a repeated retry returns the saved result: %+v %v", again, err)
	}
	if len(cf.closures.outbox) != 1 {
		t.Fatal("a retry never creates a new declaration event")
	}
	if _, err := cf.retry(dentist, failed.ID, 3, "k-retry-02"); kindOf(t, err) != model.KindConflict {
		t.Fatalf("an awaiting closure cannot be retried, got %v", err)
	}
}

func TestRetryClosureRules(t *testing.T) {
	cases := map[string]struct {
		actor   in.Actor
		version int
		reason  string
		want    model.Kind
	}{
		"assistant":          {assistant, 2, "r", model.KindForbidden},
		"unassigned dentist": {other, 2, "r", model.KindNotFound},
		"stale":              {dentist, 1, "r", model.KindStaleVersion},
		"no reason":          {dentist, 2, "", model.KindInvalidInput},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			failed := cf.failClosure(t)
			_, err := cf.svc.Retry(context.Background(), in.RetryClosureCommand{Actor: c.actor, ClosureID: failed.ID, Reason: c.reason, ExpectedVersion: c.version, IdempotencyKey: "k-r-01", RequestHash: "h"})
			if kindOf(t, err) != c.want {
				t.Fatalf("got %v", err)
			}
		})
	}
	cf := setupClosures(t)
	if _, err := cf.retry(dentist, "missing", 1, "k-r-02"); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
}
