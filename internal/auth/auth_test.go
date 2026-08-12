package auth

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestEncryption(t *testing.T) {
	key := sha256.Sum256([]byte("test-secret-key"))
	plaintext := "my-secret-totp-token"

	encrypted, err := Encrypt(plaintext, key[:])
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if encrypted == plaintext {
		t.Fatal("Encrypted text is same as plaintext")
	}

	decrypted, err := Decrypt(encrypted, key[:])
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Decrypted text %q != plaintext %q", decrypted, plaintext)
	}
}

func TestEncryptionTampering(t *testing.T) {
	key := sha256.Sum256([]byte("test-secret-key"))
	plaintext := "my-secret-totp-token"

	encrypted, _ := Encrypt(plaintext, key[:])

	// Tamper with ciphertext
	b := []byte(encrypted)
	if len(b) > 0 {
		b[len(b)-1] ^= 0xFF
	}
	tampered := string(b)

	_, err := Decrypt(tampered, key[:])
	if err == nil {
		t.Fatal("Decrypt should fail on tampered ciphertext")
	}
}

func TestEncryptionWrongKey(t *testing.T) {
	key1 := sha256.Sum256([]byte("key-1"))
	key2 := sha256.Sum256([]byte("key-2"))
	plaintext := "my-secret-totp-token"

	encrypted, _ := Encrypt(plaintext, key1[:])

	_, err := Decrypt(encrypted, key2[:])
	if err == nil {
		t.Fatal("Decrypt should fail with wrong key")
	}
}

func TestLooksLikeRecoveryCode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "standard recovery code format",
			input: "ABCD-EFGH-IJKL-MNOP",
			want:  true,
		},
		{
			name:  "unformatted valid recovery code",
			input: "abcdefghijklmnop",
			want:  true,
		},
		{
			name:  "totp code is too short",
			input: "123456",
			want:  false,
		},
		{
			name:  "empty input",
			input: "",
			want:  false,
		},
		{
			name:  "input is exactly 100 characters",
			input: strings.Repeat("A", 100),
			want:  true,
		},
		{
			name:  "overly long input should be rejected immediately",
			input: strings.Repeat("A", 101),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LooksLikeRecoveryCode(tt.input)
			if got != tt.want {
				t.Errorf("LooksLikeRecoveryCode(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
