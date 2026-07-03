package server

import (
	"encoding/csv"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"treckrr/internal/models"
)

// filterAudit applies a text query and action filter, and returns the sorted
// set of all actions seen (for the filter dropdown).
func filterAudit(all []models.AuditEntry, q, action string) (filtered []models.AuditEntry, actions []string) {
	q = strings.ToLower(strings.TrimSpace(q))
	seen := map[string]bool{}
	for _, e := range all {
		if !seen[e.Action] {
			seen[e.Action] = true
			actions = append(actions, e.Action)
		}
		if action != "" && e.Action != action {
			continue
		}
		if q != "" {
			hay := strings.ToLower(e.Username + " " + e.Action + " " + e.Entity + " " +
				e.EntityID + " " + e.Detail + " " + e.IP)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	sort.Strings(actions)
	return filtered, actions
}

// handleAudit renders the admin audit-trail view with search & action filter.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListAudit(r.Context(), 1000)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query().Get("q")
	action := r.URL.Query().Get("action")
	filtered, actions := filterAudit(all, q, action)

	data := s.newPage(w, r, "Protokoll", "admin")
	data["Entries"] = filtered
	data["Actions"] = actions
	data["Q"] = q
	data["Action"] = action
	s.render(w, r, "audit", data)
}

// handleAuditExport streams the (optionally filtered) audit trail as CSV.
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListAudit(r.Context(), 5000)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	filtered, _ := filterAudit(all, r.URL.Query().Get("q"), r.URL.Query().Get("action"))

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
			e.Username, e.Action, e.Entity, e.EntityID, e.Detail, e.IP,
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

// auditLogin records a login attempt where no ctx user is set yet.
func (s *Server) auditLogin(r *http.Request, username, action, detail string) {
	if err := s.store.AddAudit(r.Context(), nil, username, action, "auth", "", detail, s.clientIP(r)); err != nil {
		log.Printf("audit write failed (%s): %v", action, err)
	}
}

// clientIP returns the best-effort client IP. Behind a trusted reverse proxy
// (TRUST_PROXY=true) the left-most X-Forwarded-For entry is used; otherwise the
// direct connection address is used (so a directly-exposed app cannot be
// spoofed via a forged header).
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
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
