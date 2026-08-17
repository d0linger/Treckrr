// Package bankimport parses incoming bank credits from a CSV export or a camt.053
// (ISO 20022) statement, so payments can be matched to invoices by reference.
package bankimport

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Txn is one incoming credit (debits are dropped — only money received matters).
type Txn struct {
	Date      time.Time
	Amount    decimal.Decimal
	Reference string // Verwendungszweck / remittance info
	Name      string // payer, when available
	Hash      string // stable per-transaction id for import de-duplication
}

func (t *Txn) setHash() {
	sum := sha256.Sum256([]byte(t.Date.Format("2006-01-02") + "|" + t.Amount.StringFixed(2) + "|" + strings.TrimSpace(t.Reference) + "|" + strings.TrimSpace(t.Name)))
	t.Hash = hex.EncodeToString(sum[:])
}

// parseAmount accepts German ("1.234,56") or plain ("1234.56") decimals.
func parseAmount(raw string) (decimal.Decimal, bool) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return decimal.Zero, false
	}
	// German grouping+comma: strip thousands dots, comma→dot. Detect by a comma
	// present with a dot before it, or a comma as the decimal sep.
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, false
	}
	return d, true
}

// parseDate returns (t, true) for a recognized layout. On an absent or malformed
// date it returns the zero time and false — never time.Now(), which would make
// setHash non-deterministic (the same credit re-imported later would get a
// different "today" and thus a different hash, defeating de-duplication and
// double-booking the payment). Callers substitute a booking date at book time.
func parseDate(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02", "02.01.2006", "02.01.06", "2006/01/02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseCSV reads a bank CSV, detecting the delimiter and the columns by header
// name (Datum/Buchung, Betrag/Umsatz, Verwendungszweck/Referenz, Auftraggeber/Name).
// Only positive amounts (credits) are returned.
func ParseCSV(data []byte) ([]Txn, error) {
	text := strings.TrimPrefix(string(data), "\uFEFF")
	delim := ';'
	if strings.Count(text, ",") > strings.Count(text, ";") {
		delim = ','
	}
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma = delim
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV konnte nicht gelesen werden: %w", err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("CSV enthält keine Datenzeilen")
	}
	find := func(header []string, keys ...string) int {
		for i, h := range header {
			hl := strings.ToLower(strings.TrimSpace(h))
			for _, k := range keys {
				if strings.Contains(hl, k) {
					return i
				}
			}
		}
		return -1
	}
	head := rows[0]
	di := find(head, "datum", "buchung")
	ai := find(head, "betrag", "umsatz", "amount")
	ri := find(head, "verwendungszweck", "zweck", "referenz", "reference")
	ni := find(head, "auftraggeber", "name", "empfänger", "zahler")
	if ai < 0 || ri < 0 {
		return nil, fmt.Errorf("es braucht mindestens die Spalten Betrag und Verwendungszweck")
	}
	var out []Txn
	for _, rec := range rows[1:] {
		amt, ok := parseAmount(col(rec, ai))
		if !ok || !amt.IsPositive() {
			continue // skip non-credits / unparsable
		}
		date, _ := parseDate(col(rec, di))
		t := Txn{Amount: amt, Reference: col(rec, ri), Name: col(rec, ni), Date: date}
		t.setHash()
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("keine Zahlungseingänge (Gutschriften) gefunden")
	}
	return out, nil
}

func col(rec []string, i int) string {
	if i >= 0 && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

// ---- camt.053 (ISO 20022) --------------------------------------------------

type camtDoc struct {
	Entries []camtEntry `xml:"BkToCstmrStmt>Stmt>Ntry"`
}
type camtEntry struct {
	Amt         camtAmt      `xml:"Amt"`
	CdtDbtInd   string       `xml:"CdtDbtInd"`
	BookgDt     camtDt       `xml:"BookgDt"`
	AcctSvcrRef string       `xml:"AcctSvcrRef"` // bank's unique reference for the entry
	Details     []camtTxDtls `xml:"NtryDtls>TxDtls"`
}
type camtAmt struct {
	Value string `xml:",chardata"`
	Ccy   string `xml:"Ccy,attr"`
}
type camtDt struct {
	Dt   string `xml:"Dt"`
	DtTm string `xml:"DtTm"`
}
type camtTxDtls struct {
	Ustrd  []string `xml:"RmtInf>Ustrd"`
	DbtrNm string   `xml:"RltdPties>Dbtr>Nm"`
}

// ParseCamt053 reads a camt.053 statement and returns the incoming credits.
func ParseCamt053(data []byte) ([]Txn, error) {
	var doc camtDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("camt.053 konnte nicht gelesen werden: %w", err)
	}
	var out []Txn
	for _, e := range doc.Entries {
		if !strings.EqualFold(strings.TrimSpace(e.CdtDbtInd), "CRDT") {
			continue // only money received
		}
		// Reject non-EUR credits: the amount carries a Ccy attribute and the app
		// books plain euro. A foreign-currency value must never be recorded as EUR.
		if ccy := strings.TrimSpace(e.Amt.Ccy); ccy != "" && !strings.EqualFold(ccy, "EUR") {
			continue
		}
		amt, ok := parseAmount(e.Amt.Value)
		if !ok || !amt.IsPositive() {
			continue
		}
		// A batch entry (several TxDtls) carries one aggregate Amt but multiple
		// remittance texts. Joining them into a single Txn would book the batch
		// total against a merged reference and mis-attribute the money, so skip it
		// rather than emit a wrong booking.
		if len(e.Details) > 1 {
			continue
		}
		var refs []string
		name := ""
		for _, d := range e.Details {
			refs = append(refs, d.Ustrd...)
			if name == "" {
				name = strings.TrimSpace(d.DbtrNm)
			}
		}
		date := e.BookgDt.Dt
		if date == "" && len(e.BookgDt.DtTm) >= 10 {
			date = e.BookgDt.DtTm[:10]
		}
		bookg, _ := parseDate(date)
		t := Txn{Amount: amt, Reference: strings.TrimSpace(strings.Join(refs, " ")), Name: name, Date: bookg}
		// Prefer the bank's own unique entry reference for de-duplication: the
		// date/amount/reference/name tuple collides for two legitimately identical
		// credits (same payer, same amount, same day) and would silently drop the
		// second. Fall back to the tuple hash only when no bank reference exists.
		if ref := strings.TrimSpace(e.AcctSvcrRef); ref != "" {
			sum := sha256.Sum256([]byte("camt:acctsvcrref:" + ref))
			t.Hash = hex.EncodeToString(sum[:])
		} else {
			t.setHash()
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("keine Zahlungseingänge in der camt.053-Datei")
	}
	return out, nil
}

// Parse picks the parser by sniffing the content (XML → camt.053, else CSV).
func Parse(data []byte) ([]Txn, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
	if strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<Document") {
		return ParseCamt053(data)
	}
	return ParseCSV(data)
}
