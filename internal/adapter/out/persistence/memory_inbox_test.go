package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

func TestMemoryInboxAppliesEachEventOnce(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	_ = m.CreateRecord(ctx, rec("r1", "p1", now), key("k-rec", "r1"))
	_ = m.DeclareCareCompletion(ctx, closure("cc1", "r1", "c1", 2, now), 1, key("k-decl", "cc1"), nil)
	found, _ := m.FindClosuresByAppointment(ctx, "a")
	if len(found) != 1 {
		t.Fatalf("found %d", len(found))
	}
	c := found[0]
	c.RecordAppointmentCompleted(now)
	if err := m.Apply(ctx, receipt("ev-1", "PROCESSED", now), []out.ClosureUpdate{{Closure: c, FromVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(ctx, receipt("ev-1", "PROCESSED", now), nil); !errors.Is(err, out.ErrDuplicateEvent) {
		t.Fatalf("got %v", err)
	}
	if err := m.Apply(ctx, receipt("ev-2", "PROCESSED", now), []out.ClosureUpdate{{Closure: c, FromVersion: 1}}); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("got %v", err)
	}
	if seen, _ := m.Received(ctx, "ev-2"); seen {
		t.Fatal("a conflicting receipt leaves nothing behind")
	}
}
