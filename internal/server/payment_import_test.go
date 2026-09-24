package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBankImportNote(t *testing.T) {
	const prefix = "Bank-Import: "
	tests := []struct {
		name      string
		reference string
		want      string
		wantRunes int
	}{
		{
			name:      "empty reference",
			reference: "  ",
			want:      "Bank-Import",
			wantRunes: utf8.RuneCountInString("Bank-Import"),
		},
		{
			name:      "reference at limit",
			reference: strings.Repeat("a", maxNoteLen-utf8.RuneCountInString(prefix)),
			wantRunes: maxNoteLen,
		},
		{
			name:      "multibyte reference over limit",
			reference: strings.Repeat("ä", maxNoteLen+100),
			wantRunes: maxNoteLen,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bankImportNote(tt.reference)
			if tt.want != "" && got != tt.want {
				t.Errorf("bankImportNote(%q) = %q, want %q", tt.reference, got, tt.want)
			}
			if gotRunes := utf8.RuneCountInString(got); gotRunes != tt.wantRunes {
				t.Errorf("bankImportNote() length = %d runes, want %d", gotRunes, tt.wantRunes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("bankImportNote() returned invalid UTF-8: %q", got)
			}
			if strings.TrimSpace(tt.reference) != "" && !strings.HasPrefix(got, prefix) {
				t.Errorf("bankImportNote() = %q, want prefix %q", got, prefix)
			}
		})
	}
}
