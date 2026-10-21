package models

import (
	"strings"
	"time"
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
	return e.Date.Format("02.01.2006") + " · " + label + " · " + ledgerDecimal(qty) + " " + unit + " · " + cost + " €"
}
