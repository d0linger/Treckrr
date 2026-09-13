# Booking review fixes — 2026-09-13

Scope: verify the supplied review data against the current source and fix only
the three confirmed inline findings and three confirmed deadcode findings.

## Findings and changes

| Finding | Verified result and minimal correction |
| --- | --- |
| Unit-list assertion depends on database collation | Sort the returned units in Go, then compare exact membership and cardinality with `Pauschale` and `h`; retain a separate database-error check. SQL ordering is unchanged. |
| Explicit outgoing bookings silently replace invalid dates with today | Validate the date before the legacy resolver when `booking_kind` is nonempty. Existing incoming/labor validation and legacy requests' fallback remain unchanged. |
| Quick-entry submit remains enabled after invoice finalization | Apply `.HasInvoice` to `Zeilen speichern`. The cited line belonged to the normal submit button, which was already guarded. The backend invoice guard remains authoritative and unchanged. |
| `Store.FilterEntries` is unreachable | Remove the superseded query; preserve shared filter types, SQL builders and bulk-action code used by unified bookings. |
| `Store.EntryUnitsInYear` is unreachable | Remove it; the current list uses `BookingUnitsInYear`. |
| `scanEntryWithName` is unreachable | Remove its unused joined-name path and the orphaned `entryColsE`. Preserve the entry scanner's column order, nullable links and request fingerprint. |

No finding required a broader redesign, schema migration or dependency change.
An independent read-only review found no further actionable issues.

## Validation

- Reproduced the exact three CI deadcode findings before removal with Go 1.27.1,
  `GOTOOLCHAIN=local` and `deadcode@v0.49.0`.
- Demonstrated failing date and template regression tests before applying fixes.
- Full untagged unit/race run: 204 top-level tests passed (556 including subtests);
  89 top-level environment-dependent tests skipped (94 including subtests).
- Targeted regression tests passed on PostgreSQL 16 (`en_US.utf8`) and PostgreSQL
  18 (`C`): mixed booking list/filter/export, create/edit, invalid-date rejection
  without persistence, offline HTTP 422, legacy dates and template guards.
- Full PostgreSQL 16 run with integration tags, race detection and shuffled test
  order: 318 top-level tests passed (763 including subtests), no failures.
  `TestVerifyS3ObjectRejectsBadIntegration` was skipped because no test S3 endpoint
  was configured. Statement coverage: 63.0%, above the CI floor of 50.0%.
- Full Chromium suite: 76 passed, including invoice finalization, both booking
  directions, edit/copy flows, offline behavior and accessibility checks.
- `golangci-lint v2.13.1 run --build-tags=integration`: 0 issues.
- `go vet -tags=integration ./...`: passed.
- Docker application build passed. Gitleaks 8.24.3 found no secrets in the patch.
- Final `deadcode -test -tags=integration ./...`: no dead code found.

Detailed local logs and test harnesses are under the ignored directory
`tmp/review-fixes-20260913/`.

## Runtime impact and limits

No production service or data was accessed or changed. Tests used synthetic,
isolated databases; only the disposable browser fixture on port 18085 was reset.
The normal local app on port 8080 was not replaced. No push or branch sync.

After deployment, explicit unified requests with invalid dates are rejected rather
than booked for today; valid dates and legacy fallback behavior are preserved.
Finalized invoices disable both booking submit controls. Existing stored amounts
and dates are not rewritten. These checks do not constitute a production smoke test.
No external CodeRabbit CLI review or code upload was performed.
