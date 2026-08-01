package backup

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	cases := map[string]bool{
		"treckrr-2026-08-01-030000.dump.enc": true,
		"../../etc/passwd":                   false,
		"treckrr-x.dump.enc/../y":            false,
		"treckrr-../evil.dump.enc":           false,
		"":                                   false,
		"foo.dump.enc":                       false, // missing treckrr- prefix
		"treckrr-x.txt":                      false, // missing .dump.enc suffix
	}
	for name, want := range cases {
		if got := validName(name); got != want {
			t.Errorf("validName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	k := sha256.Sum256([]byte("a-sufficiently-long-backup-key!!"))
	plain := bytes.Repeat([]byte("PII+billing\x00\x01\xff"), 5000) // ~65 KB binary
	enc, err := encrypt(plain, k[:])
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(string(enc), magic) {
		t.Fatalf("ciphertext missing magic header")
	}
	if bytes.Contains(enc, plain) {
		t.Fatalf("plaintext leaked into ciphertext")
	}
	got, err := decrypt(enc, k[:])
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	k1 := sha256.Sum256([]byte("key-one-key-one-key-one-key-one!"))
	k2 := sha256.Sum256([]byte("key-two-key-two-key-two-key-two!"))
	enc, err := encrypt([]byte("secret dump"), k1[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(enc, k2[:]); err == nil {
		t.Fatal("expected decrypt to fail with the wrong key")
	}
}

func TestDecryptRejectsNonBackup(t *testing.T) {
	k := sha256.Sum256([]byte("some-backup-key-some-backup-key!"))
	if _, err := decrypt([]byte("not a backup at all"), k[:]); err == nil {
		t.Fatal("expected decrypt to reject a file without the magic header")
	}
}

func TestDecryptRejectsTruncation(t *testing.T) {
	k := sha256.Sum256([]byte("truncation-test-key-truncation!!"))
	enc, err := encrypt(bytes.Repeat([]byte("x"), 1000), k[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(enc[:len(enc)-5], k[:]); err == nil {
		t.Fatal("expected decrypt to fail on a truncated backup")
	}
}
