# Unified bookings — implementation and validation

## Scope

The approved compact concept is implemented in the real neighbor page, not only
in the before/after prototype. The existing Treckrr design system is retained.
Impeccable's progressive-disclosure and native-control guidance shaped the form:
one direction selector, one service selector, optional helper/reference details,
and an itemized settlement preview. No third-party UI runtime was added.

### Where to find it

- Overview → **Buchungen & Filter**, or Menu → **Buchungen & Filter** (`/buchungen`).
- Neighbor → **Neue Buchung erfassen**: select who charges whom and the service.
- The booking list is a page, not a separate modal. Dates, units and voided status
  are under **Weitere Filter**; direction and service type are directly visible.

## Behavior and dependency decisions

| Service | I charge the neighbor | Neighbor charges me |
| --- | --- | --- |
| Tractor / rig / vehicle | Shared price-basis catalog and calculated rate | Same catalog and calculated rate |
| Labor | Person master data with optional agreed-rate override | Named external person and agreed rate |
| Quantity | Quantity × unit price | Quantity × agreed unit price |
| Free position / costs | Positive account posting | Negative account posting |

- All quantities and prices are entered positively. The server determines the
  account sign and rounds each service line independently before summing.
- Optional equipment helpers have their own rate and, if specified, independent
  hours. Blank helper hours means the equipment hours.
- Tractors, machines and Gespanne form one price-basis pool for both directions.
  The booking direction controls the account sign, not equipment ownership.
  Incoming bookings snapshot the selected catalog IDs, label and calculated rate but
  do not contribute to own utilization, turnover or invoice lines.
- Own equipment/labor/quantity services stay in `entries`. Incoming services and
  free positions stay in `neighbor_ledger`, with structured service metadata.
  They do not become negative own turnover, invoice lines, or machine utilization.
- Free positions retain the existing settlement-posting semantics; they do not
  automatically become outgoing VAT-bearing invoice lines. Payments remain a
  separate workflow. The preview's remaining balance does not include unissued VAT.
- Migration `0054` adds nullable metadata/retry fields and a partial unique index;
  it does not rewrite existing bookings, balances or invoice snapshots.
- Account membership, completed-year and issued-invoice guards remain enforced
  server-side. Structured ledger edits cannot change kind or direction.
- Existing ledger/manual and quick-entry endpoints remain compatible. One native
  `/entries` POST handles new unified submissions; no fourth submit handler was added.
- Offline requests retain repeated machine IDs. Canonical request fingerprints
  and cross-table locking prevent changed retries from silently becoming a
  different booking. Historical callers without the new fields remain supported.
- Per-type/direction drafts cannot submit hidden fields or leak incoming prices
  into own bookings. Only own equipment/quantity preferences persist between visits.
- Structured counterclaim edit/copy retains catalog equipment and rates, quantity,
  free-text rates, independent helper hours and reference text. Own labor retains
  person attribution.
- Editing linked hours is explicit opt-in. Copying an own machine row still copies
  that row only, not its separate helper; the copy form now explains this behavior.
- Recurring own machine/helper snapshots retain independent helper hours. Old
  snapshots without that field retain their existing same-hours behavior.
- Unified list/filter/export uses both record families with explicit source IDs.
  Ledger rows cannot reach entry bulk actions or collide with entry photo links.
  CSV exports all filtered matches, including exact helper quantities/rates, not
  just the current page. Legacy yearly/neighbor exports keep their existing format.
- Privacy export includes structured metadata. Anonymization removes its personal
  text and new request fingerprints without rewriting financial amounts.

## Validation record

All mutating tests use new disposable PostgreSQL 16 fixtures under Compose project
`treckrr-unified-bookings`. Real/local business records were not used as test data.

Focused validation completed before the final regression run:

- Unit and race checks for models, store, server and rendered templates.
- Real PostgreSQL tests: metadata round-trip, exact calculations, 12 concurrent
  retries, changed/cross-table retry conflicts, invoice/year locks, void/restore,
  immutable edits, privacy erasure, and existing paired/recurring booking regressions.
- Unified list integration: 15 filter subcases, stable mixed-source pagination,
  512-record export boundary, literal search, CSV formula protection and 17-column
  export with four-decimal helper prices.
- 21 offline browser tests and mocked form tests, including all eight branches,
  native no-JS submit, failed pricing lookup, draft isolation and safe label rendering.
- 12 distinct real booking GUI cases: all eight service/direction combinations,
  independent own helper, incoming edit/copy, own labor attribution edit/copy,
  and outgoing invoice/own-statistics separation.
- Real dashboard → filter → CSV workflow, including empty results and preserved
  sort/filter state.
- First visual/a11y batch: 36 desktop/mobile/dark states, zero JavaScript errors,
  horizontal overflows or axe findings. Component screenshots hide sticky chrome
  only during cropping; accessibility/layout checks run against the normal page.

Confirmed review issues were fixed rather than suppressed: lost repeated offline
machine IDs, incoming preference leakage, stripped-kind replay bypass, rounded
document rates, incompatible precision during explicit linked-hour sync, and
selected machines becoming unsuccessful controls when hidden by search.

## Final results

- Full Go run: **313 top-level tests / 733 successful test and subtest events**,
  zero failures, all 15 packages successful, with race detection, shuffled order
  and real PostgreSQL integration. One optional S3 verification test was skipped
  because no S3-compatible test service was configured.
- Final-source confirmation after the last precision fix: **19 top-level tests /
  100 test and subtest events**, zero failures or skips. Includes the direct-store
  precision guard, unchanged-on-rejection HTTP behavior, exact document rates,
  retry conflicts, and the complete mixed-source filter/CSV test.
- Coverage from the full run: calculation 100%, PDF 90%, server 64.1%, store 57.4%,
  web/template helpers 77.5%. Coverage is not a claim that every possible state was tested.
- `go vet -tags=integration ./...`: passed. Golangci-lint v2.13.1: **0 issues**.
  The initial cold package-loading attempt exceeded five minutes; the completed
  run used the shared build cache and a 15-minute analysis timeout.
- Final staged-diff Gitleaks v8.24.3 scan: **no leaks found**, approximately 261 KB.
- Final GUI audit: **36/36 states passed**, zero JavaScript errors, horizontal
  overflows or axe violations. Desktop, 390px mobile and dark mode screenshots
  were inspected; the broader browser suite also checks 320px touch targets.
- Browser run first completed 72/73 cases; its sole failure was an outdated test
  locator for the machine-picker status sibling. The corrected case passed
  separately. The clean final run on a freshly seeded database passed
  **73/73 tests in 4.4 minutes**, including the full light/dark authenticated-page
  accessibility sweep and all new booking/filter/offline acceptance cases.
- Local handoff: `docker compose up -d --no-deps --build app` completed. Only the
  application service was recreated, not the normal database. Boot reported
  migrations applied and listening; container health is healthy, `/healthz` and
  the booking script return HTTP 200, and the script matches the tested 18086
  artifact byte-for-byte as HTTP text. No production deployment or push was made.

## Reproduction

- `go test -race -tags=integration -p 1 -count=1 -shuffle=on -coverprofile=coverage.out -json ./...`
  with `TEST_DATABASE_URL` pointing only to a disposable PostgreSQL 16 database;
  Go 1.27.1 and matching PostgreSQL 16 dump/restore tools.
- `go vet -tags=integration ./...` and `golangci-lint run`.
- In `e2e`, `BASE_URL=http://localhost:18085` followed by the complete Playwright
  suite, after creating a fresh synthetic fixture. Do not run it against local 8080.
- Ignored audit artifacts: `tmp/unified-bookings-20260913/results/` and
  `tmp/unified-bookings-20260913/gui-audit.cjs` (read-only visual fixture on 18086).

The broad directory Gitleaks scan reports three existing false positives: two S3
object-path concatenations and a synthetic password-rotation test literal. These
are outside this diff and their historical commit fingerprints already exist in
`.gitleaksignore`; no broad suppression was added. The final change-only scan is
reported with the final results.
