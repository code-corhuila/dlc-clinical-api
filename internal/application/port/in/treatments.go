package in

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// PlannedProcedureInput is one procedure of a plan; codes refer to the
// Billing catalog and never carry money.
type PlannedProcedureInput struct {
	Code          string
	AppointmentID string
}

// PlanTreatmentCommand plans a treatment for a patient.
type PlanTreatmentCommand struct {
	Actor          Actor
	PatientID      string
	DiagnosisID    string
	ClinicalReason string
	Procedures     []PlannedProcedureInput
	IdempotencyKey string
	RequestHash    string
	CorrelationID  string
}

// StartTreatmentCommand moves a PLANNED treatment to IN_PROGRESS.
type StartTreatmentCommand struct {
	Actor           Actor
	TreatmentID     string
	ExpectedVersion int
	IdempotencyKey  string
	RequestHash     string
}

// TreatmentResult tells whether the result was produced now or replayed.
type TreatmentResult struct {
	Treatment model.Treatment
	Replayed  bool
}

// ListTreatmentsQuery lists visible treatments, optionally for one patient.
type ListTreatmentsQuery struct {
	Actor     Actor
	PatientID string
	Page      PageRequest
}

// Treatments are the HU-CLN-001 treatment operations.
type Treatments interface {
	Plan(ctx context.Context, cmd PlanTreatmentCommand) (TreatmentResult, error)
	Start(ctx context.Context, cmd StartTreatmentCommand) (TreatmentResult, error)
	List(ctx context.Context, q ListTreatmentsQuery) (Page[model.Treatment], error)
	Get(ctx context.Context, actor Actor, id string) (model.Treatment, error)
}
