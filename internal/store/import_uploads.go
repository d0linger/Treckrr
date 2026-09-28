package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// Import upload kinds (import_uploads.kind).
const (
	ImportUploadPayment = "payment" // bank statement for the payment import
	ImportUploadBooking = "booking" // booking CSV for the entry import
)

// ImportUploadTTL is how long an uploaded import file stays committable after
// its preview. Long enough to review and assign a monthly statement, short
// enough that bank data (third-party names and IBANs) does not linger.
const ImportUploadTTL = 2 * time.Hour

// MaxImportUploadBytes mirrors the migration's CHECK on import_uploads.content.
const MaxImportUploadBytes = 4 << 20

// maxImportUploadsPerUser bounds how many pending previews one user can park;
// the oldest beyond it are dropped, so repeated previews cannot grow the table.
const maxImportUploadsPerUser = 10

// SaveImportUpload stores an uploaded import file for a later commit and returns
// the random token naming it. yearID is 0 for uploads not tied to a year. Expired
// uploads (everyone's) and the user's surplus ones are purged in the same
// transaction, so the table stays small without a background job.
//
// The token is 128 random bits, base64url — the same shape the booking import
// used for its idempotency keys, which it keeps doing with this token.
func (s *Store) SaveImportUpload(ctx context.Context, kind string, userID, yearID int64, content []byte) (string, error) {
	if kind != ImportUploadPayment && kind != ImportUploadBooking {
		return "", fmt.Errorf("unknown import upload kind %q", kind)
	}
	if userID == 0 {
		return "", ErrNotFound
	}
	if len(content) > MaxImportUploadBytes {
		return "", fmt.Errorf("import upload of %d bytes exceeds %d", len(content), MaxImportUploadBytes)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM import_uploads WHERE created_at < now() - make_interval(secs => $1)`,
		ImportUploadTTL.Seconds()); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO import_uploads (token, kind, user_id, billing_year_id, content) VALUES ($1,$2,$3,$4,$5)`,
		token, kind, userID, nullable(yearID), content); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM import_uploads WHERE user_id=$1 AND token NOT IN (
		   SELECT token FROM import_uploads WHERE user_id=$1
		    ORDER BY created_at DESC, token LIMIT $2)`,
		userID, maxImportUploadsPerUser); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// LoadImportUpload returns the content of an unexpired upload, but only to the
// user who made it, for the same kind and billing year (0 = none). Anything else
// — unknown, expired, another user's or another year's token — is ErrNotFound.
func (s *Store) LoadImportUpload(ctx context.Context, token, kind string, userID, yearID int64) ([]byte, error) {
	var content []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT content FROM import_uploads
		  WHERE token=$1 AND kind=$2 AND user_id=$3
		    AND billing_year_id IS NOT DISTINCT FROM $4
		    AND created_at >= now() - make_interval(secs => $5)`,
		token, kind, userID, nullable(yearID), ImportUploadTTL.Seconds()).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return content, err
}
