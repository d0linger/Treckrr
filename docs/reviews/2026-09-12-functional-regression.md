# Functional regression validation — 2026-09-12

## Outcome and scope

The UI changes shown in the comparison were already implemented in `6583b27` and `01aa243`. This follow-up validates that implementation and adds lasting regression coverage. No application behavior, database migration, production configuration, or visual identity was changed in this follow-up.

The final verification passed. Two test-infrastructure defects were corrected: order-dependent metrics state and database-name overrides defeating scratch-database isolation. No additional production-functionality regression was confirmed by the checks below.

This report supplements the [44-state page-by-page UI review](2026-09-11-page-by-page-ui.md). It does not claim that automated tests prove every possible input, browser, hardware authenticator, or external service combination.

## Environment and data safety

- Source: `dev`, based on `01aa243`, plus the test changes in this validation commit.
- Go: 1.27.1; tests used `-race`, `-count=1`, and `-shuffle=on`.
- Separate disposable PostgreSQL 16.15 and 18.6 servers, with matching `pg_dump` and `pg_restore` clients (16.15 and 18.6).
- Database packages serialized with `-p 1`; the two suite processes used different database servers.
- Browser tests: Playwright 1.62.1 / Chromium, one worker, no retries. The application used its own third disposable database seeded from the CI fixture.
- Browser writes targeted `http://localhost:18081`, never the ordinary development app on port 8080 or production. Synthetic databases used temporary in-memory storage.
- The final Go suites ran against an unchanged source tree after all Go test edits were finished.

## Final verification results

| Check | Result |
|---|---|
| Unit-only race/shuffle run | 182 top-level tests passed; 435 passing test/subtest events; 15 packages passed |
| PostgreSQL 16 full unit/integration race/shuffle run | 290 top-level tests passed; 619 passing test/subtest events; 15 packages passed |
| PostgreSQL 18 full unit/integration race/shuffle run | 290 top-level tests passed; 619 passing test/subtest events; 15 packages passed |
| Combined statement coverage | 62.3% on both PostgreSQL versions; existing CI floor remains 50% |
| Metrics order/race stress | 50 repetitions with the original failing shuffle seed, plus 50 with another shuffled order; both passed |
| Full browser suite | 32/32 passed together on a fresh fixture, without retries |
| Accessibility | Existing scans of 35 authenticated routes in light/dark mode, plus login/offline/error checks, passed their serious/critical Axe gates |
| Formatting and whitespace | `gofmt -l` on all changed Go files and `git diff --check`: clean |
| Dependency integrity | `go mod verify`: all modules verified |
| Build and vet | `go build ./...` and `go vet -tags=integration ./...`: passed |
| golangci-lint v2.13.1 | Integration-tagged scan: 0 issues |
| Deadcode v0.49.0 | Integration-tagged, test-aware scan: no findings |
| Gitleaks v8.24.3 | Affected four-commit history (`6e9ee7d^..01aa243`) and staged follow-up changes: no leaks found |
| Govulncheck v1.6.0 | 0 reachable vulnerabilities and 0 vulnerable imported packages; one unimported OpenPGP module advisory, explained below |

Counts include parent tests and their subtests as separate events; they are not counts of independent features. Unit-only runs intentionally skip environment-gated database tests. The full integration runs did not skip database or PostgreSQL backup tests.

## Confirmed defects and fixes

### Metrics tests leaked process-global state

With shuffle seed `1789225320172260569`, the concurrent metrics test ran before the cumulative-histogram test. It left 800 observations behind, so the next test observed counts of 803/805 instead of 3/5. Repeating the suite could also accumulate counter values.

`internal/metrics/metrics_test.go` now snapshots, resets, and restores the registry for every test. A new nested regression checks restoration of counter labels, histogram buckets, counts, and sum. These tests remain sequential because the production registry is intentionally process-global. Production metrics code was not changed.

### Scratch database helpers retained `dbname` overrides

`scratchStore` and `securityTestStore` replaced the path in the configured PostgreSQL URL but retained the `dbname` query parameter. That override could defeat the intended scratch-database selection.

Both helpers now remove `dbname` before deriving the administrative and scratch connection URLs. New integration regressions exercise an existing parent-database override and a verified nonexistent-database override. They query `current_database()` and assert that each helper actually connects to its own scratch database, not the parent. Both regressions passed on PostgreSQL 16 and 18.

## Added regression coverage

Seven new Go test functions, expanded error-response cases, and five new browser scenarios cover:

- Profile session lists with 0, 1, 5, and 6 sessions; disclosure only above the threshold; current-session protection; preserved per-session revoke tokens/actions; global actions outside the disclosure.
- Reminder stages 0/1/2; escaped sender data, recipient details, amount/fees/date branches, and correctly targeted PDF, QR, and optional email actions. Existing browser visibility assertions remain essential for detecting CSS-hidden document content.
- Branded browser 404 responses versus unchanged compact API/image/POST/HEAD responses, including representative malformed-record handlers and preservation of `Cache-Control: no-store`.
- Whitespace-only 400 messages and escaping of untrusted markup in error messages.
- Keyboard expansion of the mobile session disclosure, keyboard activation of a selected revoke action, actual invalidation of that auxiliary session, and preservation of another session and the current login.
- Native labelled photo selection and clearing without uploading a file. Actual image decoding, validation, storage, and visibility remain covered by server/store tests.
- Chart-link minimum height, horizontal overflow, focus, and keyboard navigation at 320 px and 390 px in both themes.
- Login, profile disclosure, and navigation without JavaScript.

Existing full-suite workflows also passed: booking creation; invoice freezing and display; reminder rendering; payment/history updates; invoice reversal; bulk booking cancellation; closed-year protection; CSV exports; offline queue preservation, correction, retry and deduplication; transient 401/403/500 recovery; CSRF/authentication/authorization; accounting concurrency; and backup/restore coordination.

## Findings during test development

- The first unit run exposed the genuine metrics ordering defect described above.
- Initial integration runs overlapped test-file edits; Go had discovered imports before those files changed, causing `internal/web` build errors. The later complete, unchanged-tree runs passed and are the authoritative results above.
- The first browser launch was blocked by Windows sandbox permissions before execution. The approved rerun targeted only the isolated test instance.
- New photo/chart checks were initially run after write-based specs had deliberately voided all bookings and closed the year. These checks now run with the populated CI fixture before those destructive test scenarios.
- The new session test initially matched both profile and drawer logout buttons during cleanup. Its selector now explicitly targets the profile's `main` region, and auxiliary session names are unique per run. The corrected complete 32-test suite passed.
- One fixture seed was attempted while application migrations were still starting and failed on its first statement. Waiting for application health before seeding resolved it; the final suite used a fresh complete fixture.

## Limits and non-blocking findings

- `TestVerifyS3ObjectRejectsBadIntegration` was the sole skipped test in each full integration run because `TEST_S3_ENDPOINT` was not configured. Real off-host S3 storage was not contacted or altered. PostgreSQL backup rehearsal, key rotation, preservation of unreadable dumps, and session-safe restoration did execute and pass.
- Govulncheck's module-only advisory is [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932), concerning the unmaintained `golang.org/x/crypto/openpgp` packages. Treckrr's scan reports no affected imported package or reachable symbol; source search found no OpenPGP imports. No unrelated dependency replacement was made to suppress this advisory.
- External SMTP delivery, actual S3 service behavior, real passkey hardware, other browser engines, and production deployment were not manually exercised. Automated transport/error and authentication tests do not replace those environment-specific checks.
- Mixed historical integration build-tag conventions remain unchanged; always set `TEST_DATABASE_URL` and use the integration tag for the full gate. A unit-only green run is not a release-equivalent database check.
- Impeccable's existing design sidecar is stale relative to `DESIGN.md`; it can be refreshed separately with `/impeccable document`. No design-contract or application-UI change required that refresh here.
- Graphify could not refresh its notes and its supporting query did not complete; direct source inspection and executable tests supplied the evidence. No graph artifacts are committed.

## Reproduction and artifacts

Core verification, with a disposable matching PostgreSQL service and clients:

```sh
go test -race -count=1 -shuffle=on ./...
go test -race -tags=integration -p 1 -count=1 -shuffle=on -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go test -race -count=50 -shuffle=1789225320172260569 ./internal/metrics
go test -race -count=50 -shuffle=on ./internal/metrics
go vet -tags=integration ./...
```

Run Playwright against a fresh CI-seeded application, never an ordinary business-data database. The browser suite intentionally writes, issues/reverses documents, and closes a billing year.

Machine-local raw JSON logs, coverage profiles, and browser failure/confirmation artifacts are preserved in the gitignored `tmp/regression-20260912/results/` directory. Earlier before/after screenshots remain in `tmp/ui-comparison-20260912/view/`. Temporary test services are removed after verification; normal development and production services are unchanged. This work is committed on `dev` only; no push or branch synchronization is performed.
