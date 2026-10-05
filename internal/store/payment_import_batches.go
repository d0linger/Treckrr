package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

const (
	PaymentImportPending   = "pending"
	PaymentImportBooked    = "booked"
	PaymentImportDuplicate = "duplicate"
	PaymentImportUnmatched = "unmatched"
	PaymentImportSkipped   = "skipped"
	PaymentImportReversed  = "reversed"
)

var (
	// ErrImportedPaymentImmutable requires imported money to be corrected with
	// an auditable counter-entry instead of changing or deleting history.
	ErrImportedPaymentImmutable = errors.New("imported payments are immutable")
	// ErrImportAlreadyReversed makes a repeated correction a harmless refusal.
	ErrImportAlreadyReversed = errors.New("imported payment already reversed")
)

// PaymentImportRowInput is the immutable parsed and matched state captured
// before a payment import starts writing individual payments.
type PaymentImportRowInput struct {
	RowNo                           int
	TransactionHash                 string
	TransactionDate                 time.Time
	Amount                          decimal.Decimal
	Reference, PayerName, PayerIBAN string
	MatchMethod, Status, Reason     string
	YearID, NeighborID, InvoiceID   int64
}

// PaymentImportBatch is one uploaded statement processing run.
type PaymentImportBatch struct {
	ID                                                int64
	SourceSHA256, UploadedBy, Status                  string
	TotalRows, BookedRows, SkippedRows, DuplicateRows int
	UnmatchedRows, ReversedRows                       int
	CreatedAt                                         time.Time
	FinishedAt                                        *time.Time
}

// ShortHash is the compact, still-identifiable source fingerprint shown in UI.
func (b PaymentImportBatch) ShortHash() string {
	if len(b.SourceSHA256) <= 12 {
		return b.SourceSHA256
	}
	return b.SourceSHA256[:12]
}

// StatusLabel returns the operator-facing German batch status.
func (b PaymentImportBatch) StatusLabel() string {
	switch b.Status {
	case "completed":
		return "abgeschlossen"
	case "failed":
		return "mit Fehler beendet"
	default:
		return "in Bearbeitung"
	}
}

// PaymentImportRow is one parsed credit and its durable outcome.
type PaymentImportRow struct {
	ID, BatchID, PaymentID, ReversalPaymentID int64
	RowNo                                     int
	TransactionHash                           string
	TransactionDate                           *time.Time
	Amount                                    decimal.Decimal
	Reference, PayerName, PayerIBAN           string
	MatchMethod, Status, Reason               string
	YearID, NeighborID, InvoiceID             int64
}

// Correctable reports whether the row has a booked payment without a counter-entry.
func (r PaymentImportRow) Correctable() bool {
	return r.Status == PaymentImportBooked && r.PaymentID != 0 && r.ReversalPaymentID == 0
}

// StatusLabel returns the operator-facing German row status.
func (r PaymentImportRow) StatusLabel() string {
	switch r.Status {
	case PaymentImportBooked:
		return "verbucht"
	case PaymentImportDuplicate:
		return "bereits importiert"
	case PaymentImportUnmatched:
		return "nicht zugeordnet"
	case PaymentImportSkipped:
		return "übersprungen"
	case PaymentImportReversed:
		return "korrigiert"
	default:
		return "offen"
	}
}

// CreatePaymentImportBatch records the source fingerprint and every parsed row
// before payments are booked. Returned row ids correspond to inputs by index.
func (s *Store) CreatePaymentImportBatch(ctx context.Context, userID int64, sourceSHA256 string, rows []PaymentImportRowInput) (int64, []int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var batchID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO payment_import_batches (source_sha256, uploaded_by, total_rows)
		VALUES ($1,$2,$3) RETURNING id`, sourceSHA256, nullable(userID), len(rows)).Scan(&batchID); err != nil {
		return 0, nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		var date any
		if !row.TransactionDate.IsZero() {
			date = row.TransactionDate
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO payment_import_rows
			       (batch_id, row_no, transaction_hash, transaction_date, amount,
			        reference, payer_name, payer_iban, match_method,
			        billing_year_id, neighbor_id, invoice_id, status, reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			RETURNING id`, batchID, row.RowNo, row.TransactionHash, date, row.Amount,
			row.Reference, row.PayerName, row.PayerIBAN, row.MatchMethod,
			nullable(row.YearID), nullable(row.NeighborID), nullable(row.InvoiceID), row.Status, row.Reason).Scan(&id); err != nil {
			return 0, nil, err
		}
		ids = append(ids, id)
	}
	if err := addAuditTx(ctx, tx, "payment_import_batch", "payment_import_batch", strconv.FormatInt(batchID, 10),
		fmt.Sprintf("source_sha256=%s; rows=%d", sourceSHA256, len(rows))); err != nil {
		return 0, nil, err
	}
	return batchID, ids, tx.Commit()
}

// MarkPaymentImportRowSkipped stores a business refusal without losing the
// parsed row. Only a still-pending row can transition to skipped.
func (s *Store) MarkPaymentImportRowSkipped(ctx context.Context, rowID int64, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE payment_import_rows SET status='skipped', reason=$2
		 WHERE id=$1 AND status='pending'`, rowID, reason)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func refreshPaymentImportBatchTx(ctx context.Context, tx *sql.Tx, batchID int64, status string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE payment_import_batches b SET
		       status=$2,
		       booked_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='booked'),
		       skipped_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='skipped'),
		       duplicate_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='duplicate'),
		       unmatched_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='unmatched'),
		       reversed_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='reversed'),
		       finished_at=CASE WHEN $2='processing' THEN NULL ELSE now() END
		 WHERE b.id=$1`, batchID, status)
	return err
}

// refreshPaymentImportBatchCountersTx updates counters without changing terminal metadata.
func refreshPaymentImportBatchCountersTx(ctx context.Context, tx *sql.Tx, batchID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE payment_import_batches b SET
		       booked_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='booked'),
		       skipped_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='skipped'),
		       duplicate_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='duplicate'),
		       unmatched_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='unmatched'),
		       reversed_rows=(SELECT count(*) FROM payment_import_rows r WHERE r.batch_id=b.id AND r.status='reversed')
		 WHERE b.id=$1`, batchID)
	return err
}

// FinishPaymentImportBatch closes a run and refreshes its outcome counters.
func (s *Store) FinishPaymentImportBatch(ctx context.Context, batchID int64, failed bool) error {
	status := "completed"
	if failed {
		status = "failed"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := refreshPaymentImportBatchTx(ctx, tx, batchID, status); err != nil {
		return err
	}
	return tx.Commit()
}

const paymentImportBatchCols = `
	b.id, b.source_sha256, COALESCE(u.username,''), b.status,
	b.total_rows, b.booked_rows, b.skipped_rows, b.duplicate_rows,
	b.unmatched_rows, b.reversed_rows, b.created_at, b.finished_at`

func scanPaymentImportBatch(row interface{ Scan(...any) error }) (PaymentImportBatch, error) {
	var b PaymentImportBatch
	var finished sql.NullTime
	err := row.Scan(&b.ID, &b.SourceSHA256, &b.UploadedBy, &b.Status,
		&b.TotalRows, &b.BookedRows, &b.SkippedRows, &b.DuplicateRows,
		&b.UnmatchedRows, &b.ReversedRows, &b.CreatedAt, &finished)
	if finished.Valid {
		b.FinishedAt = &finished.Time
	}
	return b, err
}

// ListPaymentImportBatches returns the newest import runs for the import center.
func (s *Store) ListPaymentImportBatches(ctx context.Context, limit int) ([]PaymentImportBatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+paymentImportBatchCols+`
		FROM payment_import_batches b LEFT JOIN users u ON u.id=b.uploaded_by
		ORDER BY b.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentImportBatch
	for rows.Next() {
		b, err := scanPaymentImportBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetPaymentImportBatch returns one run and all of its parsed rows.
func (s *Store) GetPaymentImportBatch(ctx context.Context, id int64) (PaymentImportBatch, []PaymentImportRow, error) {
	b, err := scanPaymentImportBatch(s.db.QueryRowContext(ctx, `SELECT `+paymentImportBatchCols+`
		FROM payment_import_batches b LEFT JOIN users u ON u.id=b.uploaded_by WHERE b.id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil, ErrNotFound
	}
	if err != nil {
		return b, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, batch_id, row_no, transaction_hash, transaction_date, amount,
		       reference, payer_name, payer_iban, match_method,
		       COALESCE(billing_year_id,0), COALESCE(neighbor_id,0), COALESCE(invoice_id,0),
		       status, reason, COALESCE(payment_id,0), COALESCE(reversal_payment_id,0)
		  FROM payment_import_rows WHERE batch_id=$1 ORDER BY row_no`, id)
	if err != nil {
		return b, nil, err
	}
	defer rows.Close()
	var out []PaymentImportRow
	for rows.Next() {
		var r PaymentImportRow
		var date sql.NullTime
		if err := rows.Scan(&r.ID, &r.BatchID, &r.RowNo, &r.TransactionHash, &date, &r.Amount,
			&r.Reference, &r.PayerName, &r.PayerIBAN, &r.MatchMethod,
			&r.YearID, &r.NeighborID, &r.InvoiceID, &r.Status, &r.Reason,
			&r.PaymentID, &r.ReversalPaymentID); err != nil {
			return b, nil, err
		}
		if date.Valid {
			r.TransactionDate = &date.Time
		}
		out = append(out, r)
	}
	return b, out, rows.Err()
}

// ReverseImportedPayment records an equal negative payment and retains both
// rows. It never edits or soft-deletes the imported payment.
func (s *Store) ReverseImportedPayment(ctx context.Context, rowID int64) (int64, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var yearID, neighborID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT p.billing_year_id, p.neighbor_id
		  FROM payment_import_rows r JOIN payments p ON p.id=r.payment_id
		 WHERE r.id=$1`, rowID).Scan(&yearID, &neighborID); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	} else if err != nil {
		return 0, 0, err
	}
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, false, false)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, ErrNotFound
	}
	var batchID, paymentID int64
	var rowNo int
	var amount decimal.Decimal
	var paidOn time.Time
	var invoiceID sql.NullInt64
	var status string
	var reversalID sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT r.batch_id, r.row_no, r.status, r.reversal_payment_id, p.id, p.amount, p.paid_on, p.invoice_id
		  FROM payment_import_rows r JOIN payments p ON p.id=r.payment_id
		 WHERE r.id=$1 FOR UPDATE OF r, p`, rowID).Scan(
		&batchID, &rowNo, &status, &reversalID, &paymentID, &amount, &paidOn, &invoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, err
	}
	if status == PaymentImportReversed || reversalID.Valid {
		return 0, batchID, ErrImportAlreadyReversed
	}
	if status != PaymentImportBooked {
		return 0, batchID, ErrNotFound
	}
	note := fmt.Sprintf("Korrektur Bankimport #%d / Position %d", batchID, rowNo)
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO payments
		       (billing_year_id, neighbor_id, amount, paid_on, note, method, invoice_id, reversal_of_payment_id)
		VALUES ($1,$2,$3,CURRENT_DATE,$4,'Korrektur',$5,$6)
		RETURNING id`, yearID, neighborID, amount.Neg(), note, nullable(invoiceID.Int64), paymentID).Scan(&reversalID); err != nil {
		return 0, batchID, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE payment_import_rows
		   SET status='reversed', reason='Gegenbuchung', reversal_payment_id=$2
		 WHERE id=$1`, rowID, reversalID.Int64); err != nil {
		return 0, batchID, err
	}
	if err := addAuditTx(ctx, tx, "payment_import_reverse", "payment", strconv.FormatInt(reversalID.Int64, 10),
		fmt.Sprintf("reversal_of=%d; amount=%s; original_paid_on=%s", paymentID, amount.Neg().StringFixed(2), paidOn.Format("2006-01-02"))); err != nil {
		return 0, batchID, err
	}
	if err := refreshPaymentImportBatchCountersTx(ctx, tx, batchID); err != nil {
		return 0, batchID, err
	}
	return reversalID.Int64, batchID, tx.Commit()
}
