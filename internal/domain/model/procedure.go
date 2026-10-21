package model

import (
	"regexp"
	"time"
)

// ExtraType classifies a clinically justified extra (HU-CLN-002).
type ExtraType string

const (
	ExtraAdditionalMaterial   ExtraType = "ADDITIONAL_MATERIAL"
	ExtraAdditionalProcedure  ExtraType = "ADDITIONAL_PROCEDURE"
	ExtraComplexityAdjustment ExtraType = "COMPLEXITY_ADJUSTMENT"
	ExtraOtherAuthorized      ExtraType = "OTHER_AUTHORIZED_CHARGE"
)

// quantityPattern is the exact positive decimal of clinical-service.yaml:
// at most four fractional digits, zero rejected, never floating point.
var quantityPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,4})?$`)
var zeroPattern = regexp.MustCompile(`^0(?:\.0{1,4})?$`)

// Extra is a material or unforeseen need recorded by Clinical without any
// price (CLN-006); Billing alone prices it from its source record id.
type Extra struct {
	SourceRecordID string
	Type           ExtraType
	Code           string
	Quantity       string
	Description    string
	ClinicalReason string
}

// ExtraInput carries one extra from the completion request.
type ExtraInput = Extra

// NewExtra validates an extra (CLN-008: justified, traceable by source id).
func NewExtra(in ExtraInput) (Extra, error) {
	switch {
	case in.SourceRecordID == "":
		return Extra{}, invalid("sourceRecordId", "required")
	case in.Type != ExtraAdditionalMaterial && in.Type != ExtraAdditionalProcedure && in.Type != ExtraComplexityAdjustment && in.Type != ExtraOtherAuthorized:
		return Extra{}, invalid("type", "must be ADDITIONAL_MATERIAL, ADDITIONAL_PROCEDURE, COMPLEXITY_ADJUSTMENT or OTHER_AUTHORIZED_CHARGE")
	case in.Code != "" && !textWithin(in.Code, 1, 80):
		return Extra{}, invalid("code", "must have 1 to 80 characters")
	case !quantityPattern.MatchString(in.Quantity) || zeroPattern.MatchString(in.Quantity):
		return Extra{}, invalid("quantity", "must be a positive decimal with at most four fractional digits")
	case !textWithin(in.Description, 1, 500):
		return Extra{}, invalid("description", "must have 1 to 500 characters")
	case !textWithin(in.ClinicalReason, 1, 1000):
		return Extra{}, invalid("clinicalReason", "must have 1 to 1000 characters")
	}
	return in, nil
}

// Complete records the individual completion of a planned procedure. It never
// completes the appointment or the encounter (APT-009, ADR-014).
func (p *Procedure) Complete(expectedVersion int, by string, at time.Time) error {
	if expectedVersion != p.Version {
		return staleVersion()
	}
	if p.Status != StatusPlanned && p.Status != StatusInProgress {
		return &Error{Kind: KindConflict, Code: "CARE_STATE_CONFLICT", Message: "the current care state does not allow procedure completion"}
	}
	p.Status, p.CompletedBy, p.CompletedAt = StatusCompleted, by, at
	p.Version++
	return nil
}
