package httpapi

// ErrorResponse is the transport envelope required by the published API contract. Handlers will
// use it once Clinical routes are added; domain errors remain transport-independent.
type ErrorResponse struct {
	Error   string        `json:"error"`
	Message string        `json:"message"`
	Details []FieldDetail `json:"details,omitempty"`
	TraceID string        `json:"traceId,omitempty"`
}

type FieldDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
