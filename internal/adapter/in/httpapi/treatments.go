package httpapi

import (
	"fmt"
	"net/http"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

const treatmentsPath = "/api/v1/treatments"

type procedureResponse struct {
	ID            string `json:"id"`
	ProcedureCode string `json:"procedureCode"`
	AppointmentID string `json:"appointmentId"`
	Status        string `json:"status"`
}

// treatmentResponse is the Treatment schema of clinical-service.yaml.
type treatmentResponse struct {
	ID             string              `json:"id"`
	PatientID      string              `json:"patientId"`
	DiagnosisID    string              `json:"diagnosisId,omitempty"`
	ClinicalReason string              `json:"clinicalReason,omitempty"`
	Status         string              `json:"status"`
	Procedures     []procedureResponse `json:"procedures"`
	CreatedAt      string              `json:"createdAt"`
	UpdatedAt      string              `json:"updatedAt"`
	Version        int                 `json:"version"`
}

func toTreatmentResponse(t model.Treatment) treatmentResponse {
	procs := make([]procedureResponse, 0, len(t.Procedures))
	for _, p := range t.Procedures {
		procs = append(procs, procedureResponse{ID: p.ID, ProcedureCode: p.Code, AppointmentID: p.AppointmentID, Status: string(p.Status)})
	}
	return treatmentResponse{ID: t.ID, PatientID: t.PatientID, DiagnosisID: t.DiagnosisID, ClinicalReason: t.ClinicalReason,
		Status: string(t.Status), Procedures: procs, CreatedAt: rfc3339(t.CreatedAt), UpdatedAt: rfc3339(t.UpdatedAt), Version: t.Version}
}

// TreatmentRoutes exposes the treatment operations of clinical-service.yaml.
func TreatmentRoutes(svc in.Treatments) []Route {
	h := treatmentHandlers{svc: svc}
	return []Route{
		{Pattern: "POST " + treatmentsPath, Handler: h.plan},
		{Pattern: "GET " + treatmentsPath, Handler: h.list},
		{Pattern: "GET " + treatmentsPath + "/{id}", Handler: h.get},
		{Pattern: "POST " + treatmentsPath + "/{id}/starts", Handler: h.start},
	}
}

type treatmentHandlers struct{ svc in.Treatments }

func (h treatmentHandlers) plan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PatientID      string `json:"patientId"`
		DiagnosisID    string `json:"diagnosisId,omitempty"`
		ClinicalReason string `json:"clinicalReason,omitempty"`
		Procedures     []struct {
			ProcedureCode string `json:"procedureCode"`
			AppointmentID string `json:"appointmentId"`
		} `json:"procedures"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	key, keyErr := idempotencyKey(r)
	var details []FieldError
	if keyErr != nil {
		details = append(details, *keyErr)
	}
	if !isUUID(body.PatientID) {
		details = append(details, FieldError{Field: "patientId", Message: "must be a UUID"})
	}
	details = optionalUUID(details, "diagnosisId", body.DiagnosisID)
	procs := make([]in.PlannedProcedureInput, 0, len(body.Procedures))
	for i, p := range body.Procedures {
		if !isUUID(p.AppointmentID) {
			details = append(details, FieldError{Field: fmt.Sprintf("procedures[%d].appointmentId", i), Message: "must be a UUID"})
		}
		procs = append(procs, in.PlannedProcedureInput{Code: p.ProcedureCode, AppointmentID: p.AppointmentID})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Plan(r.Context(), in.PlanTreatmentCommand{
		Actor: actorFrom(r), PatientID: body.PatientID, DiagnosisID: body.DiagnosisID, ClinicalReason: body.ClinicalReason,
		Procedures: procs, IdempotencyKey: key, RequestHash: requestHash(body), CorrelationID: correlationFrom(r.Context()),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", treatmentsPath+"/"+res.Treatment.ID)
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, toTreatmentResponse(res.Treatment))
}

// start answers 200 for the first transition and for a retried one.
func (h treatmentHandlers) start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	id, key, details := writeHeaderChecks(r)
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		details = append(details, FieldError{Field: "expectedVersion", Message: "required, integer from 1"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Start(r.Context(), in.StartTreatmentCommand{
		Actor: actorFrom(r), TreatmentID: id, ExpectedVersion: *body.ExpectedVersion, IdempotencyKey: key, RequestHash: requestHash(body),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTreatmentResponse(res.Treatment))
}

func (h treatmentHandlers) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, details := pageQuery(q, "patientId")
	if q.Has("patientId") && !isUUID(q.Get("patientId")) {
		details = append(details, FieldError{Field: "patientId", Message: "must be a UUID"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.List(r.Context(), in.ListTreatmentsQuery{Actor: actorFrom(r), PatientID: q.Get("patientId"), Page: page})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	data := make([]treatmentResponse, 0, len(res.Items))
	for _, t := range res.Items {
		data = append(data, toTreatmentResponse(t))
	}
	writeJSON(w, http.StatusOK, pageResponse[treatmentResponse]{Data: data, Meta: meta(res.Page, res.Limit, res.Total)})
}

func (h treatmentHandlers) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		WriteError(w, r, Validation(FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	t, err := h.svc.Get(r.Context(), actorFrom(r), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTreatmentResponse(t))
}
