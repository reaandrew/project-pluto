// Package passcode handles the four passcode-flow primitives that the
// publisher Lambda (iter 5.3) and email-draft Lambda (iter 8) need:
//
//  1. Generate(): an 8-character Crockford-Base32 cleartext passcode using
//     crypto/rand. ~1.1 trillion possibilities; with the Worker's
//     10/60s/IP rate-limit, brute-forcing one takes >2000 years per IP.
//
//  2. Hash(passcode, salt): the canonical hash format both the Worker
//     (worker/src/passcode.ts) and the publisher write to Workers KV.
//     Currently SHA-256 of `<passcode>|<salt>`. Iter 5.4 will swap this
//     and the Worker's hashPasscode together to argon2id WASM (the format
//     change is a breaking contract bump for both writers).
//
//  3. EncryptCleartext / DecryptCleartext: KMS Encrypt/Decrypt against
//     the project's `passcode-cleartext-${env}` CMK. The ciphertext is
//     stored as `Website.passcodeCipher` so the email-draft Lambda can
//     retrieve the cleartext during the 7-day revealable window. After
//     `passcodeRevealableUntil`, the operator runs a wipe job that
//     deletes the cipher field — see iter 5.3.
//
//  4. KVWriter: writes the hash to a Cloudflare Workers KV namespace via
//     the Cloudflare REST API. Used by the publisher Lambda; the Worker
//     reads from the same key to validate submitted passcodes.
//
//  5. SignOpToken: a short-lived operator-bypass token the screenshotter
//     Lambda (iter 5.5) appends as `?op=<token>` so the Cloudflare
//     Browser Rendering headless browser can fetch the preview past the
//     passcode gate. Byte-cross-pinned with worker/src/passcode.ts
//     signOpToken (test vector pinned on both sides).
//
// **Cleartext NEVER appears in events, logs, or X-Ray traces.** The only
// place cleartext is allowed to live is the KMS-encrypted `passcodeCipher`
// field, the prompt body the email-draft Lambda passes to Bedrock for that
// one call, and the email body sent via SES.
package passcode

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
)

// CrockfordBase32Alphabet is the canonical Crockford Base32 alphabet:
// 0–9 and A–Z minus the visually-ambiguous I, L, O, U. Site owners
// occasionally read the code aloud or transcribe by hand — every removed
// character is one less misread.
const CrockfordBase32Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// PasscodeLength is the canonical project-wide passcode length. Don't
// change without coordinating with the Worker (which doesn't enforce
// length but logs failed attempts that include length).
const PasscodeLength = 8

// alphabetSize is len(CrockfordBase32Alphabet); kept as a constant so the
// rejection-sampling math in randIndex stays obvious.
const alphabetSize = 32

// Generate returns a fresh 8-character Crockford-Base32 cleartext passcode.
// Each character is drawn uniformly via crypto/rand.Int (which itself does
// rejection sampling, so the result has no modulo bias).
func Generate() (string, error) {
	out := make([]byte, PasscodeLength)
	max := big.NewInt(int64(alphabetSize))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("passcode: drawing random index: %w", err)
		}
		out[i] = CrockfordBase32Alphabet[n.Int64()]
	}
	return string(out), nil
}

// Hash returns the canonical hex-encoded hash of (passcode, salt).
// Matches worker/src/passcode.ts:hashPasscode byte-for-byte (the same
// SHA-256 of `<passcode>|<salt>`). Both sides MUST stay in sync.
//
// argon2id swap stays deferred. The Go side picks up `x/crypto/argon2`
// cleanly, but the Cloudflare Workers test runtime
// (cloudflare/vitest-pool-workers) blocks dynamic
// `WebAssembly.compile()` which hash-wasm + every other available
// argon2 library on Workers depends on. Production Workers DO support
// WASM, but we need either the wrangler `wasm_modules` preload or a
// worker-pool-free test path before flipping the format on both
// sides. Tracked as a follow-up to iter 5.4.
//
// Returns the hex-encoded SHA-256 digest (64 lowercase hex chars).
func Hash(passcode, salt string) string {
	if passcode == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(passcode + "|" + salt))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares a stored hash against a fresh hash of
// (submittedPasscode, salt) in constant time, returning true on a match.
//
// Use this anywhere we compare passcode hashes — never raw `==`. The
// Worker's `verifyPasscode` does the same in TypeScript.
func ConstantTimeEqual(storedHash, submittedPasscode, salt string) bool {
	expected := Hash(submittedPasscode, salt)
	if storedHash == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(expected)) == 1
}

// OperatorTokenTTL is how long an operator-bypass token stays valid.
// Mirrors worker/src/passcode.ts OP_TOKEN_TTL_SECONDS. The screenshotter
// mints a token immediately before the Browser Rendering call, so a tight
// window is sufficient and bounds replay.
const OperatorTokenTTL = 120 * time.Second

// opTokenSig is the HMAC-SHA256 (hex) of `op:<websiteID>.<exp>` keyed by
// salt. The `op:` prefix domain-separates this from the Worker's cookie
// signature (`<websiteID>.<exp>`, no prefix) so neither can be replayed as
// the other. MUST stay byte-identical to worker/src/passcode.ts hmacHex
// over the same message.
func opTokenSig(websiteID string, exp int64, salt string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	_, _ = mac.Write([]byte(fmt.Sprintf("op:%s.%d", websiteID, exp)))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignOpToken returns `<websiteID>.<exp>.<sig>` where exp = now +
// OperatorTokenTTL (unix seconds) and sig = opTokenSig(...). The Worker's
// verifyOpToken recomputes the HMAC and constant-time-compares. `now` is a
// parameter so callers/tests stay deterministic.
func SignOpToken(websiteID, salt string, now time.Time) string {
	exp := now.Add(OperatorTokenTTL).Unix()
	return fmt.Sprintf("%s.%d.%s", websiteID, exp, opTokenSig(websiteID, exp, salt))
}

// IsValidPasscodeFormat returns true if s looks like one of our passcodes:
// exactly PasscodeLength chars, all from CrockfordBase32Alphabet. Useful
// for early-rejecting malformed submissions in the publisher Lambda
// before any DDB or KV traffic.
func IsValidPasscodeFormat(s string) bool {
	if len(s) != PasscodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		// scan the alphabet — for an alphabet of 32 chars this is fine.
		ok := false
		for j := 0; j < len(CrockfordBase32Alphabet); j++ {
			if s[i] == CrockfordBase32Alphabet[j] {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
