package server

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidSavedViewNameCountsCharacters(t *testing.T) {
	if !validSavedViewName(strings.Repeat("\u00e4", savedViewNameMax)) {
		t.Fatal("60 multibyte characters should be accepted")
	}
	if validSavedViewName(strings.Repeat("\u00e4", savedViewNameMax+1)) {
		t.Fatal("61 characters should be rejected")
	}
	if validSavedViewName("") {
		t.Fatal("empty name should be rejected")
	}
}

func TestSavedBookingQueryFitsCreateLimit(t *testing.T) {
	values := url.Values{
		"neighbor_id": {"9223372036854775807"},
		"task":        {strings.Repeat("?", maxNameLen)},
		"direction":   {"out"},
		"kind":        {"equipment"},
		"from":        {"2026-01-01"},
		"to":          {"2026-12-31"},
		"unit":        {strings.Repeat("?", 30)},
		"voided":      {"hide"},
		"sort":        {"neighbor"},
		"dir":         {"desc"},
	}
	req := httptest.NewRequest(http.MethodGet, "/buchungen?"+values.Encode(), nil)

	query := savedBookingQuery(req, math.MaxInt64)

	if len(query) > maxSavedViewQueryLen {
		t.Fatalf("maximum canonical query length = %d, exceeds create limit %d", len(query), maxSavedViewQueryLen)
	}
}
