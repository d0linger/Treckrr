package backup

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFilenameIsUTCAndOrdersAcrossDST pins UTC names: the two 02:30 local runs
// of the autumn DST change get distinct names in real time order.
func TestFilenameIsUTCAndOrdersAcrossDST(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC).In(vienna)  // 02:30 CEST
	second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC).In(vienna) // 02:30 CET
	a, b := Filename(first), Filename(second)
	if a != "treckrr-2026-10-25-003000.000000000Z.dump.enc" || b <= a {
		t.Fatalf("names %q, %q", a, b)
	}
	if !validName(a) {
		t.Fatalf("new name rejected: %q", a)
	}
	for _, tc := range []struct {
		name string
		want time.Time
	}{
		{a, first},
		{"treckrr-2026-10-25-023000.000000000.dump.enc", time.Date(2026, 10, 25, 2, 30, 0, 0, vienna)},
		{"treckrr-2026-08-01-030000.dump.enc", time.Date(2026, 8, 1, 3, 0, 0, 0, vienna)},
	} {
		got, ok := archiveTimeIn(tc.name, vienna)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("archiveTimeIn(%q) = %v, %v; want %v", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := archiveTimeIn("treckrr-garbage.dump.enc", vienna); ok {
		t.Error("garbage name parsed")
	}
}

// TestSortNewestFirstUsesEncodedTime interleaves legacy local-time names and
// UTC names by real time, including where plain string order disagrees.
func TestSortNewestFirstUsesEncodedTime(t *testing.T) {
	base := time.Date(2026, 8, 1, 3, 0, 0, 0, time.Local)
	legacy := "treckrr-" + base.Format("2006-01-02-150405") + ".dump.enc"
	newer := Filename(base.Add(time.Nanosecond))
	older := Filename(base.Add(-time.Hour))
	files := []BackupFile{{Name: older}, {Name: legacy}, {Name: newer}}
	sortNewestFirst(files)
	got := []string{files[0].Name, files[1].Name, files[2].Name}
	want := []string{newer, legacy, older}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestPruneKeepsNewestByEncodedTime verifies retention ranks by encoded time.
func TestPruneKeepsNewestByEncodedTime(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 1, 3, 0, 0, 0, time.Local)
	legacy := "treckrr-" + base.Format("2006-01-02-150405") + ".dump.enc"
	newest := Filename(base.Add(time.Nanosecond))
	old := time.Now().Add(-72 * time.Hour)
	for _, name := range []string{legacy, newest} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	New(Options{Dir: dir}, nil).prune(1)
	if _, err := os.Stat(filepath.Join(dir, newest)); err != nil {
		t.Fatalf("newest recovery point removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, legacy)); !os.IsNotExist(err) {
		t.Fatalf("older legacy dump kept: %v", err)
	}
}

// TestCronDSTWarning flags schedules that fire in the 02:xx DST window.
func TestCronDSTWarning(t *testing.T) {
	for expr, want := range map[string]bool{
		"30 2 * * *":    true,
		"0 1-3 * * *":   true,
		"0 0,2,4 * * 1": true,
		"0 */2 * * *":   true,
		"0 3 * * *":     false,
		"0 */6 * * *":   false,
		"0 * * * *":     false,
		"":              false,
		"nonsense":      false,
	} {
		if got := CronDSTWarning(expr) != ""; got != want {
			t.Errorf("CronDSTWarning(%q) warned=%v, want %v", expr, got, want)
		}
	}
	if d := DescribeCron("30 2 * * *"); !strings.HasPrefix(d, "Täglich um 02:30 Uhr.") || !strings.Contains(d, "Sommerzeit") {
		t.Fatalf("description lacks the DST warning: %q", d)
	}
}

// TestBootDelay pins the injectable first-tick delay.
func TestBootDelay(t *testing.T) {
	if d := New(Options{FirstTickDelay: -1}, nil).bootDelay(); d != 0 {
		t.Fatalf("disabled delay = %v", d)
	}
	if d := New(Options{FirstTickDelay: time.Second}, nil).bootDelay(); d != time.Second {
		t.Fatalf("explicit delay = %v", d)
	}
	for i := 0; i < 20; i++ {
		if d := New(Options{}, nil).bootDelay(); d < minBootDelay || d >= maxBootDelay {
			t.Fatalf("default delay %v outside [%v, %v)", d, minBootDelay, maxBootDelay)
		}
	}
}

// TestRunGuardedRecordsPanic verifies a panicking run becomes a recorded
// failure instead of crashing the process.
func TestRunGuardedRecordsPanic(t *testing.T) {
	s := New(Options{StatusFile: filepath.Join(t.TempDir(), "status.json")}, nil)
	if err := s.updateStatus(func(st *Status) { st.OK = true }); err != nil {
		t.Fatal(err)
	}
	err := s.runGuarded("volume", func() error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err = %v", err)
	}
	if s.readStatus().OK {
		t.Fatal("panic not recorded as a failed backup")
	}
	if err := s.runGuarded("s3", func() error { panic("boom") }); err == nil {
		t.Fatal("s3 panic hidden")
	}
	if st := s.readStatus(); st.S3OK == nil || *st.S3OK {
		t.Fatalf("s3 panic not recorded: %+v", st.S3OK)
	}
}

// TestAttemptMarkerWithoutDatabase verifies a run holds back its own retry
// before it starts, so a crash is not retried immediately.
func TestAttemptMarkerWithoutDatabase(t *testing.T) {
	s := New(Options{}, nil)
	now := time.Now()
	if err := s.markSchedulerAttempt(t.Context(), "volume", now); err != nil {
		t.Fatal(err)
	}
	if err := s.markSchedulerAttempt(t.Context(), "s3", now); err != nil {
		t.Fatal(err)
	}
	if !s.volRetryAt.Equal(now.Add(statusRetryBackoff)) || !s.s3RetryAt.Equal(now.Add(statusRetryBackoff)) {
		t.Fatalf("retry clocks = %v / %v", s.volRetryAt, s.s3RetryAt)
	}
}

// TestEncryptFileRoundTrip checks the in-place file encryption produces the
// standard TRKBK2 layout and the hash used by verifyEncrypted.
func TestEncryptFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain")
	plain := []byte("pg_dump custom archive bytes")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Options{EncKey: "unit-test-key-at-least-16"}, nil)
	enc, sum, err := encryptFile(path, int64(len(plain)), s.secret)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decrypt(enc, s.secret)
	if err != nil || string(got) != string(plain) || sum != sha256.Sum256(plain) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if err := s.verifyEncrypted(enc, sum); err != nil {
		t.Fatal(err)
	}
	if err := s.verifyEncrypted(enc, sha256.Sum256([]byte("other"))); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	if _, _, err := encryptFile(path, int64(len(plain))-1, s.secret); err == nil {
		t.Fatal("archive that grew after sizing accepted")
	}
}

// TestStalePlainTempsRemoved verifies crash leftovers with plaintext database
// copies are deleted while a fresh (in-use) scratch file stays.
func TestStalePlainTempsRemoved(t *testing.T) {
	dir := t.TempDir()
	s := New(Options{Dir: dir}, nil)
	f, err := s.createScratch()
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	fi, err := os.Stat(f.Name())
	if err != nil || fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("scratch file mode: %v %v", fi, err)
	}
	stale := filepath.Join(dir, ".treckrr-plain-old.tmp")
	if err := os.WriteFile(stale, []byte("plaintext"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * plainTempMaxAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	s.removeStalePlainTemps(plainTempMaxAge)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale plaintext kept: %v", err)
	}
	if _, err := os.Stat(f.Name()); err != nil {
		t.Fatalf("fresh scratch removed: %v", err)
	}
	if files, _ := s.List(); len(files) != 0 {
		t.Fatalf("scratch files listed as backups: %+v", files)
	}
}

// TestStaleScratchClassification pins which rehearsal databases are leftovers.
func TestStaleScratchClassification(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	hex := strings.Repeat("ab", 16)
	fresh := scratchName(now.Add(-time.Minute), hex)
	old := scratchName(now.Add(-staleScratchAge-time.Minute), hex)
	if len(fresh) > 63 {
		t.Fatalf("scratch name exceeds the identifier limit: %d", len(fresh))
	}
	cases := []struct {
		name      string
		connected bool
		want      bool
	}{
		{fresh, false, false},
		{old, true, true},
		{"treckrr_rehearsal_" + hex, false, true},
		{"treckrr_rehearsal_" + hex, true, false},
		{"treckrr_rehearsal_notours", false, false},
		{"treckrr_test_ops", false, false},
	}
	for _, tc := range cases {
		if got := staleScratch(tc.name, tc.connected, now); got != tc.want {
			t.Errorf("staleScratch(%q, %v) = %v, want %v", tc.name, tc.connected, got, tc.want)
		}
	}
}

// fakeS3 serves the minimal S3 API the mirror needs from an in-memory map.
type fakeS3 struct {
	mu        sync.Mutex
	objects   map[string][]byte
	owners    map[string]string
	listErr   bool
	puts      int
	putStatus int
	prefixes  []string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Query().Has("location") {
		fmt.Fprint(w, `<LocationConstraint>us-east-1</LocationConstraint>`)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/test/")
	method := r.Method
	if key == "" || r.URL.Path == "/test" {
		method = "LIST"
	}
	switch method {
	case http.MethodPut:
		f.puts++
		if f.putStatus != 0 {
			w.WriteHeader(f.putStatus)
			fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code><Message>exists</Message></Error>`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		f.owners[key] = r.Header.Get("X-Amz-Meta-Treckrr-Installation")
		w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
	case http.MethodHead, http.MethodGet:
		body, ok := f.objects[key]
		if !ok {
			w.Header().Set("X-Amz-Error-Code", "NoSuchKey")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
		if owner := f.owners[key]; owner != "" {
			w.Header().Set("X-Amz-Meta-Treckrr-Installation", owner)
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	default:
		if f.listErr {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`)
			return
		}
		prefix := r.URL.Query().Get("prefix")
		f.prefixes = append(f.prefixes, prefix)
		fmt.Fprint(w, `<ListBucketResult><Name>test</Name><IsTruncated>false</IsTruncated>`)
		for k, v := range f.objects {
			if strings.HasPrefix(k, prefix) {
				fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><LastModified>%s</LastModified></Contents>`,
					k, len(v), time.Now().UTC().Format(time.RFC3339))
			}
		}
		fmt.Fprint(w, `</ListBucketResult>`)
	}
}

func newFakeS3Service(t *testing.T, f *fakeS3, opt Options) *Service {
	t.Helper()
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	opt.S3 = S3Options{Endpoint: strings.TrimPrefix(ts.URL, "http://"), Bucket: "test",
		AccessKey: "test", SecretKey: "test", Prefix: "site-a/"}
	return New(opt, nil)
}

// TestUploadPreconditionAcceptsIdenticalOwnedObject verifies a 412 on the
// conditional PUT is resolved by verifying the existing owned object.
func TestUploadPreconditionAcceptsIdenticalOwnedObject(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}, owners: map[string]string{}, putStatus: http.StatusPreconditionFailed}
	s := newFakeS3Service(t, f, Options{})
	name := "treckrr-2026-09-26-030000.000000000Z.dump.enc"
	data := []byte("verified encrypted archive")
	f.objects["site-a/"+name] = data
	f.owners["site-a/"+name] = s.s3InstallationID()
	if err := s.uploadVerifiedS3(t.Context(), name, data); err != nil {
		t.Fatalf("identical owned object rejected: %v", err)
	}
	f.objects["site-a/"+name] = []byte("different bytes of equal size!")[:len(data)]
	if err := s.uploadVerifiedS3(t.Context(), name, data); err == nil {
		t.Fatal("differing object accepted")
	}
	f.objects["site-a/"+name] = data
	f.owners["site-a/"+name] = "someone-else"
	if err := s.uploadVerifiedS3(t.Context(), name, data); err == nil {
		t.Fatal("foreign object accepted")
	}
}

// TestS3MirrorFailsOnListingError verifies a listing failure fails the run and
// is recorded, instead of re-uploading into a 412 and skipping retention.
func TestS3MirrorFailsOnListingError(t *testing.T) {
	dir := t.TempDir()
	name := Filename(time.Now())
	if err := os.WriteFile(filepath.Join(dir, name), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeS3{objects: map[string][]byte{}, owners: map[string]string{}, listErr: true}
	s := newFakeS3Service(t, f, Options{Dir: dir, StatusFile: filepath.Join(dir, "status.json")})
	err := s.runS3Mirror(t.Context(), 3)
	if err == nil || !strings.Contains(err.Error(), "s3 list") {
		t.Fatalf("err = %v", err)
	}
	if f.puts != 0 {
		t.Fatalf("uploaded %d times despite the failed listing", f.puts)
	}
	if st := s.readStatus(); st.S3OK == nil || *st.S3OK {
		t.Fatalf("listing failure not recorded: %+v", st)
	}
	if removed, failures := s.pruneS3(t.Context(), 1); removed != 0 || failures != 1 {
		t.Fatalf("prune on listing error: removed=%d failures=%d", removed, failures)
	}
}

// TestS3ListNarrowsEveryNamespace verifies both the normalized and the legacy
// namespace are listed only below their backup-name prefix, so siblings such
// as "site-ab/" never count against the listing cap.
func TestS3ListNarrowsEveryNamespace(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"site-a/treckrr-2026-09-25-030000.000000000Z.dump.enc":  []byte("a"),
		"site-atreckrr-2026-09-24-030000.000000000.dump.enc":    []byte("b"),
		"site-ab/treckrr-2026-09-26-030000.000000000Z.dump.enc": []byte("c"),
	}, owners: map[string]string{}}
	s := newFakeS3Service(t, f, Options{})
	legacy := "site-a"
	s.opt.S3.LegacyPrefix = &legacy
	files, err := s.S3List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "treckrr-2026-09-25-030000.000000000Z.dump.enc" {
		t.Fatalf("files = %+v", files)
	}
	if strings.Join(f.prefixes, ",") != "site-a/treckrr-,site-atreckrr-" {
		t.Fatalf("listed prefixes = %v", f.prefixes)
	}
}

// TestS3ClientIsReused verifies one client serves repeated calls and a
// settings change builds a new one.
func TestS3ClientIsReused(t *testing.T) {
	s := New(Options{S3: S3Options{
		Endpoint: "127.0.0.1:9", Bucket: "b", Prefix: "p/",
		AccessKey: "access", SecretKey: "secret-a",
	}}, nil)
	a, err := s.s3Client()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.s3Client()
	if err != nil || a != b {
		t.Fatalf("client rebuilt: %p %p %v", a, b, err)
	}
	s.opt.S3.SecretKey = "secret-b"
	c, err := s.s3Client()
	if err != nil || c == a {
		t.Fatalf("credential change reused the old client: %v", err)
	}
	s.opt.S3.Endpoint = "127.0.0.1:10"
	d, err := s.s3Client()
	if err != nil || d == c {
		t.Fatalf("endpoint change reused the old client: %v", err)
	}
}

// TestRecordS3ResultJoinsErrors keeps the run error visible.
func TestRecordS3ResultJoinsErrors(t *testing.T) {
	s := New(Options{StatusFile: filepath.Join(t.TempDir(), "status.json")}, nil)
	want := errors.New("upload failed")
	if err := s.recordS3Result(want); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if err := s.recordS3Result(nil); err != nil {
		t.Fatal(err)
	}
	if st := s.readStatus(); st.S3OK == nil || !*st.S3OK || st.LastS3.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}
