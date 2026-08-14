package store

import (
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/auth"
)

// TestTotpAtRestDualRead locks the TOTP-at-rest KDF migration: new secrets use
// the HKDF (v2) key, legacy v1 secrets (bare sha256 key) still decrypt, ancient
// plaintext passes through, and the two keys are actually distinct.
func TestTotpAtRestDualRead(t *testing.T) {
	st := New(nil, "an-encryption-secret-at-least-32-chars!!")
	const secret = "JBSWY3DPEHPK3PXP" // #nosec G101 -- example TOTP seed used only in this test

	// Current format is v2: and round-trips.
	enc, err := st.encryptTotp(secret)
	if err != nil {
		t.Fatalf("encryptTotp: %v", err)
	}
	if !strings.HasPrefix(enc, totpPrefixV2) {
		t.Fatalf("new secret must carry the v2: prefix, got %q", enc)
	}
	if got, err := st.decryptTotp(enc); err != nil || got != secret {
		t.Fatalf("v2 round-trip: got %q err %v", got, err)
	}

	// A legacy v1: secret (encrypted with the old sha256 key) must still decrypt.
	v1blob, err := auth.Encrypt(secret, st.key)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.decryptTotp(totpPrefix + v1blob); err != nil || got != secret {
		t.Fatalf("legacy v1 decrypt: got %q err %v", got, err)
	}

	// An unprefixed (pre-encryption) value is returned as-is.
	if got, _ := st.decryptTotp("PLAINTEXT"); got != "PLAINTEXT" {
		t.Fatalf("plaintext passthrough: got %q", got)
	}

	// Key separation: the HKDF at-rest key must differ from the legacy sha256 key.
	if string(st.key) == string(st.totpKey) {
		t.Fatal("totpKey must differ from the legacy key (key separation)")
	}
}
