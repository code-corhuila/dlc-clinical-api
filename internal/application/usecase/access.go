// Package usecase implements one use case per Clinical business operation,
// depending only on ports and the domain.
package usecase

import (
	"context"
	"fmt"
	"slices"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// Staff roles issued by Auth (authentication.md).
const (
	RoleAdministrator = "ADMINISTRATOR"
	RoleDentist       = "DENTIST"
	RoleAssistant     = "SECRETARY_ASSISTANT"
)

// Explicit clinical permissions an Administrator needs besides the role
// (PAT-003, design-decisions.md). Names are an implementation choice pending
// the Auth permission catalogue.
const (
	PermClinicalRead  = "clinical:read"
	PermClinicalWrite = "clinical:write"
)

type access int

const (
	accessRead access = iota
	accessWrite
)

func forbidden() error {
	return &model.Error{Kind: model.KindForbidden, Code: "FORBIDDEN", Message: "you are not permitted to perform this action"}
}

func notFound() error {
	return &model.Error{Kind: model.KindNotFound, Code: "NOT_FOUND", Message: "the requested resource does not exist"}
}

func unavailable(what string, err error) error {
	return fmt.Errorf("%s: %v: %w", what, err, out.ErrUnavailable)
}

// clinicalAdmin reports whether the actor is an Administrator holding the
// clinical permission for the requested access.
func clinicalAdmin(a in.Actor, mode access) bool {
	if !slices.Contains(a.Roles, RoleAdministrator) {
		return false
	}
	perm := PermClinicalRead
	if mode == accessWrite {
		perm = PermClinicalWrite
	}
	return slices.Contains(a.Permissions, perm)
}

// mayUseClinical rejects actors whose role can never touch clinical data;
// the Assistant never reads or writes narrative (PAT-003).
func mayUseClinical(a in.Actor, mode access) error {
	if slices.Contains(a.Roles, RoleDentist) || clinicalAdmin(a, mode) {
		return nil
	}
	return forbidden()
}

// authorizePatient checks the current care relationship with Appointments.
// Unavailable authority fails closed; no stale projection is trusted.
func authorizePatient(ctx context.Context, care out.CareAuthority, a in.Actor, patientID string, mode access) error {
	if err := mayUseClinical(a, mode); err != nil {
		return err
	}
	if clinicalAdmin(a, mode) {
		return nil
	}
	ok, err := care.IsAssigned(ctx, patientID, a.ID)
	if err != nil {
		return unavailable("care authority", err)
	}
	if !ok {
		return forbidden()
	}
	return nil
}

// pageWindow validates page (from 1) and limit (1 to 100, default 20).
func pageWindow(p in.PageRequest) (page, limit int, err error) {
	page, limit = p.Page, p.Limit
	if page == 0 {
		page = 1
	}
	if limit == 0 {
		limit = 20
	}
	switch {
	case page < 1:
		return 0, 0, &model.Error{Kind: model.KindInvalidInput, Code: "VALIDATION_ERROR", Message: "must be 1 or greater", Field: "page"}
	case limit < 1 || limit > 100:
		return 0, 0, &model.Error{Kind: model.KindInvalidInput, Code: "VALIDATION_ERROR", Message: "must be between 1 and 100", Field: "limit"}
	}
	return page, limit, nil
}
