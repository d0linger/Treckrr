package store

import "testing"

func TestAccountingExportCreateDefaults(t *testing.T) {
	tests := []struct {
		name          string
		profile       AccountingExportProfile
		wantDelimiter string
		wantComma     bool
	}{
		{name: "omitted values", profile: AccountingExportProfile{}, wantDelimiter: ";", wantComma: true},
		{name: "explicit decimal point", profile: AccountingExportProfile{DecimalCommaSet: true}, wantDelimiter: ";", wantComma: false},
		{name: "explicit delimiter", profile: AccountingExportProfile{Delimiter: ",", DecimalCommaSet: true}, wantDelimiter: ",", wantComma: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountingExportCreateDefaults(tt.profile)
			if got.Delimiter != tt.wantDelimiter || got.DecimalComma != tt.wantComma {
				t.Fatalf("defaults = delimiter %q, decimal comma %v; want %q, %v", got.Delimiter, got.DecimalComma, tt.wantDelimiter, tt.wantComma)
			}
		})
	}
}
