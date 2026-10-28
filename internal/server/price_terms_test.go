package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPriceTermFromForm_DateLengthValidation(t *testing.T) {
	tests := []struct {
		name      string
		validFrom string
		validTo   string
		wantErr   string
	}{
		{
			name:      "valid dates",
			validFrom: "2026-01-01",
			validTo:   "2026-12-31",
			wantErr:   "",
		},
		{
			name:      "oversized valid_from",
			validFrom: strings.Repeat("2026-01-01", 10),
			validTo:   "2026-12-31",
			wantErr:   "Bitte ein gültiges „Gültig ab“-Datum angeben.",
		},
		{
			name:      "oversized valid_to",
			validFrom: "2026-01-01",
			validTo:   strings.Repeat("2026-12-31", 10),
			wantErr:   "Das „Gültig bis“-Datum muss gültig sein und darf nicht vor „Gültig ab“ liegen.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"label":        {"Test Rabatt"},
				"discount_pct": {"10"},
				"valid_from":   {tc.validFrom},
				"valid_to":     {tc.validTo},
			}
			req := httptest.NewRequest("POST", "/neighbors/1/price-terms", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			_, msg := priceTermFromForm(req)
			if msg != tc.wantErr {
				t.Errorf("got msg %q, want %q", msg, tc.wantErr)
			}
		})
	}
}
