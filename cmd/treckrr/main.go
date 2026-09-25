// Command treckrr starts the Treckrr web application.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/server"
	"github.com/d0linger/treckrr/internal/store"
)

// Audit retention separates routine security events from business records.
// Business records retain seven complete calendar years after their event year.
// This default is not a substitute for operator-managed legal holds.
const (
	auditRetentionShortYears = 1
	auditRetentionLongYears  = 7
)

// auditRetentionCutoffs returns the (short, long) purge cutoffs for a given
// reference time. Business retention starts at the year boundary, not the
// individual record's anniversary, so no part of that event year expires early.
func auditRetentionCutoffs(now time.Time) (short, long time.Time) {
	return now.AddDate(-auditRetentionShortYears, 0, 0),
		time.Date(now.Year()-auditRetentionLongYears, time.January, 1, 0, 0, 0, 0, now.Location())
}

func main() {
	setupLogging()

	// Subcommands (e.g. `treckrr restore <file>`); no args runs the web server.
	if len(os.Args) > 1 {
		if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
			slog.Error("fatal", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// setupLogging installs the process-wide structured logger. Output goes to stdout
// (captured by `docker logs`) as human-readable text by default, or JSON when
// LOG_FORMAT=json for log aggregation. LOG_LEVEL (debug|info|warn|error) sets the
// threshold; info is the default.
func setupLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}

// newBackup builds the backup service. The schedule is read live from the DB
// (GUI-editable) via SettingsFn, falling back to the BACKUP_* env.
func newBackup(cfg *config.Config, pool *sql.DB, st *store.Store, maxBytes int64, cli bool) *backup.Service {
	return backup.New(backup.Options{
		DatabaseURL:        cfg.DatabaseURL,
		EncKey:             cfg.BackupEncryptionKey,
		Dir:                cfg.BackupDir,
		StatusFile:         cfg.BackupStatusFile,
		Keep:               cfg.BackupKeep,
		MaxBytes:           maxBytes,
		SkipStartupCleanup: cli,
		RehearseURL:        cfg.BackupRehearseURL,
		S3: backup.S3Options{
			Endpoint:     cfg.S3Endpoint,
			Bucket:       cfg.S3Bucket,
			AccessKey:    cfg.S3AccessKey,
			SecretKey:    cfg.S3SecretKey,
			Prefix:       cfg.S3Prefix,
			LegacyPrefix: cfg.S3LegacyPrefix,
			UseSSL:       cfg.S3UseSSL,
		},
		SettingsFn: func(ctx context.Context) backup.Settings {
			s, err := st.GetBackupSettings(ctx)
			if err != nil {
				return backup.Settings{
					VolumeCron: "0 3 * * *", VolumeKeep: cfg.BackupKeep,
					S3Cron: "0 4 * * *", S3Keep: cfg.S3Keep,
				}
			}
			return backup.Settings(s)
		},
	}, pool)
}

// run initializes validated dependencies, starts background workers, and serves
// until the process receives a shutdown signal.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Non-fatal production-readiness note (T-01): without Secure cookies and without
	// a trusted proxy that terminates TLS, auth cookies travel in the clear. Fine for
	// a local HTTP test box; in production put a TLS proxy in front and set
	// TRUST_PROXY=true (or COOKIE_SECURE=true).
	if !cfg.CookieSecure && !cfg.TrustProxy {
		slog.Warn("auth cookies are not Secure and no trusted proxy is set — use HTTPS behind a TLS proxy (TRUST_PROXY=true) or COOKIE_SECURE=true in production")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	appLease, err := db.AcquireApplicationLease(ctx, pool)
	if err != nil {
		return err
	}
	defer appLease.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	slog.Info("migrations applied")

	st := store.New(pool, cfg.EncryptionSecret)
	if err := st.EnsureAdmin(ctx, cfg.AdminUsername, cfg.AdminPassword, cfg.AdminPasswordReset); err != nil {
		return err
	}
	// Festschreibung: freeze a content snapshot for invoices issued before the
	// snapshot columns existed, so the Beleg renders from the frozen record. This
	// reproduces the current live values, so no displayed amount changes.
	// Non-fatal: the backfill is idempotent and only fills missing snapshots, so a
	// failure here degrades gracefully (those invoices render live) and is retried
	// on the next boot — don't block startup on it.
	if n, err := st.BackfillInvoiceSnapshots(ctx); err != nil {
		slog.Error("invoice snapshot backfill failed (continuing, retried next boot)", "err", err)
	} else if n > 0 {
		slog.Info("backfilled invoice snapshots", "count", n)
	}
	// Re-encrypt any legacy plaintext/v1 TOTP seeds to v2 (T-06). Non-fatal: the
	// dual-read still works if this fails, and it retries on the next boot.
	if n, err := st.MigrateTotpSecretsToV2(ctx); err != nil {
		slog.Error("TOTP seed migration failed (continuing, retried next boot)", "err", err)
	} else if n > 0 {
		slog.Info("migrated TOTP seeds to v2 encryption", "count", n)
	}
	slog.Info("bootstrap complete")

	// Background maintenance: purge expired sessions and stale rate-limit rows on a
	// timer, so cleanup no longer depends on /healthz being hit — and /healthz can
	// stay a cheap, side-effect-free probe instead of running DELETEs per request.
	// Waited for at shutdown like the backup loop: without the wait, pool.Close()
	// fired under an in-flight tick — a mail could be DELIVERED but its
	// status='sent' write fail on the closed pool, and the next boot re-sent it.
	var purgeWG sync.WaitGroup
	purgeWG.Add(1)

	// Encrypted backups: scheduled writer (in-app) + on-demand download handler.
	// Seed the schedule from env on first boot; thereafter it is GUI-editable.
	if err := st.EnsureBackupSettings(ctx, models.BackupSettings{
		VolumeCron: "0 3 * * *",
		VolumeKeep: cfg.BackupKeep,
		S3Cron:     "0 4 * * *",
		S3Keep:     cfg.S3Keep,
	}); err != nil {
		return err
	}
	bk := newBackup(cfg, pool, st, 0, false)
	var bkWG sync.WaitGroup
	if bk.Enabled() {
		slog.Info("encrypted backups enabled (schedule via GUI)", "dir", cfg.BackupDir)
		bkWG.Add(1)
		go func() { defer bkWG.Done(); bk.Loop(ctx, slog.Default()) }()
	} else {
		slog.Info("backups disabled (set BACKUP_ENCRYPTION_KEY to enable)")
	}

	srv, err := server.New(cfg, st, bk)
	if err != nil {
		return err
	}
	srv.SetRestoreLease(appLease.Exclusive)
	var leaseWG sync.WaitGroup
	leaseWG.Add(1)
	go func() {
		defer leaseWG.Done()
		if err := appLease.Monitor(ctx, time.Second); err != nil {
			// The advisory lock vanished with its PostgreSQL session. Do not
			// attempt to reacquire it: an offline restore may already own the
			// exclusive lock. Remove readiness first, then begin shutdown.
			srv.FailClosed()
			slog.Error("application maintenance lease lost; shutting down", "err", err)
			stop()
		}
	}()
	go func() { defer purgeWG.Done(); purgeLoop(ctx, cfg, st, srv) }()

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Server-internal errors (TLS handshakes, port problems, its own panic
		// lines) otherwise go through the std log package and never reach the
		// JSON log stream everything else uses.
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}

	go func() {
		slog.Info("listening", "addr", ":"+cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = httpServer.Shutdown(shutdownCtx)
	// Give an in-flight scheduled backup a brief window to finish cleanly (its
	// runs use context.WithoutCancel) rather than being killed mid-dump on deploy.
	// Backups here are sub-second, so this rarely waits; the bound caps a large one
	// (a full run can take up to 10 min, but we don't hold up a deploy that long —
	// writeFileAtomic guarantees no partial file if we exit first).
	if !waitTimeout(&bkWG, 30*time.Second) {
		slog.Warn("shutdown: a scheduled backup was still running after 30s; exiting anyway")
	}
	// The maintenance tick stops BETWEEN outbox mails on ctx cancel and each
	// mail's own budget is 45s — this wait lets an in-flight delivery finish its
	// bookkeeping instead of leaving a delivered-but-still-pending row behind.
	if !waitTimeout(&purgeWG, 50*time.Second) {
		slog.Warn("shutdown: the maintenance tick was still running after 50s; exiting anyway")
	}
	leaseWG.Wait()
	return err
}

// waitTimeout blocks until wg is done or d elapses; it reports whether wg
// finished within the deadline.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// purgeLoop periodically removes expired sessions and stale rate-limit rows until
// ctx is canceled. It runs one purge shortly after boot, then on a fixed tick.
func purgeLoop(ctx context.Context, cfg *config.Config, st *store.Store, srv *server.Server) {
	purge := func() {
		srv.BackgroundTask(ctx, func(taskCtx context.Context) {
			metrics.Inc(metrics.MaintenanceRuns)
			maintenanceTask(taskCtx, 30*time.Second, func(bg context.Context) {
				if err := st.PurgeExpiredSessions(bg); err != nil {
					metrics.Inc(metrics.MaintenanceFails)
					slog.Error("purge sessions", "err", err)
				}
			})
			maintenanceTask(taskCtx, 30*time.Second, func(bg context.Context) {
				if err := st.PurgeStaleRateLimits(bg); err != nil {
					slog.Error("purge rate limits", "err", err)
				}
			})
			maintenanceTask(taskCtx, 30*time.Second, func(bg context.Context) {
				if err := st.PurgeExpiredWebauthnCeremonies(bg); err != nil {
					slog.Error("purge webauthn ceremonies", "err", err)
				}
			})
			// Materialize any due recurring bookings (idempotent).
			maintenanceTask(taskCtx, time.Minute, func(bg context.Context) {
				if n, err := st.RunDueRecurring(bg); err != nil {
					metrics.Inc(metrics.MaintenanceFails)
					slog.Error("recurring generation", "err", err)
				} else if n > 0 {
					metrics.Add(metrics.RecurringCreated, int64(n))
					slog.Info("recurring bookings created", "count", n)
					// These are system-created bookings (no HTTP request / user), so record a
					// system-actor audit line — otherwise the entries appear in the DB with no
					// trail explaining who created them. Best-effort: a missing line must not
					// abort the maintenance tick.
					detail := fmt.Sprintf("%d Buchung(en) aus fälligen Serien erzeugt", n)
					if err := st.AddAudit(bg, nil, "system", "recurring_run", "recurring", "", detail, ""); err != nil {
						slog.Error("audit recurring run", "err", err)
					}
				}
			})
			// Staggered audit-log retention: pure security/auth/ops noise expires after the
			// short window (DSGVO Art. 5(1)(e) data minimisation); business- and tax-relevant
			// events are kept for the long window (§ 132 BAO, 7 years). The classification
			// lives in the store (shortLivedAuditActions); everything not listed defaults to
			// the long window, so a new action is never dropped early by omission.
			// Deliver parked mail (failed synchronous sends). The sender is injected so
			// the store stays free of a config dependency; each delivery gets its own
			// bounded context so one slow SMTP dialog cannot eat the whole tick.
			if cfg.MailEnabled() {
				maintenanceTask(taskCtx, time.Minute, func(mailCtx context.Context) {
					// ProcessMailOutbox observes mailCtx between messages. A message
					// already accepted for delivery retains its own bounded settlement
					// budget so it cannot be sent and left pending on cancellation.
					sent, gaveUp, err := st.ProcessMailOutbox(mailCtx, func(ctx context.Context, messageID, to, subject, body, attName, attType string, attData []byte) error {
						sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
						defer cancel()
						var atts []mail.Attachment
						if attName != "" {
							atts = append(atts, mail.Attachment{Filename: attName, ContentType: attType, Data: attData})
						}
						return mail.SendWithMessageID(sctx, cfg, to, subject, body, atts, messageID)
					})
					if err != nil {
						metrics.Inc(metrics.MaintenanceFails)
						slog.Error("mail outbox", "err", err)
					}
					if sent > 0 {
						metrics.Add(metrics.MailSent, int64(sent))
						slog.Info("mail outbox delivered", "count", sent)
					}
					if gaveUp > 0 {
						metrics.Add(metrics.MailFailed, int64(gaveUp))
						slog.Warn("mail outbox gave up", "count", gaveUp)
					}
				})
			}
			maintenanceTask(taskCtx, 30*time.Second, func(bg context.Context) {
				if err := st.PurgeSentMail(bg, time.Now().Add(-30*24*time.Hour)); err != nil {
					slog.Error("purge sent mail", "err", err)
				}
			})
			maintenanceTask(taskCtx, time.Minute, func(bg context.Context) {
				shortCutoff, longCutoff := auditRetentionCutoffs(time.Now())
				if n, err := st.PurgeAuditLog(bg, shortCutoff, longCutoff); err != nil {
					metrics.Inc(metrics.MaintenanceFails)
					slog.Error("purge audit log", "err", err)
				} else if n > 0 {
					metrics.Add(metrics.AuditPurged, n)
					slog.Info("audit log purged", "count", n)
				}
			})
		})
	}
	purge()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purge()
		}
	}
}

// maintenanceTask runs one maintenance unit within the caller's lifecycle and
// a bounded deadline, skipping work once shutdown has begun.
func maintenanceTask(parent context.Context, timeout time.Duration, work func(context.Context)) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	work(ctx)
}

// runCommand dispatches CLI subcommands. Destructive restore requires typed
// confirmation and an offline database lease; the GUI has its own drain gate.
func runCommand(cmd string, args []string) error {
	switch cmd {
	case "restore":
		return runRestore(args)
	case "backup":
		return runBackupCLI(args)
	case "rotate-key":
		return runRotateKeyCLI(args)
	case "rehearse-restore":
		return runRehearseCLI(args)
	default:
		return fmt.Errorf("unknown command %q (known: restore, backup, rotate-key, rehearse-restore)", cmd)
	}
}

// openBackup loads config, connects, and builds the backup service for a CLI
// command. The caller must close the returned pool.
func openBackup() (*config.Config, *sql.DB, *backup.Service, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, err
	}
	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, nil, nil, err
	}
	maxBytes, err := backupCLIMaxBytes(os.Getenv("BACKUP_CLI_MAX_BYTES"))
	if err != nil {
		_ = pool.Close()
		return nil, nil, nil, err
	}
	bk := newBackup(cfg, pool, store.New(pool, cfg.EncryptionSecret), maxBytes, true)
	if !bk.Enabled() {
		_ = pool.Close()
		return nil, nil, nil, fmt.Errorf("backups are not configured (set BACKUP_ENCRYPTION_KEY)")
	}
	return cfg, pool, bk, nil
}

// runRestore handles `treckrr restore [--test] <file.dump.enc>`.
func runRestore(args []string) error {
	var file string
	test := false
	for _, a := range args {
		switch {
		case a == "--test":
			test = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		default:
			file = a
		}
	}
	if file == "" {
		return fmt.Errorf("usage: treckrr restore [--test] <file.dump.enc>")
	}
	cfg, pool, bk, err := openBackup()
	if err != nil {
		return err
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if test {
		rep, err := bk.TestRestore(ctx, file)
		if err != nil {
			return err
		}
		slog.Info("test-restore OK", "objects", rep.Objects, "schema", rep.SchemaVersion)
		return nil
	}

	// Destructive: require an explicit typed confirmation on the terminal.
	fmt.Fprintf(os.Stderr,
		"WARNING: this OVERWRITES the live database with %s.\nType RESTORE to continue: ", file)
	var answer string
	_, _ = fmt.Fscanln(os.Stdin, &answer)
	if strings.TrimSpace(answer) != "RESTORE" {
		return fmt.Errorf("aborted")
	}
	lease, err := db.AcquireOfflineRestoreLease(ctx, pool)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := bk.Restore(ctx, file, cfg.DatabaseURL); err != nil {
		return err
	}
	st := store.New(pool, cfg.EncryptionSecret)
	if err := st.ReconcileAfterRestore(ctx); err != nil {
		return fmt.Errorf("restore completed but reconciliation failed; keep the app stopped: %w", err)
	}
	slog.Info("restore complete", "file", file)
	return nil
}

// runBackupCLI handles `treckrr backup` — write one encrypted dump to BACKUP_DIR
// (the same path the scheduler uses). Handy for an external cron if preferred.
func runBackupCLI(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: treckrr backup")
	}
	_, pool, bk, err := openBackup()
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := bk.RunScheduled(context.Background()); err != nil {
		return err
	}
	slog.Info("encrypted backup written")
	return nil
}

// runRotateKeyCLI re-encrypts every stored dump from the previous key to the one
// now in BACKUP_ENCRYPTION_KEY. CLI-only: it rewrites every recovery point.
//
// The previous key is read from BACKUP_ENCRYPTION_KEY_OLD rather than an
// argument, so it never lands in the shell history or a process list.
func runRotateKeyCLI(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: treckrr rotate-key   (set BACKUP_ENCRYPTION_KEY to the new key and BACKUP_ENCRYPTION_KEY_OLD to the previous one)")
	}
	oldKey := os.Getenv("BACKUP_ENCRYPTION_KEY_OLD")
	if oldKey == "" {
		return fmt.Errorf("BACKUP_ENCRYPTION_KEY_OLD is not set — it must hold the key the existing dumps were written with")
	}
	_, pool, bk, err := openBackup()
	if err != nil {
		return err
	}
	defer pool.Close()
	res, err := bk.RotateKey(context.Background(), oldKey)
	for _, n := range res.Rotated {
		slog.Info("rotated", "file", n)
	}
	for _, n := range res.Skipped {
		slog.Warn("skipped", "detail", n)
	}
	if err != nil {
		return err
	}
	slog.Info("key rotation finished", "rotated", len(res.Rotated), "skipped", len(res.Skipped))
	if bk.S3Enabled() {
		slog.Warn("only local backups were rotated; keep the previous key in secure recovery storage for older S3 archives")
	}
	return nil
}

func backupCLIMaxBytes(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1<<20 || n > 16<<30 {
		return 0, fmt.Errorf("BACKUP_CLI_MAX_BYTES must be between 1 MiB and 16 GiB; provision offline memory accordingly")
	}
	return n, nil
}

// runRehearseCLI restores the newest dump into a scratch database and queries it
// — the real drill behind the panel's "Restore getestet" line.
func runRehearseCLI(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: treckrr rehearse-restore   (needs BACKUP_REHEARSE_URL)")
	}
	_, pool, bk, err := openBackup()
	if err != nil {
		return err
	}
	defer pool.Close()
	files, err := bk.List()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no stored dump to rehearse")
	}
	enc, err := bk.Open(files[0].Name)
	if err != nil {
		return err
	}
	rep, err := bk.RehearseRestore(context.Background(), enc)
	if err != nil {
		return err
	}
	slog.Info("restore rehearsal succeeded",
		"file", files[0].Name, "migrations", rep.Migrations,
		"tables", rep.Tables, "rows", rep.Rows, "took", rep.Duration.String())
	return nil
}
