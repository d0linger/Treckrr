package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackupWorkAdmission(t *testing.T) {
	s := New(Options{}, nil)
	ctx, release, err := s.AcquireWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, nestedRelease, err := s.AcquireWork(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nestedRelease()
	if _, _, err := s.AcquireWork(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("second admission: %v", err)
	}
	release()
	release() // release is idempotent
	_, secondRelease, err := s.AcquireWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer secondRelease()
	if _, _, err := s.AcquireWork(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("released context bypassed gate: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.AcquireWork(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
}

// TestS3LegacyPrefixRemainsReadable verifies normalized writes do not hide
// archives created by the former direct prefix-plus-name concatenation.
func TestS3LegacyPrefixRemainsReadable(t *testing.T) {
	const (
		name = "treckrr-2026-09-24-120000.000000000.dump.enc"
		body = "legacy-encrypted-backup"
	)
	var listed []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("location") {
			fmt.Fprint(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
			return
		}
		switch {
		case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/site-a/"+name):
			w.Header().Set("X-Amz-Error-Code", "NoSuchKey")
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("Last-Modified", "Thu, 24 Sep 2026 12:00:00 GMT")
			w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "site-a"+name):
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("Last-Modified", "Thu, 24 Sep 2026 12:00:00 GMT")
			w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
			fmt.Fprint(w, body)
		default:
			prefix := r.URL.Query().Get("prefix")
			listed = append(listed, prefix)
			fmt.Fprint(w, `<ListBucketResult><Name>test</Name><IsTruncated>false</IsTruncated>`)
			if prefix == "site-a" {
				fmt.Fprintf(w, `<Contents><Key>site-a%s</Key><Size>%d</Size><LastModified>2026-09-24T12:00:00Z</LastModified></Contents>`, name, len(body))
			}
			fmt.Fprint(w, `</ListBucketResult>`)
		}
	}))
	defer ts.Close()

	legacyPrefix := "site-a"
	s := New(Options{S3: S3Options{
		Endpoint: strings.TrimPrefix(ts.URL, "http://"), Bucket: "test",
		AccessKey: "test", SecretKey: "test", Prefix: "site-a/", LegacyPrefix: &legacyPrefix,
	}}, nil)
	files, err := s.S3List(t.Context())
	if err != nil || len(files) != 1 || files[0].Name != name {
		t.Fatalf("legacy listing: files=%+v err=%v", files, err)
	}
	data, err := s.S3Get(t.Context(), name)
	if err != nil || string(data) != body {
		t.Fatalf("legacy read: data=%q err=%v", data, err)
	}
	owned, err := s.s3ObjectOwned(t.Context(), name)
	if err != nil || owned {
		t.Fatalf("legacy object ownership: owned=%v err=%v", owned, err)
	}
	if strings.Join(listed, ",") != "site-a/,site-a" {
		t.Fatalf("listed prefixes = %v", listed)
	}
}

// TestS3ReadPrefixesPreservesExplicitBucketRoot verifies an explicitly
// configured empty legacy prefix remains distinct from no legacy namespace.
func TestS3ReadPrefixesPreservesExplicitBucketRoot(t *testing.T) {
	legacyPrefix := ""
	s := New(Options{S3: S3Options{Prefix: "site-a/", LegacyPrefix: &legacyPrefix}}, nil)
	if got := s.s3ReadPrefixes(); len(got) != 2 || got[0] != "site-a/" || got[1] != "" {
		t.Fatalf("explicit bucket-root prefixes = %q", got)
	}
	absent := New(Options{S3: S3Options{Prefix: "site-a/"}}, nil)
	if got := absent.s3ReadPrefixes(); len(got) != 1 || got[0] != "site-a/" {
		t.Fatalf("absent legacy prefixes = %q", got)
	}
}

func TestBackupMemoryBounds(t *testing.T) {
	s := New(Options{MaxBytes: 8}, nil)
	if data, err := s.readArchive(strings.NewReader("12345678")); err != nil || string(data) != "12345678" {
		t.Fatalf("at bound: %q %v", data, err)
	}
	if _, err := s.readArchive(strings.NewReader("123456789")); err == nil {
		t.Fatal("oversized read accepted")
	}
	var out bytes.Buffer
	w := limitedWriter{w: &out, remaining: 8}
	if _, err := w.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("9")); err == nil || out.Len() != 8 {
		t.Fatalf("oversized write: len=%d err=%v", out.Len(), err)
	}
}

func TestCLIConstructionPreservesOtherProcessStaging(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "treckrr-in-progress.dump.enc.staging")
	if err := os.WriteFile(staging, []byte("in progress"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = New(Options{Dir: dir, SkipStartupCleanup: true}, nil)
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("CLI constructor touched another process's staging file: %v", err)
	}
}

func TestRestoreListExcludesOnlyAuthenticationData(t *testing.T) {
	toc := "42; 0 10 TABLE DATA public sessions owner\n43; 0 11 TABLE DATA public webauthn_ceremonies owner\n44; 0 12 TABLE DATA public session_events owner\n45; 0 13 TABLE public sessions owner\n"
	got := string(excludeRestoredSessions([]byte(toc)))
	want := "; 42; 0 10 TABLE DATA public sessions owner\n; 43; 0 11 TABLE DATA public webauthn_ceremonies owner\n44; 0 12 TABLE DATA public session_events owner\n45; 0 13 TABLE public sessions owner\n"
	if got != want {
		t.Fatalf("filtered TOC=%q", got)
	}
}

// TestS3PruneOnlyOwnedOldArchives protects foreign and recent bucket objects.
func TestS3PruneOnlyOwnedOldArchives(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	var ownerID string
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	oldHTTP := time.Now().Add(-48 * time.Hour).UTC().Format(http.TimeFormat)
	recent := time.Now().UTC().Format(time.RFC3339)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "42")
			w.Header().Set("Last-Modified", oldHTTP)
			w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
			if !strings.Contains(r.URL.Path, "treckrr-0002.dump.enc") {
				w.Header().Set("X-Amz-Meta-Treckrr-Installation", ownerID)
			}
			w.WriteHeader(http.StatusOK)
		case r.URL.Query().Has("location"):
			fmt.Fprint(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
		default:
			fmt.Fprintf(w, `<ListBucketResult><Name>test</Name><IsTruncated>false</IsTruncated>
<Contents><Key>site-a/treckrr-9999.dump.enc</Key><Size>42</Size><LastModified>%s</LastModified></Contents>
<Contents><Key>site-a/treckrr-9998.dump.enc</Key><Size>42</Size><LastModified>%s</LastModified></Contents>
<Contents><Key>site-a/treckrr-0002.dump.enc</Key><Size>42</Size><LastModified>%s</LastModified></Contents>
<Contents><Key>site-a/treckrr-0001.dump.enc</Key><Size>42</Size><LastModified>%s</LastModified></Contents>
<Contents><Key>foreign.dump.enc</Key><Size>42</Size><LastModified>%s</LastModified></Contents>
<Contents><Key>nested/treckrr-0000.dump.enc</Key><Size>42</Size><LastModified>%s</LastModified></Contents>
</ListBucketResult>`, recent, recent, old, old, old, old)
		}
	}))
	defer ts.Close()
	s := New(Options{S3: S3Options{Endpoint: strings.TrimPrefix(ts.URL, "http://"), Bucket: "test", AccessKey: "test", SecretKey: "test", Prefix: "site-a/"}}, nil)
	ownerID = s.s3InstallationID()
	s.pruneS3(t.Context(), 1)
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "/test/site-a/treckrr-0001.dump.enc" {
		t.Fatalf("deleted=%v", deleted)
	}
}

// TestS3UploadIsConditionalAndTagged pins collision prevention and ownership metadata.
func TestS3UploadIsConditionalAndTagged(t *testing.T) {
	var ifNoneMatch, owner string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("location") {
			fmt.Fprint(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
			return
		}
		ifNoneMatch = r.Header.Get("If-None-Match")
		owner = r.Header.Get("X-Amz-Meta-Treckrr-Installation")
		w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := New(Options{S3: S3Options{
		Endpoint: strings.TrimPrefix(ts.URL, "http://"), Bucket: "test",
		AccessKey: "test", SecretKey: "test", Prefix: "site-a/",
	}}, nil)
	if err := s.uploadS3(t.Context(), "treckrr-test.dump.enc", []byte("encrypted")); err != nil {
		t.Fatal(err)
	}
	if ifNoneMatch != "*" {
		t.Fatalf("If-None-Match = %q, want *", ifNoneMatch)
	}
	if owner != s.s3InstallationID() {
		t.Fatalf("ownership metadata = %q, want %q", owner, s.s3InstallationID())
	}
}

// TestS3OversizedObjectsRejectedBeforeGet verifies size bounds precede body reads.
func TestS3OversizedObjectsRejectedBeforeGet(t *testing.T) {
	var reads atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("location") {
			fmt.Fprint(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
			return
		}
		w.Header().Set("Content-Length", "9")
		w.Header().Set("Last-Modified", "Thu, 10 Sep 2026 03:00:00 GMT")
		w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
		if r.Method == http.MethodGet {
			reads.Add(1)
			fmt.Fprint(w, "123456789")
		}
	}))
	defer ts.Close()
	s := New(Options{MaxBytes: 8, S3: S3Options{Endpoint: strings.TrimPrefix(ts.URL, "http://"), Bucket: "test", AccessKey: "test", SecretKey: "test", Prefix: "site-a/"}}, nil)
	if err := s.verifyS3Object(t.Context(), "treckrr-test.dump.enc", 9); err == nil {
		t.Fatal("oversized verify accepted")
	}
	if _, err := s.S3Get(t.Context(), "treckrr-test.dump.enc"); err == nil {
		t.Fatal("oversized download accepted")
	}
	if reads.Load() != 0 {
		t.Fatalf("body fetched %d times", reads.Load())
	}
}

func TestRehearsalStatusPersists(t *testing.T) {
	s := New(Options{StatusFile: filepath.Join(t.TempDir(), "status.json")}, nil)
	rep := Rehearsal{At: time.Now(), Tables: 12, Duration: time.Second}
	if err := s.recordRehearsal(rep); err != nil {
		t.Fatal(err)
	}
	st := s.readStatus()
	if !st.RestoreTested.Equal(rep.At) || !strings.Contains(st.RehearsalNote, "12 tables") {
		t.Fatalf("status=%+v", st)
	}
	s.opt.StatusFile = t.TempDir() // cannot replace a directory with a status file
	if err := s.recordRehearsal(rep); err == nil {
		t.Fatal("status failure hidden")
	}
}

func TestScratchDatabaseNamesAreUnique(t *testing.T) {
	_, a, err := scratchTarget("postgres://localhost/rehearsal", "postgres://localhost/live")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := scratchTarget("postgres://localhost/rehearsal", "postgres://localhost/live")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, "treckrr_rehearsal_") {
		t.Fatalf("unsafe scratch names %q %q", a, b)
	}
	target, err := url.Parse(withDatabase("postgres://localhost/old?dbname=existing", a))
	if err != nil || target.Path != "/"+a || target.Query().Get("dbname") != a {
		t.Fatalf("query dbname overrode scratch target: %v %v", target, err)
	}
}

func TestDBPasswordsExcludedFromArguments(t *testing.T) {
	t.Setenv("PGPASSWORD", "inherited")
	for _, tc := range []struct{ name, dsn, want string }{
		{"userinfo", "postgres://user:synthetic@localhost/test?sslmode=disable", "synthetic"},
		{"query", "postgres://user:ignored@localhost/test?password=synthetic&sslmode=disable", "synthetic"},
		{"keyword", "host=localhost dbname=test user=user password='synthetic secret' sslmode=disable", "synthetic secret"},
		{"escaped", `host=localhost password='synthetic\'secret' dbname=test`, "synthetic'secret"},
		{"duplicate", "host=localhost password=old password=synthetic dbname=test", "synthetic"},
		{"empty", "host=localhost password='' dbname=test", ""},
		{"inherited", "postgres://user@localhost/test", "inherited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean, env, err := dbURLEnv(tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(clean, "password") || strings.Contains(clean, "synthetic") || strings.Contains(clean, "ignored") {
				t.Fatalf("password in argv: %q", clean)
			}
			count := 0
			for _, e := range env {
				if strings.HasPrefix(e, "PGPASSWORD=") {
					count++
					if e != "PGPASSWORD="+tc.want {
						t.Fatalf("password precedence wrong for %s", tc.name)
					}
				}
			}
			if count != 1 {
				t.Fatalf("PGPASSWORD count=%d", count)
			}
		})
	}
	if _, _, err := dbURLEnv("password='synthetic"); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("invalid DSN error leaks credentials: %v", err)
	}
}
