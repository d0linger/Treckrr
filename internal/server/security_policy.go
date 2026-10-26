package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// The sign-in policy can require a second factor (TOTP or passkey) for
// administrators and, optionally, editors. Affected accounts without one see a
// banner during the grace period and are afterwards sent to the profile page,
// the only place they can still reach, until a factor exists.

// policyCacheTTL bounds how stale the per-request policy may be; saving the
// policy invalidates the cache immediately on this instance.
const policyCacheTTL = 30 * time.Second

// policyCache memoizes the single policy row across requests.
type policyCache struct {
	mu     sync.Mutex
	policy store.SecurityPolicy
	at     time.Time
	ok     bool
}

// mfaSetupURL is where a blocked account is sent to add a factor.
const mfaSetupURL = "/profile#zwei-faktor"

const secondFactorRequiredMsg = "Die Anmelderichtlinie verlangt einen zweiten Faktor. Erst einen weiteren Passkey oder die Authenticator-App einrichten, dann kann dieser entfernt werden."

const mfaRequiredReplayMsg = "Zwei-Faktor-Anmeldung erforderlich: Bitte zuerst einen Passkey oder die Authenticator-App einrichten, danach werden gespeicherte Buchungen erneut gesendet."

// securityPolicy returns the current policy, read at most every policyCacheTTL.
// A failed refresh must not turn a required factor into an optional one.
func (s *Server) securityPolicy(ctx context.Context) (store.SecurityPolicy, error) {
	s.policy.mu.Lock()
	defer s.policy.mu.Unlock()
	if s.policy.ok && time.Since(s.policy.at) < policyCacheTTL {
		return s.policy.policy, nil
	}
	if s.store == nil {
		return store.SecurityPolicy{}, errors.New("security policy store unavailable")
	}
	p, err := s.store.GetSecurityPolicy(ctx)
	if err != nil {
		slog.Warn("security policy read failed", "err", sanitizeLog(err.Error()))
		return store.SecurityPolicy{}, err
	}
	s.policy.policy, s.policy.at, s.policy.ok = p, time.Now(), true
	return p, nil
}

// invalidatePolicy drops the cached policy after an update.
func (s *Server) invalidatePolicy() {
	s.policy.mu.Lock()
	s.policy.ok = false
	s.policy.mu.Unlock()
}

// mfaBlocked reports whether the user's grace period without a second factor
// is over, so only account self-service stays reachable.
func (s *Server) mfaBlocked(r *http.Request, user *models.User) (bool, error) {
	if user.Role == models.RoleViewer {
		return false, nil // policy never requires a factor for viewers
	}
	p, err := s.securityPolicy(r.Context())
	if err != nil {
		return false, err
	}
	deadline, applies := p.Deadline(*user)
	return applies && !time.Now().Before(deadline), nil
}

// redirectToMFASetup answers a blocked request: a status for offline replays,
// otherwise the profile page with an explanation.
func (s *Server) redirectToMFASetup(w http.ResponseWriter, r *http.Request) {
	if isOfflineReplay(r) {
		http.Error(w, mfaRequiredReplayMsg, http.StatusConflict)
		return
	}
	s.setFlash(w, r, "error", "Für dein Konto ist eine Zwei-Faktor-Anmeldung vorgeschrieben. Bitte zuerst einen Passkey oder die Authenticator-App einrichten.")
	http.Redirect(w, r, mfaSetupURL, http.StatusSeeOther)
}

// keepsSecondFactor gives an early explanation before requesting step-up
// credentials. The store repeats this check under the user lock at removal.
func (s *Server) keepsSecondFactor(w http.ResponseWriter, r *http.Request, user *models.User, removingTOTP bool) bool {
	p, err := s.securityPolicy(r.Context())
	if err != nil {
		s.serverError(w, "security policy", err)
		return false
	}
	if !p.Requires(*user) {
		return true
	}
	remaining := user.TotpEnabled && !removingTOTP
	if !remaining {
		creds, err := s.store.ListWebauthnCredentials(r.Context(), user.ID)
		if err != nil {
			s.serverError(w, "remaining passkeys", err)
			return false
		}
		minimum := 1
		if !removingTOTP {
			minimum = 2
		}
		remaining = len(creds) >= minimum
	}
	if !remaining {
		s.setFlash(w, r, "error", secondFactorRequiredMsg)
		redirect(w, r, mfaSetupURL)
	}
	return remaining
}

// handleSecurityPolicySave stores which roles need a second factor.
func (s *Server) handleSecurityPolicySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if s.tooLong(w, r, "Übergangsfrist", r.FormValue("grace_days"), 50) {
		redirect(w, r, "/admin/users#anmelderichtlinie")
		return
	}
	grace, err := strconv.Atoi(r.FormValue("grace_days"))
	if err != nil || grace < 0 || grace > 90 {
		s.setFlash(w, r, "error", "Die Übergangsfrist muss zwischen 0 und 90 Tagen liegen.")
		redirect(w, r, "/admin/users#anmelderichtlinie")
		return
	}
	before, err := s.securityPolicy(r.Context())
	if err != nil {
		s.serverError(w, "security policy", err)
		return
	}
	p, err := s.store.UpdateSecurityPolicy(r.Context(), r.FormValue("require_admin") == "1", r.FormValue("require_editor") == "1", grace)
	if err != nil {
		s.serverError(w, "security policy save", err)
		return
	}
	s.invalidatePolicy()
	s.audit(r, "security_policy", "settings", 0, diffFields(
		fieldChange{"2FA für Administratoren", yesNo(before.RequireAdmin), yesNo(p.RequireAdmin)},
		fieldChange{"2FA für Bearbeiter", yesNo(before.RequireEditor), yesNo(p.RequireEditor)},
		fieldChange{"Übergangsfrist (Tage)", strconv.Itoa(before.GraceDays), strconv.Itoa(p.GraceDays)},
	))
	s.setFlash(w, r, "success", "Anmelderichtlinie gespeichert.")
	redirect(w, r, "/admin/users#anmelderichtlinie")
}

func yesNo(b bool) string {
	if b {
		return "ja"
	}
	return "nein"
}
