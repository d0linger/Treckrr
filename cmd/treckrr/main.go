// Command treckrr starts the Treckrr web application.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"treckrr/internal/backup"
	"treckrr/internal/config"
	"treckrr/internal/db"
	"treckrr/internal/server"
	"treckrr/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[treckrr] ")

	// Subcommands (e.g. `treckrr restore <file>`); no args runs the web server.
	if len(os.Args) > 1 {
		if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
			log.Fatalf("fatal: %v", err)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

// newBackup builds the backup service from config (nil-safe pool for CLI use).
func newBackup(cfg *config.Config, pool *sql.DB) *backup.Service {
	return backup.New(backup.Options{
		DatabaseURL: cfg.DatabaseURL,
		EncKey:      cfg.BackupEncryptionKey,
		Dir:         cfg.BackupDir,
		StatusFile:  cfg.BackupStatusFile,
		Keep:        cfg.BackupKeep,
		Interval:    time.Duration(cfg.BackupIntervalHours) * time.Hour,
	}, pool)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
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
	log.Println("migrations applied")

	st := store.New(pool, cfg.EncryptionSecret)
	if err := st.EnsureAdmin(ctx, cfg.AdminUsername, cfg.AdminPassword, cfg.AdminPasswordReset); err != nil {
		return err
	}
	log.Println("bootstrap complete")

	// Background maintenance: purge expired sessions and stale rate-limit rows on a
	// timer, so cleanup no longer depends on /healthz being hit — and /healthz can
	// stay a cheap, side-effect-free probe instead of running DELETEs per request.
	go purgeLoop(ctx, st)

	// Encrypted backups: scheduled writer (in-app) + on-demand download handler.
	bk := newBackup(cfg, pool)
	if bk.Enabled() {
		log.Printf("encrypted backups enabled (every %dh, keep %d, dir %s)",
			cfg.BackupIntervalHours, cfg.BackupKeep, cfg.BackupDir)
		go bk.Loop(ctx, log.Printf)
	} else {
		log.Println("backups disabled (set BACKUP_ENCRYPTION_KEY to enable)")
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
		log.Printf("listening on :%s", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// purgeLoop periodically removes expired sessions and stale rate-limit rows until
// ctx is canceled. It runs one purge shortly after boot, then on a fixed tick.
func purgeLoop(ctx context.Context, st *store.Store) {
	purge := func() {
		bg, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := st.PurgeExpiredSessions(bg); err != nil {
			log.Printf("purge sessions: %v", err)
		}
		if err := st.PurgeStaleRateLimits(bg); err != nil {
			log.Printf("purge rate limits: %v", err)
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
	bk := newBackup(cfg, pool)
	if !bk.Enabled() {
		pool.Close()
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
		log.Printf("test-restore OK: %d archive objects, schema %s", rep.Objects, rep.SchemaVersion)
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
	log.Printf("restore complete from %s", file)
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
	log.Println("encrypted backup written")
	return nil
}
