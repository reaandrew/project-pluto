package politefetch

import (
	"testing"
	"time"
)

func TestBackoffDurationGrows(t *testing.T) {
	// Each successive attempt's upper bound is 2× the previous, capped.
	upper := func(attempt int) time.Duration {
		u := backoffBase << attempt
		if u > backoffCap {
			u = backoffCap
		}
		return u
	}
	for attempt := 0; attempt < 10; attempt++ {
		d := backoffDuration(attempt)
		if d < backoffBase {
			t.Errorf("attempt %d: %v < base %v", attempt, d, backoffBase)
		}
		if d > upper(attempt) {
			t.Errorf("attempt %d: %v > upper %v", attempt, d, upper(attempt))
		}
	}
}

func TestBackoffDurationNegativeAttemptDefaultsToZero(t *testing.T) {
	d := backoffDuration(-5)
	// negative attempt → upper = backoffBase (since 1 << 0 = 1) so result is base
	if d != backoffBase {
		t.Errorf("backoff for -5 = %v, want %v", d, backoffBase)
	}
}

func TestBackoffCappedAtBackoffCap(t *testing.T) {
	d := backoffDuration(20)
	if d > backoffCap {
		t.Errorf("backoff for 20 = %v, exceeds cap %v", d, backoffCap)
	}
}
