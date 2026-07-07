package totp

import (
	"testing"
	"time"
)

// TestValidateEmptySecretFailsClosed guards against a 2FA bypass: an empty
// secret decodes to an empty HMAC key whose code is trivially computable, so
// Validate must reject it regardless of the supplied input (including the code
// that would match an empty key).
func TestValidateEmptySecretFailsClosed(t *testing.T) {
	for _, input := range []string{"", "000000", "123456"} {
		if Validate("", input) {
			t.Fatalf("Validate(\"\", %q) = true, must fail closed", input)
		}
	}
	// The code an attacker would compute for the empty key must also be rejected.
	if c, err := code("", uint64(0)); err == nil {
		if Validate("", c) {
			t.Fatal("empty-secret code was accepted; fail-open bypass")
		}
	}
}

// TestValidateRoundTrip sanity-checks that a real secret validates its own
// current code.
func TestValidateRoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	counter := uint64(time.Now().Unix() / period)
	c, err := code(secret, counter)
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	if !Validate(secret, c) {
		t.Fatal("valid current code was rejected")
	}
}
