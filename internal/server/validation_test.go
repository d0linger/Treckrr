package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLenError(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value string
		max   int
		want  string
	}{
		{"under limit", "Name", "abc", maxNameLen, ""},
		{"at limit", "Name", strings.Repeat("a", 100), maxNameLen, ""},
		{"one over limit", "Name", strings.Repeat("a", 101), maxNameLen, "Name darf höchstens 100 Zeichen lang sein."},
		{"note at limit", "Notiz", strings.Repeat("x", 500), maxNoteLen, ""},
		{"note one over limit", "Notiz", strings.Repeat("x", 501), maxNoteLen, "Notiz darf höchstens 500 Zeichen lang sein."},
		// Counts code points, not bytes: 100 × "ä" is 200 bytes but 100 runes.
		{"multibyte within limit", "Name", strings.Repeat("ä", 100), maxNameLen, ""},
		{"multibyte over limit", "Name", strings.Repeat("ä", 101), maxNameLen, "Name darf höchstens 100 Zeichen lang sein."},
		{"empty value", "Beschreibung", "", maxNoteLen, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lenError(c.field, c.value, c.max); got != c.want {
				t.Errorf("lenError(%q, runes=%d, max=%d) = %q, want %q",
					c.field, utf8.RuneCountInString(c.value), c.max, got, c.want)
			}
		})
	}
}

func TestFormInt64ListAndMachineIDs_Bounds(t *testing.T) {
	// 1. Normal parsing
	form := url.Values{}
	form.Add("ids", " 10 ")
	form.Add("ids", "20")
	form.Add("machine_ids", "100")

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm failed: %v", err)
	}

	ids := formInt64List(req, "ids")
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 20 {
		t.Errorf("formInt64List = %v, want [10 20]", ids)
	}

	mIDs := formMachineIDs(req)
	if len(mIDs) != 1 || mIDs[0] != 100 {
		t.Errorf("formMachineIDs = %v, want [100]", mIDs)
	}

	// 2. Oversized item string length check (> maxDecimalLen)
	longForm := url.Values{}
	longForm.Add("ids", strings.Repeat("1", 33)) // 33 chars > maxDecimalLen (32)
	longForm.Add("ids", "30")

	reqLong := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(longForm.Encode()))
	reqLong.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = reqLong.ParseForm()

	idsLong := formInt64List(reqLong, "ids")
	if len(idsLong) != 1 || idsLong[0] != 30 {
		t.Errorf("formInt64List with long item = %v, want [30]", idsLong)
	}

	// 3. Slice count limit check (> maxFormListLen)
	manyForm := url.Values{}
	for i := 0; i < 150; i++ {
		manyForm.Add("ids", "1")
	}

	reqMany := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(manyForm.Encode()))
	reqMany.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = reqMany.ParseForm()

	idsMany := formInt64List(reqMany, "ids")
	if len(idsMany) != maxFormListLen {
		t.Errorf("formInt64List count = %d, want capped at maxFormListLen (%d)", len(idsMany), maxFormListLen)
	}
}

func TestEntryPrecheckTaskLabelSanitization(t *testing.T) {
	s := testNeighborServer(t)

	longTask := strings.Repeat("x", 200)
	params := url.Values{}
	params.Set("hours", "5")
	params.Set("neighbor_id", "1")
	params.Set("year_id", "1")
	params.Set("entry_date", "2026-03-01")
	params.Set("task_label", longTask)

	req := httptest.NewRequest(http.MethodGet, "/api/entries/precheck?"+params.Encode(), nil)
	rr := httptest.NewRecorder()

	s.handleEntryPrecheck(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status OK, got %v", rr.Code)
	}
	// The mock store answers SimilarEntryExists with true and captures the task arg,
	// so we can assert the endpoint sanitized it BEFORE the store call: the captured
	// value must be truncated to exactly maxNameLen runes, not the raw 200. (The old
	// assertion — Contains(body, `"warn":`) — was vacuous: every precheck response has
	// a warn field, so it passed even when task_label was left un-truncated.)
	if got := utf8.RuneCountInString(capturedSimilarTask); got != maxNameLen {
		t.Errorf("store received task of %d runes, want %d (sanitizeQueryParam truncation)", got, maxNameLen)
	}
	if capturedSimilarTask != strings.Repeat("x", maxNameLen) {
		t.Errorf("store received unexpected task: %q", capturedSimilarTask)
	}
	// The raw over-length input must never be reflected back in the response.
	body := rr.Body.String()
	if strings.Contains(body, longTask) {
		t.Errorf("response leaks the un-truncated %d-rune task_label: %s", len(longTask), body)
	}
	var resp struct {
		Warn string `json:"warn"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (body: %s)", err, body)
	}
}

func TestSanitizeQueryParam(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		maxRunes int
		want     string
	}{
		{"empty string", "", 100, ""},
		{"short string", "  search term  ", 100, "search term"},
		{"exact length", strings.Repeat("a", 100), 100, strings.Repeat("a", 100)},
		{"over limit truncated", strings.Repeat("a", 150), 100, strings.Repeat("a", 100)},
		{"multibyte runes within limit", "  " + strings.Repeat("ä", 100) + "  ", 100, strings.Repeat("ä", 100)},
		{"multibyte runes truncated", strings.Repeat("ä", 150), 100, strings.Repeat("ä", 100)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeQueryParam(c.value, c.maxRunes)
			if got != c.want {
				t.Errorf("sanitizeQueryParam(%q, %d) = %q, want %q", c.value, c.maxRunes, got, c.want)
			}
			if utf8.RuneCountInString(got) > c.maxRunes {
				t.Errorf("sanitizeQueryParam(%q, %d) rune count = %d, want <= %d",
					c.value, c.maxRunes, utf8.RuneCountInString(got), c.maxRunes)
			}
		})
	}
}
