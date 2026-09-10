package bankimport

import (
	"fmt"
	"testing"
)

func TestParseCSV_NameAfterIBAN(t *testing.T) {
	csv := "Datum;Betrag;Verwendungszweck;Auftraggeber-IBAN;Auftraggeber\n" +
		"01.06.2026;10,00;Rechnung 2026-001;AT611904300234573201;Huber\n"
	txns, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if txns[0].Name != "Huber" {
		t.Errorf("payer name = %q, want Huber", txns[0].Name)
	}
	// Correcting display/matching data must not re-book statements imported when
	// this column order incorrectly omitted the name from the de-duplication key.
	legacy := txns[0]
	legacy.Name = ""
	legacy.setHash()
	if txns[0].Hash != legacy.Hash {
		t.Error("fix changed the historical import hash")
	}
}

func TestParseCamt_BatchTotal(t *testing.T) {
	for _, tc := range []struct {
		name, total string
		wantErr     bool
	}{
		{name: "matching", total: "30.00"},
		{name: "missing detail", total: "40.00", wantErr: true},
		{name: "excess detail", total: "20.00", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			xml := fmt.Sprintf(`<Document><BkToCstmrStmt><Stmt><Ntry>
<Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><NtryDtls>
<TxDtls><Amt Ccy="EUR">10.00</Amt></TxDtls>
<TxDtls><Amt Ccy="EUR">20.00</Amt></TxDtls>
</NtryDtls></Ntry></Stmt></BkToCstmrStmt></Document>`, tc.total)
			txns, err := ParseCamt([]byte(xml))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v; txns = %+v", err, tc.wantErr, txns)
			}
			if !tc.wantErr && len(txns) != 2 {
				t.Fatalf("got %d credits, want 2", len(txns))
			}
		})
	}
}

func TestParseCSV(t *testing.T) {
	csv := "Buchungsdatum;Betrag;Verwendungszweck;Auftraggeber\n" +
		"14.03.2026;1.130,00;Rechnung 2026-001;Josef Öllinger\n" +
		"20.03.2026;-50,00;Abbuchung;Bank\n" + // debit → skipped
		"22.03.2026;32,00;RG 2026-002 Danke;Maier\n"
	txns, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 2 {
		t.Fatalf("got %d credits, want 2 (debit skipped)", len(txns))
	}
	if txns[0].Amount.String() != "1130" || txns[0].Reference != "Rechnung 2026-001" || txns[0].Name != "Josef Öllinger" {
		t.Errorf("row0 wrong: %+v", txns[0])
	}
	if txns[0].Date.Format("2006-01-02") != "2026-03-14" {
		t.Errorf("row0 date %s", txns[0].Date.Format("2006-01-02"))
	}
	if txns[0].Hash == "" || txns[0].Hash == txns[1].Hash {
		t.Error("hashes must be set and distinct")
	}
}

func TestParseCamt(t *testing.T) {
	xml := `<?xml version="1.0"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.02">
 <BkToCstmrStmt><Stmt>
  <Ntry>
    <Amt Ccy="EUR">1130.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <BookgDt><Dt>2026-03-14</Dt></BookgDt>
    <NtryDtls><TxDtls>
      <RmtInf><Ustrd>Rechnung 2026-001</Ustrd></RmtInf>
      <RltdPties><Dbtr><Nm>Josef Öllinger</Nm></Dbtr></RltdPties>
    </TxDtls></NtryDtls>
  </Ntry>
  <Ntry>
    <Amt Ccy="EUR">99.00</Amt><CdtDbtInd>DBIT</CdtDbtInd>
    <BookgDt><Dt>2026-03-15</Dt></BookgDt>
  </Ntry>
 </Stmt></BkToCstmrStmt>
</Document>`
	txns, err := ParseCamt([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 1 { // the DBIT is skipped
		t.Fatalf("got %d credits, want 1", len(txns))
	}
	if txns[0].Amount.String() != "1130" || txns[0].Reference != "Rechnung 2026-001" || txns[0].Name != "Josef Öllinger" {
		t.Errorf("wrong: %+v", txns[0])
	}
}

// A foreign-currency credit must be skipped, not booked as EUR; a batch entry
// (multiple TxDtls under one aggregate Amt) must be skipped rather than mis-booked;
// and a bank AcctSvcrRef must drive de-duplication so two otherwise-identical
// credits are both kept.
func TestParseCamt053_CurrencyBatchAndBankRef(t *testing.T) {
	xml := `<?xml version="1.0"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.02">
 <BkToCstmrStmt><Stmt>
  <Ntry>
    <Amt Ccy="USD">50.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <BookgDt><Dt>2026-03-14</Dt></BookgDt>
    <NtryDtls><TxDtls><RmtInf><Ustrd>USD zahlung</Ustrd></RmtInf></TxDtls></NtryDtls>
  </Ntry>
  <Ntry>
    <Amt Ccy="EUR">200.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <BookgDt><Dt>2026-03-14</Dt></BookgDt>
    <NtryDtls>
      <TxDtls><RmtInf><Ustrd>Rechnung A</Ustrd></RmtInf></TxDtls>
      <TxDtls><RmtInf><Ustrd>Rechnung B</Ustrd></RmtInf></TxDtls>
    </NtryDtls>
  </Ntry>
  <Ntry>
    <Amt Ccy="EUR">10.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <AcctSvcrRef>BANKREF-1</AcctSvcrRef>
    <BookgDt><Dt>2026-03-14</Dt></BookgDt>
    <NtryDtls><TxDtls><RmtInf><Ustrd>Rechnung 2026-009</Ustrd></RmtInf></TxDtls></NtryDtls>
  </Ntry>
  <Ntry>
    <Amt Ccy="EUR">10.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <AcctSvcrRef>BANKREF-2</AcctSvcrRef>
    <BookgDt><Dt>2026-03-14</Dt></BookgDt>
    <NtryDtls><TxDtls><RmtInf><Ustrd>Rechnung 2026-009</Ustrd></RmtInf></TxDtls></NtryDtls>
  </Ntry>
 </Stmt></BkToCstmrStmt>
</Document>`
	txns, err := ParseCamt([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	// USD skipped + batch skipped (its details state no own amounts, so a split
	// would have to guess) → only the two BANKREF entries survive.
	if len(txns) != 2 {
		t.Fatalf("got %d credits, want 2 (USD + batch skipped)", len(txns))
	}
	// Same date/amount/reference but distinct bank refs → distinct hashes (both kept).
	if txns[0].Hash == txns[1].Hash {
		t.Errorf("identical credits with different AcctSvcrRef must get different hashes")
	}
}

func TestParseSniff(t *testing.T) {
	if _, err := Parse([]byte(`<?xml version="1.0"?><Document><BkToCstmrStmt><Stmt><Ntry><Amt Ccy="EUR">5.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><BookgDt><Dt>2026-01-01</Dt></BookgDt><NtryDtls><TxDtls><RmtInf><Ustrd>x</Ustrd></RmtInf></TxDtls></NtryDtls></Ntry></Stmt></BkToCstmrStmt></Document>`)); err != nil {
		t.Errorf("xml sniff: %v", err)
	}
	if _, err := Parse([]byte("Betrag;Verwendungszweck\n5,00;test\n")); err != nil {
		t.Errorf("csv sniff: %v", err)
	}
}

// A camt.054 Sammler: one entry, aggregate amount, two TxDtls each with its own
// EUR amount — must split into two credits with distinct hashes, payer IBANs
// attached. The .054 envelope also proves the Ntfctn root is read.
func TestParseCamt054_BatchSplit(t *testing.T) {
	xml := `<?xml version="1.0"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.054.001.02">
 <BkToCstmrDbtCdtNtfctn><Ntfctn>
  <Ntry>
    <Amt Ccy="EUR">300.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <AcctSvcrRef>SAMMLER-9</AcctSvcrRef>
    <BookgDt><Dt>2026-06-01</Dt></BookgDt>
    <NtryDtls>
      <TxDtls>
        <Amt Ccy="EUR">100.00</Amt>
        <RmtInf><Ustrd>Rechnung 2026-001</Ustrd></RmtInf>
        <RltdPties><Dbtr><Nm>Huber</Nm></Dbtr><DbtrAcct><Id><IBAN>AT61 1904 3002 3457 3201</IBAN></Id></DbtrAcct></RltdPties>
      </TxDtls>
      <TxDtls>
        <AmtDtls><TxAmt><Amt Ccy="EUR">200.00</Amt></TxAmt></AmtDtls>
        <RmtInf><Ustrd>Rechnung 2026-002</Ustrd></RmtInf>
        <RltdPties><Dbtr><Nm>Maier</Nm></Dbtr></RltdPties>
      </TxDtls>
    </NtryDtls>
  </Ntry>
 </Ntfctn></BkToCstmrDbtCdtNtfctn>
</Document>`
	txns, err := ParseCamt([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 2 {
		t.Fatalf("got %d credits, want the batch split into 2", len(txns))
	}
	if txns[0].Amount.StringFixed(2) != "100.00" || txns[1].Amount.StringFixed(2) != "200.00" {
		t.Errorf("amounts = %s / %s, want the per-detail 100.00 / 200.00", txns[0].Amount, txns[1].Amount)
	}
	if txns[0].Hash == txns[1].Hash {
		t.Errorf("split details must get distinct hashes")
	}
	if txns[0].IBAN != "AT611904300234573201" {
		t.Errorf("payer IBAN not normalized/extracted: %q", txns[0].IBAN)
	}
	if txns[1].Reference != "Rechnung 2026-002" {
		t.Errorf("detail reference lost: %q", txns[1].Reference)
	}
}

// The camt.052 report wraps the same Ntry in BkToCstmrAcctRpt>Rpt.
func TestParseCamt052_Root(t *testing.T) {
	xml := `<?xml version="1.0"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.052.001.02">
 <BkToCstmrAcctRpt><Rpt>
  <Ntry>
    <Amt Ccy="EUR">42.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <BookgDt><Dt>2026-06-02</Dt></BookgDt>
    <NtryDtls><TxDtls><RmtInf><Ustrd>Rechnung 2026-003</Ustrd></RmtInf></TxDtls></NtryDtls>
  </Ntry>
 </Rpt></BkToCstmrAcctRpt>
</Document>`
	txns, err := ParseCamt([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 1 || txns[0].Amount.StringFixed(2) != "42.00" {
		t.Fatalf("camt.052 root not read: %+v", txns)
	}
}

// Minimal MT940: credits parsed with date/amount, the :86: SEPA subfields split
// into reference, payer name and IBAN; debits and reversals dropped.
func TestParseMT940(t *testing.T) {
	data := ":20:STMT1\r\n" +
		":25:AT611904300234573201\r\n" +
		":28C:1/1\r\n" +
		":61:2606030603C150,00NTRFNONREF\r\n" +
		":86:166?00GUTSCHRIFT?20SVWZ+Rechnung 2026-004?21Teil 2?31AT483200000012345864?32Huber Franz\r\n" +
		":61:260604D99,99NTRFNONREF\r\n" +
		":86:Lastschrift Versicherung\r\n" +
		":61:2606050605RC10,00NTRFNONREF\r\n" +
		":86:Rueckbuchung\r\n" +
		":61:260606C20,50NTRFNONREF\r\n" +
		":86:Anzahlung Maier\r\n" +
		"-\r\n"
	txns, err := ParseMT940([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 2 {
		t.Fatalf("got %d credits, want 2 (debit and reversal dropped)", len(txns))
	}
	a := txns[0]
	if a.Amount.StringFixed(2) != "150.00" || a.Date.Format("2006-01-02") != "2026-06-03" {
		t.Errorf("first credit = %s on %s, want 150.00 on 2026-06-03", a.Amount, a.Date.Format("2006-01-02"))
	}
	if a.Reference != "Rechnung 2026-004 Teil 2" {
		t.Errorf("structured :86: remittance = %q", a.Reference)
	}
	if a.Name != "Huber Franz" || a.IBAN != "AT483200000012345864" {
		t.Errorf("payer = %q / %q", a.Name, a.IBAN)
	}
	if txns[1].Reference != "Anzahlung Maier" {
		t.Errorf("unstructured :86: = %q", txns[1].Reference)
	}
	if a.Hash == txns[1].Hash {
		t.Errorf("distinct credits share a hash")
	}
	// The sniffer must route SWIFT content to MT940.
	if sniffed, err := Parse([]byte(data)); err != nil || len(sniffed) != 2 {
		t.Errorf("sniff: %v (%d)", err, len(sniffed))
	}
}

// A CSV with an IBAN column feeds the matcher; an "Auftraggeber-IBAN" header
// must not be mistaken for the payer-name column.
func TestParseCSV_IBANColumn(t *testing.T) {
	csv := "Datum;Betrag;Verwendungszweck;Auftraggeber-IBAN\n" +
		"03.06.2026;75,00;Anzahlung;at61 1904 3002 3457 3201\n"
	txns, err := ParseCSV([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if txns[0].IBAN != "AT611904300234573201" {
		t.Errorf("IBAN = %q, want normalized AT61…", txns[0].IBAN)
	}
	if txns[0].Name != "" {
		t.Errorf("the IBAN column leaked into the payer name: %q", txns[0].Name)
	}
}

// German banks continue a long Verwendungszweck in ?60-?63 once ?20-?29 are
// used up. Those fields were ignored, so an invoice number that landed in the
// tail was dropped from the reference and the credit matched nothing.
func TestParseMT940_Remittance60Continuation(t *testing.T) {
	data := ":20:STMT1\r\n" +
		":25:AT611904300234573201\r\n" +
		":28C:1/1\r\n" +
		":61:2606030603C150,00NTRFNONREF\r\n" +
		":86:166?00GUTSCHRIFT?20SVWZ+Zahlung fuer?21 Leistungen laut?22 Aufstellung" +
		"?60Rechnung 2026-004?61 Restbetrag?31AT483200000012345864?32Huber Franz\r\n" +
		"-\r\n"
	txns, err := ParseMT940([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 1 {
		t.Fatalf("got %d credits, want 1", len(txns))
	}
	want := "Zahlung fuer Leistungen laut Aufstellung Rechnung 2026-004 Restbetrag"
	if txns[0].Reference != want {
		t.Errorf("remittance = %q, want %q", txns[0].Reference, want)
	}
	if txns[0].Name != "Huber Franz" || txns[0].IBAN != "AT483200000012345864" {
		t.Errorf("payer = %q / %q", txns[0].Name, txns[0].IBAN)
	}
}

// TestParseAmountSeparators pins the last-separator rule: both "1.234,56"
// (German) and "1,234.56" (en-US bank exports) must read as 1234.56 — the old
// any-comma-is-German path stripped the en-US dot and booked 1.23456.
func TestParseAmountSeparators(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"1.234,56", "1234.56", true},
		{"1,234.56", "1234.56", true},
		{"1234.56", "1234.56", true},
		{"12,5", "12.5", true},
		{"2.000,00", "2000", true},
		{"12,345,678.90", "12345678.9", true},
		{"", "0", false},
	}
	for _, c := range cases {
		got, ok := parseAmount(c.in)
		if ok != c.ok {
			t.Errorf("parseAmount(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("parseAmount(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestParseMT940_DuplicateCreditsKeepDistinctHashes: two legitimately identical
// credits in one statement (same payer, day, amount, text — routine for split
// transfers) must import as TWO payments. Their hashes differ via the
// occurrence suffix, and a re-parse reproduces the same hashes so the
// de-duplication against earlier imports still works.
func TestParseMT940_DuplicateCreditsKeepDistinctHashes(t *testing.T) {
	line := ":61:2606060606C250,00NTRFNONREF\r\n" +
		":86:166?00GUTSCHRIFT?20SVWZ+Anzahlung Maier?32Maier Josef\r\n"
	data := ":20:STMT2\r\n:25:AT611904300234573201\r\n:28C:1/1\r\n" + line + line + "-\r\n"
	txns, err := ParseMT940([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(txns) != 2 {
		t.Fatalf("got %d credits, want 2", len(txns))
	}
	if txns[0].Hash == txns[1].Hash {
		t.Fatalf("identical hashes %s — the second credit would be silently dropped on import", txns[0].Hash)
	}
	again, err := ParseMT940([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Hash != txns[0].Hash || again[1].Hash != txns[1].Hash {
		t.Fatal("hashes not stable across re-parse — re-imported statements would double-book")
	}
}
