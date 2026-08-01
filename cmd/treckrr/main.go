// Command treckrr starts the Treckrr web application.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"treckrr/internal/config"
	"treckrr/internal/db"
	"treckrr/internal/server"
	"treckrr/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[treckrr] ")

	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
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

	srv, err := server.New(cfg, st)
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
