package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/store"
)

// newReqID returns a short random id used to correlate a request's access-log
// line with any error it logs.
func newReqID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "-"
	}
	return hex.EncodeToString(b)
}

// auditPageSize is how many audit rows are shown per page (keeps the trail from
// becoming one endless scroll while staying searchable/filterable).
const auditPageSize = 50

// handleAudit renders the admin audit-trail view with search, action filter and
// pagination. Filtering, counting and paging all run in SQL so they cover the
// full audit history, not just a fixed recent batch.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	aq := auditQueryFromRequest(r)

	total, err := s.store.CountAudit(r.Context(), aq)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	totalPages := (total + auditPageSize - 1) / auditPageSize
	if totalPages < 1 {
		totalPages = 1
	}
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = p
	}
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * auditPageSize

	entries, err := s.store.ListAuditFiltered(r.Context(), aq, auditPageSize, offset)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	actions, err := s.store.AuditActions(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	users, err := s.store.AuditUsers(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	data := s.newPage(w, r, "Protokoll", "admin")
	data["Entries"] = entries
	data["Actions"] = actions
	data["Users"] = users
	data["Q"] = aq.Text
	data["Action"] = aq.Action
	data["Username"] = aq.Username
	data["From"] = r.URL.Query().Get("from")
	data["To"] = r.URL.Query().Get("to")
	data["FilterQuery"] = auditFilterQuery(r)
	data["Total"] = total
	data["Page"] = page
	data["TotalPages"] = totalPages
	data["HasPrev"] = page > 1
	data["HasNext"] = page < totalPages
	data["PrevPage"] = page - 1
	data["NextPage"] = page + 1
	rangeFrom := offset + 1
	if len(entries) == 0 {
		rangeFrom = 0 // empty result: read "0 von 0", not "1 von 0"
	}
	data["RangeFrom"] = rangeFrom
	data["RangeTo"] = offset + len(entries)
	s.render(w, r, "audit", data)
}

// handleAuditExport streams the (optionally filtered) audit trail as CSV.
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	aq := auditQueryFromRequest(r)

	filtered, err := s.store.ListAuditFiltered(r.Context(), aq, 0, 0)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	cw, finish := csvDownload(w, r, "treckrr_audit.csv")
	defer finish()
	_ = cw.Write([]string{"Zeitpunkt", "Benutzer", "Aktion", "Objekt", "ID", "Detail", "IP"})
	for _, e := range filtered {
		_ = cw.Write([]string{
			e.Created.Format("2006-01-02 15:04:05"),
			csvSafe(e.Username),
			csvSafe(e.Action),
			csvSafe(e.Entity),
			csvSafe(e.EntityID),
			csvSafe(e.Detail),
			csvSafe(e.IP),
		})
	}
}

// audit records an action in the persistent audit trail. Failures are logged
// but never block the request.
func (s *Server) audit(r *http.Request, action, entity string, entityID int64, detail string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	var (
		uid   *int64
		uname string
	)
	if u := userFromCtx(r); u != nil {
		id := u.ID
		uid = &id
		uname = u.Username
	}
	idStr := ""
	if entityID != 0 {
		idStr = strconv.FormatInt(entityID, 10)
	}
	if err := s.store.AddAudit(ctx, uid, uname, action, entity, idStr, detail, s.clientIP(r)); err != nil {
		slog.Error("audit write failed", "action", action, "entity", entity, "err", err)
	}
}

func logRequestPath(path string) string {
	if strings.HasPrefix(path, "/s/beleg/") {
		return "/s/beleg/[redacted]"
	}
	return sanitizeLog(path)
}

// fieldChange is one before/after pair for building old→new audit details.
type fieldChange struct {
	Label, Old, New string
}

// diffFields renders only the changed fields as "Label: old → new", joined with
// " · ". Empty values render as an em dash so a cleared field is visible.
func diffFields(changes ...fieldChange) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		if c.Old != c.New {
			parts = append(parts, c.Label+": "+orDash(c.Old)+" → "+orDash(c.New))
		}
	}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// maskIBAN reduces a bank account number to a change-marker for the audit trail:
// the last 4 characters are kept (enough to see WHICH account a change refers to)
// and the rest is replaced with an ellipsis, so the full IBAN never lands in the
// log. A blank value stays blank (so a cleared field still renders as an em dash),
// and a value too short to mask meaningfully is dropped to a bare marker.
func maskIBAN(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	if len(t) <= 4 {
		return "••••"
	}
	return "…" + t[len(t)-4:]
}

// ibanChangeMarker returns a masked audit fragment for an IBAN change, or "" if the
// account did not change (compared on the RAW values, so a swap to a different
// account with the same last four digits still registers). When the two masked
// tails happen to match, it emits an explicit "geändert" marker so the change is
// never silently dropped by a value-equality diff.
func ibanChangeMarker(oldIBAN, newIBAN string) string {
	if strings.TrimSpace(oldIBAN) == strings.TrimSpace(newIBAN) {
		return ""
	}
	mo, mn := maskIBAN(oldIBAN), maskIBAN(newIBAN)
	if mo == mn {
		return "IBAN: geändert (" + mn + ")"
	}
	return "IBAN: " + orDash(mo) + " → " + orDash(mn)
}

// baseName resolves a rate-basis id to its name for audit detail; falls back to
// "#id" if the basis can't be loaded (mirrors neighborName / yearLabel).
func (s *Server) baseName(r *http.Request, id int64) string {
	if b, err := s.store.GetBase(r.Context(), id); err == nil && b != nil {
		return b.Name
	}
	return "#" + strconv.FormatInt(id, 10)
}

// yearLabel resolves a billing-year id to its human year (e.g. "2025") for
// audit detail; falls back to "#id" if the year can't be loaded.
func (s *Server) yearLabel(r *http.Request, id int64) string {
	if y, err := s.store.GetBillingYear(r.Context(), id); err == nil {
		return strconv.Itoa(y.Year)
	}
	return "#" + strconv.FormatInt(id, 10)
}

// auditLogin records a login attempt where no ctx user is set yet.
func (s *Server) auditLogin(r *http.Request, username, action, detail string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.store.AddAudit(ctx, nil, username, action, "auth", "", detail, s.clientIP(r)); err != nil {
		slog.Error("audit write failed", "action", action, "err", err)
	}
}

// clientIP returns the best-effort client IP. Behind a trusted reverse proxy
// (TRUST_PROXY=true) the *right-most* X-Forwarded-For entry is used: a proxy
// appends the address it actually observed, so earlier entries are supplied by
// the client and must not be trusted (using the left-most one lets an attacker
// forge the IP to rotate past IP-keyed rate limits and to poison audit logs).
// This assumes exactly one trusted proxy hop; for N chained proxies take the
// entry N positions from the right. When not behind a trusted proxy the direct
// connection address is used so a forged header is ignored entirely.
func (s *Server) clientIP(r *http.Request) string {
	host := hostOf(r.RemoteAddr)
	if s.cfg.TrustProxy && s.proxyTrusted(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.LastIndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[i+1:])
			}
			return strings.TrimSpace(xff)
		}
	}
	return host
}

// hostOf strips the port from a RemoteAddr, tolerating an address without one.
func hostOf(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// proxyTrusted reports whether the direct peer may be trusted to have set the
// forwarded headers. With TRUSTED_PROXIES configured, only peers inside those
// CIDRs qualify (SH-05); without it, the TRUST_PROXY boolean alone decides
// (legacy behavior, so existing single-proxy deployments keep working).
func (s *Server) proxyTrusted(host string) bool {
	if len(s.cfg.TrustedProxies) == 0 {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range s.cfg.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// cookieSecure decides whether auth cookies get the Secure flag: either forced
// via COOKIE_SECURE, or auto-detected from X-Forwarded-Proto behind a trusted
// proxy that terminates TLS.
func (s *Server) cookieSecure(r *http.Request) bool {
	if s.cfg.CookieSecure {
		return true
	}
	return s.cfg.TrustProxy && s.proxyTrusted(hostOf(r.RemoteAddr)) &&
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// statusRecorder captures the response status code for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the wrapped writer to http.ResponseController, which walks the
// chain via Unwrap() to reach the underlying *http.response. Without it every
// SetWriteDeadline/Flush call made by a handler returns ErrNotSupported — silently,
// where the error is discarded — and the server's short global WriteTimeout stays
// in force. accessLog wraps EVERY request, so omitting this disabled the deadline
// extensions in the backup handlers entirely.
func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

// noisyPath reports low-value requests that should not clutter the access log
// (static assets, PWA plumbing, health checks, browser probes).
func noisyPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/livez", "/csp-report", "/manifest.webmanifest", "/sw.js", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/.well-known/")
}

// accessLog logs one meaningful request per line to stdout (Docker logs).
// Successful static/PWA/health requests are skipped to keep the log readable;
// errors are always logged.
// recoverPanic turns a handler panic into a logged 500 instead of a dropped
// connection. Without it a panic bypasses slog entirely (net/http prints a raw
// stack to stderr) and leaves no req_id to correlate. It sits INSIDE accessLog
// so the stack line carries the same req_id as the request line and the request
// itself is still logged, as a 500. http.ErrAbortHandler is re-raised: that is
// net/http's sanctioned way to abort a response mid-stream and must keep its
// special handling.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison per net/http contract
				panic(rec)
			}
			id, _ := r.Context().Value(reqIDKey).(string)
			metrics.Inc(metrics.HTTPPanics)
			slog.Error("handler panic",
				"req_id", id,
				"path", logRequestPath(r.URL.Path),
				"panic", sanitizeLog(fmt.Sprint(rec)),
				"stack", string(debug.Stack()))
			// Best effort: if the handler already streamed a body this writes into
			// it, but the status recorder still flips to 500 for the access log.
			http.Error(w, "Interner Fehler — bitte erneut versuchen.", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// statusClass buckets a status code into the 2xx/3xx/4xx/5xx label. One series
// per class rather than per code: the cardinality stays fixed and the question
// a dashboard actually asks ("are we serving errors?") is answered directly.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	}
	return "1xx"
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newReqID()
		w.Header().Set("X-Request-Id", id)
		r = r.WithContext(context.WithValue(r.Context(), reqIDKey, id))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Measured for EVERY request, including the noisy paths skipped by the log
		// below: a health-check flood or a 404 storm is exactly what a rate graph
		// should show, even when it would drown the log.
		dur := time.Since(start)
		metrics.Inc(metrics.HTTPRequests)
		metrics.IncLabel(metrics.HTTPRequestsByClass, "class", statusClass(rec.status))
		metrics.ObserveRequest(dur.Seconds())
		if noisyPath(r.URL.Path) && rec.status < 400 {
			return
		}
		user := "-"
		if u := s.currentUser(r); u != nil {
			user = u.Username
		}
		slog.Info("request",
			"req_id", id,
			"method", sanitizeLog(r.Method),
			"path", logRequestPath(r.URL.Path),
			"status", rec.status,
			"dur", dur.Round(time.Millisecond).String(),
			"user", sanitizeLog(user),
			"ip", sanitizeLog(s.clientIP(r)))
	})
}

// auditQueryFromRequest reads the protocol filters, capping every text value
// and ignoring an unparsable date rather than guessing one — a malformed
// "from" must widen the view, never silently hide history.
func auditQueryFromRequest(r *http.Request) store.AuditQuery {
	return store.AuditQuery{
		Text:     sanitizeQueryParam(r.URL.Query().Get("q"), maxNameLen),
		Action:   sanitizeQueryParam(r.URL.Query().Get("action"), maxNameLen),
		Username: sanitizeQueryParam(r.URL.Query().Get("username"), maxNameLen),
		From:     parseDay(r.URL.Query().Get("from")),
		To:       parseDay(r.URL.Query().Get("to")),
	}
}

// auditFilterQuery re-encodes the active filters, so the pager and the CSV
// link keep them instead of resetting the view.
func auditFilterQuery(r *http.Request) string {
	v := url.Values{}
	for _, k := range []string{"q", "action", "username", "from", "to"} {
		if s := strings.TrimSpace(r.URL.Query().Get(k)); s != "" {
			v.Set(k, s)
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "&" + v.Encode()
}
