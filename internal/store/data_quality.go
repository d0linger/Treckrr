package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// DataQualityIssue is one non-blocking master-data warning for a selected year.
// Code is stable for tests/automation; labels are rendered by the server.
type DataQualityIssue struct {
	Code       string
	Severity   string
	NeighborID int64
	EntityID   int64
	Subject    string
}

// DataQualityReport groups completeness findings already present in durable
// master data. It never changes or auto-fills operator-owned values.
type DataQualityReport struct {
	Issues []DataQualityIssue
}

// NeighborIssueCounts returns the number of findings for each year member.
func (r DataQualityReport) NeighborIssueCounts() map[int64]int {
	out := make(map[int64]int)
	for _, issue := range r.Issues {
		if issue.NeighborID != 0 {
			out[issue.NeighborID]++
		}
	}
	return out
}

// YearDataQuality checks the selected year's people and pricing basis. Every
// rule is advisory: existing booking, invoice and closing workflows stay valid.
func (s *Store) YearDataQuality(ctx context.Context, yearID, baseID int64) (DataQualityReport, error) {
	report := DataQualityReport{Issues: make([]DataQualityIssue, 0)}
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.name, btrim(n.address)='', btrim(n.email)='', btrim(n.iban)=''
		  FROM billing_year_neighbors byn
		  JOIN neighbors n ON n.id=byn.neighbor_id
		 WHERE byn.billing_year_id=$1 AND NOT n.anonymized
		 ORDER BY n.name`, yearID)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id int64
		var name string
		var addressMissing, emailMissing, ibanMissing bool
		if err := rows.Scan(&id, &name, &addressMissing, &emailMissing, &ibanMissing); err != nil {
			_ = rows.Close()
			return report, err
		}
		if addressMissing {
			report.Issues = append(report.Issues, DataQualityIssue{Code: "neighbor_address", Severity: "high", NeighborID: id, Subject: name})
		}
		if emailMissing {
			report.Issues = append(report.Issues, DataQualityIssue{Code: "neighbor_email", Severity: "medium", NeighborID: id, Subject: name})
		}
		if ibanMissing {
			report.Issues = append(report.Issues, DataQualityIssue{Code: "neighbor_iban", Severity: "medium", NeighborID: id, Subject: name})
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return report, err
	}

	var tractors, loadLevels, machines int
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM tractors WHERE base_id=$1 AND active),
		       (SELECT count(*) FROM load_levels WHERE base_id=$1),
		       (SELECT count(*) FROM machines WHERE base_id=$1 AND active)`, baseID).
		Scan(&tractors, &loadLevels, &machines); err != nil {
		return report, err
	}
	for _, missing := range []struct {
		count int
		code  string
		name  string
	}{
		{tractors, "catalog_tractors", "Traktoren"},
		{loadLevels, "catalog_load_levels", "Belastungsstufen"},
		{machines, "catalog_machines", "Maschinen"},
	} {
		if missing.count == 0 {
			report.Issues = append(report.Issues, DataQualityIssue{Code: missing.code, Severity: "high", Subject: missing.name})
		}
	}

	// A machine's billable rate is required for bookings. Self-cost remains an
	// optional controlling value and therefore must not create a work-queue item.
	rows, err = s.db.QueryContext(ctx, `
		SELECT id, name, working_width <= 0 OR cost_per_ab <= 0
		  FROM machines WHERE base_id=$1 AND active ORDER BY sort_order, name`, baseID)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id int64
		var name string
		var rateMissing bool
		if err := rows.Scan(&id, &name, &rateMissing); err != nil {
			_ = rows.Close()
			return report, err
		}
		if rateMissing {
			report.Issues = append(report.Issues, DataQualityIssue{Code: "machine_rate", Severity: "high", EntityID: id, Subject: name})
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return report, err
	}
	meter, err := s.meterIssues(ctx, yearID)
	if err != nil {
		return report, err
	}
	report.Issues = append(report.Issues, meter...)
	return report, nil
}

// meterReading is one booking's hour-meter pair, for the per-tractor checks.
type meterReading struct {
	entryID, neighborID int64
	tractor, tractorKey string
	date                time.Time
	start, end, hours   decimal.Decimal
}

// meterIssues checks a year's hour-meter readings per tractor (by ident, so a
// new rate basis continues the same meter): a start below the previous end is
// an overlap, a start above it is meter time without a booking (summed per
// tractor), and a reading that disagrees with the typed hours by more than a
// quarter hour and a tenth is flagged. Advisory only: the hours stay billed.
func (s *Store) meterIssues(ctx context.Context, yearID int64) ([]DataQualityIssue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.neighbor_id, COALESCE(NULLIF(btrim(t.ident), ''), t.id::text) || ' ' || t.name,
		       CASE WHEN btrim(t.ident) <> '' THEN 'ident:' || btrim(t.ident) ELSE 'id:' || t.id::text END,
		       e.entry_date, e.meter_start, e.meter_end, e.hours
		  FROM entries e JOIN tractors t ON t.id = e.tractor_id
		 WHERE e.billing_year_id = $1 AND NOT e.voided AND e.meter_end IS NOT NULL
		 ORDER BY 4, e.entry_date, e.meter_start, e.id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var readings []meterReading
	for rows.Next() {
		var m meterReading
		if err := rows.Scan(&m.entryID, &m.neighborID, &m.tractor, &m.tractorKey, &m.date, &m.start, &m.end, &m.hours); err != nil {
			return nil, err
		}
		readings = append(readings, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var issues []DataQualityIssue
	quarter, tenth := decimal.RequireFromString("0.25"), decimal.RequireFromString("0.1")
	gapHours, gapCount, gapEntry := decimal.Zero, 0, int64(0)
	flushGaps := func(tractor string) {
		if gapCount > 0 {
			issues = append(issues, DataQualityIssue{Code: "meter_gaps", Severity: "medium", EntityID: gapEntry,
				Subject: tractor + " · " + strconv.Itoa(gapCount) + " Lücke(n), " + germanDecimal(gapHours) + " h"})
		}
		gapHours, gapCount, gapEntry = decimal.Zero, 0, 0
	}
	for i, m := range readings {
		day := m.date.Format("02.01.2006")
		if i > 0 && readings[i-1].tractorKey == m.tractorKey {
			prev := readings[i-1]
			switch {
			case m.start.LessThan(prev.end):
				issues = append(issues, DataQualityIssue{Code: "meter_overlap", Severity: "high", NeighborID: m.neighborID, EntityID: m.entryID,
					Subject: m.tractor + " · " + day + " · Anfang " + germanDecimal(m.start) + " h vor Ende " + germanDecimal(prev.end) + " h"})
			case m.start.GreaterThan(prev.end):
				if gapCount == 0 {
					gapEntry = m.entryID
				}
				gapCount++
				gapHours = gapHours.Add(m.start.Sub(prev.end))
			}
		} else if i > 0 {
			flushGaps(readings[i-1].tractor)
		}
		metered := m.end.Sub(m.start)
		if metered.Sub(m.hours).Abs().GreaterThan(decimal.Max(quarter, m.hours.Mul(tenth))) {
			issues = append(issues, DataQualityIssue{Code: "meter_deviation", Severity: "medium", NeighborID: m.neighborID, EntityID: m.entryID,
				Subject: m.tractor + " · " + day + " · Zähler " + germanDecimal(metered) + " h, gebucht " + germanDecimal(m.hours) + " h"})
		}
	}
	if len(readings) > 0 {
		flushGaps(readings[len(readings)-1].tractor)
	}
	return issues, nil
}

// germanDecimal writes a decimal with a decimal comma and no trailing zeros.
func germanDecimal(d decimal.Decimal) string {
	return strings.Replace(d.String(), ".", ",", 1)
}

// LastMeterReadings returns, per tractor of a basis, the latest end reading
// booked for the same tractor (matched by ident across bases), to prefill the
// next start.
func (s *Store) LastMeterReadings(ctx context.Context, baseID int64) (map[int64]decimal.Decimal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, (SELECT e.meter_end FROM entries e JOIN tractors t2 ON t2.id = e.tractor_id
		               WHERE e.meter_end IS NOT NULL AND NOT e.voided
		                 AND (t2.id = t.id OR (btrim(t.ident) <> '' AND t2.ident = t.ident))
		               ORDER BY e.entry_date DESC, e.meter_end DESC LIMIT 1)
		  FROM tractors t WHERE t.base_id = $1`, baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]decimal.Decimal{}
	for rows.Next() {
		var id int64
		var last decimal.NullDecimal
		if err := rows.Scan(&id, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			out[id] = last.Decimal
		}
	}
	return out, rows.Err()
}
