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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/server"
	"github.com/d0linger/treckrr/internal/store"
)

// paymentUndoGrace is how long a soft-deleted payment stays restorable before the
// maintenance loop purges it for good.
const paymentUndoGrace = 7 * 24 * time.Hour

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
func newBackup(cfg *config.Config, pool *sql.DB, st *store.Store) *backup.Service {
	return backup.New(backup.Options{
		DatabaseURL: cfg.DatabaseURL,
		EncKey:      cfg.BackupEncryptionKey,
		Dir:         cfg.BackupDir,
		StatusFile:  cfg.BackupStatusFile,
		Keep:        cfg.BackupKeep,
		S3: backup.S3Options{
			Endpoint:  cfg.S3Endpoint,
			Bucket:    cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Prefix:    cfg.S3Prefix,
			UseSSL:    cfg.S3UseSSL,
		},
		SettingsFn: func(ctx context.Context) backup.Settings {
			s, err := st.GetBackupSettings(ctx)
			if err != nil {
				return backup.Settings{VolumeCron: "0 3 * * *", VolumeKeep: cfg.BackupKeep, S3Cron: "0 4 * * *"}
			}
			return backup.Settings(s)
		},
	}, pool)
}

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
	go purgeLoop(ctx, st)

	// Encrypted backups: scheduled writer (in-app) + on-demand download handler.
	// Seed the schedule from env on first boot; thereafter it is GUI-editable.
	if err := st.EnsureBackupSettings(ctx, models.BackupSettings{
		VolumeCron: "0 3 * * *",
		VolumeKeep: cfg.BackupKeep,
		S3Cron:     "0 4 * * *",
	}); err != nil {
		return err
	}
	bk := newBackup(cfg, pool, st)
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

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
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
func purgeLoop(ctx context.Context, st *store.Store) {
	purge := func() {
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := st.PurgeExpiredSessions(bg); err != nil {
			slog.Error("purge sessions", "err", err)
		}
		if err := st.PurgeStaleRateLimits(bg); err != nil {
			slog.Error("purge rate limits", "err", err)
		}
		if err := st.PurgeExpiredWebauthnCeremonies(bg); err != nil {
			slog.Error("purge webauthn ceremonies", "err", err)
		}
		// Hard-delete payments soft-deleted more than the undo grace window ago.
		if err := st.PurgeDeletedPayments(bg, time.Now().Add(-paymentUndoGrace)); err != nil {
			slog.Error("purge deleted payments", "err", err)
		}
		// Materialize any due recurring bookings (idempotent).
		if n, err := st.RunDueRecurring(bg); err != nil {
			slog.Error("recurring generation", "err", err)
		} else if n > 0 {
			slog.Info("recurring bookings created", "count", n)
		}
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

// runCommand dispatches CLI subcommands. Restore is deliberately CLI-only (it
// overwrites the live database) with a typed confirmation — never a UI button.
func runCommand(cmd string, args []string) error {
	switch cmd {
	case "restore":
		return runRestore(args)
	case "backup":
		return runBackupCLI(args)
	default:
		return fmt.Errorf("unknown command %q (known: restore, backup)", cmd)
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
	bk := newBackup(cfg, pool, store.New(pool, cfg.EncryptionSecret))
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
	ctx := context.Background()

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
	if err := bk.Restore(ctx, file, cfg.DatabaseURL); err != nil {
		return err
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
