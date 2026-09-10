package store

import (
	"slices"
	"testing"
	"time"
)

func TestEntryFilterWhere(t *testing.T) {
	t.Parallel()

	from := time.Unix(1_700_000_000, 0).UTC()
	to := from.Add(24 * time.Hour)
	tests := []struct {
		name          string
		filter        EntryFilter
		expectedWhere string
		expectedArgs  []any
	}{
		{
			name:          "year only",
			filter:        EntryFilter{YearID: 42},
			expectedWhere: " WHERE e.billing_year_id = $1",
			expectedArgs:  []any{int64(42)},
		},
		{
			name:          "neighbor follows year",
			filter:        EntryFilter{YearID: 42, NeighborID: 7},
			expectedWhere: " WHERE e.billing_year_id = $1 AND e.neighbor_id = $2",
			expectedArgs:  []any{int64(42), int64(7)},
		},
		{
			name:          "omitted filters leave no placeholder gaps",
			filter:        EntryFilter{YearID: 42, To: to, Unit: " ha "},
			expectedWhere: " WHERE e.billing_year_id = $1 AND e.entry_date <= $2 AND e.unit = $3",
			expectedArgs:  []any{int64(42), to, "ha"},
		},
		{
			name: "all filters reuse the task argument and escape wildcards",
			filter: EntryFilter{
				YearID: 42, NeighborID: 7, From: from, To: to,
				Task: ` 50%_A\B `, Unit: " ha ", Voided: "hide",
			},
			expectedWhere: " WHERE e.billing_year_id = $1 AND e.neighbor_id = $2" +
				" AND e.entry_date >= $3 AND e.entry_date <= $4" +
				` AND (lower(e.task_label) LIKE $5 ESCAPE '\' OR lower(e.note) LIKE $5 ESCAPE '\')` +
				" AND e.unit = $6 AND NOT e.voided",
			expectedArgs: []any{int64(42), int64(7), from, to, `%50\%\_a\\b%`, "ha"},
		},
		{
			name: "sql text stays in arguments and unknown void selection is ignored",
			filter: EntryFilter{
				YearID: 42,
				Task:   " X' OR TRUE; -- ",
				Unit:   " ha' UNION SELECT 1 -- ",
				Voided: "only OR TRUE; --",
			},
			expectedWhere: " WHERE e.billing_year_id = $1" +
				` AND (lower(e.task_label) LIKE $2 ESCAPE '\' OR lower(e.note) LIKE $2 ESCAPE '\')` +
				" AND e.unit = $3",
			expectedArgs: []any{int64(42), "%x' or true; --%", "ha' UNION SELECT 1 --"},
		},
		{
			name:          "only voided is a fixed predicate",
			filter:        EntryFilter{YearID: 42, Voided: "only"},
			expectedWhere: " WHERE e.billing_year_id = $1 AND e.voided",
			expectedArgs:  []any{int64(42)},
		},
		{
			name:          "blank text filters are omitted",
			filter:        EntryFilter{YearID: 42, Task: " \t ", Unit: " \n "},
			expectedWhere: " WHERE e.billing_year_id = $1",
			expectedArgs:  []any{int64(42)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			where, args := entryFilterWhere(tt.filter)
			if where != tt.expectedWhere {
				t.Errorf("where = %q, want %q", where, tt.expectedWhere)
			}
			if !slices.Equal(args, tt.expectedArgs) {
				t.Errorf("args = %#v, want %#v", args, tt.expectedArgs)
			}
		})
	}
}

func TestEntryFilterOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sort     string
		isDesc   bool
		expected string
	}{
		{
			name:     "default date ascending",
			expected: " ORDER BY e.entry_date ASC, e.id ASC",
		},
		{
			name: "date descending", sort: "date", isDesc: true,
			expected: " ORDER BY e.entry_date DESC, e.id DESC",
		},
		{
			name: "cost ascending", sort: "cost",
			expected: " ORDER BY e.cost ASC, e.id ASC",
		},
		{
			name: "cost descending", sort: "cost", isDesc: true,
			expected: " ORDER BY e.cost DESC, e.id DESC",
		},
		{
			name: "neighbor ascending", sort: "neighbor",
			expected: " ORDER BY n.name ASC, e.entry_date ASC, e.id ASC",
		},
		{
			name: "neighbor descending", sort: "neighbor", isDesc: true,
			expected: " ORDER BY n.name DESC, e.entry_date DESC, e.id DESC",
		},
		{
			name: "sql text falls back to date ascending", sort: "cost DESC; DROP TABLE entries; --",
			expected: " ORDER BY e.entry_date ASC, e.id ASC",
		},
		{
			name: "sql text falls back to date descending", sort: "neighbor NULLS FIRST; --", isDesc: true,
			expected: " ORDER BY e.entry_date DESC, e.id DESC",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := entryFilterOrder(tt.sort, tt.isDesc); got != tt.expected {
				t.Errorf("order = %q, want %q", got, tt.expected)
			}
		})
	}
}
