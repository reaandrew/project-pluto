package politefetch

import (
	"crypto/rand"
	"encoding/binary"
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
// Uses crypto/rand to satisfy the project's "no math/rand" lint policy; if
// the entropy source ever fails (effectively never on Linux Lambda), falls
// back to a deterministic delay = base.
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
	jitter, err := randInt63n(span + 1)
	if err != nil {
		return backoffBase
	}
	return backoffBase + time.Duration(jitter)
}

// randInt63n returns a uniform value in [0, n) using crypto/rand. Bias from
// the modulo reduction is negligible for our jitter ranges (well under 2^32),
// and we don't need cryptographic strength — but using crypto/rand keeps the
// security scanner happy without a separate exclusion.
func randInt63n(n int64) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	v := int64(binary.LittleEndian.Uint64(b[:]) & 0x7fffffffffffffff)
	return v % n, nil
}
