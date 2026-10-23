package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/backup"
)

// multipartKeyRequest builds a multipart POST carrying only the "key" field.
func multipartKeyRequest(t *testing.T, path, key string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("key", key); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// TestOversizedBackupKeyRejected proves the backup-key length guard (PR #104):
// an over-100-char key is rejected before any Argon2id key derivation, on both
// the JSON validate path and the form restore path.
func TestOversizedBackupKeyRejected(t *testing.T) {
	s := testServer()
	// A non-empty key makes the service Enabled(); the value is an obvious,
	// low-entropy test string (not a secret).
	s.backup = backup.New(backup.Options{EncKey: "not-a-real-secret"}, nil)
	oversized := strings.Repeat("A", maxBackupInputLen+1)

	t.Run("validate (JSON) rejects oversized key", func(t *testing.T) {
		req := multipartKeyRequest(t, "/admin/backup/validate", oversized)
		req.Header.Set("Accept", "application/json")
		rr := httptest.NewRecorder()
		s.handleBackupValidate(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("json: %v", err)
		}
		if resp["ok"] != false {
			t.Errorf("expected ok=false, got %v", resp["ok"])
		}
		if msg, _ := resp["message"].(string); !strings.Contains(msg, "höchstens 100 Zeichen") {
			t.Errorf("expected length warning, got %q", msg)
		}
	})

	t.Run("restore rejects oversized key with a redirect", func(t *testing.T) {
		req := multipartKeyRequest(t, "/admin/backup/restore", oversized)
		rr := httptest.NewRecorder()
		s.handleBackupRestore(rr, req)
		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected 303 redirect, got %d", rr.Code)
		}
	})

	t.Run("a normal-length key passes the guard", func(t *testing.T) {
		// A short key clears the length check and proceeds (then fails later for a
		// missing file — a different error, proving the guard did not trip).
		req := multipartKeyRequest(t, "/admin/backup/validate", "short-key")
		req.Header.Set("Accept", "application/json")
		rr := httptest.NewRecorder()
		s.handleBackupValidate(rr, req)
		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("json: %v", err)
		}
		if msg, _ := resp["message"].(string); strings.Contains(msg, "höchstens 100 Zeichen") {
			t.Errorf("normal key must not trip the length guard, got %q", msg)
		}
	})
}

// TestRestoreRequiresExactInstanceKey lets a GUI restore past the key gate only
// with this instance's exact backup key; foreign, trimmed or padded variants
// get the refusal message.
func TestRestoreRequiresExactInstanceKey(t *testing.T) {
	const key = "test-backup-secret"
	for _, tc := range []struct {
		name, configured, supplied string
		wantRefusal                bool
	}{
		{name: "instance key", configured: key, supplied: key},
		{name: "foreign key", configured: key, supplied: "foreign-backup-secret", wantRefusal: true},
		{name: "padded instance key", configured: " " + key + " ", supplied: " " + key + " "},
		{name: "trimmed instance key", configured: " " + key + " ", supplied: key, wantRefusal: true},
		{name: "extra whitespace", configured: key, supplied: " " + key + " ", wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer()
			s.store = closedTestStore(t)
			s.backup = backup.New(backup.Options{EncKey: tc.configured, SkipStartupCleanup: true}, nil)
			req := multipartKeyRequest(t, "/admin/backup/restore", tc.supplied)
			rr := httptest.NewRecorder()
			s.handleBackupRestore(rr, req)
			if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/admin/backup" {
				t.Fatalf("expected backup redirect, got %d %q", rr.Code, rr.Header().Get("Location"))
			}
			want := "Bitte eine Backup-Datei (.dump.enc) wählen."
			if tc.wantRefusal {
				want = foreignKeyRestoreMsg
			}
			if got := flashText(t, s, rr); got != want {
				t.Fatalf("flash = %q, want %q", got, want)
			}
		})
	}
}

// TestBackupValidationAllowsForeignKey keeps validation open to an older or
// foreign key, in form and JSON responses, so an archive can be checked
// before a key rotation.
func TestBackupValidationAllowsForeignKey(t *testing.T) {
	for _, jsonResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "form", true: "JSON"}[jsonResponse], func(t *testing.T) {
			s := testServer()
			s.backup = backup.New(backup.Options{EncKey: "instance-backup-secret", SkipStartupCleanup: true}, nil)
			req := multipartKeyRequest(t, "/admin/backup/validate", "old-backup-secret")
			if jsonResponse {
				req.Header.Set("Accept", "application/json")
			}
			rr := httptest.NewRecorder()
			s.handleBackupValidate(rr, req)
			var msg string
			if jsonResponse {
				var resp struct {
					OK      bool   `json:"ok"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Fatal(err)
				}
				if rr.Code != http.StatusOK || resp.OK {
					t.Fatalf("expected a failed file validation response, got %d %+v", rr.Code, resp)
				}
				msg = resp.Message
			} else {
				if rr.Code != http.StatusSeeOther {
					t.Fatalf("status = %d, want 303", rr.Code)
				}
				msg = flashText(t, s, rr)
			}
			// File validation, rather than the instance-key gate, must be reached.
			if msg != "Bitte eine Backup-Datei (.dump.enc) wählen." {
				t.Fatalf("foreign validation key was rejected early: %q", msg)
			}
		})
	}
}
