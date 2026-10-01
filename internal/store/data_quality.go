package store

import (
	"context"
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

	rows, err = s.db.QueryContext(ctx, `
		SELECT id, name, working_width <= 0 OR cost_per_ab <= 0, self_cost_per_h <= 0
		  FROM machines WHERE base_id=$1 AND active ORDER BY sort_order, name`, baseID)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id int64
		var name string
		var rateMissing, selfCostMissing bool
		if err := rows.Scan(&id, &name, &rateMissing, &selfCostMissing); err != nil {
			_ = rows.Close()
			return report, err
		}
		if rateMissing {
			report.Issues = append(report.Issues, DataQualityIssue{Code: "machine_rate", Severity: "high", EntityID: id, Subject: name})
		}
		if selfCostMissing {
			report.Issues = append(report.Issues, DataQualityIssue{Code: "machine_self_cost", Severity: "medium", EntityID: id, Subject: name})
		}
	}
	_ = rows.Close()
	return report, rows.Err()
}
