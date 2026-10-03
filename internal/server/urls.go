package server

import (
	"net/http"
	"strconv"
	"strings"
)

// trimmed returns the trimmed value of a form field.
func trimmed(r *http.Request, name string) string {
	return strings.TrimSpace(r.FormValue(name))
}

func itoa(n int) string { return strconv.Itoa(n) }

// plural renders "<n> <singular|plural>" choosing the form by count.
func plural(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// baseQuery renders "?base=<id>" or "" when id is zero.
func baseQuery(baseID int64) string {
	if baseID == 0 {
		return ""
	}
	return "?base=" + strconv.FormatInt(baseID, 10)
}

// yearQuery renders "?year=<id>" or "" when id is zero.
func yearQuery(yearID int64) string {
	if yearID == 0 {
		return ""
	}
	return "?year=" + strconv.FormatInt(yearID, 10)
}

func dashboardURL(yearID int64) string { return "/" + yearQuery(yearID) }

func neighborURL(id, yearID int64) string {
	return "/neighbors/" + strconv.FormatInt(id, 10) + yearQuery(yearID)
}

// neighborSectionURL opens one focused area of a neighbor's yearly account.
func neighborSectionURL(id, yearID int64, section string) string {
	separator := "?"
	if yearID != 0 {
		separator = "&"
	}
	return neighborURL(id, yearID) + separator + "view=" + section
}

// neighborBookingURL opens the single-booking capture view.
func neighborBookingURL(id, yearID int64) string {
	return neighborSectionURL(id, yearID, "booking")
}

// neighborBookingsURL opens the combined booking and ledger history.
func neighborBookingsURL(id, yearID int64) string {
	return neighborSectionURL(id, yearID, "bookings")
}

// neighborPaymentsURL opens payment history and settlement actions.
func neighborPaymentsURL(id, yearID int64) string {
	return neighborSectionURL(id, yearID, "payments")
}

func pricesURL(baseID int64) string   { return "/prices" + baseQuery(baseID) }
func gespanneURL(baseID int64) string { return "/gespanne" + baseQuery(baseID) }
