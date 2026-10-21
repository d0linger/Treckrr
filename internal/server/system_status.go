package server

import (
	"net/http"
	"runtime"
	"time"
)

// systemCheck is one line of the system page: a plain-language state with a
// tone (ok, warn, bad) and an optional link to where it can be fixed.
type systemCheck struct {
	Area, Label, Value, Tone, Href string
}

// handleSystemStatus gathers the scattered health signals (build, database,
// backups, mail, sign-in configuration, updates) on one read-only page.
func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	info, err := s.store.ReadSystemInfo(r.Context())
	if err != nil {
		s.serverError(w, "system status: database", err)
		return
	}
	ops, err := s.store.OperationsStatus(r.Context())
	if err != nil {
		s.serverError(w, "system status: operations", err)
		return
	}
	backupView := s.backupHealth()
	backupState := s.currentBackupStatus()
	policy := s.securityPolicy(r.Context())
	chainHeads, err := s.store.AuditChainHeads(r.Context())
	if err != nil {
		s.serverError(w, "system status: audit chain", err)
		return
	}
	updates := s.updateStatus()
	pool := s.store.DBStats()

	var checks []systemCheck
	add := func(area, label, value, tone, href string) {
		checks = append(checks, systemCheck{area, label, value, tone, href})
	}

	add("Betrieb", "Version", updates.Current, "ok", "")
	add("Betrieb", "Laufzeit", "seit "+s.started.Format("02.01.2006 15:04")+" · "+runtime.Version(), "ok", "")
	if s.cfg.DemoMode {
		add("Betrieb", "Übungsinstanz", "DEMO_MODE aktiv: Beispieldaten, kein Versand", "warn", "")
	}
	switch {
	case !updates.Enabled:
		add("Betrieb", "Update-Hinweis", "aus (UPDATE_CHECK=true schaltet ihn ein)", "ok", "")
	case updates.Error != "":
		add("Betrieb", "Update-Hinweis", "Abfrage fehlgeschlagen: "+updates.Error, "warn", "")
	case updates.Latest == "":
		add("Betrieb", "Update-Hinweis", "noch nicht abgefragt", "ok", "")
	case updates.Newer:
		add("Betrieb", "Update-Hinweis", "Neue Version "+updates.Latest+" verfügbar", "warn", updates.URL)
	default:
		add("Betrieb", "Update-Hinweis", "aktuell (neueste Veröffentlichung "+updates.Latest+")", "ok", updates.URL)
	}

	add("Datenbank", "PostgreSQL", info.PostgresVersion, "ok", "")
	add("Datenbank", "Größe", humanSize(info.DatabaseBytes), "ok", "")
	add("Datenbank", "Schema", info.LatestMigration+" ("+itoa(info.Migrations)+" Migrationen)", "ok", "")
	poolTone := "ok"
	if pool.MaxOpenConnections > 0 && pool.InUse >= pool.MaxOpenConnections {
		poolTone = "warn"
	}
	add("Datenbank", "Verbindungen", itoa(pool.InUse)+" aktiv / "+itoa(pool.OpenConnections)+" offen"+
		maxConnLabel(pool.MaxOpenConnections)+" · "+itoa64(pool.WaitCount)+" Wartefälle", poolTone, "")
	add("Datenbank", "Sitzungen", itoa(info.ActiveSessions)+" aktiv · "+itoa(info.ActiveUsers)+" aktive Konten", "ok", "")

	add("Sicherung", "Zustand", backupView.Title+ageSuffix(backupView.AgeLabel), backupView.Tone, "/admin/backup")
	if !backupState.RestoreTested.IsZero() {
		add("Sicherung", "Letzte Wiederherstellungsprobe", humanAgeDE(backupState.RestoreTested), "ok", "/admin/backup")
	}
	switch backupView.S3 {
	case "ok":
		add("Sicherung", "Externe Kopie (S3)", "erfolgreich", "ok", "/admin/backup")
	case "fehlgeschlagen":
		add("Sicherung", "Externe Kopie (S3)", "fehlgeschlagen", "bad", "/admin/backup")
	default:
		add("Sicherung", "Externe Kopie (S3)", "nicht eingerichtet", "warn", "/admin/backup")
	}

	switch {
	case !s.cfg.MailEnabled():
		add("E-Mail", "Versand", "nicht eingerichtet (SMTP_HOST/SMTP_FROM)", "warn", "")
	case ops.Failed+ops.Ambiguous+ops.Held > 0:
		add("E-Mail", "Versand", itoa(ops.Failed+ops.Ambiguous+ops.Held)+" Mail(s) brauchen eine Entscheidung", "bad", "/admin/mail")
	default:
		add("E-Mail", "Versand", "eingerichtet · "+itoa(ops.Pending+ops.Sending)+" wartend", "ok", "/admin/mail")
	}
	if ops.RecurringBlocked > 0 {
		add("E-Mail", "Serien", itoa(ops.RecurringBlocked)+" Serie(n) blockiert", "bad", "/recurring")
	}

	switch {
	case s.cfg.CookieSecure:
		add("Anmeldung", "Cookies", "nur über HTTPS (Secure)", "ok", "")
	case s.cfg.TrustProxy:
		add("Anmeldung", "Cookies", "Secure über vertrauenswürdigen Proxy ("+itoa(len(s.cfg.TrustedProxies))+" Netz(e))", "ok", "")
	default:
		add("Anmeldung", "Cookies", "ohne HTTPS übertragen: nur für ein lokales Testsystem geeignet", "bad", "")
	}
	switch {
	case policy.RequireAdmin && policy.RequireEditor:
		add("Anmeldung", "Zweiter Faktor", "für Administratoren und Bearbeiter vorgeschrieben", "ok", "/admin/users#anmelderichtlinie")
	case policy.RequireAdmin:
		add("Anmeldung", "Zweiter Faktor", "für Administratoren vorgeschrieben", "ok", "/admin/users#anmelderichtlinie")
	default:
		add("Anmeldung", "Zweiter Faktor", "freiwillig: Administratoren können ohne zweiten Faktor arbeiten", "warn", "/admin/users#anmelderichtlinie")
	}
	if len(s.cfg.MetricsToken) >= metricsTokenMinLen {
		add("Anmeldung", "Metriken", "/metrics aktiv (Bearer-Token)", "ok", "")
	} else {
		add("Anmeldung", "Metriken", "aus", "ok", "")
	}

	for _, h := range chainHeads {
		label := "Prüfkette Geschäftsvorfälle"
		if h.Chain == "short" {
			label = "Prüfkette Anmeldung und Betrieb"
		}
		value, tone := itoa64(h.LastSeq)+" Einträge versiegelt", "warn"
		if h.LastSeq > 0 {
			value += " · Stand " + h.ShortHash() + "…"
		}
		switch {
		case h.VerifiedAt == nil:
			value += " · noch nicht geprüft"
		case h.VerifiedOK != nil && !*h.VerifiedOK:
			value, tone = "VERLETZT: "+h.VerifyNote, "bad"
		default:
			value, tone = value+" · geprüft "+humanAgeDE(*h.VerifiedAt), "ok"
		}
		add("Protokoll", label, value, tone, "/admin/audit")
	}

	worst := "ok"
	for _, c := range checks {
		if c.Tone == "bad" || (c.Tone == "warn" && worst == "ok") {
			worst = c.Tone
		}
	}
	data := s.newPage(w, r, "Systemzustand", "system")
	data["Checks"] = checks
	data["Areas"] = []string{"Betrieb", "Datenbank", "Sicherung", "E-Mail", "Anmeldung", "Protokoll"}
	data["Overall"] = worst
	data["CheckedAt"] = time.Now()
	s.render(w, r, "system_status", data)
}

func maxConnLabel(n int) string {
	if n <= 0 {
		return ""
	}
	return " (max. " + itoa(n) + ")"
}

func ageSuffix(age string) string {
	if age == "" {
		return ""
	}
	return " · " + age
}
