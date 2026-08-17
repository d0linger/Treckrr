package bankimport

import "testing"

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

func TestParseCamt053(t *testing.T) {
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
	txns, err := ParseCamt053([]byte(xml))
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
	txns, err := ParseCamt053([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	// USD skipped + batch skipped → only the two BANKREF entries survive.
	if len(txns) != 2 {
		t.Fatalf("got %d credits, want 2 (USD + batch skipped)", len(txns))
	}
	// Same date/amount/reference but distinct bank refs → distinct hashes (both kept).
	if txns[0].Hash == txns[1].Hash {
		t.Errorf("identical credits with different AcctSvcrRef must get different hashes")
	}
}

func TestParseSniff(t *testing.T) {
	if _, err := Parse([]byte(`<?xml version="1.0"?><Document><BkToCstmrStmt><Stmt><Ntry><Amt>5.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><BookgDt><Dt>2026-01-01</Dt></BookgDt><NtryDtls><TxDtls><RmtInf><Ustrd>x</Ustrd></RmtInf></TxDtls></NtryDtls></Ntry></Stmt></BkToCstmrStmt></Document>`)); err != nil {
		t.Errorf("xml sniff: %v", err)
	}
	if _, err := Parse([]byte("Betrag;Verwendungszweck\n5,00;test\n")); err != nil {
		t.Errorf("csv sniff: %v", err)
	}
}
