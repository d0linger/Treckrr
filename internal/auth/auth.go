// Package auth handles password hashing and session token generation.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrPasswordTooShort reports a password shorter than the shared minimum.
	ErrPasswordTooShort = errors.New("password must be at least 8 bytes")
	// ErrPasswordTooLong reports a password that bcrypt cannot represent fully.
	ErrPasswordTooLong = errors.New("password must be at most 72 bytes")
	// ErrPasswordComplexity reports a password without both an ASCII letter and digit.
	ErrPasswordComplexity = errors.New("password must contain a letter and a digit")
)

// ValidatePassword applies the password policy shared by every credential-setting
// path. Length is measured in bytes because bcrypt's input limit is byte-based.
func ValidatePassword(pw string) error {
	if len(pw) < 8 {
		return ErrPasswordTooShort
	}
	if len(pw) > 72 {
		return ErrPasswordTooLong
	}
	var hasLetter, hasDigit bool
	for _, c := range pw {
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrPasswordComplexity
	}
	return nil
}

// HashPassword validates and returns a bcrypt hash of the plaintext password.
func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword reports whether the plaintext matches the stored hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NewToken returns a cryptographically random 256-bit session token (hex).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Encrypt encrypts plaintext using AES-GCM with the given 32-byte key.
// The result is base64(nonce + ciphertext).
func Encrypt(plaintext string, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts the base64-encoded ciphertext (nonce + encrypted) using
// AES-GCM with the given 32-byte key.
func Decrypt(encoded string, key []byte) (string, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(b) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := b[:ns], b[ns:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
