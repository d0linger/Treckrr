package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// CreatePriceTerm stores a standing price term for a neighbor and returns its id.
func (s *Store) CreatePriceTerm(ctx context.Context, t models.PriceTerm) (int64, error) {
	var id int64
	var validTo any
	if t.ValidTo != nil {
		validTo = *t.ValidTo
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbor_price_terms (neighbor_id, label, discount_pct, valid_from, valid_to)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		t.NeighborID, t.Label, t.DiscountPct, t.ValidFrom, validTo).Scan(&id)
	return id, err
}

// DeletePriceTerm removes one term of a neighbor. Bookings keep the term they
// were priced with; ErrNotFound when the term does not belong to the neighbor.
func (s *Store) DeletePriceTerm(ctx context.Context, neighborID, id int64) (models.PriceTerm, error) {
	var t models.PriceTerm
	var validTo sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM neighbor_price_terms WHERE id=$1 AND neighbor_id=$2
		 RETURNING id, neighbor_id, label, discount_pct, valid_from, valid_to, created_at`, id, neighborID).
		Scan(&t.ID, &t.NeighborID, &t.Label, &t.DiscountPct, &t.ValidFrom, &validTo, &t.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if validTo.Valid {
		t.ValidTo = &validTo.Time
	}
	return t, err
}

// ListPriceTerms returns a neighbor's terms, newest start first.
func (s *Store) ListPriceTerms(ctx context.Context, neighborID int64) ([]models.PriceTerm, error) {
	all, err := s.priceTerms(ctx, &neighborID)
	return all[neighborID], err
}

// PriceTermsByNeighbor returns every neighbor's terms keyed by neighbor id, for
// bulk repricing that loads them once.
func (s *Store) PriceTermsByNeighbor(ctx context.Context) (map[int64][]models.PriceTerm, error) {
	return s.priceTerms(ctx, nil)
}

// PriceTermAt returns the neighbor's term effective on date, or nil.
func (s *Store) PriceTermAt(ctx context.Context, neighborID int64, date time.Time) (*models.PriceTerm, error) {
	terms, err := s.ListPriceTerms(ctx, neighborID)
	if err != nil {
		return nil, err
	}
	return models.EffectivePriceTerm(terms, date), nil
}

// priceTerms reads the terms of one neighbor (or all when neighborID is nil).
func (s *Store) priceTerms(ctx context.Context, neighborID *int64) (map[int64][]models.PriceTerm, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, neighbor_id, label, discount_pct, valid_from, valid_to, created_at
		   FROM neighbor_price_terms
		  WHERE $1::bigint IS NULL OR neighbor_id = $1
		  ORDER BY neighbor_id, valid_from DESC, id DESC`, nullInt(neighborID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]models.PriceTerm{}
	for rows.Next() {
		var t models.PriceTerm
		var validTo sql.NullTime
		if err := rows.Scan(&t.ID, &t.NeighborID, &t.Label, &t.DiscountPct, &t.ValidFrom, &validTo, &t.Created); err != nil {
			return nil, err
		}
		if validTo.Valid {
			t.ValidTo = &validTo.Time
		}
		out[t.NeighborID] = append(out[t.NeighborID], t)
	}
	return out, rows.Err()
}

// priceTermSnapshot is the term part of a repriced catalog rate.
type priceTermSnapshot struct {
	label     string
	pct, perH decimal.Decimal
}

// applyPriceTermRate discounts a catalog rate by the term effective on date.
func applyPriceTermRate(rate decimal.Decimal, term *models.PriceTerm) (decimal.Decimal, priceTermSnapshot) {
	if term == nil {
		return rate, priceTermSnapshot{}
	}
	discounted, perH := term.Discounted(rate)
	return discounted, priceTermSnapshot{label: term.Label, pct: term.DiscountPct, perH: perH}
}
