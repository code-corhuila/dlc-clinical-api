// Package in declares what the Clinical service offers: one use case per
// business operation, independent of HTTP.
package in

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// Actor is the authenticated staff member, as verified by the inbound adapter.
type Actor struct {
	ID    string
	Roles []string
	// Clinical grants already resolved from the token by the adapter.
	ClinicalRead  bool
	ClinicalWrite bool
}

// PageRequest asks for one bounded page; zero values take the defaults.
type PageRequest struct {
	Page  int
	Limit int
}

// Page is a bounded slice of results plus the total visible to the actor.
type Page[T any] struct {
	Items []T
	Page  int
	Limit int
	Total int
}

// OpenRecordCommand opens the longitudinal record of a patient. RequestHash
// identifies the payload so a reused key with another body is detected.
type OpenRecordCommand struct {
	Actor          Actor
	PatientID      string
	IdempotencyKey string
	RequestHash    string
}

// OpenRecordResult tells whether the record was created now or replayed.
type OpenRecordResult struct {
	Record   model.ClinicalRecord
	Replayed bool
}

// ListRecordsQuery lists the records the actor may see, optionally for one patient.
type ListRecordsQuery struct {
	Actor     Actor
	PatientID string
	Page      PageRequest
}

// ClinicalRecords are the HU-CLN-001 record operations.
type ClinicalRecords interface {
	Open(ctx context.Context, cmd OpenRecordCommand) (OpenRecordResult, error)
	List(ctx context.Context, q ListRecordsQuery) (Page[model.ClinicalRecord], error)
	Get(ctx context.Context, actor Actor, id string) (model.ClinicalRecord, error)
}
