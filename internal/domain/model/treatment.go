package model

import "time"

// Status is the lifecycle of treatments and procedures.
type Status string

const (
	StatusPlanned    Status = "PLANNED"
	StatusInProgress Status = "IN_PROGRESS"
	StatusCompleted  Status = "COMPLETED"
	StatusCancelled  Status = "CANCELLED"
)

// maxProcedures bounds the embedded array, matching the dlc-clinical-db validator.
const maxProcedures = 100

// Procedure is a planned clinical act. Its code refers to the Billing catalog;
// Clinical never stores money (CLN-006).
type Procedure struct {
	ID            string
	TreatmentID   string
	Code          string
	AppointmentID string
	Status        Status
}

// Treatment groups planned procedures justified by a diagnosis or a reason.
type Treatment struct {
	ID             string
	RecordID       string
	PatientID      string
	DiagnosisID    string
	ClinicalReason string
	Status         Status
	Procedures     []Procedure
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Version        int
}

// PlannedProcedure is one requested procedure in a treatment plan.
type PlannedProcedure struct {
	ID, Code, AppointmentID string
}

// TreatmentInput carries a plan; AuthorID and At are set by the server.
type TreatmentInput struct {
	ID, RecordID, PatientID string
	DiagnosisID             string
	ClinicalReason          string
	Procedures              []PlannedProcedure
	AuthorID                string
	At                      time.Time
}

// PlanTreatment requires a diagnosis or a clinical reason and 1 to 100 procedures.
func PlanTreatment(in TreatmentInput) (Treatment, error) {
	switch {
	case in.DiagnosisID == "" && in.ClinicalReason == "":
		return Treatment{}, invalid("diagnosisId", "diagnosisId or clinicalReason is required")
	case in.ClinicalReason != "" && !textWithin(in.ClinicalReason, 1, 1000):
		return Treatment{}, invalid("clinicalReason", "must have 1 to 1000 characters")
	case len(in.Procedures) == 0 || len(in.Procedures) > maxProcedures:
		return Treatment{}, invalid("procedures", "must contain 1 to 100 items")
	case in.AuthorID == "":
		return Treatment{}, invalid("authorId", "required")
	}
	procs := make([]Procedure, len(in.Procedures))
	for i, p := range in.Procedures {
		if !textWithin(p.Code, 1, 80) {
			return Treatment{}, invalid("procedures.procedureCode", "must have 1 to 80 characters")
		}
		if p.AppointmentID == "" {
			return Treatment{}, invalid("procedures.appointmentId", "required")
		}
		procs[i] = Procedure{ID: p.ID, TreatmentID: in.ID, Code: p.Code, AppointmentID: p.AppointmentID, Status: StatusPlanned}
	}
	return Treatment{
		ID: in.ID, RecordID: in.RecordID, PatientID: in.PatientID, DiagnosisID: in.DiagnosisID,
		ClinicalReason: in.ClinicalReason, Status: StatusPlanned, Procedures: procs,
		CreatedBy: in.AuthorID, CreatedAt: in.At, UpdatedAt: in.At, Version: 1,
	}, nil
}

// Start moves PLANNED to IN_PROGRESS; any other origin is an invalid transition.
func (t *Treatment) Start(expectedVersion int, at time.Time) error {
	if expectedVersion != t.Version {
		return staleVersion()
	}
	if t.Status != StatusPlanned {
		return transition("only a PLANNED treatment can start")
	}
	t.Status = StatusInProgress
	t.UpdatedAt = at
	t.Version++
	return nil
}
