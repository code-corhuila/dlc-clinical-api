package model

import (
	"strings"
	"testing"
	"time"
)

func extraInput() ExtraInput {
	return ExtraInput{SourceRecordID: "src-1", Type: ExtraAdditionalMaterial, Code: "RES-1", Quantity: "1.5", Description: "Resina", ClinicalReason: "Fractura"}
}

func TestNewExtraValidatesClinicalJustificationWithoutMoney(t *testing.T) {
	if _, err := NewExtra(extraInput()); err != nil {
		t.Fatalf("valid extra rejected: %v", err)
	}
	cases := map[string]func(*ExtraInput){
		"no source":       func(i *ExtraInput) { i.SourceRecordID = "" },
		"unknown type":    func(i *ExtraInput) { i.Type = "MATERIAL" },
		"zero quantity":   func(i *ExtraInput) { i.Quantity = "0.0000" },
		"five decimals":   func(i *ExtraInput) { i.Quantity = "1.00001" },
		"negative":        func(i *ExtraInput) { i.Quantity = "-1" },
		"leading zero":    func(i *ExtraInput) { i.Quantity = "01" },
		"no description":  func(i *ExtraInput) { i.Description = "" },
		"description 501": func(i *ExtraInput) { i.Description = strings.Repeat("d", 501) },
		"no reason":       func(i *ExtraInput) { i.ClinicalReason = "" },
		"reason 1001":     func(i *ExtraInput) { i.ClinicalReason = strings.Repeat("r", 1001) },
		"code 81":         func(i *ExtraInput) { i.Code = strings.Repeat("c", 81) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := extraInput()
			mut(&in)
			if _, err := NewExtra(in); kindOf(t, err) != KindInvalidInput {
				t.Fatal("expected invalid input")
			}
		})
	}
	for _, q := range []string{"1", "0.5", "12.3456", "100"} {
		in := extraInput()
		in.Quantity = q
		if _, err := NewExtra(in); err != nil {
			t.Fatalf("quantity %q must be accepted: %v", q, err)
		}
	}
}

func procedure() Procedure {
	return Procedure{ID: "p-1", TreatmentID: "t-1", RecordID: "r-1", PatientID: "pat-1", Code: "OPER", AppointmentID: "a-1", Status: StatusPlanned, Version: 1}
}

func TestCompleteProcedureOnce(t *testing.T) {
	p := procedure()
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if err := p.Complete(1, "dentist-1", at); err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusCompleted || p.Version != 2 || p.CompletedBy != "dentist-1" || !p.CompletedAt.Equal(at) {
		t.Fatalf("procedure = %+v", p)
	}
	err := p.Complete(2, "dentist-1", at)
	if kindOf(t, err) != KindConflict || err.(*Error).Code != "CARE_STATE_CONFLICT" {
		t.Fatalf("a completed procedure cannot complete again, got %v", err)
	}
	fresh := procedure()
	if kindOf(t, fresh.Complete(3, "dentist-1", at)) != KindStaleVersion {
		t.Fatal("stale version must be rejected")
	}
	cancelled := procedure()
	cancelled.Status = StatusCancelled
	if kindOf(t, cancelled.Complete(1, "dentist-1", at)) != KindConflict {
		t.Fatal("a cancelled procedure cannot complete")
	}
}
