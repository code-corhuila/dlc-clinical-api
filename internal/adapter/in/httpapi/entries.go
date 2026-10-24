package httpapi

import (
	"net/http"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

const entriesPath = "/api/v1/clinical-entries"

// entryResponse is the ClinicalEntry schema of clinical-service.yaml.
type entryResponse struct {
	ID              string `json:"id"`
	RecordID        string `json:"recordId"`
	Kind            string `json:"kind"`
	Text            string `json:"text"`
	Code            string `json:"code,omitempty"`
	AppointmentID   string `json:"appointmentId,omitempty"`
	ConsultationID  string `json:"consultationId,omitempty"`
	AuthorID        string `json:"authorId"`
	AmendsEntryID   string `json:"amendsEntryId,omitempty"`
	AmendmentReason string `json:"amendmentReason,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	Version         int    `json:"version"`
}

func toEntryResponse(e model.Entry) entryResponse {
	return entryResponse{
		ID: e.ID, RecordID: e.RecordID, Kind: string(e.Kind), Text: e.Text, Code: e.Code,
		AppointmentID: e.AppointmentID, ConsultationID: e.ConsultationID, AuthorID: e.AuthorID,
		AmendsEntryID: e.AmendsEntryID, AmendmentReason: e.AmendmentReason,
		CreatedAt: rfc3339(e.CreatedAt), UpdatedAt: rfc3339(e.UpdatedAt), Version: e.Version,
	}
}

// EntryRoutes exposes the clinical entry operations of clinical-service.yaml.
func EntryRoutes(svc in.ClinicalEntries) []Route {
	h := entryHandlers{svc: svc}
	return []Route{
		{Pattern: "POST " + recordsPath + "/{id}/entries", Handler: h.record},
		{Pattern: "GET " + recordsPath + "/{id}/entries", Handler: h.list},
		{Pattern: "POST " + entriesPath + "/{id}/amendments", Handler: h.amend},
	}
}

type entryHandlers struct{ svc in.ClinicalEntries }

// optionalUUID adds a detail when a present identifier is not a UUID.
func optionalUUID(details []FieldError, field, value string) []FieldError {
	if value != "" && !isUUID(value) {
		return append(details, FieldError{Field: field, Message: "must be a UUID"})
	}
	return details
}

// writeHeaderChecks validates the path id and Idempotency-Key of a creation.
func writeHeaderChecks(r *http.Request) (id, key string, details []FieldError) {
	id = r.PathValue("id")
	if !isUUID(id) {
		details = append(details, FieldError{Field: "id", Message: "must be a UUID"})
	}
	key, keyErr := idempotencyKey(r)
	if keyErr != nil {
		details = append(details, *keyErr)
	}
	return id, key, details
}

func (h entryHandlers) record(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind           string `json:"kind"`
		Text           string `json:"text"`
		Code           string `json:"code,omitempty"`
		AppointmentID  string `json:"appointmentId,omitempty"`
		ConsultationID string `json:"consultationId,omitempty"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	recordID, key, details := writeHeaderChecks(r)
	details = optionalUUID(details, "appointmentId", body.AppointmentID)
	details = optionalUUID(details, "consultationId", body.ConsultationID)
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Record(r.Context(), in.RecordEntryCommand{
		Actor: actorFrom(r), RecordID: recordID, Kind: model.EntryKind(body.Kind), Text: body.Text, Code: body.Code,
		AppointmentID: body.AppointmentID, ConsultationID: body.ConsultationID,
		IdempotencyKey: key, RequestHash: requestHash(body), CorrelationID: correlationFrom(r.Context()),
	})
	writeEntryResult(w, r, res, err)
}

func (h entryHandlers) amend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text            string `json:"text"`
		Reason          string `json:"reason"`
		ExpectedVersion *int   `json:"expectedVersion"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	entryID, key, details := writeHeaderChecks(r)
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		details = append(details, FieldError{Field: "expectedVersion", Message: "required, integer from 1"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Amend(r.Context(), in.AmendEntryCommand{
		Actor: actorFrom(r), EntryID: entryID, Text: body.Text, Reason: body.Reason, ExpectedVersion: *body.ExpectedVersion,
		IdempotencyKey: key, RequestHash: requestHash(body),
	})
	writeEntryResult(w, r, res, err)
}

// writeEntryResult answers 201 with Location, or 200 for a replayed key.
func writeEntryResult(w http.ResponseWriter, r *http.Request, res in.EntryResult, err error) {
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", entriesPath+"/"+res.Entry.ID)
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, toEntryResponse(res.Entry))
}

func (h entryHandlers) list(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	page, details := pageQuery(r.URL.Query())
	if !isUUID(recordID) {
		details = append(details, FieldError{Field: "id", Message: "must be a UUID"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.List(r.Context(), actorFrom(r), recordID, page)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	data := make([]entryResponse, 0, len(res.Items))
	for _, e := range res.Items {
		data = append(data, toEntryResponse(e))
	}
	writeJSON(w, http.StatusOK, pageResponse[entryResponse]{Data: data, Meta: meta(res.Page, res.Limit, res.Total)})
}
