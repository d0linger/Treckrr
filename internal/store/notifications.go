package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Notification is one deduplicated, actionable in-app signal for a user.
type Notification struct {
	ID          int64
	Kind        string
	Tone        string
	Title       string
	Detail      string
	Href        string
	CreatedAt   time.Time
	ReadAt      *time.Time
	DismissedAt *time.Time
}

// NotificationSource describes one current event that should be offered to a
// role. DedupeKey is a stable event identity, not display text.
type NotificationSource struct {
	DedupeKey  string
	Kind       string
	Tone       string
	Title      string
	Detail     string
	Href       string
	AdminOnly  bool
	WriterOnly bool
}

// NotificationPreferences are intentionally small: in-app notifications are
// always available, while weekly email is explicit opt-in.
type NotificationPreferences struct {
	WeeklyEmail bool
}

// RefreshNotifications snapshots current operational signals into per-user,
// deduplicated notifications. Repeated maintenance ticks cannot duplicate an
// event because (user_id, dedupe_key) is unique.
func (s *Store) RefreshNotifications(ctx context.Context, backup *NotificationSource) error {
	sources, err := s.notificationSources(ctx)
	if err != nil {
		return err
	}
	if backup != nil {
		sources = append(sources, *backup)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, source := range sources {
		source.Title = truncateNotificationText(source.Title, 200)
		source.Detail = truncateNotificationText(source.Detail, 1000)
		if err := validateNotificationSource(source); err != nil {
			return err
		}
		roleClause := `NOT disabled`
		switch {
		case source.AdminOnly:
			roleClause += ` AND role='admin'`
		case source.WriterOnly:
			roleClause += ` AND role IN ('admin','editor')`
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notifications
			(user_id,dedupe_key,kind,tone,title,detail,href)
			SELECT id,$1,$2,$3,$4,$5,$6 FROM users WHERE `+roleClause+`
			ON CONFLICT (user_id,dedupe_key) DO NOTHING`,
			source.DedupeKey, source.Kind, source.Tone, source.Title, source.Detail, source.Href)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// truncateNotificationText limits display-only text without splitting UTF-8.
// Structural fields such as kind, dedupe key and href remain strictly checked.
func truncateNotificationText(value string, maxRunes int) string {
	count := 0
	for byteIndex := range value {
		if count == maxRunes {
			return value[:byteIndex]
		}
		count++
	}
	return value
}

// validateNotificationSource enforces the storage and same-origin link bounds
// before display data reaches every user's stream.
func validateNotificationSource(source NotificationSource) error {
	if source.DedupeKey == "" || len(source.DedupeKey) > 300 {
		return errors.New("notification source has invalid dedupe key")
	}
	switch source.Kind {
	case "due", "recurring", "import", "mail", "backup":
	default:
		return fmt.Errorf("notification source has invalid kind %q", source.Kind)
	}
	if source.Tone != "info" && source.Tone != "warn" && source.Tone != "bad" {
		return fmt.Errorf("notification source has invalid tone %q", source.Tone)
	}
	if source.Title == "" || len([]rune(source.Title)) > 200 || len([]rune(source.Detail)) > 1000 {
		return errors.New("notification source has invalid display text")
	}
	if !strings.HasPrefix(source.Href, "/") || strings.HasPrefix(source.Href, "//") || strings.Contains(source.Href, `\`) {
		return errors.New("notification source has invalid target")
	}
	return nil
}

// notificationSources reads the durable domain states that currently need an
// operator decision; it does not mutate those states.
func (s *Store) notificationSources(ctx context.Context) ([]NotificationSource, error) {
	company, err := s.GetCompany(ctx)
	if err != nil {
		return nil, err
	}
	due, err := s.DunningRows(ctx, 0, company.PaymentTermDays, time.Now())
	if err != nil {
		return nil, err
	}
	sources := make([]NotificationSource, 0, len(due)+16)
	for _, row := range due {
		sources = append(sources, NotificationSource{
			DedupeKey: "due:" + strconv.FormatInt(row.YearID, 10) + ":" + strconv.FormatInt(row.NeighborID, 10) + ":" + row.InvoiceNo,
			Kind:      "due", Tone: "warn", Title: "Zahlung überfällig · " + row.InvoiceNo,
			Detail: row.Name + " · " + strconv.Itoa(row.DaysOverdue) + " Tage · " + strings.Replace(row.Open.StringFixed(2), ".", ",", 1) + " €",
			Href:   "/mahnwesen?year=" + strconv.FormatInt(row.YearID, 10), WriterOnly: true,
		})
	}

	rows, err := s.db.QueryContext(ctx, `SELECT r.id,n.name,r.last_error,r.last_error_at
		FROM recurring_entries r JOIN neighbors n ON n.id=r.neighbor_id
		WHERE r.active AND r.last_error<>'' AND r.last_error_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name, message string
		var at time.Time
		if err := rows.Scan(&id, &name, &message, &at); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sources = append(sources, NotificationSource{
			DedupeKey: fmt.Sprintf("recurring:%d:%d", id, at.UnixNano()), Kind: "recurring", Tone: "warn",
			Title: "Serie blockiert", Detail: name + " · " + message, Href: "/recurring", WriterOnly: true,
		})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT id,status,unmatched_rows FROM payment_import_batches
		WHERE created_at>now()-interval '90 days' AND (status='failed' OR unmatched_rows>0)`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var status string
		var unmatched int
		if err := rows.Scan(&id, &status, &unmatched); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tone, detail := "warn", strconv.Itoa(unmatched)+" Zeile(n) nicht zugeordnet"
		if status == "failed" {
			tone, detail = "bad", "Importlauf fehlgeschlagen"
		}
		sources = append(sources, NotificationSource{
			DedupeKey: fmt.Sprintf("import:%d:%s", id, status), Kind: "import", Tone: tone,
			Title: "Zahlungsimport prüfen", Detail: detail,
			Href: "/payments/import/batches/" + strconv.FormatInt(id, 10), WriterOnly: true,
		})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT id,status FROM mail_outbox
		WHERE status IN ('failed','ambiguous','held') AND redacted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sources = append(sources, NotificationSource{
			DedupeKey: fmt.Sprintf("mail:%d:%s", id, status), Kind: "mail", Tone: "bad",
			Title: "E-Mail-Entscheidung erforderlich", Detail: "Mail #" + strconv.FormatInt(id, 10) + " · " + status,
			Href: "/admin/mail/" + strconv.FormatInt(id, 10), AdminOnly: true,
		})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return sources, rows.Err()
}

// ListNotifications returns a user's visible notifications, newest first.
func (s *Store) ListNotifications(ctx context.Context, userID int64, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,tone,title,detail,href,created_at,read_at,dismissed_at
		FROM notifications WHERE user_id=$1 AND dismissed_at IS NULL
		ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var read, dismissed sql.NullTime
		if err := rows.Scan(&n.ID, &n.Kind, &n.Tone, &n.Title, &n.Detail, &n.Href, &n.CreatedAt, &read, &dismissed); err != nil {
			return nil, err
		}
		if read.Valid {
			n.ReadAt = &read.Time
		}
		if dismissed.Valid {
			n.DismissedAt = &dismissed.Time
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotificationCount returns the current user's badge count.
func (s *Store) UnreadNotificationCount(ctx context.Context, userID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM notifications
		WHERE user_id=$1 AND read_at IS NULL AND dismissed_at IS NULL`, userID).Scan(&count)
	return count, err
}

// OpenNotification marks an owned notification read and returns its safe local target.
func (s *Store) OpenNotification(ctx context.Context, userID, id int64) (string, error) {
	var href string
	err := s.db.QueryRowContext(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now())
		WHERE id=$1 AND user_id=$2 AND dismissed_at IS NULL RETURNING href`, id, userID).Scan(&href)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return href, err
}

// DismissNotification hides one owned notification without deleting its audit identity.
func (s *Store) DismissNotification(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE notifications SET dismissed_at=now(),read_at=COALESCE(read_at,now())
		WHERE id=$1 AND user_id=$2 AND dismissed_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReadAllNotifications marks all visible notifications for a user as read.
func (s *Store) ReadAllNotifications(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now())
		WHERE user_id=$1 AND dismissed_at IS NULL`, userID)
	return err
}

// NotificationPreferences returns defaults for users without a preference row.
func (s *Store) NotificationPreferences(ctx context.Context, userID int64) (NotificationPreferences, error) {
	var out NotificationPreferences
	err := s.db.QueryRowContext(ctx, `SELECT weekly_email FROM user_notification_preferences WHERE user_id=$1`, userID).Scan(&out.WeeklyEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	return out, err
}

// UpdateNotificationPreferences saves the user's explicit weekly-mail opt-in.
func (s *Store) UpdateNotificationPreferences(ctx context.Context, userID int64, weekly bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_notification_preferences (user_id,weekly_email)
		VALUES ($1,$2) ON CONFLICT (user_id) DO UPDATE SET weekly_email=EXCLUDED.weekly_email,updated_at=now()`, userID, weekly)
	return err
}

// QueueWeeklyNotificationDigests creates at most one outbox intent per user and
// ISO week. The existing outbox owns retry, ambiguity and shutdown safety.
func (s *Store) QueueWeeklyNotificationDigests(ctx context.Context, now time.Time, baseURL string) (int, error) {
	start := weekStart(now)
	from := start.AddDate(0, 0, -7)
	users, err := s.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, user := range users {
		if user.Disabled || strings.TrimSpace(user.Email) == "" {
			continue
		}
		prefs, err := s.NotificationPreferences(ctx, user.ID)
		if err != nil {
			return queued, err
		}
		if !prefs.WeeklyEmail {
			continue
		}
		rows, err := s.db.QueryContext(ctx, `SELECT title,detail,href FROM notifications
			WHERE user_id=$1 AND dismissed_at IS NULL AND created_at>=$2 AND created_at<$3
			ORDER BY created_at,id`, user.ID, from, start)
		if err != nil {
			return queued, err
		}
		var body strings.Builder
		count := 0
		for rows.Next() {
			var title, detail, href string
			if err := rows.Scan(&title, &detail, &href); err != nil {
				_ = rows.Close()
				return queued, err
			}
			count++
			fmt.Fprintf(&body, "- %s", title)
			if detail != "" {
				fmt.Fprintf(&body, ": %s", detail)
			}
			fmt.Fprintf(&body, "\n  %s%s\n", strings.TrimRight(baseURL, "/"), href)
		}
		if err := rows.Close(); err != nil {
			return queued, err
		}
		if err := rows.Err(); err != nil {
			return queued, err
		}
		if count == 0 {
			continue
		}
		intro := fmt.Sprintf("Hallo %s,\n\n%d Hinweis(e) aus der vergangenen Woche:\n\n", user.Username, count)
		intent, created, err := s.CreateMailIntent(ctx, OutboxMail{
			Kind: "digest", Recipient: user.Email, Subject: "Treckrr · Wochenübersicht",
			Body:        intro + body.String() + "\nDiese Übersicht kannst du unter Einstellungen deaktivieren.\n",
			DeliveryKey: fmt.Sprintf("notification-digest:%d:%s", user.ID, start.Format("2006-01-02")),
		})
		_ = intent
		if err != nil {
			return queued, err
		}
		if created {
			queued++
		}
	}
	return queued, nil
}

// weekStart returns Monday 00:00 in the supplied time's location.
func weekStart(t time.Time) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	delta := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -delta)
}
