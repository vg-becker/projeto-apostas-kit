package clock

import (
	"testing"
	"time"
)

func TestSystemNowIsUTCAndCurrent(t *testing.T) {
	before := time.Now()

	got := System{}.Now()

	if got.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", got.Location())
	}

	if got.Before(before) || time.Since(got) > time.Minute {
		t.Fatalf("now = %v is not current", got)
	}
}

func TestFixedNowIsStableAndUTC(t *testing.T) {
	instant := time.Date(2026, 9, 8, 9, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	clock := Fixed{Instant: instant}

	first := clock.Now()
	second := clock.Now()

	if !first.Equal(second) || !first.Equal(instant) {
		t.Fatalf("fixed clock changed: %v, %v", first, second)
	}

	if first.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", first.Location())
	}
}
