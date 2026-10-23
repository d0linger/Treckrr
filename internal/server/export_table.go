package server

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/web"
	"github.com/d0linger/treckrr/internal/xlsx"
)

// exportCell carries one value in both export forms: the CSV text exactly as
// the CSV exports always wrote it, and the typed spreadsheet cell.
type exportCell struct {
	csv  string
	cell xlsx.Cell
}

// cellText is user-entered text: formula-neutralized in CSV, an inline string
// (never evaluated) in XLSX.
func cellText(s string) exportCell { return exportCell{csvSafe(s), xlsx.Text(s)} }

// cellLabel is fixed application text that needs no neutralizing.
func cellLabel(s string) exportCell { return exportCell{s, xlsx.Text(s)} }

// cellMoney is an amount in euros.
func cellMoney(d decimal.Decimal) exportCell { return exportCell{deDecimal(d), xlsx.Money(d)} }

// cellNumber is a quantity, hours or a percentage.
func cellNumber(d decimal.Decimal) exportCell { return exportCell{deDecimal(d), xlsx.Number(d)} }

// cellDate is a calendar day.
func cellDate(t time.Time) exportCell { return exportCell{web.Date(t), xlsx.Date(t)} }

// cellEmpty is a blank cell.
func cellEmpty() exportCell { return exportCell{"", xlsx.Empty()} }

// exportTable is one export: a header and typed rows, written as CSV or XLSX.
type exportTable struct {
	sheet  string
	header []string
	rows   [][]exportCell
}

func (t *exportTable) add(cells ...exportCell) { t.rows = append(t.rows, cells) }

// writeCSV writes the table in the export CSV dialect to an open writer.
func (t *exportTable) writeCSV(cw *csv.Writer) {
	_ = cw.Write(t.header)
	for _, row := range t.rows {
		line := make([]string, len(row))
		for i, c := range row {
			line[i] = c.csv
		}
		_ = cw.Write(line)
	}
}

// wantXLSX reports whether the request asks for the spreadsheet form, either
// by an .xlsx route or ?format=xlsx on a route shared with CSV.
func wantXLSX(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, ".xlsx") || r.URL.Query().Get("format") == "xlsx"
}

// writeExport answers with the table as CSV (base.csv) or XLSX (base.xlsx).
// The workbook is built in memory first, so a failure is a clean 500 instead
// of a truncated file.
func (s *Server) writeExport(w http.ResponseWriter, r *http.Request, base string, t exportTable) {
	if !wantXLSX(r) {
		cw, finish := csvDownload(w, r, base+".csv")
		defer finish()
		t.writeCSV(cw)
		return
	}
	sheet := xlsx.Sheet{Name: t.sheet, Header: t.header}
	for _, row := range t.rows {
		cells := make([]xlsx.Cell, len(row))
		for i, c := range row {
			cells[i] = c.cell
		}
		sheet.Rows = append(sheet.Rows, cells)
	}
	var buf bytes.Buffer
	if err := xlsx.Write(&buf, sheet); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+base+".xlsx\"")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}
