package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestWriteFileAtomicConcurrent locks the status.json corruption fix: many
// concurrent writers to the same path must never leave a partial/interleaved
// file, and no temp files may leak (each writer uses a unique temp + rename).
func TestWriteFileAtomicConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	const n = 40
	payloads := make([][]byte, n)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf(`{"writer":%d,"ok":true}`, i))
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := writeFileAtomic(path, payloads[i]); err != nil {
					t.Errorf("writeFileAtomic: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("final file is not valid JSON (corruption): %q: %v", got, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(leftovers) != 0 {
		t.Errorf("leftover temp files: %v", leftovers)
	}
}

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
	if !strings.HasPrefix(string(enc), magicV2) {
		t.Fatalf("new ciphertext must carry the TRKBK2 header")
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

// TestDecryptLegacyTRKBK1 locks backward compatibility: a dump written in the
// old TRKBK1 format (bare sha256(secret) key, no salt) must still decrypt with
// the raw secret after the switch to Argon2id/TRKBK2.
func TestDecryptLegacyTRKBK1(t *testing.T) {
	secret := []byte("legacy-secret-legacy-secret-1234")
	plain := []byte("old encrypted database dump payload")

	// Build a TRKBK1 blob exactly as the pre-Argon2id code did.
	k := sha256.Sum256(secret)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	legacy := append([]byte(magicV1), nonce...)
	legacy = gcm.Seal(legacy, nonce, plain, nil)

	// The new decrypt (given the raw secret) must read the old format.
	got, err := decrypt(legacy, secret)
	if err != nil {
		t.Fatalf("legacy TRKBK1 decrypt failed: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("legacy round-trip mismatch")
	}
	if _, err := DecryptWith(legacy, string(secret)); err != nil {
		t.Fatalf("DecryptWith on legacy dump failed: %v", err)
	}
}
