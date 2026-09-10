package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// RetainedPayment includes a soft-deleted payment still held in the database.
// It is for subject access, not balances or ordinary payment lists.
type RetainedPayment struct {
	models.Payment
	DeletedAt *time.Time
}

// ListRetainedPayments includes deleted_at so an access export cannot silently
// omit records merely hidden from the ordinary payment history.
func (s *Store) ListRetainedPayments(ctx context.Context, yearID, neighborID int64) ([]RetainedPayment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.billing_year_id, p.neighbor_id, p.amount, p.paid_on, p.note,
		        p.method, p.invoice_id, COALESCE(iv.number,''), p.created_at, p.deleted_at
		   FROM payments p LEFT JOIN invoices iv ON iv.id=p.invoice_id
		  WHERE p.billing_year_id=$1 AND p.neighbor_id=$2 ORDER BY p.paid_on, p.id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RetainedPayment{}
	for rows.Next() {
		var p RetainedPayment
		if err := rows.Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.Amount, &p.PaidOn, &p.Note,
			&p.Method, &p.InvoiceID, &p.InvoiceNumber, &p.Created, &p.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NeighborRecurringExport is independent of billing-year membership: a stored
// template may be the only remaining record for this neighbor.
type NeighborRecurringExport struct {
	ID           int64                `json:"id"`
	Template     models.RecurTemplate `json:"template"`
	IntervalKind string               `json:"interval_kind"`
	NextRun      time.Time            `json:"next_run"`
	Active       bool                 `json:"active"`
	CreatedAt    time.Time            `json:"created_at"`
	LastRunAt    *time.Time           `json:"last_run_at,omitempty"`
}

// ListNeighborRecurringExport scopes templates directly to the subject, including
// templates whose next occurrence has no billing year yet.
func (s *Store) ListNeighborRecurringExport(ctx context.Context, neighborID int64) ([]NeighborRecurringExport, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, template, interval_kind, next_run, active, created_at, last_run_at
		   FROM recurring_entries WHERE neighbor_id=$1 ORDER BY id`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NeighborRecurringExport{}
	for rows.Next() {
		var rule NeighborRecurringExport
		var blob []byte
		if err := rows.Scan(&rule.ID, &blob, &rule.IntervalKind, &rule.NextRun,
			&rule.Active, &rule.CreatedAt, &rule.LastRunAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(blob, &rule.Template); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// NeighborMailExport includes subject correspondence and attachment metadata,
// never attachment bytes or SMTP errors (which can contain server secrets).
type NeighborMailExport struct {
	ID              int64      `json:"id"`
	BillingYearID   *int64     `json:"billing_year_id,omitempty"`
	Kind            string     `json:"kind"`
	Recipient       string     `json:"recipient"`
	Subject         string     `json:"subject"`
	Body            string     `json:"body"`
	AttachmentName  string     `json:"attachment_name,omitempty"`
	AttachmentType  string     `json:"attachment_type,omitempty"`
	AttachmentBytes int64      `json:"attachment_bytes"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	CreatedAt       time.Time  `json:"created_at"`
	SentAt          *time.Time `json:"sent_at,omitempty"`
}

// ListNeighborMailExport includes pending, failed and sent correspondence for the
// subject. Attachments are described without loading their binary contents.
func (s *Store) ListNeighborMailExport(ctx context.Context, neighborID int64) ([]NeighborMailExport, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, billing_year_id, kind, recipient, subject, body, att_name, att_type,
		        COALESCE(octet_length(att_data),0), status, attempts, created_at, sent_at
		   FROM mail_outbox WHERE neighbor_id=$1 ORDER BY id`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NeighborMailExport{}
	for rows.Next() {
		var mail NeighborMailExport
		if err := rows.Scan(&mail.ID, &mail.BillingYearID, &mail.Kind, &mail.Recipient, &mail.Subject,
			&mail.Body, &mail.AttachmentName, &mail.AttachmentType, &mail.AttachmentBytes,
			&mail.Status, &mail.Attempts, &mail.CreatedAt, &mail.SentAt); err != nil {
			return nil, err
		}
		out = append(out, mail)
	}
	return out, rows.Err()
}
