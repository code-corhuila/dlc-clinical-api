package httpapi

import (
	"fmt"
	"net/http"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// procedureCompletionResponse is the ProcedureCompletion schema (ADR-014):
// recorded progress only, no closure and no money.
type procedureCompletionResponse struct {
	ProcedureID   string `json:"procedureId"`
	TreatmentID   string `json:"treatmentId"`
	AppointmentID string `json:"appointmentId"`
	CompletedAt   string `json:"completedAt"`
	Version       int    `json:"version"`
}

// extraRequest is one item of materialsUsed or additionalRequirements.
// quantity is a JSON string: a number is rejected, never parsed as float.
type extraRequest struct {
	SourceRecordID string `json:"sourceRecordId"`
	Type           string `json:"type"`
	Code           string `json:"code,omitempty"`
	Quantity       string `json:"quantity"`
	Description    string `json:"description"`
	ClinicalReason string `json:"clinicalReason"`
}

// ProcedureRoutes exposes POST /api/v1/procedures/{id}/completions.
func ProcedureRoutes(svc in.ProcedureCompletions) []Route {
	h := procedureHandlers{svc: svc}
	return []Route{{Pattern: "POST /api/v1/procedures/{id}/completions", Handler: h.complete}}
}

type procedureHandlers struct{ svc in.ProcedureCompletions }

func toExtras(field string, items []extraRequest, details []FieldError) ([]model.ExtraInput, []FieldError) {
	out := make([]model.ExtraInput, 0, len(items))
	for i, it := range items {
		if !isUUID(it.SourceRecordID) {
			details = append(details, FieldError{Field: fmt.Sprintf("%s[%d].sourceRecordId", field, i), Message: "must be a UUID"})
		}
		out = append(out, model.ExtraInput{SourceRecordID: it.SourceRecordID, Type: model.ExtraType(it.Type), Code: it.Code,
			Quantity: it.Quantity, Description: it.Description, ClinicalReason: it.ClinicalReason})
	}
	return out, details
}

// complete answers 200 for the first completion and for a retried one.
func (h procedureHandlers) complete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion        *int           `json:"expectedVersion"`
		MaterialsUsed          []extraRequest `json:"materialsUsed,omitempty"`
		AdditionalRequirements []extraRequest `json:"additionalRequirements,omitempty"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	id, key, details := writeHeaderChecks(r)
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		details = append(details, FieldError{Field: "expectedVersion", Message: "required, integer from 1"})
	}
	materials, details := toExtras("materialsUsed", body.MaterialsUsed, details)
	requirements, details := toExtras("additionalRequirements", body.AdditionalRequirements, details)
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Complete(r.Context(), in.CompleteProcedureCommand{
		Actor: actorFrom(r), ProcedureID: id, ExpectedVersion: *body.ExpectedVersion,
		MaterialsUsed: materials, AdditionalRequirements: requirements,
		IdempotencyKey: key, RequestHash: requestHash(body), CorrelationID: correlationFrom(r.Context()),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	p := res.Procedure
	writeJSON(w, http.StatusOK, procedureCompletionResponse{ProcedureID: p.ID, TreatmentID: p.TreatmentID, AppointmentID: p.AppointmentID,
		CompletedAt: rfc3339(p.CompletedAt), Version: p.Version})
}
