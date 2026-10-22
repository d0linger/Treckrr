package server

import (
	"bytes"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// A calendar subscription publishes the dated work (open invoices, installments,
// dunning deadlines, series runs) as an iCalendar feed for phone calendars. The
// URL is a revocable bearer token per user, like a share link.

const calendarOnceCookie = "treckrr_calendar_once"

// icsEscape escapes TEXT values (RFC 5545 3.3.11).
func icsEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

// icsLine writes one content line folded at 75 octets without splitting a
// UTF-8 sequence, terminated by CRLF.
func icsLine(buf *bytes.Buffer, line string) {
	limit := 75
	for len(line) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		buf.WriteString(line[:cut])
		buf.WriteString("\r\n ")
		line = line[cut:]
		limit = 74 // continuation lines start with the folding space
	}
	buf.WriteString(line)
	buf.WriteString("\r\n")
}

// buildICS renders the events as a VCALENDAR. minimal leaves neighbor names
// and amounts out; host qualifies the UIDs, base makes links absolute.
func buildICS(events []store.CalendarEvent, minimal bool, host, base string, now time.Time) []byte {
	var buf bytes.Buffer
	for _, l := range []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Treckrr//Kalender//DE", "CALSCALE:GREGORIAN",
		"METHOD:PUBLISH", "X-WR-CALNAME:Treckrr", "X-PUBLISHED-TTL:PT6H", "REFRESH-INTERVAL;VALUE=DURATION:PT6H"} {
		icsLine(&buf, l)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	for _, ev := range events {
		summary := ev.Title
		if !minimal {
			if ev.Neighbor != "" {
				summary += " · " + ev.Neighbor
			}
			if ev.Amount.Valid {
				summary += " · " + web.Money(ev.Amount.Decimal)
			}
		}
		icsLine(&buf, "BEGIN:VEVENT")
		icsLine(&buf, "UID:"+ev.UID+"@"+host)
		icsLine(&buf, "DTSTAMP:"+stamp)
		icsLine(&buf, "DTSTART;VALUE=DATE:"+ev.Day.Format("20060102"))
		icsLine(&buf, "DTEND;VALUE=DATE:"+ev.Day.AddDate(0, 0, 1).Format("20060102"))
		icsLine(&buf, "SUMMARY:"+icsEscape(summary))
		icsLine(&buf, "URL:"+base+ev.Path)
		icsLine(&buf, "TRANSP:TRANSPARENT")
		icsLine(&buf, "END:VEVENT")
	}
	icsLine(&buf, "END:VCALENDAR")
	return buf.Bytes()
}

// handleCalendarFeed serves /cal/{token}.ics after the same rate-limited token
// resolution as the public share links.
func (s *Server) handleCalendarFeed(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(r.PathValue("token"), ".ics")
	if token == "" || len(token) > maxBelegShareTokenLen {
		s.notFound(w, r)
		return
	}
	clientIP := s.clientIP(r)
	if s.logins.shareBlocked(r.Context(), clientIP) {
		s.notFound(w, r)
		return
	}
	userID, minimal, ok, err := s.store.ResolveCalendarFeed(r.Context(), store.HashToken(token))
	if err != nil {
		s.serverError(w, "calendar: resolve", err)
		return
	}
	if !ok {
		s.logins.shareMiss(r.Context(), clientIP)
		s.notFound(w, r)
		return
	}
	owner, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		s.serverError(w, "calendar: owner", err)
		return
	}
	blocked, err := s.mfaBlocked(r, owner)
	if err != nil {
		s.serverError(w, "calendar: security policy", err)
		return
	}
	if blocked {
		s.notFound(w, r)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, "calendar: company", err)
		return
	}
	events, err := s.store.CalendarEvents(r.Context(), company.EffectiveTermDays(), time.Now())
	if err != nil {
		s.serverError(w, "calendar: events", err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="treckrr.ics"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buildICS(events, minimal, r.Host, s.absoluteURL(r, ""), time.Now()))
}

// handleCalendarFeedCreate creates or replaces the signed-in user's feed and
// shows the new address once.
func (s *Server) handleCalendarFeedCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	user := userFromCtx(r)
	raw, err := auth.NewToken()
	if err != nil {
		s.serverError(w, "calendar: token", err)
		return
	}
	minimal := r.FormValue("minimal") == "1"
	if err := s.store.SetCalendarFeed(r.Context(), user.ID, store.HashToken(raw), minimal); err != nil {
		s.serverError(w, "calendar: save", err)
		return
	}
	detail := "Kalender-Abo erstellt"
	if minimal {
		detail += " (ohne Namen und Beträge)"
	}
	s.audit(r, "calendar_feed", "user", user.ID, detail)
	s.setCookie(w, r, &http.Cookie{Name: calendarOnceCookie, Value: raw, MaxAge: 60})
	redirect(w, r, "/profile#kalender")
}

// handleCalendarFeedDelete revokes the signed-in user's feed.
func (s *Server) handleCalendarFeedDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if err := s.store.DeleteCalendarFeed(r.Context(), user.ID); err != nil {
		s.serverError(w, "calendar: delete", err)
		return
	}
	s.audit(r, "calendar_feed_revoke", "user", user.ID, "Kalender-Abo widerrufen")
	s.setFlash(w, r, "success", "Kalender-Abo widerrufen — der bisherige Link ist ab sofort ungültig.")
	redirect(w, r, "/profile#kalender")
}

// calendarProfileData adds the feed state and the one-time address to the
// profile page.
func (s *Server) calendarProfileData(w http.ResponseWriter, r *http.Request, data pageData, userID int64) {
	if feed, err := s.store.GetCalendarFeed(r.Context(), userID); err == nil {
		data["CalendarFeed"] = feed
	}
	if c, err := s.cookie(r, calendarOnceCookie); err == nil && c.Value != "" && len(c.Value) <= maxBelegShareTokenLen {
		s.setCookie(w, r, &http.Cookie{Name: calendarOnceCookie, Value: "", MaxAge: -1})
		if id, _, ok, err := s.store.ResolveCalendarFeed(r.Context(), store.HashToken(c.Value)); err == nil && ok && id == userID {
			data["CalendarURL"] = s.absoluteURL(r, "/cal/"+c.Value+".ics")
		}
	}
}
