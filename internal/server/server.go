// Package server wires the HTTP routes, middleware and handlers together.
package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

const (
	sessionCookie = "treckrr_session"
	flashCookie   = "treckrr_flash"
	sessionTTL    = 30 * 24 * time.Hour
	// sessionAbsoluteTTL caps a session's total lifetime from creation regardless
	// of sliding refresh, so a stolen token can't be renewed indefinitely.
	sessionAbsoluteTTL = 90 * 24 * time.Hour
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg             *config.Config
	store           *store.Store
	backup          *backup.Service
	templates       map[string]*template.Template
	logins          *loginLimiter
	wa              *webauthn.WebAuthn
	started         time.Time
	maintenance     atomic.Bool  // set during a restore: the gate serves 503 for normal traffic
	leaseLost       atomic.Bool  // irreversible: the application lease session was lost
	draining        atomic.Bool  // process shutdown began: no new background tasks
	activity        sync.RWMutex // drains requests and background maintenance before restore
	restoreLease    func(context.Context) (func() error, error)
	backgroundMu    sync.Mutex
	backgroundNext  uint64
	backgroundTasks map[uint64]backgroundTask
	// photoSlots bounds concurrent image decodes; see maxConcurrentPhotoDecodes.
	photoSlots chan struct{}
}

// New constructs a Server and parses templates.
func New(cfg *config.Config, st *store.Store, bk *backup.Service) (*Server, error) {
	tpl, err := web.Templates()
	if err != nil {
		return nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "Treckrr",
		RPOrigins:     []string{cfg.RPOrigin},
		// Require user verification (PIN/biometric), not just user presence, for
		// both registration and assertion — presence-only authenticators are
		// rejected (T-03). The per-ceremony options below set the same explicitly.
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, store: st, backup: bk, templates: tpl,
		logins: newLoginLimiter(st), wa: wa, started: time.Now(),
		photoSlots: make(chan struct{}, maxConcurrentPhotoDecodes),
	}, nil
}

type ctxKey string

const userCtxKey ctxKey = "user"
const reqIDKey ctxKey = "reqid"

// Handler builds the top-level http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Fallback for any path no route matches → branded 404. In Go 1.22+ ServeMux
	// a more specific pattern always wins, so this only catches truly unknown paths
	// (the exact-root "GET /{$}" and every registered route take precedence).
	mux.HandleFunc("/", s.handleNotFound)

	// Health & PWA plumbing (public).
	s.registerPublicRoutes(mux)
	// Prometheus metrics — only registered when METRICS_TOKEN is set AND long enough
	// to resist brute force; the handler itself enforces the bearer token so an
	// unauthenticated scrape gets 401. A set-but-too-short token stays disabled with
	// a warning rather than exposing a guessable endpoint.

	// Auth (public).

	// Authenticated area.
	s.registerWorkflowRoutes(mux)
	s.registerDocumentRoutes(mux)
	s.registerBookingRoutes(mux)
	s.registerCatalogRoutes(mux)

	s.registerAccountAndOperationsRoutes(mux)

	s.registerAdminRoutes(mux)

	// securityHeaders wraps maintenanceGate so even the 503 maintenance page carries
	// the nosniff/frame/CSP headers (the gate returns before inner handlers run).
	// Drain admission wraps userCache so session SELECT/UPDATE cannot cross a
	// restore boundary. Inner handlers still share one cached user resolution.
	return s.securityHeaders(s.maintenanceGate(s.userCacheMW(s.limitBody(s.accessLog(s.recoverPanic(s.csrf(mux)))))))
}

// userCacheMW installs the per-request session memo (see currentUser).
func (s *Server) userCacheMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = withUserCache(r)
		if isMaintenanceExempt(r.URL.Path) {
			// Static files/liveness bypass drain; access logging must not resolve
			// a session (including its sliding-expiry UPDATE) for these routes.
			r.Context().Value(userCacheKey).(*userCache).done = true
		}
		next.ServeHTTP(w, r)
	})
}

// setMaintenance toggles maintenance mode. While on, maintenanceGate serves 503
// for normal traffic so no request can observe the database mid-restore.
func (s *Server) setMaintenance(on bool) { s.maintenance.Store(on) }

// maintenanceActive reports both reversible restore maintenance and irreversible
// application-lease loss; either state must keep normal traffic closed.
func (s *Server) maintenanceActive() bool {
	return s.maintenance.Load() || s.leaseLost.Load()
}

// clearRestoreMaintenance ends only reversible restore maintenance. A concurrent
// lease loss wins permanently, including when it races this cleanup.
func (s *Server) clearRestoreMaintenance() {
	if s.leaseLost.Load() {
		return
	}
	s.maintenance.Store(false)
	if s.leaseLost.Load() {
		s.maintenance.Store(true)
	}
}

// maintenanceGate returns 503 for normal traffic while a restore is in progress.
// Only the DB-free liveness probe (/livez) and static assets stay reachable, so
// the orchestrator doesn't restart the app and the 503 page's CSS renders. Every
// app route — reads included, since a read could hit a half-restored schema — is
// refused. Readiness (/readyz, /healthz) is intentionally NOT exempt: during a
// restore the app is genuinely not ready, so it should report 503 — and doing it
// through the gate is deterministic, versus letting handleHealth's DB ping flap.
func (s *Server) maintenanceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.maintenanceActive() && !isMaintenanceExempt(r.URL.Path) {
			w.Header().Set("Retry-After", "30")
			writeErrorPage(w, http.StatusServiceUnavailable, "Wartung",
				"Eine Wiederherstellung läuft gerade. Bitte in Kürze erneut versuchen.")
			return
		}
		// Restore preflight (including session resolution) participates in drain.
		// beginRestore releases only its own read admission before upgrading.
		isRestore := r.Method == http.MethodPost && r.URL.Path == "/admin/backup/restore"
		if !isMaintenanceExempt(r.URL.Path) {
			s.activity.RLock()
			var once sync.Once
			release := func() { once.Do(s.activity.RUnlock) }
			defer release()
			if s.maintenanceActive() {
				http.Error(w, "Wartung: bitte später erneut versuchen.", http.StatusServiceUnavailable)
				return
			}
			if isRestore {
				r = r.WithContext(context.WithValue(r.Context(), restoreAdmissionKey{}, release))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isMaintenanceExempt reports paths that stay reachable during maintenance: only
// the DB-free liveness probe (so the orchestrator doesn't restart the app) and
// static assets (so the 503 page's CSS renders). Readiness probes are gated too —
// the app really isn't ready mid-restore.
func isMaintenanceExempt(p string) bool {
	if p == "/livez" {
		return true
	}
	return strings.HasPrefix(p, "/static/")
}

// maxRequestBody caps how many bytes the server will read from a request body.
// Every endpoint takes only a small form post or a few-KB WebAuthn JSON payload
// (there are no uploads), so a generous 1 MiB ceiling means an oversized body — a
// cheap DoS vector — is read only up to the limit and then fails, instead of being
// buffered unbounded by ParseForm, without ever constraining legitimate use.
const maxRequestBody = 1 << 20 // 1 MiB
// maxBackupUpload is the ceiling for restore uploads (a full encrypted dump);
// the 1 MiB cap applies to every other route.
//
// Sizing note — an uploaded restore costs roughly FOUR times the file, all of it
// in RAM on this deployment, so this number is not free:
//  1. multipart spills the part past its in-memory threshold to /tmp, which is a
//     tmpfs — i.e. RAM charged to the container;
//  2. backupUpload io.ReadAll's it into one contiguous []byte;
//  3. DecryptWith returns the plaintext archive as a second []byte — AES-GCM
//     authenticates the whole ciphertext before releasing any plaintext, so this
//     copy cannot be streamed away without changing the stored format to a
//     framed/chunked one;
//  4. the restore then writes that plaintext back to a temp file, again on tmpfs.
//
// The old 512 MiB ceiling therefore implied a ~2 GiB peak on a container with no
// memory limit at all. 128 MiB keeps the peak around 550 MiB, matching the bounds
// docker-compose.yml now sets (/tmp size-capped, container memory limited), and
// still leaves orders of magnitude over a real dump of this single-tenant
// database including its bytea receipt photos. It is not a hard ceiling on what
// can be restored either way: `treckrr restore <file>` reads from BACKUP_DIR and
// never passes through this HTTP path, but defaults to the same archive cap.
// Larger offline restores require BACKUP_CLI_MAX_BYTES and provisioned memory.
//
// The 128 MiB are the ceiling, not a constant: backup.OnlineBudget lowers the
// allowance in a container started with a smaller memory limit (same 1:6 ratio
// as 128 MiB : 768 MiB), so an upload the container cannot hold is refused
// up front instead of OOM-killing the app.
func maxBackupUpload() int64 { return backup.OnlineBudget() }

// limitBody wraps the request body in an http.MaxBytesReader so a client cannot
// stream an unbounded payload into ParseForm (and onward into bcrypt, decoding,
// etc.). It sits outermost — ahead of csrf, which reads the form — so the ceiling
// is in force for all body parsing. limitBody writes no status of its own: reading
// past the limit yields a *http.MaxBytesError that downstream parsing surfaces as a
// 4xx (e.g. handleLogin turns the ParseForm error into 400). Passing the raw
// ResponseWriter lets MaxBytesReader mark the request too large so the server
// closes the connection rather than reusing it.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.backup != nil && isMemoryBackupPath(r.URL.Path) {
			if u := s.currentUser(r); u == nil || !u.IsAdmin {
				http.Error(w, "Zugriff verweigert", http.StatusForbidden)
				return
			}
			ctx, release, err := s.backup.AcquireWork(r.Context())
			if err != nil {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "Eine Backup-Operation läuft bereits. Bitte später erneut versuchen.", http.StatusServiceUnavailable)
				return
			}
			defer release()
			r = r.WithContext(ctx)
			defer func() {
				if r.MultipartForm != nil {
					_ = r.MultipartForm.RemoveAll()
				}
			}()
		}
		if r.Body != nil && r.Body != http.NoBody {
			limit := int64(maxRequestBody)
			// Restore uploads a full encrypted dump — exempt those exact routes.
			if isBackupUploadPath(r.URL.Path) {
				// The restore allowance (up to 128 MiB) is for authenticated admins only. Resolve the
				// session here — outermost, before the large body is read and before
				// CSRF's FormValue would parse it — and reject anyone else, so an
				// unauthenticated client can't drive a memory-exhaustion parse (T-02).
				if u := s.currentUser(r); u == nil || !u.IsAdmin {
					http.Error(w, "Zugriff verweigert", http.StatusForbidden)
					return
				}
				limit = maxBackupUpload()
			} else if isPhotoUploadPath(r.URL.Path) {
				// A phone photo exceeds 1 MiB; allow more, but only for an
				// authenticated user (no pre-auth large-body parse).
				if u := s.currentUser(r); u == nil {
					http.Error(w, "Zugriff verweigert", http.StatusForbidden)
					return
				}
				limit = maxPhotoUpload
			} else if isImportUploadPath(r.URL.Path) {
				// The import previews take the statement or CSV itself (up to
				// maxImportPayloadLen); same authenticated-only rule as photos.
				if u := s.currentUser(r); u == nil {
					http.Error(w, "Zugriff verweigert", http.StatusForbidden)
					return
				}
				limit = maxImportUpload
				// A body that announces its oversize is answered as what it is.
				// Read past the cap, it would surface in csrf's FormValue as an
				// empty token — a 403 "CSRF-Token ungültig" that sends the
				// operator hunting for a session problem instead of a big file.
				if r.ContentLength > limit {
					http.Error(w, "Die Datei ist zu groß — höchstens 4 MB sind möglich. Bitte den Export auf einen kürzeren Zeitraum beschränken.", http.StatusRequestEntityTooLarge)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// isBackupUploadPath reports the two routes that accept a full encrypted dump and
// therefore get the large body allowance (guarded by an admin check in limitBody).
func isBackupUploadPath(p string) bool {
	return p == "/admin/backup/restore" || p == "/admin/backup/validate"
}

// maxImportUpload is the body allowance of the two import previews: the file
// itself (maxImportPayloadLen, which the handlers enforce on the content) plus
// headroom for the multipart framing and the few other form fields. The commits
// that follow send only a token, so they stay under maxRequestBody.
const maxImportUpload = maxImportPayloadLen + 64<<10

// isImportUploadPath reports the routes that receive an import file: the
// bank-statement and booking-CSV previews (the latter also takes the corrected
// CSV text from its own editor).
func isImportUploadPath(p string) bool {
	return p == "/payments/import/preview" || p == "/entries/import/preview"
}

// isPhotoUploadPath reports the booking-photo upload route (POST
// /entries/{id}/photos), which gets the larger photo body allowance.
func isPhotoUploadPath(p string) bool {
	bookingPath := strings.HasPrefix(p, "/entries/") || strings.HasPrefix(p, "/ledger/")
	return bookingPath && strings.HasSuffix(p, "/photos")
}

// auth wraps a handler requiring an authenticated user. It also enforces the
// forced-password-change flow and read-only (viewer) restrictions.
func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Authenticated pages carry per-user data — keep them out of the browser
		// cache so they can't be recovered via the back button after logout.
		w.Header().Set("Cache-Control", "no-store")
		user := s.currentUser(r)
		if user == nil {
			s.expireStaleSession(w, r)
			// An offline replay is a background fetch, not a navigation: answer 401
			// so the client keeps the booking queued and retries after the next
			// login, instead of following a redirect it would read as success.
			if isOfflineReplay(r) {
				http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		s.refreshSessionCookie(w, r)
		// Force a password change before anything else (except the change page).
		if user.MustChangePassword && r.URL.Path != "/account/password" {
			if isOfflineReplay(r) {
				http.Error(w, mustChangePasswordReplayMsg, http.StatusConflict)
				return
			}
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		// Viewers may not mutate data, except managing their own account.
		if r.Method == http.MethodPost && !user.CanWrite() && !isSelfServicePath(r.URL.Path) {
			// A replay would read the redirect as neither success nor rejection and
			// re-send the item on every page load; a 403 lets it surface instead.
			if isOfflineReplay(r) {
				http.Error(w, readOnlyReplayMsg, http.StatusForbidden)
				return
			}
			s.setFlash(w, r, "error", "Nur-Lese-Konto: Änderungen sind nicht möglich.")
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		ctx = store.WithAuditActor(ctx, store.AuditActor{UserID: &user.ID, Username: user.Username, IP: s.clientIP(r)})
		h(w, r.WithContext(ctx))
	})
}

// Offline-replay answers for the auth branches that would otherwise redirect.
// The client shows them to the user, so they are phrased for the user.
const (
	readOnlyReplayMsg           = "Nur-Lese-Konto: Änderungen sind nicht möglich. Die gespeicherte Buchung wurde nicht übernommen."
	mustChangePasswordReplayMsg = "Passwortänderung erforderlich: Bitte zuerst das Passwort ändern, danach werden gespeicherte Buchungen erneut gesendet." // #nosec G101 -- user-facing message, not a credential
)

// isOfflineReplay reports whether the request is a background replay of a
// queued offline submission (offline.js), which must get a status code it can
// act on rather than a redirect.
func isOfflineReplay(r *http.Request) bool {
	return r.Header.Get("X-Offline-Replay") == "1"
}

// isSelfServicePath allows viewers to POST to their own account management.
func isSelfServicePath(p string) bool {
	return strings.HasPrefix(p, "/account") || strings.HasPrefix(p, "/profile")
}

// admin wraps a handler requiring an authenticated admin user.
func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user := s.currentUser(r)
		if user == nil {
			s.expireStaleSession(w, r)
			if isOfflineReplay(r) {
				http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !user.IsAdmin {
			http.Error(w, "Zugriff verweigert", http.StatusForbidden)
			return
		}
		// Force a pending password change before any admin action (mirrors auth()).
		// No /admin route is the change-password page, so redirect unconditionally.
		if user.MustChangePassword {
			if isOfflineReplay(r) {
				http.Error(w, mustChangePasswordReplayMsg, http.StatusConflict)
				return
			}
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		s.refreshSessionCookie(w, r)
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		ctx = store.WithAuditActor(ctx, store.AuditActor{UserID: &user.ID, Username: user.Username, IP: s.clientIP(r)})
		h(w, r.WithContext(ctx))
	})
}

// hostCookiePrefix binds a cookie to Secure + Path=/ + no Domain, enforced by the
// browser. Its point here is that a sibling host under the same registrable domain
// (e.g. another app on *.example.org) cannot overwrite the cookie — "cookie
// tossing". Every Treckrr cookie already satisfies the prefix's requirements, so it
// is applied to all of them, not just the session.
const hostCookiePrefix = "__Host-"

// cookieName returns a cookie's wire name: the base name prefixed with __Host-
// when the cookie will be Secure (HTTPS). The prefix is omitted over plain HTTP
// (local dev), where browsers reject __Host- cookies outright. Idempotent, so a
// name that already carries the prefix is returned unchanged.
func (s *Server) cookieName(r *http.Request, base string) string {
	if s.cookieSecure(r) && !strings.HasPrefix(base, hostCookiePrefix) {
		return hostCookiePrefix + base
	}
	return base
}

// cookie reads a request cookie by its BASE name, resolving the __Host- prefix the
// same way setCookie applies it. Read cookies through this, never r.Cookie, so a
// read can't miss a prefixed cookie (or vice versa).
func (s *Server) cookie(r *http.Request, base string) (*http.Cookie, error) {
	return r.Cookie(s.cookieName(r, base))
}

// currentUser resolves the session cookie to a user, or nil. The result is
// memoized on the request context: auth/admin resolves the session, and accessLog
// resolves it again after the handler — without memoization every authenticated
// request would run the session SELECT *and* its sliding-expiry UPDATE twice.
func (s *Server) currentUser(r *http.Request) *models.User {
	if cached, ok := r.Context().Value(userCacheKey).(*userCache); ok {
		if !cached.done {
			cached.user, cached.stale = s.resolveUser(r)
			cached.done = true
		}
		return cached.user
	}
	user, _ := s.resolveUser(r)
	return user
}

// userCache memoizes one session resolution for the lifetime of a request. A nil
// user is cached too (done), so an anonymous request doesn't re-query either.
// stale records that the cookie was present but positively unknown to the store.
type userCache struct {
	user  *models.User
	stale bool
	done  bool
}

const userCacheKey ctxKey = "usercache"

// withUserCache installs the per-request memo. It sits outermost so every later
// currentUser call — limitBody, auth/admin, the handler, accessLog — shares one
// resolution.
func withUserCache(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userCacheKey, &userCache{}))
}

// resolveUser resolves the session cookie to its user. stale reports a cookie
// the store positively no longer honors (revoked, expired, user disabled) — as
// opposed to a lookup that failed transiently, which must not cost the user a
// still-valid cookie.
func (s *Server) resolveUser(r *http.Request) (user *models.User, stale bool) {
	c, err := s.cookie(r, sessionCookie)
	if err != nil || c.Value == "" {
		return nil, false
	}
	user, err = s.store.UserFromSession(r.Context(), c.Value, sessionTTL, sessionAbsoluteTTL)
	if err != nil {
		return nil, sessionGone(err)
	}
	return user, false
}

// expireStaleSession deletes a session cookie whose server-side session is
// gone. Nothing else ever cleared it: after a password change elsewhere, a
// revocation, an admin reset or the absolute TTL, the browser kept sending a
// dead cookie for up to 30 days, and every login page rendered for it carried
// a session-derived CSRF token that no longer matched anything.
func (s *Server) expireStaleSession(w http.ResponseWriter, r *http.Request) {
	stale := false
	if cached, ok := r.Context().Value(userCacheKey).(*userCache); ok && cached.done {
		stale = cached.stale
	} else {
		_, stale = s.resolveUser(r)
	}
	if stale {
		s.setCookie(w, r, &http.Cookie{Name: sessionCookie, Value: "", MaxAge: -1})
	}
}

// refreshSessionCookie re-issues the session cookie with a fresh MaxAge so an
// actively-used session keeps a live browser cookie in step with the rolling
// server-side expiry (slid in UserFromSession).
func (s *Server) refreshSessionCookie(w http.ResponseWriter, r *http.Request) {
	if c, err := s.cookie(r, sessionCookie); err == nil && c.Value != "" {
		s.setCookie(w, r, &http.Cookie{
			Name:   sessionCookie,
			Value:  c.Value,
			MaxAge: int(sessionTTL.Seconds()),
		})
	}
}

// userFromCtx returns the authenticated user placed by the auth middleware.
func userFromCtx(r *http.Request) *models.User {
	u, _ := r.Context().Value(userCtxKey).(*models.User)
	return u
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Cheap liveness + DB-reachability probe, kept side-effect free. Maintenance
	// purges run on a timer in the main run loop, so a flood of /healthz can no
	// longer saturate the connection pool with DELETEs.
	if err := s.store.Ping(r.Context()); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// handleLive is a pure liveness probe: it answers 200 as long as the process can
// serve, with NO database call — so a transient DB outage doesn't make an
// orchestrator kill and restart a healthy container. Readiness (/readyz, /healthz)
// stays DB-checking.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// handleCSPReport records Content-Security-Policy violation reports the browser
// posts to report-uri. Never trusted for anything but a log line — the strict CSP
// has no unsafe-inline, so a report usually means an accidental inline
// handler/style regressed.
//
// report-uri is same-origin here, so the browser DOES attach the session cookie;
// what it cannot attach is a CSRF token. The csrf middleware therefore exempts
// this path explicitly (see csrf.go) — without that every report was answered
// with 403 and the channel this policy advertises never worked.
func (s *Server) handleCSPReport(w http.ResponseWriter, r *http.Request) {
	// Reports can contain bearer URLs, referrers and script samples. Decode only
	// diagnostic fields and log a fixed directive allowlist, never the raw body.
	var report struct {
		CSP struct {
			Directive string `json:"effective-directive"`
			Line      int    `json:"line-number"`
			Column    int    `json:"column-number"`
		} `json:"csp-report"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&report); err == nil {
		directive := "unknown"
		switch report.CSP.Directive {
		case "default-src", "script-src", "script-src-elem", "script-src-attr", "style-src", "style-src-elem", "style-src-attr", "img-src", "font-src", "connect-src", "frame-src", "frame-ancestors", "object-src", "base-uri", "form-action", "worker-src", "manifest-src", "media-src":
			directive = report.CSP.Directive
		}
		slog.Warn("csp violation", "directive", directive, "line", report.CSP.Line, "column", report.CSP.Column)
	}
	w.WriteHeader(http.StatusNoContent)
}

// The two possible CSP values, fixed at compile time (all assets are served
// locally, so a strict policy is possible). The secure variant additionally
// upgrades plain-HTTP subresource requests — advertised alongside HSTS only.
const (
	// img-src keeps data: for the chevron/favicon/beleg-PNG SVG-as-image; the
	// beleg export fetches its woff2 fonts same-origin (connect-src 'self') and
	// embeds them as data: inside that SVG image, so font-src stays strict. The
	// connect/manifest/worker/frame-src directives make the same-origin-only
	// posture explicit rather than relying on the default-src fallback.
	cspBase   = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; frame-src 'none'; base-uri 'self'; form-action 'self'; object-src 'none'; frame-ancestors 'none'; report-uri /csp-report"
	cspSecure = cspBase + "; upgrade-insecure-requests"
)

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// Explicitly disable the legacy XSS auditor (buggy in old browsers); the
		// strict CSP is the real XSS defense. "0" is the OWASP-recommended value.
		h.Set("X-XSS-Protection", "0")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		// Disable browser features the app never uses. WebAuthn is unaffected:
		// publickey-credentials-* are not listed, and usb=() controls WebUSB, not
		// the FIDO USB transport.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")

		// Only advertise HSTS and upgrade requests over an effective HTTPS
		// connection: over plain HTTP HSTS is ignored, and pinning it there risks
		// locking out local non-TLS deployments.
		if r.TLS != nil || s.cookieSecure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			h.Set("Content-Security-Policy", cspSecure)
		} else {
			h.Set("Content-Security-Policy", cspBase)
		}
		next.ServeHTTP(w, r)
	})
}

func staticServer() http.Handler {
	fs := http.FileServer(http.FS(web.StaticFS()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Templates append the content-hash as ?v=<hash>, so a versioned URL changes
		// whenever the asset changes — it can be cached hard (immutable, 1 year). An
		// unversioned direct hit (e.g. a bookmark) keeps the short, revalidating
		// cache so it can't get stuck on a stale build.
		//
		// Immutable only for THIS build's hash. During a rolling deploy a page from
		// the new instance can send ?v=<new> to an old instance; answering with the
		// old bytes under a year-long immutable header would pin them in every cache
		// on the way. Any other version is served but must be revalidated, and the
		// served version is named so the service worker never stores a mismatch.
		version := web.AssetVersion()
		w.Header().Set("X-Treckrr-Asset-Version", version)
		switch v := r.URL.Query().Get("v"); {
		case v == version:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case v != "":
			w.Header().Set("Cache-Control", "no-cache")
		default:
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		gzipStatic(w, r, fs)
	})
}

// gzipStatic serves next with gzip when the client accepts it and the asset is a
// compressible text type. The embedded CSS/JS ship uncompressed otherwise; gzip
// cuts them ~70-80%. Binary assets (woff2, png, svg-as-image) are already
// compressed or tiny, so they're passed through untouched.
func gzipStatic(w http.ResponseWriter, r *http.Request, next http.Handler) {
	// Vary before representation selection, so a cache keys correctly whether or not
	// we end up compressing this particular request.
	w.Header().Add("Vary", "Accept-Encoding")
	// Bypass gzip when the client didn't (properly) ask for it, the asset isn't a
	// compressible text type, or the request is a Range request — for a Range,
	// http.FileServer answers 206 with a Content-Range measured on the UNCOMPRESSED
	// body, so gzipping it would hand back bytes that don't match the range headers.
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) || !gzippableAsset(r.URL.Path) || r.Header.Get("Range") != "" {
		next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	// Content-Length would describe the uncompressed size; drop it so it isn't wrong.
	w.Header().Del("Content-Length")
	gz := gzip.NewWriter(w)
	defer gz.Close()
	next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
}

// acceptsGzip parses an Accept-Encoding header and reports whether gzip is
// acceptable: a "gzip" (or "*") token that is not disabled with q=0. Matching is
// case-insensitive and ignores unrelated tokens like "x-gzip". Per RFC 7231 an
// absent q-value defaults to 1 (acceptable).
func acceptsGzip(header string) bool {
	if header == "" {
		return false
	}
	star := false
	for _, part := range strings.Split(header, ",") {
		tok := strings.TrimSpace(part)
		name := tok
		q := 1.0
		if i := strings.IndexByte(tok, ';'); i >= 0 {
			name = strings.TrimSpace(tok[:i])
			// Look for a q= parameter; anything unparseable leaves q at its default.
			for _, p := range strings.Split(tok[i+1:], ";") {
				p = strings.TrimSpace(p)
				if v, ok := strings.CutPrefix(p, "q="); ok {
					if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
						q = f
					}
				}
			}
		}
		if strings.EqualFold(name, "gzip") {
			return q > 0
		}
		if name == "*" {
			star = q > 0 // remember, but an explicit gzip token later still wins
		}
	}
	return star
}

// gzippableAsset reports whether a static path is a text type worth compressing.
func gzippableAsset(p string) bool {
	switch {
	case strings.HasSuffix(p, ".css"), strings.HasSuffix(p, ".js"),
		strings.HasSuffix(p, ".svg"), strings.HasSuffix(p, ".json"),
		strings.HasSuffix(p, ".webmanifest"), strings.HasSuffix(p, ".map"):
		return true
	}
	return false
}

// gzipResponseWriter routes the body through gzip while leaving header writes on
// the underlying ResponseWriter.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) { return w.gz.Write(b) }

// Unwrap keeps the http.ResponseController chain intact through the gzip wrapper
// (see statusRecorder.Unwrap).
func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// setCookie wraps http.SetCookie to apply consistent security defaults (Secure,
// HttpOnly, SameSite).
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) { //nosec G124
	if c.Path == "" {
		c.Path = "/"
	}
	// Apply the __Host- prefix centrally (callers pass the base name), so a cookie
	// cannot be added later that forgets it. Path=/ above and the absent Domain
	// below satisfy the prefix's other two requirements.
	c.Name = s.cookieName(r, c.Name)
	// Default every cookie to HttpOnly; no client-side script reads a cookie
	// (theme uses localStorage, CSRF a meta tag, the rest are server-side), so
	// enforcing it centrally means a future cookie cannot accidentally omit it.
	c.HttpOnly = true
	// A cookie literal that omits the SameSite field carries the zero value (0),
	// NOT http.SameSiteDefaultMode (1). Default the unset case to Lax so the
	// attribute is actually written to the Set-Cookie header.
	if c.SameSite == http.SameSiteDefaultMode || c.SameSite == 0 {
		c.SameSite = http.SameSiteLaxMode
	}
	c.Secure = s.cookieSecure(r)
	http.SetCookie(w, c) //nosec G124 -- attributes are set dynamically or by caller
}

// extendWriteDeadline pushes this response's write deadline out so a long
// download, dump or restore is not cut off by the server's global WriteTimeout
// (30s). Call it BEFORE starting the slow work, not just before writing the body —
// the deadline is absolute and the timeout clock is already running.
//
// It depends on every ResponseWriter wrapper in the chain implementing Unwrap();
// a failure means one lost the chain, which would silently reinstate the global
// timeout, so it is logged rather than discarded.
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
		slog.Warn("could not extend the write deadline; the global WriteTimeout applies",
			"err", sanitizeLog(err.Error()))
	}
}

// sanitizeLog replaces control characters in request-derived values with a
// space so they cannot forge additional log lines (CR/LF injection) or emit ANSI
// escape sequences that spoof the display of an operator tailing the logs in a
// terminal. Printable runes (incl. umlauts and other non-ASCII text) pass through.
func sanitizeLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
