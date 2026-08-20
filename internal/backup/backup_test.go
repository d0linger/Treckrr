package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/minio/minio-go/v7"
)

// TestWriteFileAtomicConcurrent locks the status.json corruption fix: many
// concurrent writers to the same path must never leave a partial/interleaved
// file, and no temp files may leak (each writer uses a unique temp + rename).
func TestWriteFileAtomicConcurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows cannot reliably rename-replace a target that another goroutine is
		// renaming over at the same instant (ERROR_ACCESS_DENIED/SHARING_VIOLATION);
		// os.Rename is atomic on the Linux production target, where this holds.
		t.Skip("concurrent os.Rename to the same target is unreliable on Windows; production target is Linux")
	}
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
	// The final file must be exactly one of the complete payloads — never a torn
	// or interleaved mix of two concurrent writers.
	matched := false
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("final file is not a complete single payload (corruption): %q", got)
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

// TestContentSanity pins the verify-before-promote content floor against the REAL
// pg_restore --list TOC format, so it rejects an empty/foreign/truncated dump but
// never a legitimate one (a false reject would fail-close all future backups).
func TestContentSanity(t *testing.T) {
	base := []string{
		"3; 1259 16400 TABLE public users treckrr",
		"231; 1259 16519 TABLE public neighbors treckrr",
		"233; 1259 16531 TABLE public entries treckrr",
		"251; 1259 16780 TABLE public invoices treckrr",
		"239; 1259 16627 TABLE public audit_log treckrr",
	}
	lines := append([]string{}, base...)
	for i := len(lines); i < minArchiveObjects; i++ { // pad past the floor with filler
		lines = append(lines, fmt.Sprintf("%d; 1259 %d INDEX public idx_%d treckrr", 400+i, 20000+i, i))
	}
	toc := strings.Join(lines, "\n")
	n := len(lines)

	if err := contentSanity(toc, n); err != nil {
		t.Fatalf("a valid TOC was rejected: %v", err)
	}
	if err := contentSanity(toc, minArchiveObjects-1); err == nil {
		t.Error("expected reject for object count below the floor")
	}
	missing := strings.ReplaceAll(toc, "TABLE public invoices treckrr", "TABLE public somethingelse treckrr")
	if err := contentSanity(missing, n); err == nil {
		t.Error("expected reject when a core table is missing")
	}
	// A prefix-named table must NOT satisfy a core-table check (entry_photos != entries).
	prefix := strings.ReplaceAll(toc, "TABLE public entries treckrr", "TABLE public entry_photos treckrr")
	if err := contentSanity(prefix, n); err == nil {
		t.Error("expected reject when only a prefix-named table is present")
	}
}

// TestVerifyS3ObjectRejectsBadIntegration proves the post-upload S3 verification
// catches a corrupt or incomplete off-box copy. Gated on TEST_S3_ENDPOINT; the
// happy path is covered end-to-end by `treckrr backup` against MinIO.
func TestVerifyS3ObjectRejectsBadIntegration(t *testing.T) {
	ep := os.Getenv("TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_S3_ENDPOINT not set; skipping S3 integration test")
	}
	svc := New(Options{
		EncKey: "test-key-at-least-16chars",
		S3: S3Options{
			Endpoint:  ep,
			Bucket:    os.Getenv("TEST_S3_BUCKET"),
			AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
			UseSSL:    false,
		},
	}, nil)
	ctx := context.Background()
	junk := []byte("this-is-not-a-valid-encrypted-treckrr-dump-000000")
	name := "treckrr-verify-neg.dump.enc"
	if err := svc.uploadS3(ctx, name, junk); err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	t.Cleanup(func() {
		if cl, err := svc.s3Client(); err == nil {
			_ = cl.RemoveObject(context.Background(), svc.opt.S3.Bucket, svc.opt.S3.Prefix+name, minio.RemoveObjectOptions{})
		}
	})
	// Correct size, but the stored bytes are not a decryptable archive → must reject.
	if err := svc.verifyS3Object(ctx, name, int64(len(junk))); err == nil {
		t.Error("verifyS3Object accepted a non-decryptable stored object")
	}
	// Size mismatch (as from an incomplete upload) → must reject on the size check.
	if err := svc.verifyS3Object(ctx, name, int64(len(junk))+100); err == nil {
		t.Error("verifyS3Object accepted a size mismatch (incomplete upload)")
	}
}
