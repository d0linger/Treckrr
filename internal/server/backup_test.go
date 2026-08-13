package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"treckrr/internal/backup"
)

// A future last_backup timestamp (clock skew or a bad write) must not read as
// "ok": the age is clamped to zero and the state flagged stale.
func TestReadBackupStatusFutureTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"last_backup":"` + future + `","ok":true,"size_bytes":1048576}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readBackupStatus(path)
	if st.State != "stale" {
		t.Errorf("state = %q, want stale for a future timestamp", st.State)
	}
	if st.AgeHours != 0 {
		t.Errorf("AgeHours = %d, want 0 (clamped) for a future timestamp", st.AgeHours)
	}
}

// A recent successful backup reads as ok with a sane age.
func TestReadBackupStatusOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	recent := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"last_backup":"` + recent + `","ok":true,"size_bytes":2097152}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st := readBackupStatus(path)
	if st.State != "ok" {
		t.Errorf("state = %q, want ok", st.State)
	}
	if st.AgeHours < 0 {
		t.Errorf("AgeHours = %d, want >= 0", st.AgeHours)
	}
}

func createMultipartRequest(urlStr, key string) (*http.Request, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	err := writer.WriteField("key", key)
	if err != nil {
		return nil, err
	}
	writer.Close()

	req, err := http.NewRequest(http.MethodPost, urlStr, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func TestOversizedBackupInputs(t *testing.T) {
	s := testServer()
	s.backup = backup.New(backup.Options{EncKey: "test-encryption-key-at-least-16-chars"}, nil)

	t.Run("handleBackupValidate with JSON accepts oversized key", func(t *testing.T) {
		oversizedKey := strings.Repeat("A", 101)
		req, err := createMultipartRequest("/admin/backup/validate", oversizedKey)
		if err != nil {
			t.Fatalf("failed to create multipart request: %v", err)
		}
		req.Header.Set("Accept", "application/json")

		rr := httptest.NewRecorder()
		s.handleBackupValidate(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %v", rr.Code)
		}

		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		if resp["ok"] != false {
			t.Errorf("expected ok to be false, got %v", resp["ok"])
		}

		msg, _ := resp["message"].(string)
		if !strings.Contains(msg, "höchstens 100 Zeichen") {
			t.Errorf("expected limit warning, got %q", msg)
		}
	})

	t.Run("backupUpload with oversized key", func(t *testing.T) {
		oversizedKey := strings.Repeat("B", 101)
		req, err := createMultipartRequest("/admin/backup/restore", oversizedKey)
		if err != nil {
			t.Fatalf("failed to create multipart request: %v", err)
		}

		rr := httptest.NewRecorder()
		s.handleBackupRestore(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected redirect 303, got %v", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if loc != "/admin/backup" {
			t.Errorf("expected redirect to /admin/backup, got %q", loc)
		}

		// check flash error message
		cookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(cookie, "h%C3%B6chstens+100+Zeichen") { // "höchstens 100 Zeichen" url encoded
			t.Errorf("expected limit warning in flash cookie, got %q", cookie)
		}
	})

	t.Run("handleBackupSettings with oversized volume_cron", func(t *testing.T) {
		form := url.Values{}
		form.Set("volume_cron", strings.Repeat("*", 101))
		form.Set("volume_keep", "7")
		form.Set("s3_cron", "0 4 * * *")
		form.Set("s3_keep", "0")

		req := httptest.NewRequest(http.MethodPost, "/admin/backup/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		rr := httptest.NewRecorder()
		s.handleBackupSettings(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected redirect 303, got %v", rr.Code)
		}

		loc := rr.Header().Get("Location")
		if loc != "/admin/backup" {
			t.Errorf("expected redirect to /admin/backup, got %q", loc)
		}

		cookie := rr.Header().Get("Set-Cookie")
		if !strings.Contains(cookie, "h%C3%B6chstens+100+Zeichen") {
			t.Errorf("expected limit warning in flash cookie, got %q", cookie)
		}
	})
}
