package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func (tf treatmentFixture) start(actor in.Actor, id, key string) (in.TreatmentResult, error) {
	return tf.svc.Start(context.Background(), in.StartTreatmentCommand{Actor: actor, TreatmentID: id, ExpectedVersion: 1, IdempotencyKey: key, RequestHash: "h-" + key})
}

func TestStartTreatmentRules(t *testing.T) {
	cases := map[string]struct {
		actor in.Actor
		prep  func(treatmentFixture)
		kind  model.Kind
		down  bool
	}{
		"assistant":                {assistant, nil, model.KindForbidden, false},
		"unassigned dentist":       {other, nil, model.KindNotFound, false},
		"patient deactivated":      {dentist, func(tf treatmentFixture) { tf.patients.inactive["pat-1"] = true }, model.KindRule, false},
		"patients unavailable":     {dentist, func(tf treatmentFixture) { tf.patients.err = out.ErrUnavailable }, 0, true},
		"appointments unavailable": {dentist, func(tf treatmentFixture) { tf.care.err = out.ErrUnavailable }, 0, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tf := setupTreatments(t)
			planned, err := tf.plan(dentist, "pat-1", "k-sr-plan")
			if err != nil {
				t.Fatal(err)
			}
			if c.prep != nil {
				c.prep(tf)
			}
			_, err = tf.start(c.actor, planned.Treatment.ID, "k-sr-start")
			if c.down {
				if !errors.Is(err, out.ErrUnavailable) {
					t.Fatalf("an unavailable owner fails closed: %v", err)
				}
			} else if kindOf(t, err) != c.kind {
				t.Fatalf("got %v", err)
			}
			if got, _ := tf.repo.GetTreatment(context.Background(), planned.Treatment.ID); got.Status != model.StatusPlanned {
				t.Fatal("a rejected start leaves the plan unchanged")
			}
		})
	}
}

func TestReadTreatmentRules(t *testing.T) {
	tf := setupTreatments(t)
	planned, _ := tf.plan(dentist, "pat-1", "k-rr-plan")
	ctx := context.Background()
	if _, err := tf.svc.Get(ctx, assistant, planned.Treatment.ID); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("the assistant never reads clinical data: %v", err)
	}
	if _, err := tf.svc.Get(ctx, dentist, "missing"); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	foreign, err := tf.svc.List(ctx, in.ListTreatmentsQuery{Actor: dentist, PatientID: "pat-3"})
	if err != nil || foreign.Total != 0 || len(foreign.Items) != 0 {
		t.Fatalf("an unassigned patient filter answers an empty page: %+v %v", foreign, err)
	}
	if _, err := tf.svc.List(ctx, in.ListTreatmentsQuery{Actor: dentist, Page: in.PageRequest{Limit: 101}}); kindOf(t, err) != model.KindInvalidInput {
		t.Fatalf("got %v", err)
	}
	tf.care.err = out.ErrUnavailable
	if _, err := tf.svc.List(ctx, in.ListTreatmentsQuery{Actor: dentist}); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("the dentist scope fails closed: %v", err)
	}
	if _, err := tf.svc.Get(ctx, dentist, planned.Treatment.ID); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("and so does a single read: %v", err)
	}
}
