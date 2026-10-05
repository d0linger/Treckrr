package server

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// accountSettlement is the common live account state used by the neighbor page
// and receipt assembler. Invoice-specific gross reconciliation stays separate
// because an issued invoice is an immutable tax document, not a live subtotal.
type accountSettlement struct {
	Entries   []models.Entry
	Cost      decimal.Decimal
	Hours     decimal.Decimal
	Ledger    []models.LedgerEntry
	LedgerSum decimal.Decimal
	Saldo     decimal.Decimal
	Payments  []models.Payment
	PaidSum   decimal.Decimal
	Remaining decimal.Decimal
}

type settlementService struct {
	store *store.Store
}

func settlementTotals(cost decimal.Decimal, ledger []models.LedgerEntry, payments []models.Payment) (
	ledgerSum, saldo, paidSum, remaining decimal.Decimal,
) {
	for _, posting := range ledger {
		if !posting.Voided {
			ledgerSum = ledgerSum.Add(posting.Amount)
		}
	}
	for _, payment := range payments {
		paidSum = paidSum.Add(payment.Amount)
	}
	saldo = cost.Add(ledgerSum)
	remaining = saldo.Sub(paidSum)
	return ledgerSum, saldo, paidSum, remaining
}

func (s *Server) settlements() settlementService {
	return settlementService{store: s.store}
}

// Load returns one account's live activity without applying invoice VAT. The
// remaining value is the live service/ledger saldo less payments; callers that
// settle an issued invoice must use PayableRemaining instead.
func (s settlementService) Load(ctx context.Context, yearID, neighborID int64) (accountSettlement, error) {
	entries, err := s.store.ListEntries(ctx, neighborID, yearID)
	if err != nil {
		return accountSettlement{}, fmt.Errorf("load settlement entries: %w", err)
	}
	cost, hours, err := s.store.NeighborTotal(ctx, neighborID, yearID)
	if err != nil {
		return accountSettlement{}, fmt.Errorf("load settlement totals: %w", err)
	}
	ledger, err := s.store.ListNeighborLedger(ctx, yearID, neighborID)
	if err != nil {
		return accountSettlement{}, fmt.Errorf("load settlement ledger: %w", err)
	}
	payments, err := s.store.ListPayments(ctx, yearID, neighborID)
	if err != nil {
		return accountSettlement{}, fmt.Errorf("load settlement payments: %w", err)
	}
	ledgerSum, saldo, paidSum, remaining := settlementTotals(cost, ledger, payments)
	return accountSettlement{
		Entries: entries, Cost: cost, Hours: hours,
		Ledger: ledger, LedgerSum: ledgerSum, Saldo: saldo,
		Payments: payments, PaidSum: paidSum, Remaining: remaining,
	}, nil
}

// PayableRemaining uses the store's locked invoice-aware definition. It is the
// only balance suitable for payment, carry-forward, and settle actions.
func (s settlementService) PayableRemaining(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	remaining, err := s.store.AccountRemaining(ctx, yearID, neighborID)
	if err != nil {
		return decimal.Zero, fmt.Errorf("load payable remaining: %w", err)
	}
	return remaining, nil
}
