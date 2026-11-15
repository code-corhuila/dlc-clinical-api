package system

import (
	"regexp"
	"testing"
	"time"
)

func TestUUIDsAreVersion4AndUnique(t *testing.T) {
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := UUIDs{}.NewID()
		if !v4.MatchString(id) || seen[id] {
			t.Fatalf("bad or repeated UUID %q", id)
		}
		seen[id] = true
	}
}

func TestClockIsUTCSeconds(t *testing.T) {
	now := Clock{}.Now()
	if now.Location() != time.UTC || now.Nanosecond() != 0 {
		t.Fatalf("got %v", now)
	}
}
