package authority

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

var appointmentStates = map[string]bool{
	"PROGRAMADA": true, "CONFIRMADA": true, out.AppointmentInAttention: true, "FINALIZADA": true, "CANCELADA": true, "NO_ASISTIO": true,
}

// EncounterStatus adapts GET /internal/v1/appointments/{id}/encounter-status
// (scope appointments:encounter-status:read).
func (a Appointments) EncounterStatus(ctx context.Context, appointmentID string) (out.EncounterStatus, error) {
	var v struct {
		AppointmentID string `json:"appointmentId"`
		PatientID     string `json:"patientId"`
		DentistID     string `json:"dentistId"`
		Status        string `json:"status"`
		Version       int    `json:"version"`
	}
	err := a.Client.get(ctx, "/appointments/"+url.PathEscape(appointmentID)+"/encounter-status", nil, &v)
	if errors.Is(err, errOwnerNotFound) {
		return out.EncounterStatus{}, out.ErrNotFound
	}
	if err != nil {
		return out.EncounterStatus{}, err
	}
	if v.AppointmentID != appointmentID || !appointmentStates[v.Status] {
		return out.EncounterStatus{}, fmt.Errorf("encounter status does not describe the requested appointment: %w", out.ErrUnavailable)
	}
	return out.EncounterStatus(v), nil
}

// PatientGates adapts the Appointments patient gate (scope
// patient-gates:coordinate) to out.PatientGate.
type PatientGates struct{ Client *Client }

var _ out.PatientGate = PatientGates{}

var guardStates = map[string]bool{out.GuardOpen: true, out.GuardFinalized: true, out.GuardAborted: true}

// AcquireActivityGuard sends a fresh Idempotency-Key on every call: the owner
// deduplicates by operationId and must rotate the token on a repeat (ADR-017
// §2.2), which a replayed response would not do.
func (g PatientGates) AcquireActivityGuard(ctx context.Context, patientID, operationID string) (out.Guard, error) {
	var v struct {
		ID          string `json:"id"`
		PatientID   string `json:"patientId"`
		OperationID string `json:"operationId"`
		State       string `json:"state"`
		Token       string `json:"token"`
	}
	key, err := freshKey("acquire")
	if err != nil {
		return out.Guard{}, err
	}
	err = g.Client.post(ctx, "/patients/"+url.PathEscape(patientID)+"/activity-guards", key, map[string]string{"operationId": operationID}, &v)
	if errors.Is(err, errOwnerNotFound) {
		return out.Guard{}, out.ErrNotFound
	}
	if err != nil {
		return out.Guard{}, err
	}
	if v.PatientID != patientID || v.OperationID != operationID || v.ID == "" || v.Token == "" || !guardStates[v.State] {
		return out.Guard{}, fmt.Errorf("guard does not describe the requested operation: %w", out.ErrUnavailable)
	}
	return out.Guard{ID: v.ID, State: v.State, Token: v.Token}, nil
}

// ResolveGuard reports the durable outcome of the guarded write. The key is
// stable per guard and outcome, so a retried resolution is deduplicated.
func (g PatientGates) ResolveGuard(ctx context.Context, guardID, token, outcome string) error {
	var v struct {
		ID          string `json:"id"`
		PatientID   string `json:"patientId"`
		OperationID string `json:"operationId"`
		State       string `json:"state"`
	}
	err := g.Client.post(ctx, "/patient-guards/"+url.PathEscape(guardID)+"/resolutions", "resolve-"+guardID+"-"+outcome,
		map[string]string{"token": token, "outcome": outcome}, &v)
	if errors.Is(err, errOwnerNotFound) {
		return out.ErrNotFound
	}
	return err
}

// Billing adapts the encounter manual-charge freeze (billing-service, scope
// billing:encounter-freeze:write) to out.BillingAuthority.
type Billing struct{ Client *Client }

var _ out.BillingAuthority = Billing{}

type settlement struct {
	OperationID    string   `json:"operationId"`
	FreezeState    string   `json:"freezeState"`
	ClinicalRecord string   `json:"clinicalRecordId"`
	Consultation   string   `json:"consultationId"`
	Settled        *bool    `json:"settled"`
	PendingWrites  int      `json:"pendingWrites"`
	ChargeIDs      []string `json:"chargeIds"`
	Version        int      `json:"version"`
}

func freezesPath(recordID, consultationID string) string {
	return "/encounter-accounts/" + url.PathEscape(recordID) + "/" + url.PathEscape(consultationID) + "/manual-charges/freezes"
}

func (b Billing) FreezeManualCharges(ctx context.Context, recordID, consultationID, operationID string) (out.ManualChargeSettlement, error) {
	var v settlement
	err := b.Client.post(ctx, freezesPath(recordID, consultationID), "freeze-"+operationID, map[string]string{"operationId": operationID}, &v)
	if errors.Is(err, errOwnerNotFound) {
		return out.ManualChargeSettlement{}, fmt.Errorf("encounter account not found: %w", out.ErrUnavailable)
	}
	if err != nil {
		return out.ManualChargeSettlement{}, err
	}
	if v.OperationID != operationID || v.ClinicalRecord != recordID || v.Consultation != consultationID || v.FreezeState != "FROZEN" || v.Settled == nil {
		return out.ManualChargeSettlement{}, fmt.Errorf("freeze does not describe the requested encounter: %w", out.ErrUnavailable)
	}
	charges := v.ChargeIDs
	if charges == nil {
		charges = []string{}
	}
	return out.ManualChargeSettlement{Settled: *v.Settled, PendingWrites: v.PendingWrites, ChargeIDs: charges, Version: v.Version}, nil
}

// ReleaseManualChargeFreeze is idempotent: a freeze Billing does not know is
// already released.
func (b Billing) ReleaseManualChargeFreeze(ctx context.Context, recordID, consultationID, operationID string) error {
	var v settlement
	err := b.Client.post(ctx, freezesPath(recordID, consultationID)+"/"+url.PathEscape(operationID)+"/releases", "release-"+operationID, nil, &v)
	if errors.Is(err, errOwnerNotFound) {
		return nil
	}
	return err
}

func freshKey(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("idempotency key: %v: %w", err, out.ErrUnavailable)
	}
	return prefix + "-" + hex.EncodeToString(b), nil
}
