package store

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestValidateEntryGroup rejects forged memberships and invalid helper values
// before any database connection can be used.
func TestValidateEntryGroup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*models.Entry, *[]*models.Entry)
	}{
		{"missing account", func(e *models.Entry, _ *[]*models.Entry) { e.NeighborID = 0 }},
		{"nested group", func(e *models.Entry, _ *[]*models.Entry) { id := int64(7); e.LinkedEntryID = &id }},
		{"cross account", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].NeighborID++ }},
		{"cross year", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].BillingYearID++ }},
		{"wrong unit", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].Unit = "h" }},
		{"wrong day", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].Date = (*h)[0].Date.AddDate(0, 0, 1) }},
		{"existing creation ID", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].ID = 9 }},
		{"negative hours", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].Quantity = decimal.NewFromInt(-1) }},
		{"negative rate", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].UnitPrice = decimal.NewFromInt(-1) }},
		{"unrepresentable hours", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].Quantity = decimal.New(1, -5) }},
		{"unnamed free text", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].PersonName = " " }},
		{"voided new helper", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0].Voided = true }},
		{"nil helper", func(_ *models.Entry, h *[]*models.Entry) { (*h)[0] = nil }},
		{"too many people", func(_ *models.Entry, h *[]*models.Entry) { *h = make([]*models.Entry, MaxEntryCompanions+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			main, people := unitEntryGroup()
			tc.change(main, &people)
			if err := validateEntryGroup(main, people, true); !errors.Is(err, ErrInvalidEntryGroup) {
				t.Fatalf("validation = %v, want ErrInvalidEntryGroup", err)
			}
		})
	}
	t.Run("multiple free-text people on quantity booking", func(t *testing.T) {
		main, people := unitEntryGroup()
		main.Unit = "Ballen"
		other := *people[0]
		other.PersonName = "Another person"
		people = append(people, &other)
		if err := validateEntryGroup(main, people, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("duplicate edit IDs", func(t *testing.T) {
		main, people := unitEntryGroup()
		main.ID, people[0].ID = 1, 2
		people = append(people, people[0])
		if err := validateEntryGroup(main, people, false); !errors.Is(err, ErrInvalidEntryGroup) {
			t.Fatalf("duplicate IDs = %v", err)
		}
	})
}

// TestEntryGroupCopies verifies deterministic group identity, immutable inputs,
// authoritative rounded helper costs, and collision-safe derived retry keys.
func TestEntryGroupCopies(t *testing.T) {
	t.Parallel()
	main, people := unitEntryGroup()
	main.IdempotencyKey = "offline-original"
	people[0].Cost = decimal.NewFromInt(999)
	first, firstPeople, err := entryGroupCopies(main, []int64{2, 1}, people)
	if err != nil {
		t.Fatal(err)
	}
	second, secondPeople, err := entryGroupCopies(main, []int64{1, 2}, people)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestFingerprint == "" || first.RequestFingerprint != second.RequestFingerprint ||
		firstPeople[0].IdempotencyKey != secondPeople[0].IdempotencyKey {
		t.Fatal("equivalent input generated different replay identities")
	}
	if firstPeople[0].RequestFingerprint != first.RequestFingerprint || firstPeople[0].IdempotencyKey == main.IdempotencyKey {
		t.Fatal("helper lacks distinct key and shared request identity")
	}
	if main.RequestFingerprint != "" || people[0].IdempotencyKey != "" || !people[0].Cost.Equal(decimal.NewFromInt(999)) {
		t.Fatal("group normalization modified caller data")
	}
	if !firstPeople[0].Cost.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("normalized helper cost = %s", firstPeople[0].Cost)
	}
	people[0].Quantity = decimal.NewFromInt(3)
	changed, _, err := entryGroupCopies(main, nil, people)
	if err != nil || changed.RequestFingerprint == first.RequestFingerprint {
		t.Fatalf("changed hours kept identity: %v", err)
	}
	main.RequestFingerprint = "frozen-active-form"
	frozen, _, err := entryGroupCopies(main, nil, people)
	if err != nil || frozen.RequestFingerprint != main.RequestFingerprint {
		t.Fatalf("form fingerprint overwritten: %v", err)
	}
	people[0].IdempotencyKey = main.IdempotencyKey
	if _, _, err := entryGroupCopies(main, nil, people); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("duplicate key = %v", err)
	}
}

// unitEntryGroup builds a minimal valid group without touching a database.
func unitEntryGroup() (*models.Entry, []*models.Entry) {
	date := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	main := &models.Entry{NeighborID: 1, BillingYearID: 1, Date: date,
		Unit: "h", Hours: decimal.NewFromInt(2), HourlyRate: decimal.NewFromInt(40), Cost: decimal.NewFromInt(80)}
	helper := &models.Entry{NeighborID: 1, BillingYearID: 1, Date: date,
		Unit: models.UnitMannstunde, PersonName: "Free-text person",
		Quantity: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(30)}
	return main, []*models.Entry{helper}
}
