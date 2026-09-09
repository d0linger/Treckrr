// Package bankimport parses incoming bank credits from a CSV export, an ISO
// 20022 message (camt.052 report, camt.053 statement, camt.054 notification) or
// a minimal MT940 statement, so payments can be matched to invoices by
// reference or to neighbors by payer IBAN.
package bankimport

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"regexp"
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
	IBAN      string // payer account, when available — matcher fallback key.
	// IBAN is deliberately NOT part of the de-dup hash: adding it would change
	// the hash of every previously imported credit and re-book old statements.
	Hash string // stable per-transaction id for import de-duplication
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
	ii := find(head, "iban")
	ni := find(head, "auftraggeber", "name", "empfänger", "zahler")
	if ni == ii {
		ni = -1 // a header like "Auftraggeber-IBAN" matched both; it is the IBAN
	}
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
		t := Txn{Amount: amt, Reference: col(rec, ri), Name: col(rec, ni), IBAN: normIBAN(col(rec, ii)), Date: date}
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

// normIBAN strips spaces and uppercases, so stored and parsed IBANs compare.
func normIBAN(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}

// ---- camt.052 / .053 / .054 (ISO 20022) ------------------------------------
//
// The three messages differ only in the envelope around the same Ntry element:
// 052 is an intraday report, 053 the end-of-day statement, 054 a credit
// notification. One struct collects the entries from whichever root is present.

type camtDoc struct {
	Stmt   []camtEntry `xml:"BkToCstmrStmt>Stmt>Ntry"`
	Rpt    []camtEntry `xml:"BkToCstmrAcctRpt>Rpt>Ntry"`
	Ntfctn []camtEntry `xml:"BkToCstmrDbtCdtNtfctn>Ntfctn>Ntry"`
}

func (d camtDoc) entries() []camtEntry {
	out := make([]camtEntry, 0, len(d.Stmt)+len(d.Rpt)+len(d.Ntfctn))
	out = append(out, d.Stmt...)
	out = append(out, d.Rpt...)
	out = append(out, d.Ntfctn...)
	return out
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
	Ustrd    []string `xml:"RmtInf>Ustrd"`
	DbtrNm   string   `xml:"RltdPties>Dbtr>Nm"`
	DbtrIBAN string   `xml:"RltdPties>DbtrAcct>Id>IBAN"`
	// Per-transaction amount, present on batch entries. camt.054 tends to put it
	// directly in Amt, statements in AmtDtls>TxAmt.
	Amt   camtAmt `xml:"Amt"`
	TxAmt camtAmt `xml:"AmtDtls>TxAmt>Amt"`
}

// amount returns the detail's own EUR amount, or false when it has none.
func (d camtTxDtls) amount() (decimal.Decimal, bool) {
	for _, a := range []camtAmt{d.Amt, d.TxAmt} {
		if strings.TrimSpace(a.Value) == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(a.Ccy), "EUR") {
			return decimal.Zero, false
		}
		amt, ok := parseAmount(a.Value)
		if ok && amt.IsPositive() {
			return amt, true
		}
		return decimal.Zero, false
	}
	return decimal.Zero, false
}

// ParseCamt reads a camt.052/.053/.054 message and returns the incoming credits.
func ParseCamt(data []byte) ([]Txn, error) {
	var doc camtDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("camt-Datei konnte nicht gelesen werden: %w", err)
	}
	var out []Txn
	for _, e := range doc.entries() {
		if !strings.EqualFold(strings.TrimSpace(e.CdtDbtInd), "CRDT") {
			continue // only money received
		}
		// Book only explicit-EUR credits: the amount carries a Ccy attribute and the
		// app books plain euro. Reject a foreign currency (would be mis-booked as EUR)
		// and also a blank/absent Ccy (ambiguous — real camt.053 always states it).
		if !strings.EqualFold(strings.TrimSpace(e.Amt.Ccy), "EUR") {
			continue
		}
		amt, ok := parseAmount(e.Amt.Value)
		if !ok || !amt.IsPositive() {
			continue
		}
		date := e.BookgDt.Dt
		if date == "" && len(e.BookgDt.DtTm) >= 10 {
			date = e.BookgDt.DtTm[:10]
		}
		bookg, _ := parseDate(date)
		// A batch entry (several TxDtls) carries one aggregate Amt but multiple
		// remittance texts. When every detail states its own EUR amount, split the
		// batch into one Txn per detail — that is what a SEPA Sammler in camt.054
		// looks like. When any detail lacks its amount the whole entry is skipped:
		// booking the batch total against a merged reference would mis-attribute
		// the money.
		if len(e.Details) > 1 {
			out = append(out, splitBatch(e, bookg)...)
			continue
		}
		var refs []string
		name, iban := "", ""
		for _, d := range e.Details {
			refs = append(refs, d.Ustrd...)
			if name == "" {
				name = strings.TrimSpace(d.DbtrNm)
			}
			if iban == "" {
				iban = normIBAN(d.DbtrIBAN)
			}
		}
		t := Txn{Amount: amt, Reference: strings.TrimSpace(strings.Join(refs, " ")), Name: name, IBAN: iban, Date: bookg}
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
		return nil, fmt.Errorf("keine Zahlungseingänge in der camt-Datei")
	}
	return out, nil
}

// splitBatch turns one batch entry into per-detail credits. Returns nil unless
// EVERY detail states its own positive EUR amount — a partial split would book
// some money twice (details + a later manual booking of the rest).
func splitBatch(e camtEntry, bookg time.Time) []Txn {
	txns := make([]Txn, 0, len(e.Details))
	for i, d := range e.Details {
		amt, ok := d.amount()
		if !ok {
			return nil
		}
		t := Txn{
			Amount:    amt,
			Reference: strings.TrimSpace(strings.Join(d.Ustrd, " ")),
			Name:      strings.TrimSpace(d.DbtrNm),
			IBAN:      normIBAN(d.DbtrIBAN),
			Date:      bookg,
		}
		// De-dup: the bank's entry reference plus the detail's position. The
		// single-detail form keeps its historical hash (no suffix), so statements
		// imported before batch support stay recognized.
		if ref := strings.TrimSpace(e.AcctSvcrRef); ref != "" {
			sum := sha256.Sum256([]byte("camt:acctsvcrref:" + ref + "#" + fmt.Sprint(i)))
			t.Hash = hex.EncodeToString(sum[:])
		} else {
			t.setHash()
		}
		txns = append(txns, t)
	}
	return txns
}

// ---- MT940 (SWIFT) ---------------------------------------------------------

// mt61 matches the start of a :61: statement line: value date YYMMDD, optional
// entry date MMDD, debit/credit mark (C, D, RC, RD), optional funds code letter,
// then the comma-decimal amount.
var mt61 = regexp.MustCompile(`^(\d{6})(\d{4})?(R?[CD])[A-Z]?(\d+,\d*)`)

// parse86 splits a structured SEPA :86: text (?20-?29 plus ?60-?63 remittance,
// ?32/?33 payer name, ?31 payer IBAN) into its parts. Unstructured text passes
// through as the reference.
func parse86(s string) (ref, name, iban string) {
	if !strings.Contains(s, "?") {
		return strings.TrimSpace(s), "", ""
	}
	var refs []string
	for _, part := range strings.Split(s, "?") {
		if len(part) < 2 {
			continue
		}
		code, text := part[:2], strings.TrimSpace(part[2:])
		switch {
		// ?20-?29 hold the Verwendungszweck and ?60-?63 CONTINUE it — German
		// banks spill into the 60s as soon as the text outgrows ten fields.
		// Dropping them truncated exactly the tail an invoice number lands in,
		// so a long remittance text matched nothing.
		case (code >= "20" && code <= "29") || (code >= "60" && code <= "63"):
			text = strings.TrimPrefix(text, "SVWZ+")
			if text != "" {
				refs = append(refs, text)
			}
		case code == "31":
			iban = normIBAN(text)
		case code == "32" || code == "33":
			if name == "" {
				name = text
			} else {
				name += " " + text
			}
		}
	}
	return strings.Join(refs, " "), name, iban
}

// ParseMT940 reads a minimal MT940 statement: :61: lines carry date, direction
// and amount, the following :86: block the remittance text. Credits only ("C" —
// RC/RD reversals are corrections, not income).
func ParseMT940(data []byte) ([]Txn, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []Txn
	var cur *Txn
	var raw86 string
	flush := func() {
		if cur == nil {
			return
		}
		ref, name, iban := parse86(raw86)
		cur.Reference, cur.Name, cur.IBAN = ref, name, iban
		cur.setHash()
		out = append(out, *cur)
		cur, raw86 = nil, ""
	}
	in86 := false
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, ":61:"):
			flush()
			in86 = false
			m := mt61.FindStringSubmatch(ln[4:])
			if m == nil || m[3] != "C" {
				continue
			}
			amt, ok := parseAmount(m[4])
			if !ok || !amt.IsPositive() {
				continue
			}
			date, _ := time.Parse("060102", m[1])
			cur = &Txn{Date: date, Amount: amt}
		case strings.HasPrefix(ln, ":86:"):
			if cur != nil {
				raw86 = strings.TrimSpace(ln[4:])
				in86 = true
			}
		case in86 && cur != nil && !strings.HasPrefix(ln, ":") && !strings.HasPrefix(ln, "-") && strings.TrimSpace(ln) != "":
			// :86: continuation lines (no tag) belong to the running text.
			raw86 += "\n" + strings.TrimSpace(ln)
		default:
			in86 = false
			if strings.HasPrefix(ln, ":") {
				flush()
			}
		}
	}
	flush()
	if len(out) == 0 {
		return nil, fmt.Errorf("keine Zahlungseingänge in der MT940-Datei")
	}
	return out, nil
}

// Parse picks the parser by sniffing the content: XML → camt.052/053/054,
// SWIFT tags → MT940, else CSV.
func Parse(data []byte) ([]Txn, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
	if strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<Document") {
		return ParseCamt(data)
	}
	if strings.HasPrefix(trimmed, ":20:") || strings.HasPrefix(trimmed, "{1:") || strings.Contains(trimmed, "\n:61:") {
		return ParseMT940(data)
	}
	return ParseCSV(data)
}
