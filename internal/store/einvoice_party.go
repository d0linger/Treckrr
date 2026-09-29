package store

import (
	"context"

	"github.com/d0linger/treckrr/internal/models"
)

// GetCompanyEInvoiceParty returns the optional structured issuer fields.
func (s *Store) GetCompanyEInvoiceParty(ctx context.Context) (models.InvoiceParty, error) {
	var p models.InvoiceParty
	err := s.db.QueryRowContext(ctx, `SELECT einvoice_street, einvoice_zip, einvoice_town, einvoice_country_code
		FROM company WHERE id=1`).Scan(&p.Street, &p.ZIP, &p.Town, &p.CountryCode)
	return p, err
}

// GetNeighborEInvoiceParty returns the optional structured recipient fields.
func (s *Store) GetNeighborEInvoiceParty(ctx context.Context, id int64) (models.InvoiceParty, error) {
	var p models.InvoiceParty
	err := s.db.QueryRowContext(ctx, `SELECT einvoice_street, einvoice_zip, einvoice_town,
		einvoice_country_code, einvoice_order_id FROM neighbors WHERE id=$1`, id).Scan(
		&p.Street, &p.ZIP, &p.Town, &p.CountryCode, &p.OrderID)
	return p, err
}

// ListNeighborEInvoiceParties returns structured recipient fields keyed by id.
func (s *Store) ListNeighborEInvoiceParties(ctx context.Context) (map[int64]models.InvoiceParty, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, einvoice_street, einvoice_zip, einvoice_town,
		einvoice_country_code, einvoice_order_id FROM neighbors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]models.InvoiceParty)
	for rows.Next() {
		var id int64
		var p models.InvoiceParty
		if err := rows.Scan(&id, &p.Street, &p.ZIP, &p.Town, &p.CountryCode, &p.OrderID); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}
