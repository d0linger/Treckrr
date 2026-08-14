package server

import (
	"strings"

	"github.com/shopspring/decimal"
)

// epcPayload builds the EPC069-12 ("GiroCode") QR text for a SEPA credit transfer:
// BCD service data, one field per line (LF). Version 002 lets the BIC be empty;
// purpose and structured reference stay empty, and the invoice reference goes into
// the unstructured remittance field. Names/references are truncated to the spec's
// rune limits.
func epcPayload(name, iban string, amount decimal.Decimal, reference string) string {
	trunc := func(s string, n int) string {
		if r := []rune(s); len(r) > n {
			return string(r[:n])
		}
		return s
	}
	return strings.Join([]string{
		"BCD",                             // service tag
		"002",                             // version (BIC optional)
		"1",                               // character set: UTF-8
		"SCT",                             // SEPA credit transfer
		"",                                // BIC (optional in v002)
		trunc(name, 70),                   // beneficiary name
		strings.ReplaceAll(iban, " ", ""), // beneficiary IBAN (no spaces)
		"EUR" + amount.StringFixed(2),     // amount
		"",                                // purpose (optional)
		"",                                // structured remittance (unused)
		trunc(reference, 140),             // unstructured remittance
	}, "\n")
}
