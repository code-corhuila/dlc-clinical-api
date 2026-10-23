package model

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
	author = "dentist-1"
)

func kindOf(t *testing.T, err error) Kind {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("expected a domain error, got %v", err)
	}
	return de.Kind
}

func TestOpenRecordRequiresPatientAndActor(t *testing.T) {
	r, err := OpenRecord("rec-1", "pat-1", author, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 1 || r.CreatedBy != author || !r.CreatedAt.Equal(now) || !r.UpdatedAt.Equal(now) {
		t.Fatalf("record = %+v", r)
	}
	if _, err := OpenRecord("rec-1", "", author, now); kindOf(t, err) != KindInvalidInput {
		t.Fatal("patient is required")
	}
}

func consultation(t *testing.T) Entry {
	t.Helper()
	e, err := NewEntry(EntryInput{ID: "c-1", RecordID: "rec-1", Kind: KindConsultation, Text: "Dolor en 36", AppointmentID: "apt-1", AuthorID: author, At: now})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNewEntryKeepsServerAuthorAndTime(t *testing.T) {
	e := consultation(t)
	if e.AuthorID != author || !e.CreatedAt.Equal(now) || e.Version != 1 || e.AmendsEntryID != "" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestNewEntryValidatesShape(t *testing.T) {
	base := EntryInput{ID: "e-1", RecordID: "rec-1", Kind: KindEvolution, Text: "ok", AuthorID: author, At: now}
	cases := map[string]func(*EntryInput){
		"unknown kind":           func(i *EntryInput) { i.Kind = "NOTE" },
		"empty text":             func(i *EntryInput) { i.Text = "" },
		"text over 10000":        func(i *EntryInput) { i.Text = strings.Repeat("a", 10001) },
		"code over 80":           func(i *EntryInput) { i.Code = strings.Repeat("c", 81) },
		"diagnosis without cons": func(i *EntryInput) { i.Kind = KindDiagnosis },
		"missing author":         func(i *EntryInput) { i.AuthorID = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := base
			mut(&in)
			if _, err := NewEntry(in); kindOf(t, err) != KindInvalidInput {
				t.Fatalf("expected invalid input")
			}
		})
	}
}

func TestTextLimitCountsCharactersNotBytes(t *testing.T) {
	in := EntryInput{ID: "e-1", RecordID: "rec-1", Kind: KindAllergy, Text: strings.Repeat("ñ", 10000), AuthorID: author, At: now}
	if _, err := NewEntry(in); err != nil {
		t.Fatalf("10000 characters must be accepted: %v", err)
	}
}

func TestDiagnosisMustReferenceConsultationOfSameRecordAndEncounter(t *testing.T) {
	cons := consultation(t)
	diag := EntryInput{ID: "d-1", RecordID: "rec-1", Kind: KindDiagnosis, Text: "Caries", Code: "K02.1", AppointmentID: "apt-1", ConsultationID: "c-1", AuthorID: author, At: now}
	if err := CheckDiagnosisConsultation(diag, cons); err != nil {
		t.Fatalf("matching consultation rejected: %v", err)
	}

	other := cons
	other.RecordID = "rec-2"
	if kindOf(t, CheckDiagnosisConsultation(diag, other)) != KindRule {
		t.Fatal("another record's consultation must be rejected")
	}
	otherVisit := diag
	otherVisit.AppointmentID = "apt-9"
	if kindOf(t, CheckDiagnosisConsultation(otherVisit, cons)) != KindRule {
		t.Fatal("another encounter must be rejected")
	}
	notCons := cons
	notCons.Kind = KindEvolution
	if kindOf(t, CheckDiagnosisConsultation(diag, notCons)) != KindRule {
		t.Fatal("the reference must be a consultation")
	}
	wrongID := diag
	wrongID.ConsultationID = "c-404"
	if kindOf(t, CheckDiagnosisConsultation(wrongID, cons)) != KindRule {
		t.Fatal("the consultation id must match the loaded consultation")
	}
}

func TestAmendPreservesOriginalAndLinksCorrection(t *testing.T) {
	orig := consultation(t)
	later := now.Add(time.Hour)
	fix, err := orig.Amend(Amendment{ID: "c-2", Text: "Dolor en 46", Reason: "Pieza equivocada", ExpectedVersion: 1, AuthorID: "dentist-2", At: later})
	if err != nil {
		t.Fatal(err)
	}
	if orig.Text != "Dolor en 36" || orig.Version != 1 {
		t.Fatal("original must not change")
	}
	if fix.AmendsEntryID != "c-1" || fix.AmendmentReason != "Pieza equivocada" || fix.Kind != orig.Kind ||
		fix.RecordID != orig.RecordID || fix.AppointmentID != "apt-1" || fix.AuthorID != "dentist-2" || !fix.CreatedAt.Equal(later) {
		t.Fatalf("amendment = %+v", fix)
	}
}

func TestAmendRequiresReasonAndCurrentVersion(t *testing.T) {
	orig := consultation(t)
	if _, err := orig.Amend(Amendment{ID: "c-2", Text: "x", ExpectedVersion: 1, AuthorID: author, At: now}); kindOf(t, err) != KindInvalidInput {
		t.Fatal("reason is mandatory")
	}
	if _, err := orig.Amend(Amendment{ID: "c-2", Text: "x", Reason: "r", ExpectedVersion: 2, AuthorID: author, At: now}); kindOf(t, err) != KindStaleVersion {
		t.Fatal("a stale version must be rejected")
	}
}

func planInput() TreatmentInput {
	return TreatmentInput{
		ID: "t-1", RecordID: "rec-1", PatientID: "pat-1", DiagnosisID: "d-1", AuthorID: author, At: now,
		Procedures: []PlannedProcedure{{ID: "p-1", Code: "OPER-01", AppointmentID: "apt-1"}},
	}
}

func TestPlanTreatment(t *testing.T) {
	tr, err := PlanTreatment(planInput())
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != StatusPlanned || tr.Version != 1 || len(tr.Procedures) != 1 || tr.Procedures[0].Status != StatusPlanned {
		t.Fatalf("treatment = %+v", tr)
	}
}

func TestPlanTreatmentRules(t *testing.T) {
	cases := map[string]func(*TreatmentInput){
		"no diagnosis nor reason": func(i *TreatmentInput) { i.DiagnosisID = "" },
		"no procedures":           func(i *TreatmentInput) { i.Procedures = nil },
		"procedure without code":  func(i *TreatmentInput) { i.Procedures[0].Code = "" },
		"procedure without visit": func(i *TreatmentInput) { i.Procedures[0].AppointmentID = "" },
		"reason over 1000":        func(i *TreatmentInput) { i.DiagnosisID = ""; i.ClinicalReason = strings.Repeat("r", 1001) },
		"over 100 procedures": func(i *TreatmentInput) {
			i.Procedures = make([]PlannedProcedure, 101)
			for n := range i.Procedures {
				i.Procedures[n] = PlannedProcedure{ID: "p", Code: "c", AppointmentID: "a"}
			}
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := planInput()
			in.Procedures = append([]PlannedProcedure(nil), in.Procedures...)
			mut(&in)
			if _, err := PlanTreatment(in); kindOf(t, err) != KindInvalidInput {
				t.Fatal("expected invalid input")
			}
		})
	}
	reasonOnly := planInput()
	reasonOnly.DiagnosisID, reasonOnly.ClinicalReason = "", "Control preventivo"
	if _, err := PlanTreatment(reasonOnly); err != nil {
		t.Fatalf("a clinical reason replaces the diagnosis: %v", err)
	}
}

func TestStartTreatmentTransitions(t *testing.T) {
	tr, _ := PlanTreatment(planInput())
	later := now.Add(time.Minute)
	if err := tr.Start(1, later); err != nil {
		t.Fatal(err)
	}
	if tr.Status != StatusInProgress || tr.Version != 2 || !tr.UpdatedAt.Equal(later) {
		t.Fatalf("treatment = %+v", tr)
	}
	if kindOf(t, tr.Start(2, later)) != KindTransition {
		t.Fatal("starting twice is an invalid transition")
	}
	fresh, _ := PlanTreatment(planInput())
	if kindOf(t, fresh.Start(7, later)) != KindStaleVersion {
		t.Fatal("stale version must be rejected before the transition")
	}
}

func TestOpenRecordAndAmendmentNeedAnAuthor(t *testing.T) {
	if _, err := OpenRecord("rec-1", "pat-1", "", now); kindOf(t, err) != KindInvalidInput {
		t.Fatal("the author of a record always comes from the server")
	}
	orig := consultation(t)
	long := strings.Repeat("x", 10001)
	for name, a := range map[string]Amendment{
		"no author":     {ID: "c-2", Text: "x", Reason: "r", ExpectedVersion: 1, At: now},
		"text too long": {ID: "c-2", Text: long, Reason: "r", ExpectedVersion: 1, AuthorID: author, At: now},
	} {
		_, err := orig.Amend(a)
		if kindOf(t, err) != KindInvalidInput || err.Error() == "" {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}
