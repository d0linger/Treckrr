package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/d0linger/treckrr/internal/store"
)

// RefreshNotifications snapshots actionable status sources for the in-app
// center. It is called by the bounded maintenance loop, never by a page GET.
func (s *Server) RefreshNotifications(ctx context.Context) error {
	var backupNotice *store.NotificationSource
	health := s.backupHealth()
	if health.Tone != "ok" {
		status := readBackupStatus(s.cfg.BackupStatusFile)
		identity := status.State + ":" + strconv.FormatInt(status.LastBackup.Unix(), 10)
		if s.backup == nil || !s.backup.Enabled() {
			identity = "disabled"
		}
		backupNotice = &store.NotificationSource{
			DedupeKey: "backup:" + identity, Kind: "backup", Tone: health.Tone,
			Title: health.Title, Detail: health.AgeLabel, Href: "/admin/backup", AdminOnly: true,
		}
	}
	return s.store.RefreshNotifications(ctx, backupNotice)
}

// handleNotifications renders the signed-in user's notification stream and
// weekly digest preference.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	items, err := s.store.ListNotifications(r.Context(), user.ID, 100)
	if err != nil {
		s.serverError(w, "notifications: list", err)
		return
	}
	prefs, err := s.store.NotificationPreferences(r.Context(), user.ID)
	if err != nil {
		s.serverError(w, "notifications: preferences", err)
		return
	}
	data := s.newPage(w, r, "Hinweise", "notifications")
	data["Notifications"] = items
	data["NotificationPreferences"] = prefs
	data["MailEnabled"] = s.cfg.MailEnabled()
	s.render(w, r, "notifications", data)
}

// handleNotificationOpen marks an owned item read before redirecting to its
// prevalidated local task target.
func (s *Server) handleNotificationOpen(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	href, err := s.store.OpenNotification(r.Context(), user.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "notifications: open", err)
		return
	}
	if len(href) == 0 || href[0] != '/' ||
		(len(href) > 1 && (href[1] == '/' || href[1] == '\\')) ||
		strings.Contains(href[1:], `\`) || hasControlChar(href) {
		s.serverError(w, "notifications: invalid stored target", fmt.Errorf("notification %d has unsafe target", id))
		return
	}
	redirect(w, r, href)
}

// handleNotificationDismiss hides one item for its owning user.
func (s *Server) handleNotificationDismiss(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.DismissNotification(r.Context(), user.ID, id); errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, "notifications: dismiss", err)
		return
	}
	redirect(w, r, "/notifications")
}

// handleNotificationsReadAll marks the current visible stream read.
func (s *Server) handleNotificationsReadAll(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if err := s.store.ReadAllNotifications(r.Context(), user.ID); err != nil {
		s.serverError(w, "notifications: read all", err)
		return
	}
	redirect(w, r, "/notifications")
}

// handleNotificationPreferences updates the explicit weekly-mail opt-in.
func (s *Server) handleNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Einstellung konnte nicht verarbeitet werden.")
		return
	}
	if !s.cfg.MailEnabled() {
		s.setFlash(w, r, "error", "Der Wochenbericht ist ohne SMTP nicht verf\u00fcgbar. Die bisherige Einstellung bleibt unver\u00e4ndert.")
		redirect(w, r, "/notifications")
		return
	}
	weekly := r.FormValue("weekly_email") == "on"
	if weekly && strings.TrimSpace(user.Email) == "" {
		s.setFlash(w, r, "error", "Für den Wochenbericht muss dein Benutzerkonto eine E-Mail-Adresse haben.")
		redirect(w, r, "/notifications")
		return
	}
	if err := s.store.UpdateNotificationPreferences(r.Context(), user.ID, weekly); err != nil {
		s.serverError(w, "notifications: save preferences", err)
		return
	}
	s.setFlash(w, r, "success", "Benachrichtigungseinstellungen gespeichert.")
	redirect(w, r, "/notifications")
}
