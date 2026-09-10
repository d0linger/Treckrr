package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/db"
)

// Changing BACKUP_ENCRYPTION_KEY used to orphan the whole archive: new dumps got
// the new key, old ones could no longer be opened, and nothing said so. These
// tests pin the rotation and — more importantly — that a failed rotation leaves
// every dump exactly as it was.
func TestRotateKeyIntegration(t *testing.T) {
	requirePGTools(t)
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const oldKey = "old-backup-key-at-least-32-bytes!!!!"
	const newKey = "new-backup-key-at-least-32-bytes!!!!"
	dir := t.TempDir()

	// Write two dumps under the OLD key, the way the service normally would.
	oldSvc := backup.New(backup.Options{DatabaseURL: url, EncKey: oldKey, Dir: dir}, pool)
	// Explicit names: CreateEncrypted's timestamp has second resolution, so two
	// dumps taken back to back would otherwise land on the same filename.
	for _, name := range []string{"treckrr-20260101-010101.dump.enc", "treckrr-20260101-020202.dump.enc"} {
		enc, _, err := oldSvc.CreateEncrypted(ctx)
		if err != nil {
			t.Fatalf("create dump: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), enc, 0o600); err != nil { //nolint:gosec // G703: name is from the fixed list above, dir from t.TempDir()
			t.Fatalf("write dump: %v", err)
		}
	}

	newSvc := backup.New(backup.Options{DatabaseURL: url, EncKey: newKey, Dir: dir}, pool)

	// Before rotation the new key cannot open them — the failure this prevents.
	files, err := newSvc.List()
	if err != nil || len(files) != 2 {
		t.Fatalf("list: %v (n=%d)", err, len(files))
	}
	encBefore, err := newSvc.Open(files[0].Name)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := backup.DecryptWith(encBefore, newKey); err == nil {
		t.Fatal("fixture is wrong: the new key already opens the old dump")
	}

	res, err := newSvc.RotateKey(ctx, oldKey)
	if err != nil {
		t.Fatalf("rotate: %v (skipped: %v)", err, res.Skipped)
	}
	if len(res.Rotated) != 2 {
		t.Errorf("rotated %d dumps, want 2 (skipped: %v)", len(res.Rotated), res.Skipped)
	}
	for _, f := range files {
		enc, err := newSvc.Open(f.Name)
		if err != nil {
			t.Fatalf("open after rotate: %v", err)
		}
		if _, err := backup.DecryptWith(enc, newKey); err != nil {
			t.Errorf("%s does not open with the new key after rotation: %v", f.Name, err)
		}
		if _, err := backup.DecryptWith(enc, oldKey); err == nil {
			t.Errorf("%s still opens with the OLD key — it was not re-encrypted", f.Name)
		}
	}

	// Resumable: a second run finds nothing to do and says so instead of failing.
	res2, err := newSvc.RotateKey(ctx, oldKey)
	if err == nil {
		t.Error("a second rotation should report that nothing could be rotated")
	}
	if len(res2.Rotated) != 0 || len(res2.Skipped) != 2 {
		t.Errorf("second run: rotated=%d skipped=%d, want 0/2", len(res2.Rotated), len(res2.Skipped))
	}

	// The guard against rotating to the same key.
	if _, err := newSvc.RotateKey(ctx, newKey); err == nil {
		t.Error("rotating to the same key should be refused")
	}
}

// A dump that cannot be read with the stated previous key must be left ALONE:
// the operator ends up with "some files still use the old key", never with a
// destroyed recovery point.
func TestRotateKeyLeavesUnreadableDumpsUntouchedIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	dir := t.TempDir()
	// A file that is not decryptable with any key we will present.
	junk := filepath.Join(dir, "treckrr-20260101-000000.dump.enc")
	original := []byte("not an encrypted dump at all")
	if err := os.WriteFile(junk, original, 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	svc := backup.New(backup.Options{
		DatabaseURL: url, EncKey: "new-backup-key-at-least-32-bytes!!!!", Dir: dir,
	}, pool)
	res, err := svc.RotateKey(ctx, "old-backup-key-at-least-32-bytes!!!!")
	if err == nil {
		t.Error("rotation with nothing rotatable should report an error")
	}
	if len(res.Rotated) != 0 {
		t.Errorf("rotated %v, want nothing", res.Rotated)
	}
	after, err := os.ReadFile(junk) //nolint:gosec // G304: path built by this test
	if err != nil {
		t.Fatalf("the file was removed: %v", err)
	}
	if string(after) != string(original) {
		t.Error("an unrotatable dump was modified — a failed rotation must be a no-op")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.staging")); len(leftovers) != 0 {
		t.Errorf("staging files left behind: %v", leftovers)
	}
}
