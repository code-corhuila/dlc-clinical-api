package authority

import (
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestCredentialsAndClient(t *testing.T) {
	if tok, err := StaticCredential("from-env").Token(); err != nil || tok != "from-env" {
		t.Fatalf("got %q %v", tok, err)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cred := FileCredential(file)
	if tok, err := cred.Token(); err != nil || tok != "from-file" {
		t.Fatalf("a mounted secret is trimmed: %q %v", tok, err)
	}
	// A short-lived service JWT is rotated in place: every call reads the file.
	if err := os.WriteFile(file, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := cred.Token(); err != nil || tok != "rotated" {
		t.Fatalf("rotation not picked up: %q %v", tok, err)
	}
	for name, c := range map[string]Credential{"missing file": FileCredential(filepath.Join(t.TempDir(), "missing")), "empty": StaticCredential("")} {
		if _, err := c.Token(); err == nil {
			t.Fatalf("%s must be an error", name)
		}
	}
	c := NewClient("http://owner:8080/", StaticCredential("tok"), 2*time.Second)
	if c.BaseURL != "http://owner:8080" || c.HTTP.Timeout != 2*time.Second {
		t.Fatalf("client = %+v", c)
	}
}

func TestUnreadableCredentialFailsClosedWithoutCalling(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, FileCredential(filepath.Join(t.TempDir(), "missing")), time.Second)
	_, err := Patients{Client: c}.IsActive(ctx, patient)
	if !errors.Is(err, out.ErrUnavailable) || called {
		t.Fatalf("got %v, owner called %v", err, called)
	}
}

func TestCredentialForPrefersTheOwnerFile(t *testing.T) {
	cases := map[string]struct {
		own, sharedFile, shared string
		want                    Credential
	}{
		"owner file":   {"/own", "/shared", "v", FileCredential("/own")},
		"shared file":  {"", "/shared", "", FileCredential("/shared")},
		"shared value": {"", "", "v", StaticCredential("v")},
	}
	for name, c := range cases {
		if got := CredentialFor(c.own, c.sharedFile, c.shared); got != c.want {
			t.Fatalf("%s: got %#v", name, got)
		}
	}
}
