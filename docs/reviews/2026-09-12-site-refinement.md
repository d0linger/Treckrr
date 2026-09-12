# Site-wide task and form refinement — 2026-09-12

## Scope and implementation

This is an implementation in Treckrr's existing server-rendered application, not the standalone booking demonstration. It applies the requested consolidation, progressive disclosure, compact responsive grouping and keyboard/error hardening across the shared interface. The Werkblatt identity, native controls, self-hosted assets, CSP, server calculations and established POST endpoints remain in place. No Tailwind CDN, new frontend framework, pricing algorithm, database migration or production-data change was introduced.

The inventory remains the 44 rendered states in [the page-by-page review](2026-09-11-page-by-page-ui.md). Every state was included in the browser audit. Sixteen page templates receive task-specific changes, alongside both shared templates, shared CSS and the form helpers. Already short security/payment forms and formal financial documents retain their structure; reviewing all pages does not mean hiding essential information on every page.

### Shared behavior

- Form grids use usable minimum control widths and intentional two-column groups rather than squeezing long labels into 140 px columns.
- Optional native disclosures preserve all successful controls, including collapsed values. Invalid controls reveal enclosing disclosures before receiving keyboard focus.
- Machine selection is shared by booking and Gespann forms: search names/categories, selected-only filtering, count and empty feedback. Filtering never disables or clears selected IDs; Enter in the search does not save the form.
- A single visible billing choice drives the original `unit` and `mode` controls. Existing capture, offline, precheck and submit contracts remain intact; a no-JavaScript render retains the original controls.
- Page-entry animation and card translation are removed while users target controls. Theme tokens and existing 44 px touch conventions are retained. New form help is included in the service-worker shell.
- Local comparisons, fixtures and screenshots are excluded from Docker build context.

## Complete page coverage

Numbers refer to the original 44-state inventory; fixture IDs differ from local business data.

| States | Page | Treatment |
|---|---|---|
| 1 | Login | Existing linear authentication flow retained; shared focus behavior checked. |
| 2 | Offline | Recovery message and retry retained; new form helper precached. |
| 3, 4, 10 | Missing/invalid records | Branded recovery and HTTP status semantics retained. |
| 5 | Dashboard | Existing year, totals and neighbor hierarchy retained; shared steady layout. |
| 6 | Neighbor / new booking | Consolidated billing choice, searchable equipment, hours beside costs, optional person/note, compact date shortcuts with a clean date label. |
| 7 | Statement / invoice | Financial content and issuance/export actions retained. |
| 8 | Cross-year history | Complete historical rows, amounts and print layout retained. |
| 9 | Invoice confirmation | Legal checks and irreversible-effect explanation remain visible. |
| 11, 29 | Recalculation | Before/after amounts, paid-account warning and confirmations retained. |
| 12 | Bookings | Search/neighbor first; active secondary filters reopen; sort, paging and bulk actions preserved. |
| 13, 14 | Booking edit/copy | Same billing/equipment grouping; populated notes visible; recurring setup disclosed. Existing record values cannot be overwritten by remembered new-booking defaults. |
| 15, 16 | Ledger edit/copy | Small linear form and debit/credit meaning retained; shared responsive fields. |
| 17, 18 | Payment edit/copy | Amount/date and method/note retained; shared responsive fields. |
| 19, 20 | Year/all-year statistics | Totals, charts, period context and pricing caveats retained. |
| 21, 22 | Neighbor management/archived | Full-width editing rows, paired identity fields, state-aware billing/contact disclosure; readable inactive states. |
| 23 | People | Name/rate grouping and rate warnings retained. |
| 24 | Dunning | Stages, open balances and distinct send actions retained. |
| 25 | Invoice journal | Full document/tax table and totals retained; concise export labels and wrapping header prevent narrow-screen overflow. |
| 26 | Billing years | Required year and basis together; optional label disclosed. |
| 27 | Year closing | Unresolved checks and closing consequences remain visible. |
| 28 | Batch invoicing | Selection, statuses, amounts and incomplete-neighbor explanations retained. |
| 30 | Price bases | Year/source together; optional name disclosed; lock/delete restrictions preserved. |
| 31 | Prices | Full-width edit rows, combined search/category toolbar, optional ordering/self-cost details with saved-value summaries. |
| 32 | Price comparison | Select and submit share a desktop toolbar; native no-JS submit remains. |
| 33 | Gespanne | Full-width rows, paired tractor/load, shared machine picker, secondary ordering. |
| 34 | Recurring entries | Schedule/status and separately confirmed extra execution retained. |
| 35 | Booking import | File/preview first; detailed format help disclosed; sample, correction and import-token flows retained. |
| 36 | Payment import | File preview and transaction-to-neighbor assignment retained. |
| 37 | Users | Paired account fields; password reset disclosed; role and danger actions remain separate endpoints. |
| 38 | Backup | Health, validation, scheduling and protected restore retained. |
| 39 | Company | Twenty fields grouped into sender/payment, tax and named secondary settings. One existing save transaction; stored secondary settings stay summarized. |
| 40 | Audit | Search/action first, state-aware user/date filters; corrected filter-preserving CSV export. |
| 41 | Profile | Security status/recovery warnings retained; passkey enrollment inputs disclosed. |
| 42 | Password change | Linear three-field security task retained. |
| 43 | Two-factor setup | Required setup stays visible; manual key/URL is available under QR help. Newly issued recovery codes remain visible. |
| 44 | Reminder letter | Complete formal document and amounts retained. |

## Confirmed defects corrected

1. **Remembered defaults overwrote edited/copied bookings.** A browser regression first reproduced an unrelated remembered task replacing the stored task. `capture.js` now opts into defaults only on the new-booking form. Edit and copy retain server-rendered values; new bookings still restore remembered defaults.
2. **Audit CSV links lost filters.** Appending a query fragment inside an HTML-template URL caused `&from=…&username=…` to become part of the encoded `export` value. The template now constructs the complete URL before contextual escaping. The browser regression verifies both the URL and an empty filtered CSV, not just the presence of an export button.
3. **Inactive cards lost contrast.** Whole-card opacity made populated archived records fail dark-mode contrast. Explicit inactive badges now carry the state without fading readable text or links.
4. **Mobile icon widths were overridden.** A later 36 px base rule overrode the earlier touch width. The final touch rule now follows the base rules; a 320 px browser regression asserts actual 44 × 44 px booking targets.
5. **A populated journal overflowed on mobile.** Once a real test invoice existed, the CSV action beside the long heading exceeded the viewport, and the archive label exceeded 320 px layouts. Shared page headings now wrap their actions; archive/PDF labels are concise with explanatory text outside the buttons. The invoice workflow asserts the populated journal at 320 px. Financial tables retain their own keyboard-accessible horizontal scrolling.

## Verification

| Check | Validated result |
|---|---|
| Go unit suite, race detector and shuffled execution | 182 top-level tests; 435 passing tests/subtests across 15 packages. |
| Full PostgreSQL 16 integration suite, race detector and shuffled execution | 290 top-level tests; 619 passing tests/subtests across 15 packages. One environment-gated S3 case skipped. |
| Statement coverage | 62.0%; above the existing 50% floor. |
| Build, vet and integration-tagged golangci-lint | Passed; lint reported zero issues. |
| Embedded web/template tests after the last journal correction | Passed with the race detector. |
| Browser regression suite | 42/42 passed in Chromium in the final confirmation run (4.1 minutes), including ten new form/accessibility regressions and the populated journal at 320 px. |
| Complete rendered-page audit | All 44 states in desktop, mobile and dark mode: 132/132 valid results, no serious/critical Axe findings, no viewport overflow, no unexpected HTTP status, JavaScript error or failed request. Each has one primary heading and no duplicate IDs. |
| Expanded form states | 18 checks: manual booking, company and price forms at 320/768/1440 px in both themes. No serious/critical Axe findings or viewport overflow; 320 px booking action targets meet 44 × 44 px. |
| Before/after form contracts | 14/14 changed pages retain their submitted fields, values and endpoints. |
| JavaScript syntax and whitespace checks | Passed for the changed scripts and staged diff. |
| Staged secret scan | Gitleaks 8.24.3 passed with no leaks. |

The company page is 2,004 → 1,192 px tall at desktop width (40.5% shorter) and 2,300 → 1,684 px on mobile (26.8% shorter) with the same fixture and all twenty fields retained. These measurements apply to that page, not to every page: dense financial tables intentionally keep their complete content. Actual paired screenshots cover fourteen pages in three display modes.

Tests use dedicated ephemeral PostgreSQL databases and local ports 18081–18083. No browser writes target the ordinary development app or production. The comparison uses the previous local application image against the same synthetic visual fixture; no private business data appears in screenshots.

The form-contract comparison checks actions, methods, names, values, checked/disabled/required states across fourteen changed pages. It normalizes independently generated idempotency keys and the intentional text-to-search input change; CSRF/session tokens are not compared across independent logins. Real submit/queue regressions separately cover the keys and CSRF behavior.

## Limits and review notes

- The database suite uses PostgreSQL 16 with a matching dump/restore client. PostgreSQL 18 was not rerun for this frontend-only change; the preceding regression report records that separate baseline.
- Real SMTP/S3 delivery, hardware passkeys, other browser engines and production deployment are not claimed as exercised. The environment-gated S3 integration case remains skipped; PostgreSQL backup/restore tests do run.
- The raw design detector treats unrendered Go templates as HTML and reads an already-stale design sidecar. Its raw-template white-on-mint/color findings are not substitutes for computed browser contrast. Existing status stripes, code-font fallbacks and app-root clipping are recorded as retained implementation choices; no new clipping overlay is introduced. The new CSS uses existing tokens. Refreshing the design sidecar remains a separate `/impeccable document` task.
- Browser test selectors were adapted to the consolidated billing control and nested contact disclosure. During test setup, the screenshot probe also needed CSP-safe Axe injection, actual invoice endpoint selection, and UTF-8 fixture transfer. These were harness corrections, not changes to application security or billing rules.

## Artifacts

Ignored local artifacts live in `tmp/site-refinement-20260912/`: comparison `index.html`, compose/fixtures, `results/page-audit.json`, `form-contracts.json`, expanded-state checks, screenshots, complete browser logs, unit/integration JSON streams and coverage. The executable regression cases are committed under `e2e/tests/a11y_forms.spec.ts`.
