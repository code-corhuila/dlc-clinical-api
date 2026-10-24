package persistence

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// newTestMongo connects to a database migrated by dlc-clinical-db. Without
// TEST_MONGO_URI the integration tests are skipped, never failed (Anexo C).
func newTestMongo(t *testing.T) *Mongo {
	t.Helper()
	uri := os.Getenv("TEST_MONGO_URI")
	if uri == "" {
		t.Skip("TEST_MONGO_URI not set; integration test skipped")
	}
	db := os.Getenv("TEST_MONGO_DATABASE")
	if db == "" {
		db = "clinical"
	}
	m, err := OpenMongo(context.Background(), MongoOptions{URI: uri, Database: db, MaxPoolSize: 5, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	for _, c := range []string{"clinical_records", "idempotency_keys"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestMongoRoundTripPreservesTypes(t *testing.T) {
	m := newTestMongo(t)
	at := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	r := rec("11111111-0000-4000-8000-000000000001", "22222222-0000-4000-8000-000000000001", at)
	idem := key("key-00000001", r.ID)
	idem.CreatedAt, idem.ExpiresAt, idem.RequestHash = at, at.Add(7*24*time.Hour), "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := m.CreateRecord(ctx, r, idem); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetRecord(ctx, r.ID)
	if err != nil || got != r {
		t.Fatalf("got %+v %v, want %+v", got, err, r)
	}
	saved, ok, err := m.FindIdempotency(ctx, "d", "op", "key-00000001")
	if err != nil || !ok || saved != idem {
		t.Fatalf("idempotency %+v %v %v, want %+v", saved, ok, err, idem)
	}
	if _, err := m.GetRecord(ctx, "33333333-0000-4000-8000-000000000001"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMongoTransactionRollsBackBothDocuments(t *testing.T) {
	m := newTestMongo(t)
	at := time.Now().UTC().Truncate(time.Second)
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	mk := func(k, id string) out.IdempotencyRecord {
		i := key(k, id)
		i.RequestHash, i.CreatedAt, i.ExpiresAt = hash, at, at.Add(time.Hour)
		return i
	}
	_ = m.CreateRecord(ctx, rec("r-1", "p-1", at), mk("key-00000001", "r-1"))

	if err := m.CreateRecord(ctx, rec("r-2", "p-2", at), mk("key-00000001", "r-2")); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("duplicate key: got %v", err)
	}
	if _, err := m.GetRecord(ctx, "r-2"); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("the record of a rejected key must be rolled back")
	}
	if err := m.CreateRecord(ctx, rec("r-3", "p-1", at), mk("key-00000003", "r-3")); !errors.Is(err, out.ErrRecordExists) {
		t.Fatalf("second record for patient: got %v", err)
	}
	if _, ok, _ := m.FindIdempotency(ctx, "d", "op", "key-00000003"); ok {
		t.Fatal("the key of a rejected record must be rolled back")
	}
}

func TestMongoListNewestFirstWithFilterAndWindow(t *testing.T) {
	m := newTestMongo(t)
	base := time.Now().UTC().Truncate(time.Second)
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for i, p := range []string{"p1", "p2", "p3"} {
		k := key("key-0000000"+p, "r"+p)
		k.RequestHash, k.CreatedAt, k.ExpiresAt = hash, base, base.Add(time.Hour)
		if err := m.CreateRecord(ctx, rec("r"+p, p, base.Add(time.Duration(i)*time.Minute)), k); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := m.ListRecords(ctx, out.RecordFilter{Limit: 2})
	if err != nil || total != 3 || len(items) != 2 || items[0].PatientID != "p3" || items[1].PatientID != "p2" {
		t.Fatalf("got %d %+v %v", total, items, err)
	}
	items, total, _ = m.ListRecords(ctx, out.RecordFilter{PatientIDs: []string{"p1", "p2"}, Offset: 1, Limit: 5})
	if total != 2 || len(items) != 1 || items[0].PatientID != "p1" {
		t.Fatalf("got %d %+v", total, items)
	}
	items, total, _ = m.ListRecords(ctx, out.RecordFilter{PatientIDs: []string{}, Limit: 5})
	if total != 0 || len(items) != 0 {
		t.Fatalf("an empty scope sees nothing, got %d", total)
	}
}
