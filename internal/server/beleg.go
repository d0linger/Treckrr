package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

// BelegTractorLoad is one Belastungsstufe a tractor was used at this year: its
// €/PS·h, the resulting tractor €/h, and the machines run with that combination.
type BelegTractorLoad struct {
	Load     string
	CostPS   string
	Rate     decimal.Decimal
	Machines []string
}

// BelegTractor groups the load levels a tractor was used at this year. Ident and
// PS fill the left "rail" column of the Traktoren table.
type BelegTractor struct {
	Ident string
	PS    string
	Loads []BelegTractorLoad
}

// BelegMachine is one machine used this year, with its €/AB·h and €/h.
type BelegMachine struct {
	Name   string
	Width  string
	CostAB string
	Rate   decimal.Decimal
}

// deu formats a decimal in German notation (comma) without trailing zeros,
// e.g. 1.1500 -> "1,15", 0.4752 -> "0,4752", 100 -> "100".
func deu(d decimal.Decimal) string {
	s := d.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.ReplaceAll(s, ".", ",")
}

// deu2 is like deu but keeps at least two decimals, for unit prices (€/PS·h,
// €/AB·h): 0.4 -> "0,40", 12 -> "12,00", 0.4752 -> "0,4752".
func deu2(d decimal.Decimal) string {
	s := deu(d)
	if i := strings.IndexByte(s, ','); i < 0 {
		s += ",00"
	} else if n := len(s) - i - 1; n < 2 {
		s += strings.Repeat("0", 2-n)
	}
	return s
}

// handleNeighborBeleg renders a compact, share-friendly statement for one
// neighbor and year (bookings + ledger + saldo) — a clean list to screenshot
// and hand over. Read-only; no actions, no editing.
func (s *Server) handleNeighborBeleg(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}

	entries, err := s.store.ListEntries(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "beleg: entries", err)
		return
	}
	cost, hours, err := s.store.NeighborTotal(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "beleg: total", err)
		return
	}
	ledger, err := s.store.ListNeighborLedger(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, "beleg: ledger", err)
		return
	}
	ledgerSum := decimal.Zero
	for _, l := range ledger {
		if !l.Voided {
			ledgerSum = ledgerSum.Add(l.Amount)
		}
	}

	// Basis items (ordered lists + id lookups) for the Kostengrundlage. Errors
	// are tolerated: without them the appendix simply stays empty.
	tractorList, _ := s.store.ListTractors(r.Context(), year.BaseID)
	loadList, _ := s.store.ListLoadLevels(r.Context(), year.BaseID)
	machineList, _ := s.store.ListMachines(r.Context(), year.BaseID)
	tractorByID := map[int64]models.Tractor{}
	for _, t := range tractorList {
		tractorByID[t.ID] = t
	}
	loadByID := map[int64]models.LoadLevel{}
	for _, l := range loadList {
		loadByID[l.ID] = l
	}
	machineByID := map[int64]models.Machine{}
	for _, m := range machineList {
		machineByID[m.ID] = m
	}

	// Collect what THIS neighbor actually used this year: which tractors, at
	// which load levels, and which machines with each — plus the set of machines
	// used overall. Voided bookings don't count.
	usedTractor := map[int64]map[int64]map[int64]bool{} // tractor -> load -> set(machine)
	usedMachine := map[int64]bool{}
	bookings := 0
	for _, e := range entries {
		if e.Voided {
			continue
		}
		bookings++
		if e.TractorID == nil || e.LoadLevelID == nil {
			continue
		}
		if _, ok := tractorByID[*e.TractorID]; !ok {
			continue
		}
		if _, ok := loadByID[*e.LoadLevelID]; !ok {
			continue
		}
		loads := usedTractor[*e.TractorID]
		if loads == nil {
			loads = map[int64]map[int64]bool{}
			usedTractor[*e.TractorID] = loads
		}
		set := loads[*e.LoadLevelID]
		if set == nil {
			set = map[int64]bool{}
			loads[*e.LoadLevelID] = set
		}
		if mids, err := s.store.EntryMachineIDs(r.Context(), e.ID); err == nil {
			for _, mid := range mids {
				if _, ok := machineByID[mid]; ok {
					set[mid] = true
					usedMachine[mid] = true
				}
			}
		}
	}

	// Build the two tables in the basis' own order (tractors, load levels,
	// machines), keeping only what was used.
	var gTractors []BelegTractor
	for _, t := range tractorList {
		loads := usedTractor[t.ID]
		if loads == nil {
			continue
		}
		ident := t.Ident
		if ident == "" {
			ident = t.Name
		}
		bt := BelegTractor{Ident: ident, PS: deu(t.PS)}
		for _, l := range loadList {
			set := loads[l.ID]
			if set == nil {
				continue
			}
			btl := BelegTractorLoad{Load: l.Name, CostPS: deu2(l.CostPerPS), Rate: calc.TractorRate(t, l)}
			for _, m := range machineList {
				if set[m.ID] {
					btl.Machines = append(btl.Machines, m.Name)
				}
			}
			bt.Loads = append(bt.Loads, btl)
		}
		if len(bt.Loads) > 0 {
			gTractors = append(gTractors, bt)
		}
	}
	var gMachines []BelegMachine
	for _, m := range machineList {
		if usedMachine[m.ID] {
			gMachines = append(gMachines, BelegMachine{Name: m.Name, Width: deu(m.WorkingWidth), CostAB: deu2(m.CostPerAB), Rate: calc.MachineRate(m)})
		}
	}

	// Populate the basis (name + year) for the appendix header.
	if year.Base == nil {
		if b, err := s.store.GetBase(r.Context(), year.BaseID); err == nil {
			year.Base = b
		}
	}
	// Payment status is only tracked once a year is completed.
	paid := false
	if year.Completed() {
		payments, err := s.store.YearPayments(r.Context(), year.ID)
		if err != nil {
			s.serverError(w, "beleg: payments", err)
			return
		}
		paid = payments[neighbor.ID]
	}

	data := s.newPage(w, r, neighbor.Name+" · Beleg", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, "beleg: year selector", err)
		return
	}
	data["Neighbor"] = neighbor
	data["Entries"] = entries
	data["TotalCost"] = cost
	data["TotalHours"] = hours
	data["Ledger"] = ledger
	data["LedgerSum"] = ledgerSum
	data["Saldo"] = cost.Add(ledgerSum)
	data["Completed"] = year.Completed()
	data["Paid"] = paid
	data["GrundTractors"] = gTractors
	data["GrundMachines"] = gMachines
	data["HasGrund"] = len(gTractors) > 0 || len(gMachines) > 0
	data["Bookings"] = bookings
	data["ShowGrund"] = r.URL.Query().Get("grundlage") == "1"
	data["Today"] = time.Now().Format("02.01.2006")
	s.render(w, r, "beleg", data)
}
