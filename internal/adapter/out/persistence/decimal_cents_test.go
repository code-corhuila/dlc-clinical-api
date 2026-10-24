package persistence

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestDecimalCentsIsExact(t *testing.T) {
	for raw, want := range map[string]int64{"150000.50": 15000050, "1.5E+5": 15000000, "0.10": 10, "12": 1200} {
		d, _ := bson.ParseDecimal128(raw)
		if got, err := decimalCents(d); err != nil || got != want {
			t.Errorf("%s -> %d %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0.001", "1E+30"} {
		d, _ := bson.ParseDecimal128(raw)
		if _, err := decimalCents(d); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
}
