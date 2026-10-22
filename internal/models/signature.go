package models

import (
	"strings"
	"time"
	"unicode/utf8"
)

// EntrySignature is the neighbor's on-site confirmation of one booking: a
// handwritten signature image with the signer's name, the device time of
// signing, the server time it arrived and the booking summary it confirmed.
// Image is empty when only the metadata was loaded.
type EntrySignature struct {
	ID            int64
	EntryID       int64
	SignerName    string
	SignedAt      time.Time
	ReceivedAt    time.Time
	Image         []byte
	ConfirmedText string
	CreatedBy     string
}

// SignatureSummary is the booking text a signature confirms: date, service,
// amount of work and line amount. Comparing it with the stored confirmation
// shows whether the booking changed after it was signed.
func SignatureSummary(e Entry) string {
	label := e.TaskLabel
	if label == "" {
		label = e.Note
	}
	if label == "" {
		label = "Sonstige"
	}
	qty, unit := e.Hours, "h"
	if e.Unit != "" && e.Unit != "h" {
		qty, unit = e.Quantity, e.Unit
	}
	cost := strings.Replace(e.Cost.StringFixed(2), ".", ",", 1)
	prefix := e.Date.Format("02.01.2006") + " · "
	suffix := " · " + ledgerDecimal(qty) + " " + unit + " · " + cost + " €"
	// confirmed_text is limited to 500 characters. Legacy bookings can use a
	// full-length note as their label; retain the date, quantity and total.
	if limit := 500 - utf8.RuneCountInString(prefix+suffix); utf8.RuneCountInString(label) > limit && limit > 0 {
		label = string([]rune(label)[:limit-1]) + "…"
	}
	return prefix + label + suffix
}
