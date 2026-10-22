package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// Statement links ("Zwischenstand teilen") show a neighbor the year's work that
// is not invoiced yet plus the annual account, before any invoice exists. They
// follow the invoice-link pattern: only the token hash is stored, the raw link
// reaches the creator once through a short-lived cookie, and revocation is
// immediate.
const (
	statementOnceCookie = "treckrr_statement_once"
	statementQRCookie   = "treckrr_statement_qr"
)

// statementShareView is the public, read-only projection behind a link. It
// holds the would-be invoice lines and the account totals, never notes,
// photos or other neighbors.
type statementShareView struct {
	Issuer    string
	Recipient string
	Year      int
	AsOf      time.Time
	Pending   *models.InvoiceContent
	Account   *store.NeighborYearHistoryRow
}

// explicitYear reads the mandatory year of a statement-link mutation. Unlike
// resolveYear it never falls back to the latest year, so a stale form cannot
// create or revoke a link of the wrong account.
func (s *Server) explicitYear(r *http.Request) (*models.BillingYear, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("year"), 10, 64)
	if err != nil || id <= 0 {
		return nil, false
	}
	year, err := s.store.GetBillingYear(r.Context(), id)
	return year, err == nil
}

// statementAccountURL is the neighbor page anchor holding the link card.
func statementAccountURL(neighborID, yearID int64) string {
	return "/neighbors/" + itoa64(neighborID) + "?year=" + itoa64(yearID) + "#zwischenstand"
}

// handleStatementShareCreate mints a statement link for one neighbor and year.
func (s *Server) handleStatementShareCreate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, ok := s.explicitYear(r)
	if !ok {
		s.badRequest(w, "Das Abrechnungsjahr fehlt — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	days := belegShareDays(strings.TrimSpace(r.FormValue("days")))
	raw, err := auth.NewToken()
	if err != nil {
		s.serverError(w, "statement share: token", err)
		return
	}
	expires := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	createdBy := ""
	if u := userFromCtx(r); u != nil {
		createdBy = u.Username
	}
	if _, err := s.store.CreateStatementShare(r.Context(), store.HashToken(raw), neighbor.ID, year.ID, expires, createdBy); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.setFlash(w, r, "error", neighbor.Name+" ist dem Jahr "+strconv.Itoa(year.Year)+" nicht zugeordnet.")
			redirect(w, r, statementAccountURL(neighbor.ID, year.ID))
			return
		}
		s.serverError(w, "statement share: store", err)
		return
	}
	s.audit(r, "statement_share", "neighbor", neighbor.ID,
		neighbor.Name+" · "+strconv.Itoa(year.Year)+" · Zwischenstand gültig "+strconv.Itoa(days)+" Tage (bis "+expires.Format("02.01.2006")+")")
	// The raw token never enters a URL: one render of the account page shows it.
	s.setCookie(w, r, &http.Cookie{Name: statementOnceCookie, Value: raw, MaxAge: 60})
	s.setCookie(w, r, &http.Cookie{Name: statementQRCookie, Value: raw, MaxAge: 300})
	redirect(w, r, statementAccountURL(neighbor.ID, year.ID))
}

// handleStatementShareRevoke deletes one statement link of the account.
func (s *Server) handleStatementShareRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, ok := s.explicitYear(r)
	if !ok {
		s.badRequest(w, "Das Abrechnungsjahr fehlt — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	shareID := formInt64(r, "share_id")
	if shareID <= 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	revoked, err := s.store.RevokeStatementShare(r.Context(), shareID, id, year.ID)
	if err != nil {
		s.serverError(w, "statement share revoke", err)
		return
	}
	if revoked {
		s.audit(r, "statement_share_revoke", "neighbor", id, s.neighborName(r, id)+" · "+strconv.Itoa(year.Year)+" · Zwischenstand-Link gelöscht")
		s.setFlash(w, r, "success", "Zwischenstand-Link gelöscht — er ist ab sofort ungültig.")
	} else {
		s.setFlash(w, r, "info", "Der Link war bereits gelöscht oder abgelaufen.")
	}
	redirect(w, r, statementAccountURL(id, year.ID))
}

// handleStatementShareQR renders the freshly created link as a QR code without
// the raw token appearing in this endpoint's URL or access log.
func (s *Server) handleStatementShareQR(w http.ResponseWriter, r *http.Request) {
	c, err := s.cookie(r, statementQRCookie)
	if err != nil || c.Value == "" || len(c.Value) > maxBelegShareTokenLen {
		s.notFound(w, r)
		return
	}
	if _, _, _, ok, err := s.store.ResolveStatementShare(r.Context(), store.HashToken(c.Value)); err != nil || !ok {
		s.notFound(w, r)
		return
	}
	png, err := qrPNG(s.absoluteURL(r, "/s/konto/"+c.Value))
	if err != nil {
		s.serverError(w, "statement share qr", err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// statementShareData adds the link card to the neighbor overview: the active
// links and the one-time raw link for writers, and the feedback for everyone.
func (s *Server) statementShareData(w http.ResponseWriter, r *http.Request, data pageData, neighborID, yearID int64) error {
	feedback, err := s.store.ListStatementFeedback(r.Context(), neighborID, yearID)
	if err != nil {
		return err
	}
	data["StatementFeedback"] = feedback
	user := userFromCtx(r)
	if user == nil || !user.CanWrite() {
		return nil
	}
	shares, err := s.store.ListStatementShares(r.Context(), neighborID, yearID)
	if err != nil {
		return err
	}
	data["StatementShares"] = shares
	data["ShareDayOptions"], data["ShareDefaultDays"] = belegShareDayOptions, belegShareDefaultDays
	if c, err := s.cookie(r, statementOnceCookie); err == nil && c.Value != "" && len(c.Value) <= maxBelegShareTokenLen {
		s.setCookie(w, r, &http.Cookie{Name: statementOnceCookie, Value: "", MaxAge: -1})
		if _, nID, yID, ok, err := s.store.ResolveStatementShare(r.Context(), store.HashToken(c.Value)); err == nil && ok && nID == neighborID && yID == yearID {
			data["StatementShareToken"], data["StatementShareURL"] = c.Value, s.absoluteURL(r, "/s/konto/"+c.Value)
		}
	}
	return nil
}

// resolveStatementShare applies the bounded, rate-limited token resolution of
// the invoice portal to every public statement action.
func (s *Server) resolveStatementShare(w http.ResponseWriter, r *http.Request) (shareID, neighborID, yearID int64, token string, ok bool) {
	token = r.PathValue("token")
	if token == "" || len(token) > maxBelegShareTokenLen {
		s.notFound(w, r)
		return
	}
	clientIP := s.clientIP(r)
	if s.logins.shareBlocked(r.Context(), clientIP) {
		s.notFound(w, r)
		return
	}
	var err error
	shareID, neighborID, yearID, ok, err = s.store.ResolveStatementShare(r.Context(), store.HashToken(token))
	if err != nil {
		s.serverError(w, "statement: resolve", err)
		return 0, 0, 0, token, false
	}
	if !ok {
		s.logins.shareMiss(r.Context(), clientIP)
		s.notFound(w, r)
		return 0, 0, 0, token, false
	}
	return
}

// loadStatementShareView assembles the public projection. Work that is not
// invoiced yet comes from the same builder as the Festschreibung preview, so
// the lines read exactly as the later invoice would.
func (s *Server) loadStatementShareView(ctx context.Context, neighborID, yearID int64) (statementShareView, error) {
	view := statementShareView{AsOf: time.Now()}
	year, err := s.store.GetBillingYear(ctx, yearID)
	if err != nil {
		return view, err
	}
	view.Year = year.Year
	company, err := s.store.GetCompany(ctx)
	if err != nil {
		return view, err
	}
	view.Issuer = company.Name
	neighbor, err := s.store.GetNeighbor(ctx, neighborID)
	if err != nil {
		return view, err
	}
	view.Recipient = neighbor.Name
	content, err := s.store.BuildPendingInvoiceContent(ctx, yearID, neighborID)
	switch {
	case errors.Is(err, store.ErrNoPendingEntries):
	case err != nil:
		return view, err
	default:
		view.Pending = &content
	}
	history, err := s.store.NeighborYearHistory(ctx, neighborID)
	if err != nil {
		return view, err
	}
	for i := range history {
		if history[i].YearID == yearID {
			view.Account = &history[i]
			break
		}
	}
	return view, nil
}

// handleSharedStatement renders the public running statement for a valid link.
func (s *Server) handleSharedStatement(w http.ResponseWriter, r *http.Request) {
	_, neighborID, yearID, token, ok := s.resolveStatementShare(w, r)
	if !ok {
		return
	}
	view, err := s.loadStatementShareView(r.Context(), neighborID, yearID)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "statement: view", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	data := pageData{
		"Title": "Zwischenstand " + strconv.Itoa(view.Year), "Theme": s.themeFromCookie(r),
		"Statement": view, "StatementToken": token, "FeedbackResult": feedbackResult(r),
	}
	s.render(w, r, "statement_share", data)
}

// handleStatementFeedback records a confirmation or an objection. It never
// changes bookings or balances; a line choice is frozen as label and amount.
func (s *Server) handleStatementFeedback(w http.ResponseWriter, r *http.Request) {
	shareID, neighborID, yearID, token, ok := s.resolveStatementShare(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Rückmeldung konnte nicht verarbeitet werden.")
		return
	}
	status := r.FormValue("status")
	if status != "confirmed" && status != "disputed" {
		s.badRequest(w, "Ungültige Rückmeldung.")
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if utf8.RuneCountInString(message) > maxNoteLen {
		s.badRequest(w, "Die Nachricht ist zu lang.")
		return
	}
	if status == "disputed" && message == "" {
		s.badRequest(w, "Bitte den Einwand kurz beschreiben.")
		return
	}
	var label string
	var amount decimal.NullDecimal
	if raw := strings.TrimSpace(r.FormValue("line")); raw != "" && status == "disputed" {
		idx, err := strconv.Atoi(raw)
		if err != nil || idx < 0 || utf8.RuneCountInString(raw) > 6 {
			s.badRequest(w, "Ungültige Position.")
			return
		}
		content, err := s.store.BuildPendingInvoiceContent(r.Context(), yearID, neighborID)
		if err != nil && !errors.Is(err, store.ErrNoPendingEntries) {
			s.serverError(w, "statement feedback: lines", err)
			return
		}
		if err != nil || idx >= len(content.Lines) {
			s.badRequest(w, "Die Positionen haben sich inzwischen geändert. Bitte die Seite neu laden und den Einwand erneut senden.")
			return
		}
		line := content.Lines[idx]
		label = web.Date(line.Date) + " · " + line.Label
		if utf8.RuneCountInString(label) > 300 {
			label = string([]rune(label)[:300])
		}
		amount = decimal.NullDecimal{Decimal: line.Cost, Valid: true}
	}
	if status == "confirmed" {
		message = ""
	}
	err := s.store.CreateStatementFeedback(r.Context(), shareID, neighborID, yearID, status, label, amount, message)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return
	case errors.Is(err, store.ErrFeedbackLimit):
		writeErrorPage(w, http.StatusTooManyRequests, "Zu viele Rückmeldungen",
			"Über diesen Link wurden heute bereits viele Rückmeldungen gesendet. Bitte morgen erneut versuchen.")
		return
	case err != nil:
		s.serverError(w, "statement feedback", err)
		return
	}
	redirect(w, r, "/s/konto/"+token+"?feedback="+status)
}
