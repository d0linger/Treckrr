package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestVerifyBelegShare_OversizedToken proves the LENGTH CAP — not an incidental
// signature failure — is what rejects an oversized share token. The token is crafted
// to be otherwise VALID (correct HMAC, parseable payload) but padded past the cap, so
// if the cap were removed it would verify successfully and this test would fail.
func TestVerifyBelegShare_OversizedToken(t *testing.T) {
	s := testServer()

	// A normal token still verifies.
	if nID, yID, ok := s.verifyBelegShare(s.signBelegShare(10, 20, time.Now().Add(time.Hour))); !ok || nID != 10 || yID != 20 {
		t.Fatalf("valid token failed to verify: nID=%d yID=%d ok=%v", nID, yID, ok)
	}

	// craft builds a share token exactly like signBelegShare, but pads the expiry with
	// `pad` leading zeros — which ParseInt still accepts — to grow the length at will.
	craft := func(pad int) string {
		payload := fmt.Sprintf("%d:%d:%s%d", 10, 20, strings.Repeat("0", pad), time.Now().Add(time.Hour).Unix())
		mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
		mac.Write([]byte("belegshare:" + payload))
		return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
			base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	// Sanity: an un-padded crafted token verifies — proving the crafting matches the
	// implementation, so the ONLY difference in the oversized case is its length.
	if _, _, ok := s.verifyBelegShare(craft(0)); !ok {
		t.Fatal("crafted (un-padded) token should verify — construction mismatch")
	}
	over := craft(300)
	if len(over) <= maxBelegShareTokenLen {
		t.Fatalf("crafted token is not oversized: len=%d", len(over))
	}
	if _, _, ok := s.verifyBelegShare(over); ok {
		t.Fatal("an otherwise-valid oversized share token must be rejected by the length cap")
	}
}

// TestVerifyPending2FA_OversizedToken — same isolation of the length cap, for the
// pending-2FA cookie token.
func TestVerifyPending2FA_OversizedToken(t *testing.T) {
	s := testServer()

	if uID, ok := s.verifyPending2FA(s.signPending2FA(42)); !ok || uID != 42 {
		t.Fatalf("valid pending-2FA token failed to verify: uID=%d ok=%v", uID, ok)
	}

	craft := func(pad int) string {
		payload := fmt.Sprintf("2fa:%d|%s%d", 42, strings.Repeat("0", pad), time.Now().Add(pending2FATTL).Unix())
		mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
		mac.Write([]byte(payload))
		return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
	}
	if _, ok := s.verifyPending2FA(craft(0)); !ok {
		t.Fatal("crafted (un-padded) pending-2FA token should verify — construction mismatch")
	}
	over := craft(300)
	if len(over) <= maxPending2FATokenLen {
		t.Fatalf("crafted token is not oversized: len=%d", len(over))
	}
	if _, ok := s.verifyPending2FA(over); ok {
		t.Fatal("an otherwise-valid oversized pending-2FA token must be rejected by the length cap")
	}
}
