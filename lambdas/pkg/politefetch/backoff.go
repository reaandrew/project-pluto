package politefetch

import (
	"math/rand"
	"time"
)

// backoffBase is the first retry delay; subsequent retries double until cap.
// 200ms → 400ms → 800ms → 1.6s → 3.2s → 6.4s, capped.
const (
	backoffBase = 200 * time.Millisecond
	backoffCap  = 30 * time.Second
)

// backoffDuration returns the delay before the (attempt+1)-th attempt.
// Decorrelated jitter — uniform random in [base, capped(2^attempt * base)].
// rand is intentionally math/rand: jitter doesn't need crypto strength.
func backoffDuration(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	upper := backoffBase << attempt
	if upper > backoffCap {
		upper = backoffCap
	}
	if upper <= backoffBase {
		return upper
	}
	span := int64(upper - backoffBase)
	jitter := time.Duration(rand.Int63n(span + 1)) // #nosec G404 — non-cryptographic jitter
	return backoffBase + jitter
}
