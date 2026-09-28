package bankimport

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// TestParseAmountRejectsHostileInput pins WEB-01: scientific notation, over-long
// text, implausible magnitudes and sub-cent precision are refused before any
// decimal arithmetic runs on them. "1e999999999" used to parse cheaply and then
// blow up in setHash's StringFixed(2).
func TestParseAmountRejectsHostileInput(t *testing.T) {
	for _, in := range []string{
		"1e999999999",
		"1E9",
		"1,5e3",
		"-1e999999999",
		strings.Repeat("9", 33),
		"1" + strings.Repeat("0", 9), // 1e9 exactly
		"1.000.000.000,00",
		"999999999,999",
		"12,345", // lone comma = German decimal; three decimals are not cents
		"0,001",
		"abc",
	} {
		if got, ok := parseAmount(in); ok {
			t.Errorf("parseAmount(%q) = %s, want rejection", in, got)
		}
	}
}

// TestParseAmountKeepsValidAmounts: every realistic amount still parses to the
// same value, including trailing zero decimals and the largest allowed figure.
func TestParseAmountKeepsValidAmounts(t *testing.T) {
	for in, want := range map[string]string{
		"999.999.999,99": "999999999.99",
		"-50,00":         "-50",
		"12,500":         "12.5", // trailing zero: still whole cents
		"0,01":           "0.01",
		" 1 234,56 ":     "1234.56",
	} {
		got, ok := parseAmount(in)
		if !ok || got.String() != want {
			t.Errorf("parseAmount(%q) = %s, %v; want %s", in, got, ok, want)
		}
	}
}

// TestSetHashFormatUnchanged pins the de-duplication key byte for byte:
// statements imported before the amount guard must hash identically, or a
// re-upload would book every old credit a second time.
func TestSetHashFormatUnchanged(t *testing.T) {
	csv := "Buchungsdatum;Betrag;Verwendungszweck;Auftraggeber\n" +
		"14.03.2026;1.130,00;Rechnung 2026-001;Josef Öllinger\n" +
		"15.03.2026;12,5;RG 2;Maier\n"
	txns, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{
		"2026-03-14|1130.00|Rechnung 2026-001|Josef Öllinger",
		"2026-03-15|12.50|RG 2|Maier",
	} {
		sum := sha256.Sum256([]byte(key))
		if want := hex.EncodeToString(sum[:]); txns[i].Hash != want {
			t.Errorf("row %d hash = %s, want %s (key %q)", i, txns[i].Hash, want, key)
		}
	}
}

// TestParseRejectsExponentCredits covers both statement formats end to end:
// a crafted credit is skipped (and a file with nothing else reports "no
// credits") instead of reaching setHash.
func TestParseRejectsExponentCredits(t *testing.T) {
	csv := "Datum;Betrag;Verwendungszweck\n01.01.2026;1e999999999;x\n01.01.2026;5,00;ok\n"
	txns, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 1 || txns[0].Amount.String() != "5" {
		t.Fatalf("CSV credits = %+v, want only the 5,00 row", txns)
	}

	camt := `<Document><BkToCstmrStmt><Stmt><Ntry>
<Amt Ccy="EUR">1e999999999</Amt><CdtDbtInd>CRDT</CdtDbtInd>
<BookgDt><Dt>2026-01-01</Dt></BookgDt>
</Ntry></Stmt></BkToCstmrStmt></Document>`
	if _, err := ParseCamt([]byte(camt)); err == nil {
		t.Fatal("camt with only an exponent credit parsed without error")
	}

	batch := `<Document><BkToCstmrStmt><Stmt><Ntry>
<Amt Ccy="EUR">20.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><NtryDtls>
<TxDtls><Amt Ccy="EUR">1e1</Amt></TxDtls>
<TxDtls><Amt Ccy="EUR">10.00</Amt></TxDtls>
</NtryDtls></Ntry></Stmt></BkToCstmrStmt></Document>`
	if txns, err := ParseCamt([]byte(batch)); err == nil {
		t.Fatalf("batch with an exponent detail was split: %+v", txns)
	}
}

// FuzzParseAmount: whatever the input, an accepted amount is bounded, has at
// most two decimals, and hashes quickly (the property the exponent guard
// protects).
func FuzzParseAmount(f *testing.F) {
	for _, seed := range []string{"1.234,56", "1,234.56", "1e999999999", "12,5", "-0,01", "", "9" + strings.Repeat("0", 40)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		d, ok := parseAmount(in)
		if !ok {
			return
		}
		if d.Abs().GreaterThanOrEqual(maxAmount) {
			t.Fatalf("parseAmount(%q) = %s exceeds the bound", in, d)
		}
		if !d.Equal(d.Truncate(2)) {
			t.Fatalf("parseAmount(%q) = %s has sub-cent precision", in, d)
		}
		tx := Txn{Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Amount: d}
		tx.setHash()
		if len(tx.Hash) != 64 {
			t.Fatalf("hash %q", tx.Hash)
		}
	})
}
