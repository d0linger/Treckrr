package server

import (
	"fmt"
	"net/url"
	"regexp"
)

// Festschreibung is bound to the previewed content (WEB-07): the confirm page
// and the batch preview carry the content hash, and a POST without the matching
// hash issues nothing. These helpers drive the real flow — render the preview,
// take its hash(es), post — so tests that only need "an issued invoice" go
// through exactly what a browser does.

var (
	contentHashRe = regexp.MustCompile(`name="content_hash" value="([0-9a-f]*)"`)
	batchHashRe   = regexp.MustCompile(`name="(hash_[0-9]+)" value="([0-9a-f]*)"`)
)

// postIssue posts the single-invoice Festschreibung for neighbor nid with the
// content hash the confirm page shows. form must carry year_id; extra fields
// (issued_on) pass through. A confirm page without a form (incomplete § 11
// data, already issued) contributes no hash, like a browser would.
func (e *itEnv) postIssue(nid int64, form url.Values) string {
	e.t.Helper()
	page := e.get(fmt.Sprintf("/neighbors/%d/invoice/confirm?year=%s", nid, form.Get("year_id")))
	if m := contentHashRe.FindStringSubmatch(page); m != nil {
		form.Set("content_hash", m[1])
	}
	return e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), form)
}

// postIssueAll posts the Sammel-Festschreibung with every per-neighbor hash the
// batch preview renders; the neighbor_id selection is the caller's.
func (e *itEnv) postIssueAll(yid int64, form url.Values) string {
	e.t.Helper()
	page := e.get(fmt.Sprintf("/years/%d/issue-all", yid))
	for _, m := range batchHashRe.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], m[2])
	}
	return e.post(fmt.Sprintf("/years/%d/issue-all", yid), form)
}
