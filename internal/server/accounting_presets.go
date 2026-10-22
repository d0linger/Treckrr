package server

import (
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/store"
)

// accountingPreset is a fixed import layout for one accounting product. The
// layouts follow the products' documented CSV booking imports; they are
// offered as templates until a tax adviser has imported a real file, which
// the page says next to them.
type accountingPreset struct {
	Key, Label, Hint string
	header           []string
	row              func(store.JournalRow, store.AccountingExportProfile) []string
}

// accountingPresets lists the built-in layouts in display order.
var accountingPresets = []accountingPreset{
	{
		Key: "bmd", Label: "BMD NTCS (Buchungsimport)",
		Hint: "Felder satzart … kost, Belegdatum TT.MM.JJJJ, Dezimalkomma, Buchcode 1 = Soll / 2 = Haben am Debitor. Debitor = Konto, Erlöskonto = Gegenkonto.",
		header: []string{"satzart", "konto", "gkonto", "belegnr", "belegdatum", "buchsymbol", "buchcode",
			"prozent", "steuercode", "betrag", "steuer", "text", "kost"},
		row: func(j store.JournalRow, p store.AccountingExportProfile) []string {
			code, gross, vat := debitCreditCode(j.Gross, "1", "2"), j.Gross.Abs(), j.VATAmount.Abs()
			return []string{"0", csvSafe(p.ReceivableAccount), csvSafe(p.RevenueAccount), csvSafe(j.Number),
				j.IssuedOn.Format("02.01.2006"), "AR", code, deDecimal(j.VATRate), csvSafe(taxCodeFor(j, p)),
				deDecimal(gross), deDecimal(vat), csvSafe(accountingText(j)), csvSafe(p.CostCenter)}
		},
	},
	{
		Key: "rzl", Label: "RZL (Buchungsimport)",
		Hint: "Spalten Belegdatum … Kostenstelle, Datum TT.MM.JJJJ, Dezimalkomma, S/H-Kennzeichen am Debitor. Debitor = Konto, Erlöskonto = Gegenkonto.",
		header: []string{"Belegdatum", "Belegnummer", "Buchungstext", "Konto", "Gegenkonto", "SH", "Betrag",
			"Steuersatz", "Steuerbetrag", "Steuercode", "Kostenstelle"},
		row: func(j store.JournalRow, p store.AccountingExportProfile) []string {
			return []string{j.IssuedOn.Format("02.01.2006"), csvSafe(j.Number), csvSafe(accountingText(j)),
				csvSafe(p.ReceivableAccount), csvSafe(p.RevenueAccount), debitCreditCode(j.Gross, "S", "H"),
				deDecimal(j.Gross.Abs()), deDecimal(j.VATRate), deDecimal(j.VATAmount.Abs()),
				csvSafe(taxCodeFor(j, p)), csvSafe(p.CostCenter)}
		},
	},
}

// accountingPresetFor returns the preset of a profile, or nil for the free
// column mapping.
func accountingPresetFor(format string) *accountingPreset {
	for i := range accountingPresets {
		if accountingPresets[i].Key == format {
			return &accountingPresets[i]
		}
	}
	return nil
}

// debitCreditCode books an invoice on the debtor's debit side and a storno
// or credit note (negative gross) on its credit side, with positive amounts.
func debitCreditCode(gross decimal.Decimal, debit, credit string) string {
	if gross.IsNegative() {
		return credit
	}
	return debit
}

// taxCodeFor passes the profile's tax code only for taxed documents; a
// document without VAT carries none.
func taxCodeFor(j store.JournalRow, p store.AccountingExportProfile) string {
	if j.VATAmount.IsZero() {
		return ""
	}
	return p.TaxCode
}

// accountingText names the document for the booking text.
func accountingText(j store.JournalRow) string {
	return strings.TrimSpace(orKey(documentKindLabels, j.Kind) + " " + j.Number + " " + j.NeighborName)
}
