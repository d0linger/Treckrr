package server

import (
	"strings"
	"testing"
	"time"
)

func TestVerifyBelegShare_OversizedToken(t *testing.T) {
	s := testServer()

	// Valid token should verify correctly.
	validToken := s.signBelegShare(10, 20, time.Now().Add(1*time.Hour))
	nID, yID, ok := s.verifyBelegShare(validToken)
	if !ok || nID != 10 || yID != 20 {
		t.Fatalf("expected valid token to verify successfully, got nID=%d, yID=%d, ok=%v", nID, yID, ok)
	}

	// Oversized token (> 200 chars) must be rejected up front.
	oversizedToken := validToken + "." + strings.Repeat("A", 250)
	_, _, ok = s.verifyBelegShare(oversizedToken)
	if ok {
		t.Fatalf("expected oversized share token to be rejected, but verifyBelegShare returned ok=true")
	}
}

func TestVerifyPending2FA_OversizedToken(t *testing.T) {
	s := testServer()

	// Valid token should verify correctly.
	validToken := s.signPending2FA(42)
	uID, ok := s.verifyPending2FA(validToken)
	if !ok || uID != 42 {
		t.Fatalf("expected valid pending 2FA token to verify, got uID=%d, ok=%v", uID, ok)
	}

	// Oversized token (> 200 chars) must be rejected up front.
	oversizedToken := validToken + "." + strings.Repeat("B", 250)
	_, ok = s.verifyPending2FA(oversizedToken)
	if ok {
		t.Fatalf("expected oversized pending 2FA token to be rejected, but verifyPending2FA returned ok=true")
	}
}
