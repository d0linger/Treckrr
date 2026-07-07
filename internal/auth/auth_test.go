package auth

import (
	"crypto/sha256"
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
