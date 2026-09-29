package store

import (
	"context"
	"time"
)

// CommunicationEvent is one payload-free event in a neighbor's communication
// history. Message bodies, tokens and transport errors are deliberately absent.
type CommunicationEvent struct {
	OccurredAt time.Time
	Year       int
	Kind       string
	Title      string
	Detail     string
	Status     string
	Reference  int64
}

// NeighborCommunication returns a newest-first read model assembled from
// existing document, delivery, reminder, outbox and share records.
func (s *Store) NeighborCommunication(ctx context.Context, neighborID int64, limit int) ([]CommunicationEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT occurred_at, year, kind, title, detail, status, reference
		FROM (
			SELECT i.created_at AS occurred_at, y.year, 'document'::text AS kind,
			       CASE i.kind WHEN 'invoice' THEN 'Rechnung festgeschrieben'
			         WHEN 'gutschrift' THEN 'Gutschrift erstellt'
			         WHEN 'storno' THEN 'Storno erstellt'
			         WHEN 'anzahlung' THEN 'Anzahlung angefordert'
			         ELSE 'Dokument erstellt' END AS title,
			       i.number AS detail, i.status, i.id AS reference
			  FROM invoices i JOIN billing_years y ON y.id=i.billing_year_id
			 WHERE i.neighbor_id=$1
			UNION ALL
			SELECT bs.sent_at, y.year, 'delivery', 'Beleg übergeben', bs.channel, 'sent', bs.id
			  FROM beleg_sends bs JOIN billing_years y ON y.id=bs.billing_year_id
			 WHERE bs.neighbor_id=$1
			UNION ALL
			SELECT d.sent_at, y.year, 'dunning',
			       CASE d.stage WHEN 0 THEN 'Zahlungserinnerung' ELSE d.stage::text||'. Mahnung' END,
			       d.channel, 'sent', d.id
			  FROM dunning_notices d JOIN billing_years y ON y.id=d.billing_year_id
			 WHERE d.neighbor_id=$1
			UNION ALL
			SELECT COALESCE(m.sent_at,m.created_at), COALESCE(y.year,0), 'mail', m.subject,
			       m.recipient, m.status, m.id
			  FROM mail_outbox m LEFT JOIN billing_years y ON y.id=m.billing_year_id
			 WHERE m.neighbor_id=$1
			UNION ALL
			SELECT sh.created_at, y.year, 'share', 'Freigabelink erstellt',
			       'widerrufbar', CASE WHEN sh.expires_at>now() THEN 'active' ELSE 'expired' END, sh.id
			  FROM beleg_shares sh JOIN billing_years y ON y.id=sh.billing_year_id
			 WHERE sh.neighbor_id=$1
			UNION ALL
			SELECT sh.last_used_at, y.year, 'share', 'Freigabelink geöffnet',
			       'öffentlicher Belegzugriff', 'used', sh.id
			  FROM beleg_shares sh JOIN billing_years y ON y.id=sh.billing_year_id
			 WHERE sh.neighbor_id=$1 AND sh.last_used_at IS NOT NULL
		) event
		ORDER BY occurred_at DESC, reference DESC
		LIMIT $2`, neighborID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CommunicationEvent, 0)
	for rows.Next() {
		var event CommunicationEvent
		if err := rows.Scan(&event.OccurredAt, &event.Year, &event.Kind, &event.Title,
			&event.Detail, &event.Status, &event.Reference); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
