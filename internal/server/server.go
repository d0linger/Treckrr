// Package server wires the HTTP routes, middleware and handlers together.
package server

import (
	"context"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	cfg       *config.Config
	store     *store.Store
	backup    *backup.Service
	templates map[string]*template.Template
	logins    *loginLimiter
	wa        *webauthn.WebAuthn
	started   time.Time
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
	return &Server{cfg: cfg, store: st, backup: bk, templates: tpl, logins: newLoginLimiter(st), wa: wa, started: time.Now()}, nil
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
	mux.HandleFunc("GET /healthz", s.handleHealth) // legacy alias (DB-checking) — kept
	mux.HandleFunc("GET /readyz", s.handleHealth)  // readiness: app + DB reachable
	mux.HandleFunc("GET /livez", s.handleLive)     // liveness: process only, no DB
	mux.HandleFunc("POST /csp-report", s.handleCSPReport)
	// Prometheus metrics — only registered when METRICS_TOKEN is set AND long enough
	// to resist brute force; the handler itself enforces the bearer token so an
	// unauthenticated scrape gets 401. A set-but-too-short token stays disabled with
	// a warning rather than exposing a guessable endpoint.
	if len(s.cfg.MetricsToken) >= metricsTokenMinLen {
		mux.HandleFunc("GET /metrics", s.handleMetrics)
	} else if s.cfg.MetricsToken != "" {
		slog.Warn("METRICS_TOKEN is too short; /metrics stays disabled", "min_len", metricsTokenMinLen)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticServer()))
	mux.HandleFunc("GET /theme", s.handleTheme)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /offline", s.handleOffline)

	// Auth (public).
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /login/2fa", s.handleLogin2FA)
	mux.HandleFunc("POST /login/passkey/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /login/passkey/finish", s.handlePasskeyLoginFinish)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// Authenticated area.
	mux.Handle("GET /{$}", s.auth(s.handleDashboard))
	mux.Handle("GET /stats", s.auth(s.handleStats))
	mux.Handle("GET /stats/all", s.auth(s.handleStatsAll))
	mux.Handle("GET /neighbors/{id}", s.auth(s.handleNeighborDetail))
	mux.Handle("GET /neighbors/{id}/overview", s.auth(s.handleNeighborOverview))
	mux.Handle("GET /neighbors/{id}/beleg", s.auth(s.handleNeighborBeleg))
	mux.Handle("GET /neighbors/{id}/invoice/confirm", s.auth(s.handleInvoiceConfirm))
	mux.Handle("POST /neighbors/{id}/invoice", s.auth(s.handleInvoiceIssue))
	mux.Handle("POST /neighbors/{id}/invoice/storno", s.auth(s.handleInvoiceStorno))
	mux.Handle("POST /neighbors/{id}/invoice/gutschrift", s.auth(s.handleInvoiceGutschrift))
	mux.Handle("GET /neighbors/{id}/invoice/epc-qr.png", s.auth(s.handleInvoiceEpcQR))
	mux.Handle("GET /neighbors", s.auth(s.handleNeighborsManage))
	mux.Handle("POST /neighbors/create", s.auth(s.handleNeighborManageCreate))
	mux.Handle("POST /neighbors/{id}/update", s.auth(s.handleNeighborUpdate))
	mux.Handle("POST /neighbors/{id}/archive", s.auth(s.handleNeighborArchive))
	mux.Handle("POST /neighbors/{id}/delete", s.auth(s.handleNeighborDelete))
	mux.Handle("POST /neighbors/{id}/anonymize", s.auth(s.handleNeighborAnonymize))
	mux.Handle("POST /years/add-neighbor", s.auth(s.handleYearAddNeighbor))
	mux.Handle("POST /years/remove-neighbor", s.auth(s.handleYearRemoveNeighbor))
	mux.Handle("POST /years/carry-over", s.auth(s.handleCarryOverNeighbors))
	mux.Handle("POST /years/mark-paid", s.auth(s.handleNeighborSettle))

	mux.Handle("POST /entries", s.auth(s.handleEntryCreate))
	mux.Handle("POST /entries/quick", s.auth(s.handleQuickEntries))
	mux.Handle("GET /entries/{id}/edit", s.auth(s.handleEntryEditForm))
	mux.Handle("GET /entries/{id}/copy", s.auth(s.handleEntryCopy))
	mux.Handle("POST /entries/{id}/photos", s.auth(s.handleEntryPhotoUpload))
	mux.Handle("GET /entries/{id}/photos/{pid}", s.auth(s.handleEntryPhotoServe))
	mux.Handle("POST /entries/{id}/photos/{pid}/delete", s.auth(s.handleEntryPhotoDelete))
	mux.Handle("POST /entries/{id}/update", s.auth(s.handleEntryUpdate))
	mux.Handle("POST /entries/{id}/void", s.auth(s.handleEntryVoid))
	mux.Handle("POST /entries/{id}/delete", s.auth(s.handleEntryDelete))
	mux.Handle("POST /neighbors/{id}/ledger", s.auth(s.handleLedgerAdd))
	mux.Handle("POST /neighbors/{id}/payments", s.auth(s.handlePaymentAdd))
	mux.Handle("POST /neighbors/{id}/carry-forward", s.auth(s.handleNeighborCarryForward))
	mux.Handle("POST /payments/{id}/delete", s.auth(s.handlePaymentDelete))
	mux.Handle("POST /payments/{id}/restore", s.auth(s.handlePaymentRestore))
	mux.Handle("GET /neighbors/{id}/recalc", s.auth(s.handleNeighborRecalcPreview))
	mux.Handle("POST /neighbors/{id}/recalc", s.auth(s.handleNeighborRecalcApply))
	mux.Handle("GET /years/{id}/recalc", s.auth(s.handleYearRecalcPreview))
	mux.Handle("POST /years/{id}/recalc", s.auth(s.handleYearRecalcApply))
	mux.Handle("GET /ledger/{id}/edit", s.auth(s.handleLedgerEditForm))
	mux.Handle("POST /ledger/{id}/update", s.auth(s.handleLedgerUpdate))
	mux.Handle("POST /ledger/{id}/void", s.auth(s.handleLedgerVoid))
	mux.Handle("POST /ledger/{id}/delete", s.auth(s.handleLedgerDelete))
	mux.Handle("GET /api/base/{id}/pricing", s.auth(s.handlePricingAPI))
	mux.Handle("GET /api/search", s.auth(s.handleSearchAPI))
	mux.Handle("GET /api/entries/precheck", s.auth(s.handleEntryPrecheck))

	mux.Handle("GET /prices", s.auth(s.handlePrices))
	mux.Handle("GET /prices/compare", s.auth(s.handlePriceCompare))
	mux.Handle("POST /prices/loadlevels", s.auth(s.handleLoadLevelSave))
	mux.Handle("POST /prices/loadlevels/{id}/delete", s.auth(s.handleLoadLevelDelete))
	mux.Handle("POST /prices/tractors", s.auth(s.handleTractorSave))
	mux.Handle("POST /prices/tractors/{id}/toggle", s.auth(s.handleTractorToggle))
	mux.Handle("POST /prices/tractors/{id}/delete", s.auth(s.handleTractorDelete))
	mux.Handle("POST /prices/machines", s.auth(s.handleMachineSave))
	mux.Handle("POST /prices/machines/{id}/toggle", s.auth(s.handleMachineToggle))
	mux.Handle("POST /prices/machines/{id}/delete", s.auth(s.handleMachineDelete))

	mux.Handle("GET /gespanne", s.auth(s.handleGespanne))
	mux.Handle("POST /gespanne", s.auth(s.handleGespannSave))
	mux.Handle("POST /gespanne/{id}/delete", s.auth(s.handleGespannDelete))

	mux.Handle("GET /years", s.auth(s.handleYears))
	mux.Handle("POST /years", s.auth(s.handleYearCreate))
	mux.Handle("POST /years/{id}/status", s.auth(s.handleYearStatus))
	mux.Handle("POST /years/{id}/update", s.auth(s.handleYearUpdate))
	mux.Handle("POST /years/{id}/delete", s.auth(s.handleYearDelete))

	mux.Handle("GET /bases", s.auth(s.handleBases))
	mux.Handle("POST /bases", s.auth(s.handleBaseCreate))
	mux.Handle("POST /bases/{id}/update", s.auth(s.handleBaseUpdate))
	mux.Handle("POST /bases/{id}/delete", s.auth(s.handleBaseDelete))
	mux.Handle("POST /bases/{id}/lock", s.auth(s.handleBaseLock))
	mux.Handle("POST /bases/{id}/unlock", s.auth(s.handleBaseUnlock))

	mux.Handle("GET /profile", s.auth(s.handleProfile))
	mux.Handle("GET /account/password", s.auth(s.handleAccountPasswordForm))
	mux.Handle("POST /account/password", s.auth(s.handleAccountPasswordSubmit))
	mux.Handle("GET /account/passkeys", s.auth(s.handlePasskeys))
	mux.Handle("POST /account/passkeys/register/begin", s.auth(s.handlePasskeyRegisterBegin))
	mux.Handle("POST /account/passkeys/register/finish", s.auth(s.handlePasskeyRegisterFinish))
	mux.Handle("POST /account/passkeys/{id}/delete", s.auth(s.handlePasskeyDelete))
	mux.Handle("GET /account/2fa", s.auth(s.handleTwoFactor))
	mux.Handle("GET /account/2fa/qr.png", s.auth(s.handleTwoFactorQR))
	mux.Handle("POST /account/2fa/confirm", s.auth(s.handleTwoFactorConfirm))
	mux.Handle("POST /account/2fa/recovery", s.auth(s.handleRecoveryRegenerate))
	mux.Handle("POST /account/2fa/disable", s.auth(s.handleTwoFactorDisable))
	mux.Handle("POST /account/sessions/revoke", s.auth(s.handleSessionRevoke))
	mux.Handle("POST /account/sessions/revoke-others", s.auth(s.handleSessionRevokeOthers))

	mux.Handle("GET /entries/import", s.auth(s.handleImportForm))
	mux.Handle("GET /entries/import/sample.csv", s.auth(s.handleImportSample))
	mux.Handle("POST /entries/import/preview", s.auth(s.handleImportPreview))
	mux.Handle("POST /entries/import", s.auth(s.handleImportCommit))
	mux.Handle("GET /export/year/{id}", s.auth(s.handleExportYear))
	mux.Handle("GET /export/neighbor/{id}", s.auth(s.handleExportNeighbor))
	mux.Handle("GET /neighbors/{id}/dsgvo-export.json", s.auth(s.handleNeighborDataExport))

	// Mahnwesen (dunning): overdue list + printable reminder + its EPC-QR.
	mux.Handle("GET /mahnwesen", s.auth(s.handleMahnwesen))
	mux.Handle("GET /neighbors/{id}/mahnung", s.auth(s.handleNeighborMahnung))
	mux.Handle("GET /neighbors/{id}/mahnung/epc-qr.png", s.auth(s.handleMahnungEpcQR))

	// Admin only.
	mux.Handle("GET /admin/audit", s.admin(s.handleAudit))
	mux.Handle("GET /admin/audit/export", s.admin(s.handleAuditExport))
	mux.Handle("GET /admin/backup", s.admin(s.handleBackupStatus))
	mux.Handle("POST /admin/backup/run", s.admin(s.handleBackupRun))
	mux.Handle("POST /admin/backup/run-scheduled", s.admin(s.handleBackupRunScheduled))
	mux.Handle("POST /admin/backup/settings", s.admin(s.handleBackupSettings))
	mux.Handle("GET /admin/backup/file/{name}", s.admin(s.handleBackupFile))
	mux.Handle("POST /admin/backup/validate", s.admin(s.handleBackupValidate))
	mux.Handle("POST /admin/backup/restore", s.admin(s.handleBackupRestore))
	mux.Handle("POST /admin/backup/s3/test", s.admin(s.handleBackupS3Test))
	mux.Handle("POST /admin/backup/s3/run", s.admin(s.handleBackupS3Run))
	mux.Handle("GET /admin/backup/s3/file/{name}", s.admin(s.handleBackupS3File))
	mux.Handle("GET /admin/company", s.admin(s.handleCompany))
	mux.Handle("POST /admin/company", s.admin(s.handleCompanySave))
	mux.Handle("GET /admin/users", s.admin(s.handleUsers))
	mux.Handle("POST /admin/users", s.admin(s.handleUserCreate))
	mux.Handle("POST /admin/users/{id}/password", s.admin(s.handleUserPassword))
	mux.Handle("POST /admin/users/{id}/update", s.admin(s.handleUserUpdate))
	mux.Handle("POST /admin/users/{id}/role", s.admin(s.handleUserRole))
	mux.Handle("POST /admin/users/{id}/reset-2fa", s.admin(s.handleUserResetTotp))
	mux.Handle("POST /admin/users/{id}/delete", s.admin(s.handleUserDelete))

	return s.limitBody(s.accessLog(s.securityHeaders(s.csrf(mux))))
}

// maxRequestBody caps how many bytes the server will read from a request body.
// Every endpoint takes only a small form post or a few-KB WebAuthn JSON payload
// (there are no uploads), so a generous 1 MiB ceiling means an oversized body — a
// cheap DoS vector — is read only up to the limit and then fails, instead of being
// buffered unbounded by ParseForm, without ever constraining legitimate use.
const maxRequestBody = 1 << 20 // 1 MiB
// maxBackupUpload is the ceiling for restore uploads (a full encrypted dump);
// the 1 MiB cap applies to every other route.
const maxBackupUpload = 512 << 20 // 512 MiB

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
		if r.Body != nil && r.Body != http.NoBody {
			limit := int64(maxRequestBody)
			// Restore uploads a full encrypted dump — exempt those exact routes.
			if isBackupUploadPath(r.URL.Path) {
				// The 512 MiB allowance is for authenticated admins only. Resolve the
				// session here — outermost, before the large body is read and before
				// CSRF's FormValue would parse it — and reject anyone else, so an
				// unauthenticated client can't drive a memory-exhaustion parse (T-02).
				if u := s.currentUser(r); u == nil || !u.IsAdmin {
					http.Error(w, "Zugriff verweigert", http.StatusForbidden)
					return
				}
				limit = maxBackupUpload
			} else if isPhotoUploadPath(r.URL.Path) {
				// A phone photo exceeds 1 MiB; allow more, but only for an
				// authenticated user (no pre-auth large-body parse).
				if u := s.currentUser(r); u == nil {
					http.Error(w, "Zugriff verweigert", http.StatusForbidden)
					return
				}
				limit = maxPhotoUpload
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

// isPhotoUploadPath reports the booking-photo upload route (POST
// /entries/{id}/photos), which gets the larger photo body allowance.
func isPhotoUploadPath(p string) bool {
	return strings.HasPrefix(p, "/entries/") && strings.HasSuffix(p, "/photos")
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
			// An offline replay is a background fetch, not a navigation: answer 401
			// so the client keeps the booking queued and retries after the next
			// login, instead of following a redirect it would read as success.
			if r.Header.Get("X-Offline-Replay") == "1" {
				http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		s.refreshSessionCookie(w, r)
		// Force a password change before anything else (except the change page).
		if user.MustChangePassword && r.URL.Path != "/account/password" {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		// Viewers may not mutate data, except managing their own account.
		if r.Method == http.MethodPost && !user.CanWrite() && !isSelfServicePath(r.URL.Path) {
			s.setFlash(w, r, "error", "Nur-Lese-Konto: Änderungen sind nicht möglich.")
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		h(w, r.WithContext(ctx))
	})
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
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		s.refreshSessionCookie(w, r)
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		h(w, r.WithContext(ctx))
	})
}

// currentUser resolves the session cookie to a user, or nil.
// sessionCookieName returns the session cookie name, prefixed with __Host- when
// the cookie is Secure (HTTPS). The prefix binds the cookie to Secure + Path=/ +
// no Domain (browser-enforced), hardening it against subdomain injection. It is
// omitted over plain HTTP (local dev), where browsers reject __Host- cookies.
func (s *Server) sessionCookieName(r *http.Request) string {
	if s.cookieSecure(r) {
		return "__Host-" + sessionCookie
	}
	return sessionCookie
}

func (s *Server) currentUser(r *http.Request) *models.User {
	c, err := r.Cookie(s.sessionCookieName(r))
	if err != nil || c.Value == "" {
		return nil
	}
	user, err := s.store.UserFromSession(r.Context(), c.Value, sessionTTL, sessionAbsoluteTTL)
	if err != nil {
		return nil
	}
	return user
}

// refreshSessionCookie re-issues the session cookie with a fresh MaxAge so an
// actively-used session keeps a live browser cookie in step with the rolling
// server-side expiry (slid in UserFromSession).
func (s *Server) refreshSessionCookie(w http.ResponseWriter, r *http.Request) {
	name := s.sessionCookieName(r)
	if c, err := r.Cookie(name); err == nil && c.Value != "" {
		s.setCookie(w, r, &http.Cookie{
			Name:   name,
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
// posts to report-uri. Public (browsers send it without credentials) and never
// trusted for anything but a log line — the strict CSP has no unsafe-inline, so a
// report usually means an accidental inline handler/style regressed.
func (s *Server) handleCSPReport(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10)) // reports are small
	slog.Warn("csp violation", "report", sanitizeLog(strings.TrimSpace(string(body))), "ua", sanitizeLog(r.UserAgent()))
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
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fs.ServeHTTP(w, r)
	})
}

// setCookie wraps http.SetCookie to apply consistent security defaults (Secure,
// HttpOnly, SameSite).
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) { //nosec G124
	if c.Path == "" {
		c.Path = "/"
	}
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
