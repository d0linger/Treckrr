package models

import "github.com/shopspring/decimal"

// MoneyPlaces is the precision every stored money amount (payment, ledger
// posting, document gross, installment) is kept at: whole cents.
const MoneyPlaces = 2

// RoundMoney normalizes an amount to whole cents, half away from zero — the
// same rule PostgreSQL's round(numeric, 2) applies, so a value rounded here and
// one rounded in SQL always agree.
func RoundMoney(d decimal.Decimal) decimal.Decimal {
	return d.Round(MoneyPlaces)
}

// HasSubCent reports whether an amount carries digits below a cent (9,995).
// Forms reject such input instead of silently rounding what the user typed.
func HasSubCent(d decimal.Decimal) bool {
	return !d.Equal(RoundMoney(d))
}

// BalanceState is the single settled/credit rule for an account balance
// (payable minus paid). The balance is judged at cent precision: a sub-cent
// leftover from legacy rows is neither an open item nor a Guthaben, while
// anything that rounds to at least one cent is. Paid, the dunning list and the
// year-closing checklist all use this rule (or its SQL twin, round(x, 2)).
func BalanceState(remaining decimal.Decimal) (settled, credit bool) {
	r := RoundMoney(remaining)
	return r.IsZero(), r.IsNegative()
}
