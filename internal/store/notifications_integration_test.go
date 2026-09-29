package store_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestNotificationsIntegration verifies role scoping, event deduplication,
// ownership, read/dismiss state and weekly outbox idempotency on PostgreSQL.
func TestNotificationsIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := context.Background()
	adminID, err := st.CreateUser(ctx, "notify-admin", "notify-admin-password-1", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	editorID, err := st.CreateUser(ctx, "notify-editor", "notify-editor-password-1", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateUserAccount(ctx, adminID, "notify-admin", "notify-admin@example.invalid"); err != nil {
		t.Fatal(err)
	}

	source := &store.NotificationSource{
		DedupeKey: "backup:failed:1", Kind: "backup", Tone: "bad", Title: "Backup fehlgeschlagen",
		Detail: "vor 2 Stunden", Href: "/admin/backup", AdminOnly: true,
	}
	if err := st.RefreshNotifications(ctx, source); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := st.RefreshNotifications(ctx, source); err != nil {
		t.Fatalf("deduplicated refresh: %v", err)
	}
	items, err := st.ListNotifications(ctx, adminID, 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("admin notifications: %v / %+v", err, items)
	}
	if editorItems, err := st.ListNotifications(ctx, editorID, 100); err != nil || len(editorItems) != 0 {
		t.Fatalf("admin-only notification leaked to editor: %v / %+v", err, editorItems)
	}
	if count, err := st.UnreadNotificationCount(ctx, adminID); err != nil || count != 1 {
		t.Fatalf("unread count = %d, %v; want 1", count, err)
	}
	href, err := st.OpenNotification(ctx, adminID, items[0].ID)
	if err != nil || href != "/admin/backup" {
		t.Fatalf("open = %q, %v", href, err)
	}
	if _, err := st.OpenNotification(ctx, editorID, items[0].ID); err != store.ErrNotFound {
		t.Fatalf("cross-user open = %v, want ErrNotFound", err)
	}

	if err := st.UpdateNotificationPreferences(ctx, adminID, true); err != nil {
		t.Fatal(err)
	}
	prefs, err := st.NotificationPreferences(ctx, adminID)
	if err != nil || !prefs.WeeklyEmail {
		t.Fatalf("preferences: %+v / %v", prefs, err)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(int(now.Weekday())+6)%7)
	if _, err := pool.ExecContext(ctx, `UPDATE notifications SET created_at=$2 WHERE id=$1`, items[0].ID, start.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if queued, err := st.QueueWeeklyNotificationDigests(ctx, now, "https://treckrr.example"); err != nil || queued != 1 {
		t.Fatalf("first digest: queued=%d err=%v", queued, err)
	}
	if queued, err := st.QueueWeeklyNotificationDigests(ctx, now, "https://treckrr.example"); err != nil || queued != 0 {
		t.Fatalf("duplicate digest: queued=%d err=%v", queued, err)
	}
	var mails int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM mail_outbox WHERE delivery_key=$1`,
		"notification-digest:"+strconv.FormatInt(adminID, 10)+":"+start.Format("2006-01-02")).Scan(&mails); err != nil || mails != 1 {
		t.Fatalf("digest intents = %d, %v; want 1", mails, err)
	}

	if err := st.DismissNotification(ctx, adminID, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if items, err := st.ListNotifications(ctx, adminID, 100); err != nil || len(items) != 0 {
		t.Fatalf("dismissed notification remains visible: %v / %+v", err, items)
	}
	bad := *source
	bad.DedupeKey = "bad-target"
	bad.Href = "//attacker.invalid"
	if err := st.RefreshNotifications(ctx, &bad); err == nil {
		t.Fatal("unsafe notification target must be rejected")
	}
}
