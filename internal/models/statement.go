package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// FeedbackKey binds a statement's line selection to the complete displayed
// line. A running statement can change between rendering and submitting it.
func (l InvoiceLine) FeedbackKey() string {
	// Quote every field separately so embedded separators are unambiguous.
	// Formatting the date also avoids time.Time.MarshalJSON's year-range error
	// turning distinct legacy lines into the same empty-payload fingerprint.
	data := fmt.Sprintf("%q|%q|%q|%q|%q|%q", l.Date.Format(time.RFC3339Nano), l.Label, l.Unit,
		l.Quantity.String(), l.UnitPrice.String(), l.Cost.String())
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}
