package store

import (
	"context"

	"github.com/shopspring/decimal"
)

// ---- Jahresabschluss-Checkliste (Ausbaukarte 59) ---------------------------

// ClosingCheck is one item of the pre-close review: what it looked at, how many
// rows fall through, and the names behind them. Blocking is deliberately not a
// field — the checklist informs the operator, it does not decide for them.
type ClosingCheck struct {
	Key    string // stable id, for the template
	Label  string
	Detail string   // what "open" means for this check
	Count  int      // 0 = clean
	Names  []string // at most a handful, for the hint line
	Amount decimal.Decimal
}

// Clean reports whether nothing is outstanding for this check.
func (c ClosingCheck) Clean() bool { return c.Count == 0 }

// More is how many affected rows the Names sample leaves unnamed, so the
// template can say "… und N weitere" without doing arithmetic.
func (c ClosingCheck) More() int { return c.Count - len(c.Names) }

// namesQuery runs a "name" list query and returns count + the first few names,
// so the checklist can say WHO is affected instead of only how many.
func (s *Store) namesQuery(ctx context.Context, q string, args ...any) (int, []string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var names []string
	n := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, nil, err
		}
		n++
		if len(names) < 5 {
			names = append(names, name)
		}
	}
	return n, names, rows.Err()
}

// YearClosingChecks reviews a billing year before it is closed: who still has
// bookings without a frozen invoice, which invoices were never handed over,
// what is still unpaid, and which Abschläge never got a Schlussrechnung.
func (s *Store) YearClosingChecks(ctx context.Context, yearID int64) ([]ClosingCheck, error) {
	out := make([]ClosingCheck, 0, 4)

	// 1. Bookings but no frozen invoice — the classic "forgot someone".
	n, names, err := s.namesQuery(ctx,
		`SELECT n.name FROM neighbors n
		  WHERE EXISTS (SELECT 1 FROM entries e
		                 WHERE e.neighbor_id = n.id AND e.billing_year_id = $1 AND NOT e.voided)
		    AND NOT EXISTS (SELECT 1 FROM invoices iv
		                     WHERE iv.neighbor_id = n.id AND iv.billing_year_id = $1
		                       AND iv.kind = 'invoice' AND iv.status = 'issued')
		  ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	out = append(out, ClosingCheck{
		Key: "uninvoiced", Label: "Buchungen ohne festgeschriebene Rechnung",
		Detail: "Diese Nachbarn haben Leistungen im Jahr, aber keine ausgestellte Rechnung.",
		Count:  n, Names: names,
	})

	// 2. Issued but never handed over (no beleg_send, no mail).
	n, names, err = s.namesQuery(ctx,
		`SELECT n.name FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		  WHERE iv.billing_year_id = $1 AND iv.kind = 'invoice' AND iv.status = 'issued'
		    AND NOT EXISTS (SELECT 1 FROM beleg_sends bs
		                     WHERE bs.billing_year_id = iv.billing_year_id
		                       AND bs.neighbor_id = iv.neighbor_id)
		  ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	out = append(out, ClosingCheck{
		Key: "unsent", Label: "Rechnungen nie versendet",
		Detail: "Festgeschrieben, aber kein Versand vermerkt (E-Mail, Druck oder Übergabe).",
		Count:  n, Names: names,
	})

	// 3. Still unpaid: the invoice's own remaining, per neighbor. Same shape as
	// InvoiceRemaining, aggregated — an amount, not just a count.
	rows, err := s.db.QueryContext(ctx,
		`SELECT n.name, iv.gross
		      + COALESCE((SELECT SUM(g.gross) FROM invoices g
		                   WHERE g.billing_year_id = iv.billing_year_id AND g.neighbor_id = iv.neighbor_id
		                     AND g.kind = 'gutschrift' AND g.status = 'issued'), 0)
		      + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		                   WHERE l.billing_year_id = iv.billing_year_id AND l.neighbor_id = iv.neighbor_id
		                     AND NOT l.voided), 0)
		      - COALESCE((SELECT SUM(p.amount) FROM payments p
		                   WHERE p.billing_year_id = iv.billing_year_id AND p.neighbor_id = iv.neighbor_id
		                     AND p.deleted_at IS NULL), 0) AS rest
		   FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		  WHERE iv.billing_year_id = $1 AND iv.kind = 'invoice' AND iv.status = 'issued'
		  ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	open := ClosingCheck{
		Key: "unpaid", Label: "Offene Beträge",
		Detail: "Ausgestellte Rechnungen, die noch nicht vollständig bezahlt sind.",
	}
	for rows.Next() {
		var name string
		var rest decimal.Decimal
		if err := rows.Scan(&name, &rest); err != nil {
			return nil, err
		}
		// Cent-level rounding leftovers are not an open item.
		if rest.LessThan(decimal.NewFromFloat(0.01)) {
			continue
		}
		open.Count++
		open.Amount = open.Amount.Add(rest)
		if len(open.Names) < 5 {
			open.Names = append(open.Names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out = append(out, open)

	// 4. Abschläge that never became a Schlussrechnung (Ausbaukarte 54): money
	// was requested on account and the final document is missing.
	n, names, err = s.namesQuery(ctx,
		`SELECT DISTINCT n.name FROM invoices a JOIN neighbors n ON n.id = a.neighbor_id
		  WHERE a.billing_year_id = $1 AND a.kind = 'anzahlung' AND a.status = 'issued'
		    AND NOT EXISTS (SELECT 1 FROM invoices iv
		                     WHERE iv.neighbor_id = a.neighbor_id AND iv.billing_year_id = a.billing_year_id
		                       AND iv.kind = 'invoice' AND iv.status = 'issued')
		  ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	out = append(out, ClosingCheck{
		Key: "openanzahlung", Label: "Abschläge ohne Schlussrechnung",
		Detail: "Es wurde eine Anzahlung angefordert, aber nie endabgerechnet.",
		Count:  n, Names: names,
	})
	return out, nil
}

// OpenClosingChecks counts the checks that are not clean.
func OpenClosingChecks(checks []ClosingCheck) int {
	n := 0
	for _, c := range checks {
		if !c.Clean() {
			n++
		}
	}
	return n
}
