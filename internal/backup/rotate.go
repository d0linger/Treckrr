package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// RotateResult summarizes a key rotation. Current holds dumps a previous run
// already rotated; Skipped holds genuine failures that still need the old key.
type RotateResult struct {
	Rotated []string
	Current []string
	Skipped []string
}

// ErrRotationIncomplete reports a rotation that left at least one dump on the
// previous key. Successfully rotated files stay rotated, and a rerun resumes.
var ErrRotationIncomplete = errors.New("key rotation incomplete")

// RotateKey re-encrypts every dump in the backup directory from oldSecret to the
// service's current key.
//
// Without this, changing BACKUP_ENCRYPTION_KEY silently orphans the entire
// archive: new dumps use the new key, old ones can no longer be opened, and the
// operator discovers it at the worst possible moment. Rotation is deliberately a
// CLI operation — it rewrites every recovery point, which is not a button.
//
// Safety properties, in order of importance:
//   - Nothing is overwritten until the re-encrypted bytes have been decrypted
//     again with the NEW key and validated as a restorable archive. A dump that
//     fails is left untouched and reported, so a partial rotation degrades to
//     "some files still use the old key", never to "some files are unreadable".
//   - Each file is replaced atomically (staging file + rename), so an interrupted
//     run cannot leave a half-written dump in place.
//   - A file that already opens with the new key is left as is, which makes the
//     whole operation resumable: run it again after fixing whatever failed.
//   - Any failed dump makes the call return ErrRotationIncomplete, even when
//     others rotated: a nil error is what tells the operator (or automation)
//     that no local dump needs the previous key anymore.
func (s *Service) RotateKey(ctx context.Context, oldSecret string) (RotateResult, error) {
	var res RotateResult
	ctx, release, err := s.AcquireWork(ctx)
	if err != nil {
		return res, err
	}
	defer release()
	if !s.Enabled() {
		return res, ErrDisabled
	}
	if oldSecret == "" {
		return res, errors.New("rotate: the previous key is required")
	}
	if oldSecret == string(s.secret) {
		return res, errors.New("rotate: the previous key equals the current one — nothing to do")
	}

	files, err := filepath.Glob(filepath.Join(s.opt.Dir, "treckrr-*.dump.enc"))
	if err != nil {
		return res, err
	}
	sort.Strings(files)

	for _, path := range files {
		name := filepath.Base(path)
		enc, err := s.readFile(path)
		if err != nil {
			res.Skipped = append(res.Skipped, name+": "+err.Error())
			continue
		}
		// Already on the new key? Then a previous run got this far; leave it.
		if _, derr := decrypt(enc, s.secret); derr == nil {
			res.Current = append(res.Current, name)
			continue
		}
		raw, err := decrypt(enc, []byte(oldSecret))
		if err != nil {
			res.Skipped = append(res.Skipped, name+": not readable with the previous key")
			continue
		}
		reenc, err := encrypt(raw, s.secret)
		if err != nil {
			res.Skipped = append(res.Skipped, name+": re-encrypt failed: "+err.Error())
			continue
		}
		// Prove the new bytes are both decryptable with the new key AND still a
		// valid archive BEFORE anything on disk changes.
		if err := s.verifyRestorable(ctx, reenc); err != nil {
			res.Skipped = append(res.Skipped, name+": verification after re-encryption failed: "+err.Error())
			continue
		}
		// A canceled operation (Ctrl-C, or the work lease was lost) replaces
		// nothing more; the dump stays on the old key. This only narrows the
		// window — it is no proof the lease is still held during the write.
		if ctx.Err() != nil {
			res.Skipped = append(res.Skipped, name+": not replaced, rotation canceled: "+context.Cause(ctx).Error())
			continue
		}
		staging := path + ".staging"
		if err := writeFileAtomic(staging, reenc); err != nil {
			res.Skipped = append(res.Skipped, name+": write failed: "+err.Error())
			continue
		}
		if err := durableRename(staging, path); err != nil {
			_ = os.Remove(staging)
			res.Skipped = append(res.Skipped, name+": replace failed: "+err.Error())
			continue
		}
		res.Rotated = append(res.Rotated, name)
		slog.Info("backup re-encrypted with the new key", "file", name)
	}
	if len(res.Skipped) > 0 {
		return res, fmt.Errorf("%w: %d of %d dumps still need the previous key", ErrRotationIncomplete, len(res.Skipped), len(files))
	}
	return res, nil
}
