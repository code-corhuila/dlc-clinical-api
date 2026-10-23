package model

import (
	"strings"
	"testing"
	"time"
)

func declaration() DeclarationInput {
	return DeclarationInput{ClosureID: "cc-1", RecordID: "r-1", ConsultationID: "c-1", AppointmentID: "a-1", PatientID: "p-1", DentistID: "d-1",
		ProcedureIDs: []string{"p-2", "p-1"}, ManualChargeIDs: []string{"m-1"}, ClinicalVersion: 2, CorrelationID: "corr", At: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)}
}

func TestDeclareCareCompletionFixesTheSourceSet(t *testing.T) {
	c, err := DeclareCareCompletion(declaration())
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != ClosureAwaitingFinalization || c.AppointmentOutcome != OutcomePending || c.BillingOutcome != OutcomePending || c.Version != 1 {
		t.Fatalf("closure = %+v", c)
	}
	if strings.Join(c.ProcedureIDs, ",") != "p-1,p-2" {
		t.Fatalf("source set must be sorted for a stable fingerprint: %v", c.ProcedureIDs)
	}
	in := declaration()
	in.ProcedureIDs[0] = "changed"
	if c.ProcedureIDs[1] != "p-2" {
		t.Fatal("the declared set must be a copy, never shared with the caller")
	}
}

func TestDeclarationRules(t *testing.T) {
	cases := map[string]func(*DeclarationInput){
		"no consultation":     func(i *DeclarationInput) { i.ConsultationID = "" },
		"no appointment":      func(i *DeclarationInput) { i.AppointmentID = "" },
		"no dentist":          func(i *DeclarationInput) { i.DentistID = "" },
		"duplicate procedure": func(i *DeclarationInput) { i.ProcedureIDs = []string{"p-1", "p-1"} },
		"too many charges":    func(i *DeclarationInput) { i.ManualChargeIDs = make([]string, 101) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := declaration()
			mut(&in)
			if _, err := DeclareCareCompletion(in); kindOf(t, err) != KindInvalidInput {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestClosureCompletesOnlyWithBothOutcomesInAnyOrder(t *testing.T) {
	at := time.Now()
	billingFirst, _ := DeclareCareCompletion(declaration())
	billingFirst.RecordBillingCompleted(at)
	if billingFirst.Status != ClosureAwaitingFinalization {
		t.Fatalf("waiting for the administrative finalization is the normal state, got %s", billingFirst.Status)
	}
	billingFirst.RecordAppointmentCompleted(at)
	if billingFirst.Status != ClosureCompleted || billingFirst.Version != 3 {
		t.Fatalf("got %+v", billingFirst)
	}

	appointmentFirst, _ := DeclareCareCompletion(declaration())
	appointmentFirst.RecordAppointmentCompleted(at)
	if appointmentFirst.Status != ClosurePending {
		t.Fatalf("got %s", appointmentFirst.Status)
	}
	appointmentFirst.RecordAppointmentCompleted(at)
	if appointmentFirst.Version != 2 {
		t.Fatal("a repeated outcome is idempotent")
	}
	appointmentFirst.RecordBillingCompleted(at)
	if appointmentFirst.Status != ClosureCompleted {
		t.Fatalf("got %s", appointmentFirst.Status)
	}
}

func TestClosureRetryResumesOnlyPendingOrFailed(t *testing.T) {
	at := time.Now()
	awaiting, _ := DeclareCareCompletion(declaration())
	if err := awaiting.Retry(1, at); kindOf(t, err) != KindConflict {
		t.Fatal("waiting for administrative finalization is never a retry condition (ADR-014)")
	}

	failed, _ := DeclareCareCompletion(declaration())
	failed.MarkFailed("billing snapshot rejected", at)
	if failed.Status != ClosureFailed || failed.FailureReason == "" || failed.Version != 2 {
		t.Fatalf("failed = %+v", failed)
	}
	if kindOf(t, failed.Retry(1, at)) != KindStaleVersion {
		t.Fatal("stale version must be rejected")
	}
	if err := failed.Retry(2, at); err != nil {
		t.Fatal(err)
	}
	if failed.Status != ClosureAwaitingFinalization || failed.FailureReason != "" || failed.Version != 3 {
		t.Fatalf("a retried closure resumes from its recorded outcomes: %+v", failed)
	}

	pending, _ := DeclareCareCompletion(declaration())
	pending.RecordAppointmentCompleted(at)
	if err := pending.Retry(2, at); err != nil || pending.Status != ClosurePending || pending.AppointmentOutcome != OutcomeCompleted {
		t.Fatalf("a retry never undoes a recorded outcome: %+v %v", pending, err)
	}

	done, _ := DeclareCareCompletion(declaration())
	done.RecordAppointmentCompleted(at)
	done.RecordBillingCompleted(at)
	if kindOf(t, done.Retry(3, at)) != KindConflict {
		t.Fatal("a completed closure cannot be retried")
	}
	long := strings.Repeat("x", 600)
	other, _ := DeclareCareCompletion(declaration())
	other.MarkFailed(long, at)
	if len([]rune(other.FailureReason)) != 500 {
		t.Fatal("the safe failure reason is bounded by the validator")
	}
}
