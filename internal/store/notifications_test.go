package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateNotificationTextPreservesUTF8 verifies display text is bounded
// in runes while shorter content is returned unchanged.
func TestTruncateNotificationTextPreservesUTF8(t *testing.T) {
	short := "kurz"
	if got := truncateNotificationText(short, 200); got != short {
		t.Fatalf("short text changed to %q", got)
	}
	got := truncateNotificationText(strings.Repeat("ä", 201), 200)
	if utf8.RuneCountInString(got) != 200 || !utf8.ValidString(got) {
		t.Fatalf("truncated text has %d runes or invalid UTF-8", utf8.RuneCountInString(got))
	}
}
