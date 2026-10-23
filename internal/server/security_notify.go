package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// Security events (sign-in from an unfamiliar browser, a changed password,
// added or removed factors, administrator resets) reach the affected account
// as a notification and, when mail is configured and the account has an
// address, as an outbox mail. Both are best effort on top of the audit log:
// a failure is logged and never blocks the action that caused it.

// securityEvent is one notice for one account.
type securityEvent struct {
	Key    string // stable event name, part of the dedupe key
	Title  string
	Detail string
	Mail   bool
}

// notifySecurity records the event for the target account.
func (s *Server) notifySecurity(r *http.Request, target *models.User, ev securityEvent) {
	if target == nil || target.ID == 0 {
		return
	}
	now := time.Now()
	key := fmt.Sprintf("security:%d:%s:%d", target.ID, ev.Key, now.UnixNano())
	detail := ev.Detail
	stamp := now.Format("02.01.2006 15:04")
	if detail == "" {
		detail = stamp
	} else {
		detail += " · " + stamp
	}
	if err := s.store.NotifyUser(r.Context(), target.ID, store.NotificationSource{
		DedupeKey: key, Kind: "security", Tone: "warn", Title: ev.Title, Detail: detail, Href: "/profile",
	}); err != nil {
		slog.Error("security notification failed", "event", ev.Key, "err", sanitizeLog(err.Error()))
	}
	if !ev.Mail || s.cfg == nil || !s.cfg.MailEnabled() || strings.TrimSpace(target.Email) == "" {
		return
	}
	body := "Hallo " + target.Username + ",\n\n" + ev.Title + " (" + detail + ").\n\n" +
		"Warst du das nicht, ändere sofort dein Passwort und prüfe unter Einstellungen deine Passkeys und aktiven Sitzungen, " +
		"oder bitte einen Administrator, den Zugang zurückzusetzen.\n\n" + s.absoluteURL(r, "/profile") + "\n"
	if _, _, err := s.store.CreateMailIntent(r.Context(), store.OutboxMail{
		Kind: "security", Recipient: target.Email, Subject: "Treckrr · Sicherheitshinweis: " + ev.Title,
		Body: body, DeliveryKey: key,
	}); err != nil {
		slog.Error("security mail failed", "event", ev.Key, "err", sanitizeLog(err.Error()))
	}
}

// notifySecurityByID resolves an account touched by an administrator.
func (s *Server) notifySecurityByID(r *http.Request, userID int64, ev securityEvent) {
	target, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		slog.Error("security notification: user lookup failed", "err", sanitizeLog(err.Error()))
		return
	}
	s.notifySecurity(r, target, ev)
}

// deviceLabel reduces a user agent to "browser · system", coarse enough that a
// browser update is not a new device and with nothing identifying stored.
func deviceLabel(ua string) string {
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	system := "unbekanntes System"
	switch {
	case strings.Contains(ua, "iPhone"):
		system = "iPhone"
	case strings.Contains(ua, "iPad"):
		system = "iPad"
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		system = "macOS"
	case strings.Contains(ua, "Linux"):
		system = "Linux"
	}
	return browser + " · " + system
}

// noteSignIn flags a sign-in from a browser/system the account never used.
func (s *Server) noteSignIn(r *http.Request, user *models.User) {
	device := deviceLabel(r.UserAgent())
	isNew, err := s.store.RememberDevice(r.Context(), user.ID, device)
	if err != nil {
		slog.Error("device memory failed", "err", sanitizeLog(err.Error()))
		return
	}
	if isNew {
		s.notifySecurity(r, user, securityEvent{Key: "new-device", Title: "Anmeldung von neuem Gerät", Detail: device, Mail: true})
	}
}
