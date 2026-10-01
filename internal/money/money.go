// Package money contains exact-decimal primitives shared by billing models,
// catalog calculations, request adapters and persistence workflows.
package money

import "github.com/shopspring/decimal"

// Amount multiplies a billable quantity by its unit price and rounds the line
// once to euro cents. Callers must pass an already rounded unit price when the
// business rule prices individual catalog components before summing them.
func Amount(quantity, unitPrice decimal.Decimal) decimal.Decimal {
	return quantity.Mul(unitPrice).Round(2)
}
