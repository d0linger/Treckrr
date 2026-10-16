package server

import (
	"context"
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

const mfaRequiredReplayMsg = "Zwei-Faktor-Anmeldung erforderlich: Bitte zuerst einen Passkey oder die Authenticator-App einrichten, danach werden gespeicherte Buchungen erneut gesendet."

// securityPolicy returns the current policy, read at most every policyCacheTTL.
// A read error keeps the last known policy, or enforces nothing on a cold
// cache: the request then fails on its own database access anyway.
func (s *Server) securityPolicy(ctx context.Context) store.SecurityPolicy {
	s.policy.mu.Lock()
	defer s.policy.mu.Unlock()
	if s.policy.ok && time.Since(s.policy.at) < policyCacheTTL {
		return s.policy.policy
	}
	if s.store == nil {
		return store.SecurityPolicy{}
	}
	p, err := s.store.GetSecurityPolicy(ctx)
	if err != nil {
		slog.Warn("security policy read failed", "err", sanitizeLog(err.Error()))
		return s.policy.policy
	}
	s.policy.policy, s.policy.at, s.policy.ok = p, time.Now(), true
	return p
}

// invalidatePolicy drops the cached policy after an update.
func (s *Server) invalidatePolicy() {
	s.policy.mu.Lock()
	s.policy.ok = false
	s.policy.mu.Unlock()
}

// mfaBlocked reports whether the user's grace period without a second factor
// is over, so only account self-service stays reachable.
func (s *Server) mfaBlocked(r *http.Request, user *models.User) bool {
	deadline, applies := s.securityPolicy(r.Context()).Deadline(*user)
	return applies && !time.Now().Before(deadline)
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

// keepsSecondFactor refuses removing an account's last factor while the policy
// requires one; it sets the explanation and returns false in that case.
func (s *Server) keepsSecondFactor(w http.ResponseWriter, r *http.Request, user *models.User, removingTotp bool) bool {
	if !s.securityPolicy(r.Context()).Requires(*user) {
		return true
	}
	remaining := false
	if removingTotp {
		remaining = user.HasPasskey
	} else if user.TotpEnabled {
		remaining = true
	} else {
		creds, err := s.store.ListWebauthnCredentials(r.Context(), user.ID)
		remaining = err == nil && len(creds) > 1
	}
	if !remaining {
		s.setFlash(w, r, "error", "Die Anmelderichtlinie verlangt einen zweiten Faktor. Erst einen weiteren Passkey oder die Authenticator-App einrichten, dann kann dieser entfernt werden.")
	}
	return remaining
}

// handleSecurityPolicySave stores which roles need a second factor.
func (s *Server) handleSecurityPolicySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	grace := formInt(r, "grace_days")
	if grace < 0 || grace > 90 {
		s.setFlash(w, r, "error", "Die Übergangsfrist muss zwischen 0 und 90 Tagen liegen.")
		redirect(w, r, "/admin/users#anmelderichtlinie")
		return
	}
	before := s.securityPolicy(r.Context())
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
