// Package authority adapts the Appointments and Patients owner contracts.
package authority

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// Unavailable answers every question with out.ErrUnavailable. It is wired
// while the owner services are not deployed, so clinical access fails closed
// (503) instead of trusting an unverified relationship or patient state.
type Unavailable struct{}

var (
	_ out.CareAuthority    = Unavailable{}
	_ out.PatientDirectory = Unavailable{}
)

func (Unavailable) IsAssigned(context.Context, string, string) (bool, error) {
	return false, out.ErrUnavailable
}

func (Unavailable) AssignedPatients(context.Context, string) ([]string, error) {
	return nil, out.ErrUnavailable
}

func (Unavailable) IsActive(context.Context, string) (bool, error) { return false, out.ErrUnavailable }

var (
	_ out.AppointmentAuthority = Unavailable{}
	_ out.BillingAuthority     = Unavailable{}
	_ out.PatientGate          = Unavailable{}
)

func (Unavailable) EncounterStatus(context.Context, string) (out.EncounterStatus, error) {
	return out.EncounterStatus{}, out.ErrUnavailable
}

func (Unavailable) FreezeManualCharges(context.Context, string, string, string) (out.ManualChargeSettlement, error) {
	return out.ManualChargeSettlement{}, out.ErrUnavailable
}

func (Unavailable) ReleaseManualChargeFreeze(context.Context, string, string, string) error {
	return out.ErrUnavailable
}

func (Unavailable) AcquireActivityGuard(context.Context, string, string) (out.Guard, error) {
	return out.Guard{}, out.ErrUnavailable
}

func (Unavailable) ResolveGuard(context.Context, string, string, string) error {
	return out.ErrUnavailable
}
