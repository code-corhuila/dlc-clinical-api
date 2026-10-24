package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidPattern.MatchString(s) }

// DecodeJSON reads exactly one JSON object into dst. Malformed bodies,
// unknown fields and trailing data are validation errors, never 500s.
func DecodeJSON(_ http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return Validation(bodyFieldError(err))
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Validation(FieldError{Field: "body", Message: "must contain a single JSON object"})
	}
	return nil
}

func bodyFieldError(err error) FieldError {
	var typeErr *json.UnmarshalTypeError
	var maxErr *http.MaxBytesError
	msg := err.Error()
	switch {
	case errors.Is(err, io.EOF):
		return FieldError{Field: "body", Message: "required"}
	case errors.As(err, &maxErr):
		return FieldError{Field: "body", Message: "too large"}
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return FieldError{Field: typeErr.Field, Message: "has the wrong type"}
	case strings.HasPrefix(msg, `json: unknown field "`):
		return FieldError{Field: strings.TrimSuffix(strings.TrimPrefix(msg, `json: unknown field "`), `"`), Message: "is not allowed"}
	default:
		return FieldError{Field: "body", Message: "malformed JSON"}
	}
}
