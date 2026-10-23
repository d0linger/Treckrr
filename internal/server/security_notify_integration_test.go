//go:build integration

package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// TestSecurityNotificationsIntegration signs the env's admin in from a new
// browser (one notification), again from the same browser (none), then changes
// the password: the account gets a notification and, with mail configured and
// an address on file, an outbox mail of kind "security".
func TestSecurityNotificationsIntegration(t *testing.T) {
	e := newItEnv(t)
	count := func(title string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM notifications n JOIN users u ON u.id=n.user_id
			WHERE u.username=$1 AND n.kind='security' AND n.title=$2`, e.uname, title).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
	signIn := func(ua string) {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar}
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/login", nil)
		req.Header.Set("User-Agent", ua)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		m := csrfRe.FindStringSubmatch(string(body))
		if m == nil {
			t.Fatal("login form without token")
		}
		form := url.Values{"username": {e.uname}, "password": {e.adminPass}, "csrf_token": {m[1]}}
		req, _ = http.NewRequest(http.MethodPost, e.srv.URL+"/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", ua)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if n := count("Anmeldung von neuem Gerät"); n != 0 {
		t.Fatalf("first device of an account counted as new: %d", n)
	}
	signIn(firefox)
	if n := count("Anmeldung von neuem Gerät"); n != 1 {
		t.Fatalf("new browser raised %d notifications, want 1", n)
	}
	signIn(firefox)
	if n := count("Anmeldung von neuem Gerät"); n != 1 {
		t.Fatalf("known browser raised another notification: %d", n)
	}

	if _, err := e.pool.ExecContext(e.ctx, `UPDATE users SET email='it-security@example.invalid' WHERE username=$1`, e.uname); err != nil {
		t.Fatal(err)
	}
	e.post("/account/password", url.Values{"current_password": {e.adminPass},
		"new_password": {"Neues-Passwort-456!"}, "new_password_confirm": {"Neues-Passwort-456!"}})
	if n := count("Passwort geändert"); n != 1 {
		t.Fatalf("password change raised %d notifications, want 1", n)
	}
	var mails int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM mail_outbox WHERE kind='security' AND recipient='it-security@example.invalid'`).Scan(&mails); err != nil || mails != 1 {
		t.Fatalf("security mails=%d err=%v, want 1", mails, err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM mail_outbox WHERE recipient='it-security@example.invalid'`); err != nil {
		t.Fatal(err)
	}
}
