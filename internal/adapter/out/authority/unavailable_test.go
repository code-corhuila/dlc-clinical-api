package authority

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// An owner that is not configured never answers yes: every call fails closed.
func TestUnavailableOwnerFailsClosed(t *testing.T) {
	u := Unavailable{}
	_, e1 := u.IsAssigned(ctx, patient, dentist)
	_, e2 := u.AssignedPatients(ctx, dentist)
	_, e3 := u.IsActive(ctx, patient)
	_, e4 := u.EncounterStatus(ctx, appointment)
	_, e5 := u.FreezeManualCharges(ctx, record, consult, operation)
	e6 := u.ReleaseManualChargeFreeze(ctx, record, consult, operation)
	_, e7 := u.AcquireActivityGuard(ctx, patient, operation)
	e8 := u.ResolveGuard(ctx, guardID, "t", out.GuardAborted)
	for i, err := range []error{e1, e2, e3, e4, e5, e6, e7, e8} {
		if !errors.Is(err, out.ErrUnavailable) {
			t.Fatalf("call %d answered %v", i+1, err)
		}
	}
}

func TestServiceTokenAndClient(t *testing.T) {
	if tok, err := ServiceToken("from-env", ""); err != nil || tok != "from-env" {
		t.Fatalf("got %q %v", tok, err)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := ServiceToken("", file); err != nil || tok != "from-file" {
		t.Fatalf("a mounted secret is trimmed: %q %v", tok, err)
	}
	if _, err := ServiceToken("", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing secret file is an error")
	}
	c := NewClient("http://owner:8080/", "tok", 2*time.Second)
	if c.BaseURL != "http://owner:8080" || c.Token != "tok" || c.HTTP.Timeout != 2*time.Second {
		t.Fatalf("client = %+v", c)
	}
}
