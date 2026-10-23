package models

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

func TestLegacySignatureSummaryKeepsTotalsWithinStorageLimit(t *testing.T) {
	e := Entry{Date: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), Note: strings.Repeat("Ä", 500),
		Hours: decimal.RequireFromString("2.125"), Cost: decimal.RequireFromString("123.45")}
	summary := SignatureSummary(e)
	if utf8.RuneCountInString(summary) != 500 || !strings.HasPrefix(summary, "22.10.2026 · Ä") ||
		!strings.HasSuffix(summary, "… · 2,125 h · 123,45 €") {
		t.Fatalf("invalid legacy summary (%d runes): %s", utf8.RuneCountInString(summary), summary)
	}
	e.TaskLabel = "Mähen"
	if got := SignatureSummary(e); got != "22.10.2026 · Mähen · 2,125 h · 123,45 €" {
		t.Fatalf("ordinary signature text changed: %q", got)
	}
}
