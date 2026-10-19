package httpapi

import (
	"errors"
	"net/http"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// FieldError names one invalid input, including headers such as Idempotency-Key.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type apiError struct {
	Error   string       `json:"error"`
	Message string       `json:"message"`
	Details []FieldError `json:"details,omitempty"`
	TraceID string       `json:"traceId"`
}

// httpError is an error the adapter already knows how to present.
type httpError struct {
	status  int
	code    string
	message string
	details []FieldError
}

func (e *httpError) Error() string { return e.code + ": " + e.message }

// Validation reports invalid input shape: 400 with one detail per field.
func Validation(details ...FieldError) error {
	return &httpError{http.StatusBadRequest, "VALIDATION_ERROR", "the request has invalid fields", details}
}

func unauthorized() error {
	return &httpError{status: http.StatusUnauthorized, code: "UNAUTHORIZED", message: "authentication is required"}
}

func notFound() error {
	return &httpError{status: http.StatusNotFound, code: "NOT_FOUND", message: "the requested resource does not exist"}
}

func methodNotAllowed() error {
	return &httpError{status: http.StatusMethodNotAllowed, code: "METHOD_NOT_ALLOWED", message: "the method is not allowed for this resource"}
}

// WriteError is the single place where an error becomes an HTTP status and
// envelope. Anything unrecognized is a neutral 500; its detail goes only to
// the log, with the request's traceId.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	he := translate(err)
	if he == nil {
		loggerFrom(r.Context()).Error("unhandled error", "error", err.Error())
		he = &httpError{status: http.StatusInternalServerError, code: "INTERNAL_ERROR", message: "an unexpected error occurred"}
	}
	if he.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="di-lucca"`)
	}
	writeJSON(w, he.status, apiError{
		Error:   he.code,
		Message: he.message,
		Details: he.details,
		TraceID: correlationFrom(r.Context()),
	})
}

// domainStatus maps each domain error kind to its HTTP status. This table is
// the only place where the domain meets HTTP (Anexo C).
var domainStatus = map[model.Kind]int{
	model.KindInvalidInput: http.StatusBadRequest,
	model.KindRule:         http.StatusUnprocessableEntity,
	model.KindTransition:   http.StatusUnprocessableEntity,
	model.KindStaleVersion: http.StatusConflict,
	model.KindConflict:     http.StatusConflict,
	model.KindNotFound:     http.StatusNotFound,
	model.KindForbidden:    http.StatusForbidden,
}

// translate returns the presentable form of err, or nil for an unexpected error.
func translate(err error) *httpError {
	var he *httpError
	if errors.As(err, &he) {
		return he
	}
	var de *model.Error
	if errors.As(err, &de) {
		if status, ok := domainStatus[de.Kind]; ok {
			if de.Kind == model.KindInvalidInput {
				return Validation(FieldError{Field: de.Field, Message: de.Message}).(*httpError)
			}
			return &httpError{status: status, code: de.Code, message: de.Message}
		}
	}
	if errors.Is(err, out.ErrUnavailable) {
		return &httpError{status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", message: "the operation is temporarily unavailable; retry later"}
	}
	return nil
}
