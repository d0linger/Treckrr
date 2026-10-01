package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

// TestYearRolloverIntegration verifies the assistant resumes safely and never
// duplicates either the target year or carried memberships.
func TestYearRolloverIntegration(t *testing.T) {
	e := newItEnv(t)
	path := fmt.Sprintf("/years/%d/wechsel", e.yearID64)
	t.Cleanup(func() {
		if target, err := e.st.BillingYearByNumber(e.ctx, e.year+1); err == nil {
			_ = e.st.DeleteBillingYear(e.ctx, target.ID)
		}
	})

	page := e.get(path)
	for _, want := range []string{"Geführter Jahreswechsel", "Sicherung", "Offene Posten", "Serien"} {
		if !strings.Contains(page, want) {
			t.Errorf("rollover preview missing %q", want)
		}
	}
	body := e.post(path+"/create", url.Values{
		"base_mode": {"reuse"}, "base_id": {itoa64(e.baseID64)},
	})
	if !strings.Contains(body, "zuerst das Ausgangsjahr abschließen") {
		t.Fatal("open source year was allowed to create a target")
	}
	if err := e.st.SetYearStatus(e.ctx, e.yearID64, models.YearCompleted); err != nil {
		t.Fatal(err)
	}
	e.post(path+"/create", url.Values{
		"base_mode": {"reuse"}, "base_id": {itoa64(e.baseID64)},
	})
	target, err := e.st.BillingYearByNumber(e.ctx, e.year+1)
	if err != nil {
		t.Fatalf("target year: %v", err)
	}
	if target.BaseID != e.baseID64 || target.Completed() {
		t.Fatalf("target year = %+v, want open and existing basis", target)
	}
	e.post(path+"/create", url.Values{
		"base_mode": {"reuse"}, "base_id": {itoa64(e.baseID64)},
	})
	e.post(path+"/neighbors", url.Values{})
	secondNeighbors := e.post(path+"/neighbors", url.Values{})
	if !strings.Contains(secondNeighbors, "Alle aktiven Nachbarn sind bereits enthalten; nichts doppelt angelegt.") {
		t.Fatal("second neighbor carry did not return its idempotency flash")
	}
	members, err := e.st.ListYearNeighbors(e.ctx, target.ID)
	if err != nil || len(members) != 1 || members[0].ID != e.neighborID {
		t.Fatalf("target memberships = %+v, %v", members, err)
	}
	page = e.get(path)
	if !strings.Contains(page, "Technischer Jahreswechsel abgeschlossen") {
		t.Fatal("completed/resumed rollover state is not visible")
	}
}
