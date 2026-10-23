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

func freshKey(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("idempotency key: %v: %w", err, out.ErrUnavailable)
	}
	return prefix + "-" + hex.EncodeToString(b), nil
}
