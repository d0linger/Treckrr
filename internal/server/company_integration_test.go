//go:build integration

package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestCompanyDecimalLimitsIntegration verifies accepted boundary values and
// that rejecting one oversized field leaves every saved setting untouched.
func TestCompanyDecimalLimitsIntegration(t *testing.T) {
	e := newItEnv(t)
	fields := []string{"dunning_fee_1", "dunning_fee_2", "vat_rate", "travel_flat", "travel_per_km", "small_business_limit"}
	for _, tc := range []struct{ name, value, want string }{
		{name: "empty", value: "", want: "0.00"},
		{name: "normal", value: "2,5", want: "2.50"},
		{name: "at limit", value: strings.Repeat("0", 29) + "2,5", want: "2.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"name": {"Limit test"}, "tax_mode": {"regel"}}
			for _, field := range fields {
				form.Set(field, tc.value)
			}
			if body := e.post("/admin/company", form); !strings.Contains(body, "Betriebsdaten gespeichert.") {
				t.Fatal("valid settings were not accepted")
			}
			got, err := e.st.GetCompany(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			for i, value := range []decimal.Decimal{
				got.DunningFee1, got.DunningFee2, got.VATRate,
				got.TravelFlat, got.TravelPerKm, got.SmallBusinessLimit,
			} {
				if value.StringFixed(2) != tc.want {
					t.Errorf("%s = %s, want %s", fields[i], value, tc.want)
				}
			}
		})
	}
	for _, field := range fields {
		t.Run(field+" over limit", func(t *testing.T) {
			body := e.post("/admin/company", url.Values{
				"name": {"must not save"}, field: {strings.Repeat("1", 33)},
			})
			if !strings.Contains(body, "höchstens 32 Zeichen") {
				t.Fatal("missing length validation")
			}
			got, err := e.st.GetCompany(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "Limit test" {
				t.Error("rejected submission changed company name")
			}
			for _, value := range []decimal.Decimal{
				got.DunningFee1, got.DunningFee2, got.VATRate,
				got.TravelFlat, got.TravelPerKm, got.SmallBusinessLimit,
			} {
				if value.StringFixed(2) != "2.50" {
					t.Error("rejected submission changed saved decimal settings")
				}
			}
		})
	}
}

func TestCompanyRejectsInvalidSkontoIntegration(t *testing.T) {
	e := newItEnv(t)
	c, err := e.st.GetCompany(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.SkontoPct = decimal.NewFromInt(2)
	if err := e.st.UpdateCompany(e.ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"15", "-1", "not-a-number", "1e9", strings.Repeat("9", 33)} {
		t.Run(value, func(t *testing.T) {
			body := e.post("/admin/company", url.Values{"name": {"must not save"}, "skonto_pct": {value}})
			if !strings.Contains(body, "Skonto muss zwischen 0 und 10 % liegen.") {
				t.Error("missing validation message")
			}
			got, err := e.st.GetCompany(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != c.Name || !got.SkontoPct.Equal(c.SkontoPct) {
				t.Error("invalid submission modified the company")
			}
		})
	}
	for _, value := range []string{"0", "10", "2,5", ""} {
		e.post("/admin/company", url.Values{"name": {c.Name}, "skonto_pct": {value}})
		got, err := e.st.GetCompany(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := decimal.Zero
		if value != "" {
			want = decimal.RequireFromString(strings.ReplaceAll(value, ",", "."))
		}
		if !got.SkontoPct.Equal(want) {
			t.Errorf("valid Skonto %q stored as %s", value, got.SkontoPct)
		}
	}
}
