package store

import (
	"context"
	"database/sql"
	"errors"
)

// AccountingExportProfile contains mappings used only while rendering an
// accounting CSV. It never changes the underlying invoice snapshots.
type AccountingExportProfile struct {
	ID                int64
	Name              string
	RevenueAccount    string
	ReceivableAccount string
	TaxCode           string
	CostCenter        string
	Columns           string
	Delimiter         string
	DecimalComma      bool
}

// ListAccountingExportProfiles returns the configured profiles by name.
func (s *Store) ListAccountingExportProfiles(ctx context.Context) ([]AccountingExportProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,revenue_account,receivable_account,tax_code,cost_center,columns,delimiter,decimal_comma
		FROM accounting_export_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountingExportProfile
	for rows.Next() {
		var p AccountingExportProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.RevenueAccount, &p.ReceivableAccount, &p.TaxCode,
			&p.CostCenter, &p.Columns, &p.Delimiter, &p.DecimalComma); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetAccountingExportProfile returns one configured profile.
func (s *Store) GetAccountingExportProfile(ctx context.Context, id int64) (AccountingExportProfile, error) {
	var p AccountingExportProfile
	err := s.db.QueryRowContext(ctx, `SELECT id,name,revenue_account,receivable_account,tax_code,cost_center,columns,delimiter,decimal_comma
		FROM accounting_export_profiles WHERE id=$1`, id).Scan(
		&p.ID, &p.Name, &p.RevenueAccount, &p.ReceivableAccount, &p.TaxCode,
		&p.CostCenter, &p.Columns, &p.Delimiter, &p.DecimalComma,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}

// SaveAccountingExportProfile creates or updates a named export profile.
func (s *Store) SaveAccountingExportProfile(ctx context.Context, p AccountingExportProfile) (int64, error) {
	if p.ID == 0 {
		err := s.db.QueryRowContext(ctx, `INSERT INTO accounting_export_profiles
			(name,revenue_account,receivable_account,tax_code,cost_center,columns,delimiter,decimal_comma)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.Name, p.RevenueAccount,
			p.ReceivableAccount, p.TaxCode, p.CostCenter, p.Columns, p.Delimiter, p.DecimalComma).Scan(&p.ID)
		return p.ID, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounting_export_profiles SET
		name=$1,revenue_account=$2,receivable_account=$3,tax_code=$4,cost_center=$5,
		columns=$6,delimiter=$7,decimal_comma=$8,updated_at=now() WHERE id=$9`, p.Name,
		p.RevenueAccount, p.ReceivableAccount, p.TaxCode, p.CostCenter, p.Columns,
		p.Delimiter, p.DecimalComma, p.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return p.ID, nil
}

// DeleteAccountingExportProfile removes only an export configuration.
func (s *Store) DeleteAccountingExportProfile(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounting_export_profiles WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
