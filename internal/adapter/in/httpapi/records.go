package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

const recordsPath = "/api/v1/clinical-records"

// recordResponse is the ClinicalRecord schema of clinical-service.yaml; the
// domain entity is never serialized directly (Norma 5.3.4).
type recordResponse struct {
	ID        string `json:"id"`
	PatientID string `json:"patientId"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Version   int    `json:"version"`
}

type pageMeta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

type pageResponse[T any] struct {
	Data []T      `json:"data"`
	Meta pageMeta `json:"meta"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func toRecordResponse(r model.ClinicalRecord) recordResponse {
	return recordResponse{ID: r.ID, PatientID: r.PatientID, CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt), Version: r.Version}
}

// RecordRoutes exposes the clinical record operations of clinical-service.yaml.
func RecordRoutes(svc in.ClinicalRecords) []Route {
	h := recordHandlers{svc: svc}
	return []Route{
		{Pattern: "POST " + recordsPath, Handler: h.open},
		{Pattern: "GET " + recordsPath, Handler: h.list},
		{Pattern: "GET " + recordsPath + "/{id}", Handler: h.get},
	}
}

type recordHandlers struct{ svc in.ClinicalRecords }

func actorFrom(r *http.Request) in.Actor {
	p, _ := PrincipalFrom(r.Context())
	return in.Actor{ID: p.Subject, Roles: p.Roles, Permissions: p.Permissions}
}

func (h recordHandlers) open(w http.ResponseWriter, r *http.Request) {
	key, keyErr := idempotencyKey(r)
	var body struct {
		PatientID string `json:"patientId"`
	}
	if err := DecodeJSON(w, r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	var details []FieldError
	if keyErr != nil {
		details = append(details, *keyErr)
	}
	if !isUUID(body.PatientID) {
		details = append(details, FieldError{Field: "patientId", Message: "must be a UUID"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.Open(r.Context(), in.OpenRecordCommand{
		Actor: actorFrom(r), PatientID: body.PatientID, IdempotencyKey: key, RequestHash: requestHash(body),
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", recordsPath+"/"+res.Record.ID)
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, toRecordResponse(res.Record))
}

func (h recordHandlers) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, details := pageQuery(q, "patientId")
	patient := q.Get("patientId")
	if q.Has("patientId") && !isUUID(patient) {
		details = append(details, FieldError{Field: "patientId", Message: "must be a UUID"})
	}
	if len(details) > 0 {
		WriteError(w, r, Validation(details...))
		return
	}
	res, err := h.svc.List(r.Context(), in.ListRecordsQuery{Actor: actorFrom(r), PatientID: patient, Page: page})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	data := make([]recordResponse, 0, len(res.Items))
	for _, rec := range res.Items {
		data = append(data, toRecordResponse(rec))
	}
	writeJSON(w, http.StatusOK, pageResponse[recordResponse]{Data: data, Meta: meta(res.Page, res.Limit, res.Total)})
}

func (h recordHandlers) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		WriteError(w, r, Validation(FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	rec, err := h.svc.Get(r.Context(), actorFrom(r), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRecordResponse(rec))
}

// idempotencyKey requires the Idempotency-Key header with 8 to 128 characters (Norma 5.3.8).
func idempotencyKey(r *http.Request) (string, *FieldError) {
	key := r.Header.Get("Idempotency-Key")
	if n := len(key); n < 8 || n > 128 {
		return "", &FieldError{Field: "Idempotency-Key", Message: "header required, 8 to 128 characters"}
	}
	return key, nil
}

// requestHash fingerprints the decoded body so a reused key with another
// payload is detected regardless of whitespace or field order.
func requestHash(body any) string {
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// pageQuery parses page/limit and rejects unknown filters (Anexo C: 400).
func pageQuery(q url.Values, filters ...string) (in.PageRequest, []FieldError) {
	allowed := map[string]bool{"page": true, "limit": true}
	for _, f := range filters {
		allowed[f] = true
	}
	var details []FieldError
	for name := range q {
		if !allowed[name] {
			details = append(details, FieldError{Field: name, Message: "unknown filter"})
		}
	}
	number := func(name string) int {
		if !q.Has(name) {
			return 0
		}
		n, err := strconv.Atoi(q.Get(name))
		if err != nil || n < 1 || (name == "limit" && n > 100) {
			msg := "must be an integer from 1"
			if name == "limit" {
				msg = "must be an integer from 1 to 100"
			}
			details = append(details, FieldError{Field: name, Message: msg})
		}
		return n
	}
	return in.PageRequest{Page: number("page"), Limit: number("limit")}, details
}

func meta(page, limit, total int) pageMeta {
	return pageMeta{Page: page, Limit: limit, Total: total, TotalPages: (total + limit - 1) / limit}
}
