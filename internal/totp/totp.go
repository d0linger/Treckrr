// Package totp implements time-based one-time passwords (RFC 6238, SHA1, 6
// digits, 30s step) — compatible with common authenticator apps. No external
// dependencies.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nosec G505 -- RFC 6238 (TOTP) mandates HMAC-SHA1; required for authenticator-app compatibility
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	digits = 6
	period = 30
)

// GenerateSecret returns a new random base32 secret (no padding).
func GenerateSecret() (string, error) {
	b := make([]byte, 20) // 160-bit secret
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// code computes the TOTP for the given secret and counter.
func code(secret string, counter uint64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%0*d", digits, value%1_000_000), nil
}

// Validate reports whether the supplied code matches the secret within a ±1
// step window (to tolerate clock skew).
func Validate(secret, input string) bool {
	input = strings.TrimSpace(input)
	if len(input) != digits {
		return false
	}
	counter := uint64(time.Now().Unix() / period) //nosec G115 -- Unix time is non-negative
	for delta := -1; delta <= 1; delta++ {
		probe := counter
		if delta < 0 {
			probe -= uint64(-delta) //nosec G115 -- -delta is 1 (non-negative)
		} else {
			probe += uint64(delta) //nosec G115 -- delta is 0 or 1 (non-negative)
		}
		if c, err := code(secret, probe); err == nil && c == input {
			return true
		}
	}
	return false
}

// ProvisioningURI builds the otpauth:// URI for manual entry / QR codes.
func ProvisioningURI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("digits", fmt.Sprintf("%d", digits))
	q.Set("period", fmt.Sprintf("%d", period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
