package server

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestEpcPayload(t *testing.T) {
	got := epcPayload("Hof Bergmann", "AT61 1904 3002 3457 3201", decimal.RequireFromString("246.34"), "2026-014")
	want := []string{
		"BCD", "002", "1", "SCT",
		"",                     // BIC
		"Hof Bergmann",         // name
		"AT611904300234573201", // IBAN, spaces stripped
		"EUR246.34",            // amount
		"",                     // purpose
		"",                     // structured
		"2026-014",             // unstructured remittance
	}
	lines := strings.Split(got, "\n")
	if len(lines) != len(want) {
		t.Fatalf("expected %d fields, got %d: %q", len(want), len(lines), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestEpcPayloadTruncatesRuneSafe(t *testing.T) {
	longName := strings.Repeat("ä", 80) // 80 runes (160 bytes)
	got := epcPayload(longName, "AT61", decimal.RequireFromString("1.00"), strings.Repeat("x", 200))
	lines := strings.Split(got, "\n")
	if r := []rune(lines[5]); len(r) != 70 {
		t.Errorf("name should be truncated to 70 runes, got %d", len(r))
	}
	if r := []rune(lines[10]); len(r) != 140 {
		t.Errorf("remittance should be truncated to 140 runes, got %d", len(r))
	}
}
