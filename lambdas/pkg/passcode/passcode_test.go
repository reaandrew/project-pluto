package passcode

import (
	"strings"
	"testing"
	"time"
)

// --- Generate -------------------------------------------------------------

func TestGenerateProducesEightCrockfordChars(t *testing.T) {
	for i := 0; i < 50; i++ {
		got, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if len(got) != PasscodeLength {
			t.Errorf("len = %d, want %d (got %q)", len(got), PasscodeLength, got)
		}
		for j, c := range got {
			if !strings.ContainsRune(CrockfordBase32Alphabet, c) {
				t.Errorf("char %d (%q) in %q is outside the alphabet", j, c, got)
			}
		}
	}
}

func TestGenerateEntropyAcrossManyDraws(t *testing.T) {
	// 200 draws from a 32^8 ≈ 1.1e12 space — collisions are vanishingly unlikely.
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		got, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if _, dup := seen[got]; dup {
			t.Fatalf("duplicate passcode %q after %d draws — entropy looks broken", got, i)
		}
		seen[got] = struct{}{}
	}
}

// --- Hash -----------------------------------------------------------------

func TestHashMatchesWorkerFormat(t *testing.T) {
	// Cross-check vector: SHA-256("ABCD1234|salt-xyz") in lowercase hex.
	// Computed via `printf 'ABCD1234|salt-xyz' | sha256sum` — pin to keep
	// publisher (Go) and Worker (TS) in lockstep on the wire format.
	got := Hash("ABCD1234", "salt-xyz")
	const want = "65934f822746fbae6a622934c35054988be3803c8e911e4cc566a1123113cfd1"
	if got != want {
		t.Errorf("Hash drift — Worker would mis-validate.\n got: %s\nwant: %s", got, want)
	}
}

func TestHashIsDeterministic(t *testing.T) {
	a := Hash("ABCDEFGH", "salt")
	b := Hash("ABCDEFGH", "salt")
	if a != b {
		t.Error("Hash not deterministic")
	}
}

func TestHashChangesOnPasscodeOrSaltChange(t *testing.T) {
	base := Hash("ABCDEFGH", "salt")
	if Hash("BBCDEFGH", "salt") == base {
		t.Error("Hash collided across different passcodes")
	}
	if Hash("ABCDEFGH", "salt2") == base {
		t.Error("Hash collided across different salts")
	}
}

func TestHashEmptyPasscodeReturnsEmpty(t *testing.T) {
	if got := Hash("", "salt"); got != "" {
		t.Errorf("Hash('') = %q, want empty (avoids accidental valid hash)", got)
	}
}

func TestHashIsHexEncoded64Chars(t *testing.T) {
	got := Hash("ABCDEFGH", "salt")
	if len(got) != 64 {
		t.Errorf("len = %d, want 64 (hex sha256)", len(got))
	}
	for _, c := range got {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char %q in %q", c, got)
		}
	}
}

// --- ConstantTimeEqual ---------------------------------------------------

func TestConstantTimeEqualMatch(t *testing.T) {
	stored := Hash("ABCDEFGH", "salt-x")
	if !ConstantTimeEqual(stored, "ABCDEFGH", "salt-x") {
		t.Error("ConstantTimeEqual rejected matching passcode")
	}
}

func TestConstantTimeEqualMismatch(t *testing.T) {
	stored := Hash("ABCDEFGH", "salt-x")
	if ConstantTimeEqual(stored, "ABCDEFG1", "salt-x") {
		t.Error("ConstantTimeEqual accepted wrong passcode")
	}
	if ConstantTimeEqual(stored, "ABCDEFGH", "salt-y") {
		t.Error("ConstantTimeEqual accepted wrong salt")
	}
}

func TestConstantTimeEqualEmptyInputsAlwaysFalse(t *testing.T) {
	if ConstantTimeEqual("", "ABCDEFGH", "salt") {
		t.Error("empty stored hash should never match")
	}
	if ConstantTimeEqual(Hash("ABCDEFGH", "salt"), "", "salt") {
		t.Error("empty submitted passcode should never match")
	}
}

// --- IsValidPasscodeFormat ------------------------------------------------

func TestIsValidPasscodeFormat(t *testing.T) {
	cases := map[string]bool{
		"ABCDEFGH":  true,  // all in alphabet
		"01234567":  true,  // numeric prefix valid
		"ABCDEFG":   false, // too short
		"ABCDEFGHI": false, // too long
		"ABCDEFGI":  false, // I excluded from Crockford
		"ABCDEFGL":  false, // L excluded
		"ABCDEFGO":  false, // O excluded
		"ABCDEFGU":  false, // U excluded
		"abcdefgh":  false, // lowercase rejected (alphabet is uppercase)
		"":          false,
	}
	for s, want := range cases {
		t.Run(s, func(t *testing.T) {
			if got := IsValidPasscodeFormat(s); got != want {
				t.Errorf("IsValidPasscodeFormat(%q) = %v, want %v", s, got, want)
			}
		})
	}
}

// --- Generate produces values that pass IsValidPasscodeFormat ------------

func TestGenerateProducesValidFormat(t *testing.T) {
	for i := 0; i < 30; i++ {
		got, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !IsValidPasscodeFormat(got) {
			t.Errorf("Generate produced %q which IsValidPasscodeFormat rejects", got)
		}
	}
}

// --- SignOpToken (cross-pinned with worker/src/passcode.ts) --------------

// crossPinned* is the canonical operator-token vector. The identical
// triple is asserted in worker/tests/passcode.test.ts so the Go signer
// and the Worker verifier can never silently drift.
const (
	crossPinnedWebsiteID = "site-pin"
	crossPinnedSalt      = "pin-salt-vector"
	crossPinnedExp       = int64(4102444800) // 2100-01-01T00:00:00Z
	crossPinnedSig       = "0687580f2382fc3742256b57a28655ebbf478bfce3dcb1910e67ac78c84a96b4"
)

func TestOpTokenSigMatchesPinnedVector(t *testing.T) {
	if got := opTokenSig(crossPinnedWebsiteID, crossPinnedExp, crossPinnedSalt); got != crossPinnedSig {
		t.Fatalf("opTokenSig drift: got %q, want pinned %q (worker/src/passcode.ts must match)", got, crossPinnedSig)
	}
}

func TestSignOpTokenStructureAndExpiry(t *testing.T) {
	// now chosen so exp lands exactly on the pinned value.
	now := time.Unix(crossPinnedExp, 0).Add(-OperatorTokenTTL)
	tok := SignOpToken(crossPinnedWebsiteID, crossPinnedSalt, now)
	want := crossPinnedWebsiteID + ".4102444800." + crossPinnedSig
	if tok != want {
		t.Fatalf("SignOpToken = %q, want %q", tok, want)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token must have 3 dot-separated parts, got %d (%q)", len(parts), tok)
	}
}

func TestSignOpTokenDistinctPerWebsite(t *testing.T) {
	now := time.Now()
	a := SignOpToken("site-a", crossPinnedSalt, now)
	b := SignOpToken("site-b", crossPinnedSalt, now)
	if a == b {
		t.Fatal("tokens for different websiteIds must differ")
	}
}
