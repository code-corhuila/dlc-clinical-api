package correlation

import (
	"context"
	"testing"
)

func TestCorrelationTravelsInTheContext(t *testing.T) {
	if ID(context.Background()) != "" {
		t.Fatal("no id without one")
	}
	if got := ID(WithID(context.Background(), "corr-1")); got != "corr-1" {
		t.Fatalf("got %q", got)
	}
}
