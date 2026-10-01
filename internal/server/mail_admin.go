package server

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

const mailAdminListLimit = 100

// adminStatusView combines existing health signals for the compact strip shown
// on admin pages. It is read-only and does not run maintenance work.
type adminStatusView struct {
	Backup     backupHealthView
	Operations store.MailOperationsStatus
	MailTone   string
	MailLabel  string
	SeriesTone string
	Build      string
	LoadFailed bool
}

// adminStatus reads the same durable states used by the detailed admin pages.
func (s *Server) adminStatus(r *http.Request) adminStatusView {
	v := adminStatusView{Backup: s.backupHealth(), Build: buildLabel()}
	ops, err := s.store.OperationsStatus(r.Context())
	if err != nil {
		slog.Error("admin status query failed", "err", sanitizeLog(err.Error()))
		v.MailTone, v.MailLabel, v.SeriesTone, v.LoadFailed = "bad", "Status nicht lesbar", "bad", true
		return v
	}
	v.Operations = ops
	switch {
	case ops.Ambiguous+ops.Failed+ops.Held > 0:
		v.MailTone, v.MailLabel = "bad", "Entscheidung nötig"
	case ops.ActiveMail() > 0:
		v.MailTone, v.MailLabel = "warn", "Versand ausstehend"
	default:
		v.MailTone, v.MailLabel = "ok", "Ausgang unauffällig"
	}
	if ops.RecurringBlocked > 0 {
		v.SeriesTone = "bad"
	} else {
		v.SeriesTone = "ok"
	}
	return v
}

// buildLabel returns a short deployment identifier without exposing paths or
// full build metadata. Release version wins, otherwise the VCS revision does.
func buildLabel() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		revision, dirty := "", false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
		if len(revision) > 10 {
			revision = revision[:10]
		}
		if revision != "" {
			if dirty {
				revision += "*"
			}
			return revision
		}
	}
	return web.AssetVersion()
}

// handleMailOutbox renders the operational delivery queue with an exact status
// filter. The existing worker remains the only automatic delivery mechanism.
func (s *Server) handleMailOutbox(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if !isMailStatus(status) {
		s.badRequest(w, "Der gewählte E-Mail-Status ist ungültig.")
		return
	}
	items, err := s.store.ListMailOutbox(r.Context(), status, mailAdminListLimit)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	ops, err := s.store.OperationsStatus(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Mailausgang", "mail")
	data["Items"], data["Status"], data["Operations"] = items, status, ops
	data["MailEnabled"] = s.cfg.MailEnabled()
	s.render(w, r, "mail_outbox", data)
}

// handleMailOutboxDetail shows metadata and attempt history without loading the
// retained message body or attachment bytes into the page.
func (s *Server) handleMailOutboxDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	detail, err := s.store.GetMailOutboxDetail(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "E-Mail-Details", "mail")
	data["Mail"], data["MailEnabled"] = detail, s.cfg.MailEnabled()
	s.render(w, r, "mail_outbox_detail", data)
}

// handleMailRetry requeues a definitively failed intent. Ambiguous outcomes use
// the separate force-resend endpoint and explicit duplicate warning.
func (s *Server) handleMailRetry(w http.ResponseWriter, r *http.Request) {
	s.requeueMail(w, r, false)
}

// handleMailForceResend requeues an ambiguous intent only after the dedicated
// confirmation form was submitted.
func (s *Server) handleMailForceResend(w http.ResponseWriter, r *http.Request) {
	s.requeueMail(w, r, true)
}

// requeueMail applies one explicit retry decision and maps store conflicts to
// operator-facing messages without exposing transport details.
func (s *Server) requeueMail(w http.ResponseWriter, r *http.Request, forceAmbiguous bool) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	item, err := s.store.RequeueMail(r.Context(), id, forceAmbiguous)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Diese E-Mail existiert nicht mehr.")
	case errors.Is(err, store.ErrMailPayloadUnavailable):
		s.setFlash(w, r, "error", "Der Nachrichteninhalt wurde gemäß Aufbewahrung bereits entfernt und kann nicht erneut gesendet werden.")
	case errors.Is(err, store.ErrMailStateConflict):
		s.setFlash(w, r, "error", "Der Zustellstatus hat sich bereits geändert. Bitte Details neu laden.")
	case err != nil:
		slog.Error("manual mail retry failed", "id", id, "err", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "E-Mail konnte nicht erneut eingeplant werden.")
	default:
		msg := "„" + item.Subject + "“ wurde erneut eingeplant."
		if !s.cfg.MailEnabled() {
			msg += " SMTP ist derzeit nicht konfiguriert; die E-Mail bleibt wartend."
		}
		s.setFlash(w, r, "success", msg)
	}
	redirect(w, r, "/admin/mail/"+strconv.FormatInt(id, 10))
}

// isMailStatus validates the exact filter vocabulary accepted by the outbox.
func isMailStatus(status string) bool {
	switch status {
	case "", store.MailStatusPending, store.MailStatusSending, store.MailStatusSent,
		store.MailStatusFailed, store.MailStatusAmbiguous, store.MailStatusHeld:
		return true
	default:
		return false
	}
}
