//go:build integration

package server

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestMutationHandlersAcceptValidFormsIntegration keeps valid CSRF-header
// forms working while malformed bodies are rejected before these mutations.
func TestMutationHandlersAcceptValidFormsIntegration(t *testing.T) {
	e := newItEnv(t)
	var createdUsers []string
	t.Cleanup(func() {
		for _, username := range createdUsers {
			e.purge(username)
		}
	})
	client := *e.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	token := e.csrf("/")
	for _, encoding := range []string{"urlencoded", "multipart"} {
		t.Run(encoding, func(t *testing.T) {
			post := func(t *testing.T, path, back string) {
				t.Helper()
				fields := url.Values{"year_id": {itoa64(e.yearID64)}}
				var body io.Reader = strings.NewReader(fields.Encode())
				contentType := "application/x-www-form-urlencoded"
				if encoding == "multipart" {
					var buffer bytes.Buffer
					writer := multipart.NewWriter(&buffer)
					if err := writer.WriteField("year_id", fields.Get("year_id")); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					body, contentType = &buffer, writer.FormDataContentType()
				}
				req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, body)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", contentType)
				req.Header.Set(csrfHeaderName, token)
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != back {
					t.Fatalf("POST %s = %d %q, want 303 %q", path, resp.StatusCode, resp.Header.Get("Location"), back)
				}
			}
			for _, operation := range []string{"reset-2fa", "delete"} {
				t.Run(operation, func(t *testing.T) {
					username := e.uname + "_" + encoding + "_" + operation
					uid, err := e.st.CreateUser(e.ctx, username, "Fixture-password-123!", models.RoleEditor)
					if err != nil {
						t.Fatal(err)
					}
					createdUsers = append(createdUsers, username)
					if err := e.st.SetTotp(e.ctx, uid, true, "JBSWY3DPEHPK3PXP"); err != nil {
						t.Fatal(err)
					}
					if _, err := e.st.CreateSession(e.ctx, uid, time.Hour, "test", "192.0.2.1"); err != nil {
						t.Fatal(err)
					}
					post(t, "/admin/users/"+itoa64(uid)+"/"+operation, "/admin/users")
					user, err := e.st.GetUser(e.ctx, uid)
					if err != nil {
						t.Fatal(err)
					}
					if user.TotpEnabled || user.Disabled != (operation == "delete") {
						t.Fatalf("state after %s: TOTP=%v disabled=%v", operation, user.TotpEnabled, user.Disabled)
					}
					sessions, err := e.st.ListSessionsForUser(e.ctx, uid)
					if err != nil || len(sessions) != 0 {
						t.Fatalf("sessions after %s: %d, err=%v", operation, len(sessions), err)
					}
				})
			}
			t.Run("delete accounting profile", func(t *testing.T) {
				id, err := e.st.SaveAccountingExportProfile(e.ctx, store.AccountingExportProfile{Name: e.uname + "_" + encoding})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = e.st.DeleteAccountingExportProfile(e.ctx, id) })
				post(t, "/rechnungsjournal/profile/"+itoa64(id)+"/delete", "/rechnungsjournal?year="+itoa64(e.yearID64))
				if _, err := e.st.GetAccountingExportProfile(e.ctx, id); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("profile after delete: %v, want not found", err)
				}
			})
		})
	}
}
