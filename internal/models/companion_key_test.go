package models

import (
	"strings"
	"testing"
)

// The companion's key is derived from the machine booking's key, and the whole
// pair rests on that derivation being collision-free: both halves are inserted
// in ONE transaction against a unique index, so a derived key that equals the
// machine's own key makes the companion's insert a no-op and commits the pair
// half-booked — machine hours billed, the helper's Mannstunden silently gone.
func TestCompanionKeyNeverCollidesWithItsBase(t *testing.T) {
	// The truncating derivation this replaced collapsed exactly here: a base of
	// 98 characters and that same base with the suffix already appended both
	// produced the identical key.
	for n := 90; n <= maxIdempotencyKeyLen; n++ {
		base := strings.Repeat("a", n)
		withSuffix := base
		if len(withSuffix) > maxIdempotencyKeyLen-2 {
			withSuffix = withSuffix[:maxIdempotencyKeyLen-2]
		}
		withSuffix += "-p"
		for _, k := range []string{base, withSuffix} {
			got := CompanionKey(k)
			if got == k {
				t.Errorf("CompanionKey(%d chars) returned its own base — the pair would commit half-booked", len(k))
			}
			if len(got) > maxIdempotencyKeyLen {
				t.Errorf("CompanionKey(%d chars) = %d chars, over the column's limit", len(k), len(got))
			}
		}
		if a, b := CompanionKey(base), CompanionKey(withSuffix); a == b && base != withSuffix {
			t.Errorf("two different keys of length %d/%d derive the same companion key %q", len(base), len(withSuffix), a)
		}
	}
}

// Keys the client actually sends are 36-character UUIDs. Their derivation must
// stay byte-identical, or a batch already sitting in an offline queue would
// replay against a different companion key and book the helper's hours twice.
func TestCompanionKeyStableForClientKeys(t *testing.T) {
	if got := CompanionKey(""); got != "" {
		t.Errorf("CompanionKey(\"\") = %q, want \"\" — an online submit carries no key", got)
	}
	uuid := "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	if got := CompanionKey(uuid); got != uuid+"-p" {
		t.Errorf("CompanionKey(uuid) = %q, want %q", got, uuid+"-p")
	}
}
