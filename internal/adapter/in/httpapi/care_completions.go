package httpapi

import (
	"net/http"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

const closuresPath = "/api/v1/care-closures"

// careCompletionResponse is the ClinicalCareCompletion schema; the
// declaration and its encounter closure share one identifier (ADR-014).
type careCompletionResponse struct {
	ID              string   `json:"id"`
	CareClosureID   string   `json:"careClosureId"`
	ClinicalRecord  string   `json:"clinicalRecordId"`
	ConsultationID  string   `json:"consultationId"`
	AppointmentID   string   `json:"appointmentId"`
	PatientID       string   `json:"patientId"`
	DentistID       string   `json:"dentistId"`
	ProcedureIDs    []string `json:"procedureIds"`
	ManualChargeIDs []string `json:"manualChargeIds"`
	CompletedAt     string   `json:"completedAt"`
	ClinicalVersion int      `json:"clinicalVersion"`
}

// careClosureResponse is the CareClosure schema; it never exposes failure
// details beyond the safe reason (Norma 5.8.3).
type careClosureResponse struct {
	ID                 string   `json:"id"`
	ClinicalRecordID   string   `json:"clinicalRecordId"`
	ConsultationID     string   `json:"consultationId"`
	ProcedureIDs       []string `json:"procedureIds"`
	ManualChargeIDs    []string `json:"manualChargeIds"`
	AppointmentID      string   `json:"appointmentId"`
	Status             string   `json:"status"`
	AppointmentOutcome string   `json:"appointmentOutcome"`
	BillingOutcome     string   `json:"billingOutcome"`
	FailureReason      string   `json:"failureReason,omitempty"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
	Version            int      `json:"version"`
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// CareCompletionRoutes exposes the Dentist declaration and the closure status.
func CareCompletionRoutes(svc in.CareCompletions) []Route {
	h := careHandlers{svc: svc}
	return []Route{
		{Pattern: "POST " + recordsPath + "/{id}/care-completions", Handler: h.declare},
		{Pattern: "GET " + closuresPath + "/{id}", Handler: h.get},
	}
}

type careHandlers struct{ svc in.CareCompletions }

// declare rejects any client-supplied source set: the server derives it.
func (h careHandlers) declare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ConsultationID  string `json:"consultationId"`
		AppointmentID   string `json:"appointmentId"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	recordID, key, details := writeHeaderChecks(r)
	for _, f := range []struct{ name, value string }{{"consultationId", body.ConsultationID}, {"appointmentId", body.AppointmentID}} {
		if !isUUID(f.value) {
			details = append(details, FieldError{Field: f.name, Message: "required UUID"})
		}
	}
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		details = append(details, FieldError{Field: "expectedVersion", Message: "required, integer from 1"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Declare(r.Context(), in.DeclareCareCompletionCommand{
		Actor: actorFrom(r), RecordID: recordID, ConsultationID: body.ConsultationID, AppointmentID: body.AppointmentID,
		ExpectedVersion: *body.ExpectedVersion, IdempotencyKey: key, RequestHash: requestHash(body), CorrelationID: correlationFrom(r.Context()),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	c := res.Closure
	w.Header().Set("Location", closuresPath+"/"+c.ID)
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, careCompletionResponse{ID: c.ID, CareClosureID: c.ID, ClinicalRecord: c.RecordID, ConsultationID: c.ConsultationID,
		AppointmentID: c.AppointmentID, PatientID: c.PatientID, DentistID: c.DentistID, ProcedureIDs: nonNil(c.ProcedureIDs),
		ManualChargeIDs: nonNil(c.ManualChargeIDs), CompletedAt: rfc3339(c.CompletedAt), ClinicalVersion: c.ClinicalVersion})
}

func (h careHandlers) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		WriteError(w, r, Validation(FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	c, err := h.svc.GetClosure(r.Context(), actorFrom(r), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toClosureResponse(c))
}

func toClosureResponse(c model.CareClosure) careClosureResponse {
	return careClosureResponse{ID: c.ID, ClinicalRecordID: c.RecordID, ConsultationID: c.ConsultationID, ProcedureIDs: nonNil(c.ProcedureIDs),
		ManualChargeIDs: nonNil(c.ManualChargeIDs), AppointmentID: c.AppointmentID, Status: string(c.Status),
		AppointmentOutcome: string(c.AppointmentOutcome), BillingOutcome: string(c.BillingOutcome), FailureReason: c.FailureReason,
		CreatedAt: rfc3339(c.CreatedAt), UpdatedAt: rfc3339(c.UpdatedAt), Version: c.Version}
}
