package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// CalendarFeed is a user's calendar subscription; the raw token is never stored.
type CalendarFeed struct {
	Minimal    bool
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// CalendarEvent is one all-day entry of the feed.
type CalendarEvent struct {
	UID      string
	Day      time.Time
	Kind     string // invoice, installment, dunning, recurring
	Neighbor string
	Title    string
	Amount   decimal.NullDecimal
	Path     string
}

// SetCalendarFeed creates or replaces the user's feed; the previous link stops
// working at once.
func (s *Store) SetCalendarFeed(ctx context.Context, userID int64, tokenHash string, minimal bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO calendar_feeds (user_id, token_hash, minimal) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET token_hash = EXCLUDED.token_hash, minimal = EXCLUDED.minimal,
		    created_at = now(), last_used_at = NULL`, userID, tokenHash, minimal)
	return err
}

// GetCalendarFeed returns the user's feed or ErrNotFound.
func (s *Store) GetCalendarFeed(ctx context.Context, userID int64) (CalendarFeed, error) {
	var f CalendarFeed
	err := s.db.QueryRowContext(ctx, `SELECT minimal, created_at, last_used_at FROM calendar_feeds WHERE user_id = $1`, userID).
		Scan(&f.Minimal, &f.CreatedAt, &f.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// DeleteCalendarFeed revokes the user's feed.
func (s *Store) DeleteCalendarFeed(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM calendar_feeds WHERE user_id = $1`, userID)
	return err
}

// ResolveCalendarFeed maps a token hash to an active user's feed and stamps
// its use at most once per day.
func (s *Store) ResolveCalendarFeed(ctx context.Context, tokenHash string) (userID int64, minimal, ok bool, err error) {
	var usedToday bool
	err = s.db.QueryRowContext(ctx, `SELECT f.user_id, f.minimal,
		       (f.last_used_at IS NOT NULL AND f.last_used_at::date = CURRENT_DATE)
		  FROM calendar_feeds f JOIN users u ON u.id = f.user_id
		 WHERE f.token_hash = $1 AND NOT u.disabled`, tokenHash).Scan(&userID, &minimal, &usedToday)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, err
	}
	if !usedToday {
		_, _ = s.db.ExecContext(ctx, `UPDATE calendar_feeds SET last_used_at = now() WHERE user_id = $1`, userID) // best-effort
	}
	return userID, minimal, true, nil
}

// CalendarEvents collects the dated work of the coming year (and the past 30
// days): due dates of open invoices with Mahnwesen terms, installments, dunning
// grace deadlines and the next run of every active series.
func (s *Store) CalendarEvents(ctx context.Context, termDays int, now time.Time) ([]CalendarEvent, error) {
	from, until := now.AddDate(0, 0, -30), now.AddDate(1, 0, 0)
	in := func(d time.Time) bool { return !d.Before(from) && !d.After(until) }
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // read-only; no-op after Commit
	var out []CalendarEvent
	open, err := invoiceOpenRows(ctx, tx, 0, termDays)
	if err != nil {
		return nil, err
	}
	for _, row := range open {
		if row.Open.IsPositive() && in(row.DueOn) {
			out = append(out, CalendarEvent{
				UID: "invoice-" + strconv.FormatInt(row.InvoiceID, 10), Day: row.DueOn, Kind: "invoice", Neighbor: row.Name,
				Title: "Rechnung " + row.InvoiceNo + " fällig", Amount: decimal.NullDecimal{Decimal: row.Open, Valid: true},
				Path: "/neighbors/" + strconv.FormatInt(row.NeighborID, 10) + "/beleg?year=" + strconv.FormatInt(row.YearID, 10),
			})
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT 'installment', p.id, p.due_on, n.name, p.amount, p.neighbor_id, p.billing_year_id, ''
		  FROM payment_plans p JOIN neighbors n ON n.id = p.neighbor_id
		 WHERE p.due_on BETWEEN $1 AND $2 AND NOT n.anonymized
		UNION ALL
		SELECT 'dunning', d.id, d.grace_until, n.name, NULL, d.neighbor_id, d.billing_year_id, d.invoice_number
		  FROM dunning_notices d JOIN neighbors n ON n.id = d.neighbor_id
		 WHERE d.grace_until BETWEEN $1 AND $2 AND NOT n.anonymized
		UNION ALL
		SELECT 'recurring', r.id, r.next_run, n.name, NULL, r.neighbor_id, 0, COALESCE(r.template->>'task_label', '')
		  FROM recurring_entries r JOIN neighbors n ON n.id = r.neighbor_id
		 WHERE r.active AND r.next_run BETWEEN $1 AND $2 AND (r.ends_on IS NULL OR r.next_run <= r.ends_on)
		 ORDER BY 3, 1, 2`, from, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name, extra string
		var id, neighborID, yearID int64
		var day time.Time
		var amount decimal.NullDecimal
		if err := rows.Scan(&kind, &id, &day, &name, &amount, &neighborID, &yearID, &extra); err != nil {
			return nil, err
		}
		ev := CalendarEvent{UID: kind + "-" + strconv.FormatInt(id, 10), Day: day, Kind: kind, Neighbor: name, Amount: amount}
		switch kind {
		case "installment":
			ev.Title = "Rate fällig"
			ev.Path = "/neighbors/" + strconv.FormatInt(neighborID, 10) + "?year=" + strconv.FormatInt(yearID, 10) + "&view=payments"
		case "dunning":
			ev.Title = "Nachfrist endet"
			if extra != "" {
				ev.Title += " · " + extra
			}
			ev.Path = "/mahnwesen?year=" + strconv.FormatInt(yearID, 10)
		default:
			ev.Title = "Serie läuft"
			if extra != "" {
				ev.Title += " · " + extra
			}
			ev.Path = "/recurring"
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
