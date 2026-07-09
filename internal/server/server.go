// Package server wires the HTTP routes, middleware and handlers together.
package server

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"treckrr/internal/config"
	"treckrr/internal/models"
	"treckrr/internal/store"
	"treckrr/internal/web"
)

const (
	sessionCookie = "treckrr_session"
	flashCookie   = "treckrr_flash"
	sessionTTL    = 30 * 24 * time.Hour
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	cfg       *config.Config
	store     *store.Store
	templates map[string]*template.Template
	logins    *loginLimiter
	wa        *webauthn.WebAuthn
}

// New constructs a Server and parses templates.
func New(cfg *config.Config, st *store.Store) (*Server, error) {
	tpl, err := web.Templates()
	if err != nil {
		return nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: "Treckrr",
		RPOrigins:     []string{cfg.RPOrigin},
	})
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: st, templates: tpl, logins: newLoginLimiter(st), wa: wa}, nil
}

type ctxKey string

const userCtxKey ctxKey = "user"

// Handler builds the top-level http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health & PWA plumbing (public).
	mux.HandleFunc("GET /healthz", s.handleHealth)
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
	mux.Handle("GET /neighbors", s.auth(s.handleNeighborsManage))
	mux.Handle("POST /neighbors/create", s.auth(s.handleNeighborManageCreate))
	mux.Handle("POST /neighbors/{id}/update", s.auth(s.handleNeighborUpdate))
	mux.Handle("POST /neighbors/{id}/archive", s.auth(s.handleNeighborArchive))
	mux.Handle("POST /neighbors/{id}/delete", s.auth(s.handleNeighborDelete))
	mux.Handle("POST /years/add-neighbor", s.auth(s.handleYearAddNeighbor))
	mux.Handle("POST /years/remove-neighbor", s.auth(s.handleYearRemoveNeighbor))
	mux.Handle("POST /years/carry-over", s.auth(s.handleCarryOverNeighbors))
	mux.Handle("POST /years/mark-paid", s.auth(s.handleNeighborPaid))

	mux.Handle("POST /entries", s.auth(s.handleEntryCreate))
	mux.Handle("POST /entries/quick", s.auth(s.handleQuickEntries))
	mux.Handle("GET /entries/{id}/edit", s.auth(s.handleEntryEditForm))
	mux.Handle("POST /entries/{id}/update", s.auth(s.handleEntryUpdate))
	mux.Handle("POST /entries/{id}/void", s.auth(s.handleEntryVoid))
	mux.Handle("POST /entries/{id}/delete", s.auth(s.handleEntryDelete))
	mux.Handle("POST /neighbors/{id}/ledger", s.auth(s.handleLedgerAdd))
	mux.Handle("GET /ledger/{id}/edit", s.auth(s.handleLedgerEditForm))
	mux.Handle("POST /ledger/{id}/update", s.auth(s.handleLedgerUpdate))
	mux.Handle("POST /ledger/{id}/void", s.auth(s.handleLedgerVoid))
	mux.Handle("POST /ledger/{id}/delete", s.auth(s.handleLedgerDelete))
	mux.Handle("GET /api/base/{id}/pricing", s.auth(s.handlePricingAPI))

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

	mux.Handle("GET /export/year/{id}", s.auth(s.handleExportYear))
	mux.Handle("GET /export/neighbor/{id}", s.auth(s.handleExportNeighbor))

	// Admin only.
	mux.Handle("GET /admin/audit", s.admin(s.handleAudit))
	mux.Handle("GET /admin/audit/export", s.admin(s.handleAuditExport))
	mux.Handle("GET /admin/users", s.admin(s.handleUsers))
	mux.Handle("POST /admin/users", s.admin(s.handleUserCreate))
	mux.Handle("POST /admin/users/{id}/password", s.admin(s.handleUserPassword))
	mux.Handle("POST /admin/users/{id}/role", s.admin(s.handleUserRole))
	mux.Handle("POST /admin/users/{id}/reset-2fa", s.admin(s.handleUserResetTotp))
	mux.Handle("POST /admin/users/{id}/delete", s.admin(s.handleUserDelete))

	return s.accessLog(s.securityHeaders(s.csrf(mux)))
}

// auth wraps a handler requiring an authenticated user. It also enforces the
// forced-password-change flow and read-only (viewer) restrictions.
func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r)
		if user == nil {
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
		user := s.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !user.IsAdmin {
			http.Error(w, "Zugriff verweigert", http.StatusForbidden)
			return
		}
		s.refreshSessionCookie(w, r)
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		h(w, r.WithContext(ctx))
	})
}

// currentUser resolves the session cookie to a user, or nil.
func (s *Server) currentUser(r *http.Request) *models.User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	user, err := s.store.UserFromSession(r.Context(), c.Value, sessionTTL)
	if err != nil {
		return nil
	}
	return user
}

// refreshSessionCookie re-issues the session cookie with a fresh MaxAge so an
// actively-used session keeps a live browser cookie in step with the rolling
// server-side expiry (slid in UserFromSession).
func (s *Server) refreshSessionCookie(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.setCookie(w, r, &http.Cookie{
			Name:     sessionCookie,
			Value:    c.Value,
			HttpOnly: true,
			MaxAge:   int(sessionTTL.Seconds()),
		})
	}
}

// userFromCtx returns the authenticated user placed by the auth middleware.
func userFromCtx(r *http.Request) *models.User {
	u, _ := r.Context().Value(userCtxKey).(*models.User)
	return u
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.PurgeExpiredSessions(r.Context()); err != nil {
		log.Printf("purge sessions: %v", err)
	}
	if err := s.store.PurgeStaleRateLimits(r.Context()); err != nil {
		log.Printf("purge rate limits: %v", err)
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// The two possible CSP values, fixed at compile time (all assets are served
// locally, so a strict policy is possible). The secure variant additionally
// upgrades plain-HTTP subresource requests — advertised alongside HSTS only.
const (
	cspBase   = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'self'; form-action 'self'; object-src 'none'; frame-ancestors 'none'"
	cspSecure = cspBase + "; upgrade-insecure-requests"
)

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
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
// SameSite).
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) { //nosec G124
	if c.Path == "" {
		c.Path = "/"
	}
	// A cookie literal that omits the SameSite field carries the zero value (0),
	// NOT http.SameSiteDefaultMode (1). Default the unset case to Lax so the
	// attribute is actually written to the Set-Cookie header.
	if c.SameSite == http.SameSiteDefaultMode || c.SameSite == 0 {
		c.SameSite = http.SameSiteLaxMode
	}
	c.Secure = s.cookieSecure(r)
	http.SetCookie(w, c) //nosec G124 -- attributes are set dynamically or by caller
}
