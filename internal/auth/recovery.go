package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

var recoveryEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateRecoveryCodes returns `count` human-friendly one-time recovery codes
// (formatted for display, e.g. "ABCD-EFGH-IJKL-MNOP") together with their
// hashes for storage.
func GenerateRecoveryCodes(count int) (plain, hashes []string, err error) {
	plain = make([]string, 0, count)
	hashes = make([]string, 0, count)
	for i := 0; i < count; i++ {
		b := make([]byte, 10) // 80 bits -> 16 base32 chars
		if _, err = rand.Read(b); err != nil {
			return nil, nil, err
		}
		display := groupCode(recoveryEnc.EncodeToString(b))
		plain = append(plain, display)
		hashes = append(hashes, HashRecoveryCode(display))
	}
	return plain, hashes, nil
}

// NormalizeRecoveryCode removes separators/whitespace and upper-cases a code so
// it compares regardless of how the user typed it.
func NormalizeRecoveryCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// HashRecoveryCode returns the SHA-256 hex hash of the normalized code. Recovery
// codes carry enough entropy that a fast hash is sufficient and permits a direct
// indexed lookup at verification time.
func HashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// LooksLikeRecoveryCode reports whether the input resembles a recovery code
// rather than a 6-digit TOTP (used to route login verification).
func LooksLikeRecoveryCode(s string) bool {
	return len(NormalizeRecoveryCode(s)) >= 12
}

func groupCode(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}
