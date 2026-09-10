//go:build integration

package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

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
