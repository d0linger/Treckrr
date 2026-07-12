package server

import (
	"encoding/csv"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// auditPageSize is how many audit rows are shown per page (keeps the trail from
// becoming one endless scroll while staying searchable/filterable).
const auditPageSize = 50

// handleAudit renders the admin audit-trail view with search, action filter and
// pagination. Filtering, counting and paging all run in SQL so they cover the
// full audit history, not just a fixed recent batch.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	action := r.URL.Query().Get("action")

	total, err := s.store.CountAudit(r.Context(), q, action)
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

	entries, err := s.store.ListAuditFiltered(r.Context(), q, action, auditPageSize, offset)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	actions, err := s.store.AuditActions(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	data := s.newPage(w, r, "Protokoll", "admin")
	data["Entries"] = entries
	data["Actions"] = actions
	data["Q"] = q
	data["Action"] = action
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
	filtered, err := s.store.ListAuditFiltered(r.Context(),
		r.URL.Query().Get("q"), r.URL.Query().Get("action"), 0, 0)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"treckrr_audit.csv\"")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	defer cw.Flush()
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
	if err := s.store.AddAudit(r.Context(), uid, uname, action, entity, idStr, detail, s.clientIP(r)); err != nil {
		log.Printf("audit write failed (%s %s): %v", action, entity, err)
	}
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
	if err := s.store.AddAudit(r.Context(), nil, username, action, "auth", "", detail, s.clientIP(r)); err != nil {
		log.Printf("audit write failed (%s): %v", action, err)
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
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.LastIndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[i+1:])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cookieSecure decides whether auth cookies get the Secure flag: either forced
// via COOKIE_SECURE, or auto-detected from X-Forwarded-Proto behind a trusted
// proxy that terminates TLS.
func (s *Server) cookieSecure(r *http.Request) bool {
	if s.cfg.CookieSecure {
		return true
	}
	return s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
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

// noisyPath reports low-value requests that should not clutter the access log
// (static assets, PWA plumbing, health checks, browser probes).
func noisyPath(p string) bool {
	switch p {
	case "/healthz", "/manifest.webmanifest", "/sw.js", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/.well-known/")
}

// accessLog logs one meaningful request per line to stdout (Docker logs).
// Successful static/PWA/health requests are skipped to keep the log readable;
// errors are always logged.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if noisyPath(r.URL.Path) && rec.status < 400 {
			return
		}
		user := "-"
		if u := s.currentUser(r); u != nil {
			user = u.Username
		}
		log.Printf("%s %s %d %s user=%s ip=%s",
			sanitizeLog(r.Method), sanitizeLog(r.URL.Path), rec.status,
			time.Since(start).Round(time.Millisecond), sanitizeLog(user), sanitizeLog(s.clientIP(r)))
	})
}

// sanitizeLog strips CR/LF from request-derived values so they cannot forge
// additional log lines (log injection).
func sanitizeLog(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}
