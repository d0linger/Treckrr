package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// securityTestStore keeps security tests isolated from shared account/settings
// fixtures. Only the explicitly supplied test server receives this scratch DB.
func securityTestStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	base, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminURL := *base
	adminURL.Path = "/postgres"
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "treckrr_web_test_" + hex.EncodeToString(suffix[:])
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("remove owned scratch database: %v", err)
		}
	})
	scratchURL := *base
	scratchURL.Path = "/" + name
	pool, err := db.Connect(t.Context(), scratchURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return store.New(pool, "test-security-key"), pool
}

func TestPasswordChangeRotatesCookieIntegration(t *testing.T) {
	st, _ := securityTestStore(t)
	id, err := st.CreateUser(t.Context(), "rotation-editor", "Original-password-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := st.CreateSession(t.Context(), id, sessionTTL, "test", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret"}, store: st, logins: newLoginLimiter(st)}
	form := url.Values{"current_password": {"Original-password-123"}, "new_password": {"Replacement-password-456"}, "new_password_confirm": {"Replacement-password-456"}}
	r := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode())).WithContext(t.Context())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: oldToken})
	r.Header.Set(csrfHeaderName, s.csrfToken(r))
	w := httptest.NewRecorder()
	s.csrf(s.auth(s.handleAccountPasswordSubmit)).ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/profile" {
		t.Fatalf("password change status %d, location %s", w.Code, w.Header().Get("Location"))
	}
	newToken := ""
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie {
			newToken = cookie.Value
		}
	}
	if newToken == "" || newToken == oldToken {
		t.Fatal("current cookie was not rotated")
	}
	if _, err := st.UserFromSession(t.Context(), oldToken, sessionTTL, sessionAbsoluteTTL); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("copied old cookie survived: %v", err)
	}
	if _, err := st.UserFromSession(t.Context(), newToken, sessionTTL, sessionAbsoluteTTL); err != nil {
		t.Fatal("replacement cookie invalid", err)
	}
}

func TestConcurrentHTTPLoginAdmissionIntegration(t *testing.T) {
	st, pool := securityTestStore(t)
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret"}, store: st, logins: newLoginLimiter(st)}
	var done sync.WaitGroup
	start := make(chan struct{})
	for range 40 {
		done.Go(func() {
			<-start
			const seed = "test-login-csrf-seed"
			form := url.Values{"username": {"missing-target"}, "password": {"wrong-password"}, csrfFieldName: {s.signLoginCSRF(seed)}}
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode())).WithContext(t.Context())
			r.RemoteAddr = "192.0.2.99:54321"
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: loginCSRFCookie, Value: seed})
			w := httptest.NewRecorder()
			s.limitBody(s.csrf(http.HandlerFunc(s.handleLogin))).ServeHTTP(w, r)
			if w.Code != http.StatusSeeOther {
				t.Errorf("login status %d", w.Code)
			}
		})
	}
	close(start)
	done.Wait()
	var failures, blocked int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE action='login_failed'), count(*) FILTER (WHERE action='login_blocked') FROM audit_log`).Scan(&failures, &blocked); err != nil {
		t.Fatal(err)
	}
	if failures != loginMaxFails || blocked != 40-loginMaxFails {
		t.Fatalf("failures=%d blocked=%d, want %d/%d", failures, blocked, loginMaxFails, 40-loginMaxFails)
	}
}

func TestCredentialAdmissionFailsClosed(t *testing.T) {
	pool, err := sql.Open("mock_account", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store.New(pool, "test-key")}
	w := httptest.NewRecorder()
	allowed, failed := s.admitVerification(w, httptest.NewRequest(http.MethodPost, "/login", nil), "test", 5, time.Minute)
	if allowed || !failed || w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admission allowed=%v failed=%v status=%d", allowed, failed, w.Code)
	}
}
