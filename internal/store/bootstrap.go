package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// EnsureAdmin creates the bootstrap admin from env config if it does not exist.
// If the user exists, its password is reset to the configured value so the
// admin can always regain access via Docker ENV.
func (s *Store) EnsureAdmin(ctx context.Context, username, password string) error {
	var (
		id      int64
		isAdmin bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, is_admin FROM users WHERE username=$1`, username).Scan(&id, &isAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.CreateUser(ctx, username, password, models.RoleAdmin); err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up admin: %w", err)
	}
	if err := s.UpdatePassword(ctx, id, password); err != nil {
		return err
	}
	if !isAdmin {
		return s.SetAdmin(ctx, id, true)
	}
	return nil
}

// seedTractor mirrors the source spreadsheet's tractor list.
type seedTractor struct {
	ident string
	ps    float64
}

// seedMachine mirrors the source spreadsheet's machine list.
type seedMachine struct {
	name  string
	width float64
	cost  float64
}

// SeedDefaultData creates an initial pricing basis (year 2025) populated with
// the values from the source spreadsheet, a cloned 2026 basis, and the three
// example neighbors. It is a no-op if any pricing basis already exists.
func (s *Store) SeedDefaultData(ctx context.Context) error {
	bases, err := s.ListBases(ctx)
	if err != nil {
		return err
	}
	if len(bases) > 0 {
		return nil
	}

	baseID, err := s.CreateEmptyBase(ctx, 2023, "Bemessungsgrundlage 2023")
	if err != nil {
		return err
	}

	// Load levels: cost per PS per hour.
	loads := []struct {
		name string
		cost float64
		sort int
	}{
		{"leicht", 0.33, 1},
		{"mittel", 0.36, 2},
		{"schwer", 0.38, 3},
	}
	loadIDs := map[string]int64{}
	for _, l := range loads {
		id, err := s.CreateLoadLevel(ctx, baseID, l.name, decimal.NewFromFloat(l.cost), l.sort)
		if err != nil {
			return err
		}
		loadIDs[l.name] = id
	}

	tractors := []seedTractor{
		{"948", 50}, {"8070", 64}, {"9083", 94}, {"4095", 100}, {"4130", 130},
	}
	tractorIDs := map[string]int64{}
	for _, t := range tractors {
		id, err := s.CreateTractor(ctx, baseID, t.ident, "", decimal.NewFromFloat(t.ps), 0)
		if err != nil {
			return err
		}
		tractorIDs[t.ident] = id
	}

	machines := []seedMachine{
		{"Heckmähwerk", 2.4, 10},
		{"Frontmähwerk", 3.06, 12},
		{"Kreiselzettwender", 8.8, 4.5},
		{"Schwader", 3.8, 5},
		{"Mulcher", 2.8, 7},
		{"Fräse", 2.0, 18},
	}
	machineIDs := map[string]int64{}
	for _, m := range machines {
		id, err := s.CreateMachine(ctx, baseID, m.name, decimal.NewFromFloat(m.width), decimal.NewFromFloat(m.cost), "", 0)
		if err != nil {
			return err
		}
		machineIDs[m.name] = id
	}

	// Fixed gespanne mirroring the spreadsheet's task columns.
	type seedGespann struct {
		name     string
		tractor  string
		load     string
		machines []string
	}
	gespanne := []seedGespann{
		{"Mähen", "4095", "mittel", []string{"Heckmähwerk", "Frontmähwerk"}},
		{"Umkehren", "8070", "leicht", []string{"Kreiselzettwender"}},
		{"Schwadern klein", "948", "leicht", []string{"Schwader"}},
		{"Schwadern groß", "4130", "mittel", nil},
		{"Ballentransport", "4130", "mittel", nil},
		{"Mulchen", "4130", "mittel", []string{"Mulcher"}},
		{"Fräsen", "9083", "schwer", []string{"Fräse"}},
	}
	for _, g := range gespanne {
		tID := tractorIDs[g.tractor]
		lID := loadIDs[g.load]
		var mids []int64
		for _, mn := range g.machines {
			mids = append(mids, machineIDs[mn])
		}
		if _, err := s.CreateGespann(ctx, baseID, g.name, &tID, &lID, mids, 0); err != nil {
			return err
		}
	}

	// Create a sample billing year (Abrechnungsjahr) that uses this basis and
	// add the example neighbors as participants. Further years can be created
	// in the app and may reuse this basis or a newer one.
	yearID, err := s.CreateBillingYear(ctx, 2025, baseID, "Abrechnung 2025")
	if err != nil {
		return err
	}
	for _, name := range []string{"Musterhof Berg", "Beispielhof Au", "Nachbar Talblick"} {
		nid, err := s.CreateNeighbor(ctx, name, "")
		if err != nil {
			return err
		}
		if err := s.AddNeighborToYear(ctx, yearID, nid); err != nil {
			return err
		}
	}
	return nil
}
