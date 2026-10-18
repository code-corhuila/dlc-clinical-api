// Package model holds the Clinical domain: entities, value objects and
// invariants. It imports nothing from the web, database or messaging layers.
package model

// Kind classifies a domain error; the HTTP adapter maps each kind to a status
// in a single place. The domain never knows what a 422 is.
type Kind int

const (
	KindInvalidInput Kind = iota + 1 // input shape is wrong
	KindRule                         // a business invariant forbids it
	KindTransition                   // the lifecycle forbids it
	KindStaleVersion                 // expectedVersion is not current
	KindNotFound                     // the resource does not exist or is out of scope
	KindForbidden                    // the actor may not perform it
	KindConflict                     // the current state conflicts (duplicate, closed encounter)
)

// Error is a typed domain error. Field names the offending input, when any.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Field   string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func invalid(field, message string) error {
	return &Error{Kind: KindInvalidInput, Code: "VALIDATION_ERROR", Message: message, Field: field}
}

func rule(message string) error {
	return &Error{Kind: KindRule, Code: "BUSINESS_RULE_VIOLATION", Message: message}
}

func transition(message string) error {
	return &Error{Kind: KindTransition, Code: "INVALID_STATUS_TRANSITION", Message: message}
}

func staleVersion() error {
	return &Error{Kind: KindStaleVersion, Code: "STALE_VERSION", Message: "the resource has changed; reload it before submitting a new operation"}
}

// textWithin reports whether s has between min and max characters (not bytes).
func textWithin(s string, min, max int) bool {
	n := len([]rune(s))
	return n >= min && n <= max
}
