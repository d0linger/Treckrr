package server

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// calendarDay is one cell of the month grid.
type calendarDay struct {
	Date    time.Time
	InMonth bool
	Weekend bool
	Today   bool
	Count   int
	Sum     decimal.Decimal
	URL     string
	Label   string // accessible summary of the cell
}

// bookingCalendar is the month view of the filtered bookings.
type bookingCalendar struct {
	Month            time.Time
	Weeks            [][]calendarDay
	Count, Days      int
	Sum              decimal.Decimal
	PrevURL, NextURL string
	ListURL          string
}

var germanWeekdays = [...]string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}

var germanMonths = [...]string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August",
	"September", "Oktober", "November", "Dezember"}

// MonthLabel names the month in German, e.g. "Mai 2026".
func (c bookingCalendar) MonthLabel() string {
	return germanMonths[c.Month.Month()-1] + " " + strconv.Itoa(c.Month.Year())
}

// calendarMonth picks the month to show: ?month=YYYY-MM, else the month of the
// filter's start, else this month when it lies in the billing year, else the
// year's January.
func calendarMonth(r *http.Request, year *models.BillingYear, f store.BookingFilter, now time.Time) time.Time {
	monthRaw := sanitizeQueryParam(r.URL.Query().Get("month"), maxNameLen)
	if m, err := time.ParseInLocation("2006-01", monthRaw, time.Local); err == nil && m.Year() > 1900 && m.Year() < 3100 {
		return m
	}
	if !f.From.IsZero() {
		return time.Date(f.From.Year(), f.From.Month(), 1, 0, 0, 0, 0, time.Local)
	}
	if now.Year() == year.Year {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	}
	return time.Date(year.Year, time.January, 1, 0, 0, 0, 0, time.Local)
}

// calendarQuery keeps the list filters (not paging or sorting) for links out
// of the calendar.
func calendarQuery(r *http.Request, yearID int64, keep ...string) url.Values {
	q := url.Values{"year": {itoa64(yearID)}}
	for _, k := range keep {
		if v := sanitizeQueryParam(r.URL.Query().Get(k), maxNameLen); v != "" {
			q.Set(k, v)
		}
	}
	return q
}

// handleBookingCalendar renders the filtered bookings as a month grid with
// count and sum per day, so gaps and clusters show at once. A day opens the
// list filtered to that day.
func (s *Server) handleBookingCalendar(w http.ResponseWriter, r *http.Request, year *models.BillingYear, f store.BookingFilter, data pageData) {
	now := time.Now()
	month := calendarMonth(r, year, f, now)
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.Local)
	last := first.AddDate(0, 1, -1)

	// The month narrows the filter's own period; it never widens it.
	cf := f
	cf.Limit, cf.Offset = 0, 0
	if cf.From.IsZero() || cf.From.Before(first) {
		cf.From = first
	}
	if cf.To.IsZero() || cf.To.After(last) {
		cf.To = last
	}
	totals := map[string]store.BookingDay{}
	if !cf.From.After(cf.To) {
		var err error
		if totals, err = s.store.BookingDayTotals(r.Context(), cf); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
	}

	filterKeys := []string{"neighbor_id", "task", "unit", "voided", "direction", "kind"}
	cal := bookingCalendar{Month: first}
	nav := func(m time.Time) string {
		q := calendarQuery(r, year.ID, append(filterKeys, "from", "to")...)
		q.Set("view", "kalender")
		q.Set("month", m.Format("2006-01"))
		return "/buchungen?" + q.Encode()
	}
	cal.PrevURL, cal.NextURL = nav(first.AddDate(0, -1, 0)), nav(first.AddDate(0, 1, 0))
	cal.ListURL = "/buchungen?" + calendarQuery(r, year.ID, append(filterKeys, "from", "to")...).Encode()

	// Weeks start on Monday, as in every German calendar.
	start := first.AddDate(0, 0, -((int(first.Weekday()) + 6) % 7))
	today := now.Format("2006-01-02")
	for day := start; !day.After(last) || day.Weekday() != time.Monday; day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Monday {
			cal.Weeks = append(cal.Weeks, nil)
		}
		key := day.Format("2006-01-02")
		cell := calendarDay{Date: day, InMonth: day.Month() == first.Month(),
			Weekend: day.Weekday() == time.Saturday || day.Weekday() == time.Sunday, Today: key == today}
		if cell.InMonth {
			t := totals[key]
			cell.Count, cell.Sum = t.Count, t.Sum
			q := calendarQuery(r, year.ID, filterKeys...)
			q.Set("from", key)
			q.Set("to", key)
			cell.URL = "/buchungen?" + q.Encode()
			cell.Label = germanWeekdays[day.Weekday()] + ", " + day.Format("02.01.2006") + ": "
			if t.Count == 0 {
				cell.Label += "keine Buchung"
			} else {
				cell.Label += strconv.Itoa(t.Count) + " Buchung(en), " + web.Money(t.Sum)
				cal.Days++
			}
			cal.Count += t.Count
			cal.Sum = cal.Sum.Add(t.Sum)
		}
		cal.Weeks[len(cal.Weeks)-1] = append(cal.Weeks[len(cal.Weeks)-1], cell)
	}
	data["Calendar"] = cal
	s.render(w, r, "entries", data)
}
