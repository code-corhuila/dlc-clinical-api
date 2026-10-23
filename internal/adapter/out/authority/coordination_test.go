package authority

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

const (
	appointment = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	record      = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	consult     = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	operation   = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	guardID     = "99999999-9999-4999-8999-999999999999"
)

// captured is a request as the owner received it.
type captured struct {
	method, path, idempotencyKey string
	body                         map[string]any
}

func capture(t *testing.T, status int, answer string) (*Client, *[]captured) {
	t.Helper()
	var got []captured
	c, _ := owner(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got = append(got, captured{r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), body})
		reply(status, answer)(w, r)
	})
	return c, &got
}

func TestActivityGuardAcquisitionAndResolution(t *testing.T) {
	c, got := capture(t, 201, `{"id":"`+guardID+`","patientId":"`+patient+`","operationId":"`+operation+`","state":"OPEN","token":"proof"}`)
	g, err := PatientGates{Client: c}.AcquireActivityGuard(ctx, patient, operation)
	r := (*got)[0]
	if err != nil || g != (out.Guard{ID: guardID, State: out.GuardOpen, Token: "proof"}) || r.method != http.MethodPost ||
		r.path != "/internal/v1/patients/"+patient+"/activity-guards" || r.body["operationId"] != operation || len(r.idempotencyKey) < 8 {
		t.Fatalf("got %+v %v %+v", g, err, r)
	}
	blocked, _ := capture(t, 409, `{"error":"CONFLICT","message":"x"}`)
	if _, err := (PatientGates{Client: blocked}).AcquireActivityGuard(ctx, patient, operation); !errors.Is(err, out.ErrOwnerConflict) {
		t.Fatalf("blocked gate: %v", err)
	}
	other, _ := capture(t, 201, `{"id":"`+guardID+`","patientId":"`+patient+`","operationId":"`+record+`","state":"OPEN","token":"p"}`)
	if _, err := (PatientGates{Client: other}).AcquireActivityGuard(ctx, patient, operation); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("guard of another operation: %v", err)
	}

	res, got := capture(t, 200, `{"id":"`+guardID+`","patientId":"`+patient+`","operationId":"`+operation+`","state":"FINALIZED"}`)
	if err := (PatientGates{Client: res}).ResolveGuard(ctx, guardID, "proof", out.GuardFinalized); err != nil {
		t.Fatal(err)
	}
	r = (*got)[0]
	if r.path != "/internal/v1/patient-guards/"+guardID+"/resolutions" || r.body["token"] != "proof" || r.body["outcome"] != "FINALIZED" || r.idempotencyKey == "" {
		t.Fatalf("resolution request %+v", r)
	}
	stale, _ := capture(t, 409, `{"error":"CONFLICT","message":"x"}`)
	if err := (PatientGates{Client: stale}).ResolveGuard(ctx, guardID, "old", out.GuardAborted); !errors.Is(err, out.ErrOwnerConflict) {
		t.Fatalf("stale token: %v", err)
	}
}
