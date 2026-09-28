package models

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestMoneyHelpers(t *testing.T) {
	d := decimal.RequireFromString
	if got := RoundMoney(d("9.995")); !got.Equal(d("10")) {
		t.Fatalf("RoundMoney(9.995) = %s, want 10.00 (half away from zero, like SQL round)", got)
	}
	if got := RoundMoney(d("-0.125")); !got.Equal(d("-0.13")) {
		t.Fatalf("RoundMoney(-0.125) = %s, want -0.13", got)
	}
	if !HasSubCent(d("9.995")) || HasSubCent(d("9.9900")) || HasSubCent(d("10")) {
		t.Fatal("HasSubCent misclassifies")
	}
	for _, tc := range []struct {
		remaining       string
		settled, credit bool
	}{
		{"0", true, false},
		{"0.004", true, false},  // sub-cent leftover: settled, not open
		{"-0.004", true, false}, // and not a Guthaben either
		{"0.005", false, false}, // rounds to one cent: open (dunning agrees)
		{"-0.005", false, true}, // rounds to minus one cent: Guthaben
		{"12.50", false, false},
	} {
		settled, credit := BalanceState(d(tc.remaining))
		if settled != tc.settled || credit != tc.credit {
			t.Errorf("BalanceState(%s) = %v/%v, want %v/%v", tc.remaining, settled, credit, tc.settled, tc.credit)
		}
	}
}

func TestActiveCreditFor(t *testing.T) {
	ref := int64(7)
	other := int64(8)
	cases := []struct {
		iv   Invoice
		want bool
	}{
		{Invoice{Kind: "gutschrift", Status: "issued", ReferencesInvoiceID: &ref}, true},
		{Invoice{Kind: "gutschrift", Status: "canceled", ReferencesInvoiceID: &ref}, false},
		{Invoice{Kind: "gutschrift", Status: "issued", ReferencesInvoiceID: &other}, false},
		{Invoice{Kind: "gutschrift", Status: "issued"}, false}, // free credit note
		{Invoice{Kind: "storno", Status: "issued", ReferencesInvoiceID: &ref}, false},
	}
	for i, tc := range cases {
		if got := tc.iv.ActiveCreditFor(ref); got != tc.want {
			t.Errorf("case %d: ActiveCreditFor = %v, want %v", i, got, tc.want)
		}
	}
}
