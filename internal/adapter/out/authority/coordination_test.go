package authority

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
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

func TestEncounterStatusReadsTheAppointment(t *testing.T) {
	c, got := capture(t, 200, `{"appointmentId":"`+appointment+`","patientId":"`+patient+`","dentistId":"`+dentist+`","status":"EN_ATENCION","version":4}`)
	s, err := Appointments{Client: c}.EncounterStatus(ctx, appointment)
	want := out.EncounterStatus{AppointmentID: appointment, PatientID: patient, DentistID: dentist, Status: out.AppointmentInAttention, Version: 4}
	if err != nil || s != want || (*got)[0].path != "/internal/v1/appointments/"+appointment+"/encounter-status" {
		t.Fatalf("got %+v %v %+v", s, err, *got)
	}
	for name, c := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"unknown appointment": {404, `{"error":"NOT_FOUND","message":"x"}`, out.ErrNotFound},
		"owner down":          {503, `{"error":"SERVICE_UNAVAILABLE","message":"x"}`, out.ErrUnavailable},
		"other appointment":   {200, `{"appointmentId":"` + record + `","patientId":"` + patient + `","dentistId":"` + dentist + `","status":"EN_ATENCION","version":1}`, out.ErrUnavailable},
		"unknown status":      {200, `{"appointmentId":"` + appointment + `","patientId":"` + patient + `","dentistId":"` + dentist + `","status":"OTRO","version":1}`, out.ErrUnavailable},
	} {
		client, _ := capture(t, c.status, c.body)
		if _, err := (Appointments{Client: client}).EncounterStatus(ctx, appointment); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
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

func TestManualChargeFreezeAndRelease(t *testing.T) {
	base := "/internal/v1/encounter-accounts/" + record + "/" + consult + "/manual-charges/freezes"
	settlement := `{"operationId":"` + operation + `","freezeState":"FROZEN","clinicalRecordId":"` + record + `","consultationId":"` + consult + `","settled":true,"pendingWrites":0,"chargeIds":["` + guardID + `"],"version":2}`
	c, got := capture(t, 200, settlement)
	s, err := Billing{Client: c}.FreezeManualCharges(ctx, record, consult, operation)
	if err != nil || !s.Settled || s.PendingWrites != 0 || !slices.Equal(s.ChargeIDs, []string{guardID}) || s.Version != 2 ||
		(*got)[0].path != base || (*got)[0].body["operationId"] != operation || (*got)[0].idempotencyKey == "" {
		t.Fatalf("got %+v %v %+v", s, err, *got)
	}
	open, _ := capture(t, 200, `{"operationId":"`+operation+`","freezeState":"OPEN","clinicalRecordId":"`+record+`","consultationId":"`+consult+`","settled":true}`)
	if _, err := (Billing{Client: open}).FreezeManualCharges(ctx, record, consult, operation); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("a freeze that is not FROZEN is never trusted: %v", err)
	}

	rel, got := capture(t, 200, `{"operationId":"`+operation+`","freezeState":"OPEN","clinicalRecordId":"`+record+`","consultationId":"`+consult+`","settled":true}`)
	if err := (Billing{Client: rel}).ReleaseManualChargeFreeze(ctx, record, consult, operation); err != nil || (*got)[0].path != base+"/"+operation+"/releases" {
		t.Fatalf("release: %v %+v", err, *got)
	}
	for status, want := range map[int]error{404: nil, 409: out.ErrOwnerConflict, 503: out.ErrUnavailable} {
		client, _ := capture(t, status, `{"error":"X","message":"x"}`)
		if err := (Billing{Client: client}).ReleaseManualChargeFreeze(ctx, record, consult, operation); !errors.Is(err, want) {
			t.Fatalf("release %d: got %v", status, err)
		}
	}
}

// _shared.yaml declares Idempotency-Key as format uuid: a strict owner rejects
// anything else. Retries of the same step must reuse the same key.
func TestOwnerIdempotencyKeysAreUUIDsStableAcrossRetries(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	guard := `{"id":"` + guardID + `","patientId":"` + patient + `","operationId":"` + operation + `","state":"OPEN","token":"p"}`
	resolved := `{"id":"` + guardID + `","patientId":"` + patient + `","operationId":"` + operation + `","state":"FINALIZED"}`
	settled := `{"operationId":"` + operation + `","freezeState":"FROZEN","clinicalRecordId":"` + record + `","consultationId":"` + consult + `","settled":true}`
	released := `{"operationId":"` + operation + `","freezeState":"OPEN","clinicalRecordId":"` + record + `","consultationId":"` + consult + `","settled":true}`
	keys := func(answer string, call func(c *Client)) []string {
		c, got := capture(t, 200, answer)
		call(c)
		call(c)
		var k []string
		for _, r := range *got {
			if !uuid.MatchString(r.idempotencyKey) {
				t.Fatalf("Idempotency-Key %q of %s is not a UUID", r.idempotencyKey, r.path)
			}
			k = append(k, r.idempotencyKey)
		}
		return k
	}
	keys(guard, func(c *Client) { _, _ = PatientGates{Client: c}.AcquireActivityGuard(ctx, patient, operation) })
	finalized := keys(resolved, func(c *Client) { _ = PatientGates{Client: c}.ResolveGuard(ctx, guardID, "p", out.GuardFinalized) })
	aborted := keys(resolved, func(c *Client) { _ = PatientGates{Client: c}.ResolveGuard(ctx, guardID, "p", out.GuardAborted) })
	freeze := keys(settled, func(c *Client) { _, _ = Billing{Client: c}.FreezeManualCharges(ctx, record, consult, operation) })
	release := keys(released, func(c *Client) { _ = Billing{Client: c}.ReleaseManualChargeFreeze(ctx, record, consult, operation) })
	for name, k := range map[string][]string{"resolution": finalized, "freeze": freeze, "release": release} {
		if k[0] != k[1] {
			t.Fatalf("%s retries must reuse the key: %v", name, k)
		}
	}
	if finalized[0] == aborted[0] || freeze[0] == release[0] {
		t.Fatal("different steps must not share a key")
	}
}
