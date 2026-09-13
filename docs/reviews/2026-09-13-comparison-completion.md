# Before/after comparison completion — 2026-09-13

## Outcome and scope

The current application includes the changes shown in both before/after galleries,
not just the unified booking form. A source/history audit by two independent agents
found no omitted page-refinement proposal. The older galleries depicted changes
already shipped in `01aa243` and `d777b1d`; the selected compact booking concept was
subsequently implemented in `7651ff9`.

This follow-up corrects a concrete accessibility regression discovered with the
richer comparison fixture, extends executable regression coverage, and updates the
local comparisons to distinguish historical concepts from the implemented app.
It does not introduce another visual identity or change financial calculations.

## Complete comparison mapping

The numbered states below refer to the [44-state page inventory](2026-09-11-page-by-page-ui.md).
Every state was rendered again on the current application in desktop light,
mobile light and desktop dark. The [site-refinement report](2026-09-12-site-refinement.md)
contains the original per-page implementation rationale.

| States | Compared treatment | Current implementation |
|---|---|---|
| 1–4, 10 | Login, offline and recoverable error states | Established authentication, branded HTTP errors, focus and offline recovery retained. |
| 5 | Dashboard hierarchy and navigation | Existing totals/year hierarchy retained; direct **Buchungen & Filter** shortcut present. |
| 6 | Compact booking with optional details | Selected unified form implemented for four service types in both directions; independent drafts and itemized preview. |
| 7–9, 44 | Statements, history, confirmation and reminder | Complete financial content, formal sender/recipient, prominent balance and issuance safeguards retained. |
| 11, 29 | Recalculation | Before/after values, warnings and explicit confirmation retained. |
| 12 | Bookings and filters | Search, active secondary filters, direction/type, paging, sort and CSV implemented; task-link accessibility corrected in this follow-up. |
| 13–18 | Booking, ledger and payment edit/copy | Responsive grouping and native submits retained; structured reverse-booking edit/copy included. |
| 19–20 | Statistics | Complete totals/charts, touch targets and keyboard-scrollable tables retained. |
| 21–23 | Neighbors, archive and people | Full-width editors, grouped identity/rate fields, contact disclosure and readable archived state implemented. |
| 24–28 | Dunning, journal, years, closing and batch invoicing | Complete financial actions and safeguards retained; compact year creation and wrapping journal actions implemented. |
| 30–33 | Bases, prices, comparison and Gespanne | Paired fields, optional ordering/self-cost details, searchable machine selection and desktop comparison toolbar implemented. |
| 34–36 | Recurrence and imports | Schedule/confirmation behavior retained; booking-format help disclosed, preview/corrections and payment assignment retained. |
| 37–40 | Users, backup, company and audit | Grouped forms, optional password reset/company settings, backup feedback and filter-preserving audit export implemented. |
| 41–43 | Profile, password and two-factor setup | Long sessions and passkey enrollment disclosed; manual QR fallback optional, required security/recovery information visible. |

Shared changes remain present: explicit headings and active navigation, native
disclosures that reveal invalid fields, responsive grids, stable layout, 44 px
mobile targets, self-hosted assets, and preserved successful form controls.

## Confirmed issue and correction

A populated booking task cell may include a note or counterparty details immediately
beside its edit link. Global links had no underline, so that link relied on color
alone. The 132-state audit reproduced Axe `link-in-text-block` in desktop light and
dark. New real-browser tests also reproduced it for both own and incoming work.

The task link now uses a narrowly scoped underline with a readable offset. Existing
URLs, amounts, fields, bulk selection, filters and POST endpoints are unchanged.
Two new light/dark regressions exercise populated cells; a third checks native
radio-arrow/disclosure keyboard behavior, focus, isolated drafts, collapsed helper
values and the absence of accidental submissions.

## Comparison artifacts

- `tmp/site-refinement-20260912/index.html` now defaults to current application
  screenshots for all fourteen comparison pages and retains the September 12 view
  as a separate choice.
- `tmp/ui-comparison-20260912/view/index.html` links prominently to that updated
  comparison and the real bookings overview.
- The three booking concepts remain a historical, non-saving demo. Misleading
  “current state” and “not implemented” labels have been corrected.
- Guided and batch concepts are mutually exclusive alternatives, not missing
  requirements of the selected compact form. Fictional equipment catalogs, freely
  overridden own equipment prices and draft-only totals are not silently imported
  into production accounting.

Raw checks, current screenshots and logs are stored in the ignored project folder
`tmp/comparison-completion-20260913/`. The gallery files remain local artifacts;
the application fix, regression tests and this report are tracked.

## Verification

| Check | Result |
|---|---|
| Full page confirmation | 132/132 renders: 44 states × desktop light, mobile light and desktop dark; no Axe violations, unexpected HTTP statuses, JavaScript/request errors, duplicate IDs or viewport overflow; one H1 per page. |
| Preserved non-booking form contracts | 11/11 match the shipped refinement: actions, methods, names, values, checked/required/disabled states. Session-generated keys are normalized. |
| Go suite with race detector and shuffled execution | 202 passing top-level tests, 540 passing tests/subtests, 15 passing packages, no failures. 94 environment-gated DB/S3 cases skipped in this unit-only run; not counted as passes. |
| Full browser regression suite | 76/76 pass in Chromium (4.5 minutes), including the new light/dark populated-row regressions and native keyboard/draft test, all eight real save variants, edit/copy, filter/CSV, invoice and offline workflows. |
| Local comparison gallery | 84 selection combinations × two successfully decoded screenshots; 320 px layout and the updated gallery link pass; no JavaScript errors. |
| Historical dialog demo | 10/10 functional checks and 18/18 layout/accessibility states pass. |
| Build and source checks | Corrected Docker image built; whitespace check and Gitleaks 8.24.3 staged-change scan pass with no leaks. |

The initial page audit had exactly two failing states (the same task-link issue in
desktop light and dark). Dedicated real-record regressions reproduced it before
the fix; keyboard behavior passed before and after. The final page captures were
visually inspected for the corrected links in desktop light, dark and mobile.
Company, prices and the current unified booking surface were also inspected.

The normal local app on port 8080 was updated using the exact tested image
`sha256:e727d577ab0493a93353de8929be6f751f55de826ed7d8b0943c58e953037c04`.
Its container is healthy, `/healthz` returns 200, and its CSS matches the verified
18087 build byte-for-byte. Only the app container was recreated; the normal
database container was not reset. No push or production deployment was performed.

## Data safety and limits

The page audit uses port 18087 and a separate PostgreSQL 16 tmpfs database restored
from the existing **synthetic** visual comparison fixture. The baseline fixture
and ordinary local/production business data are not modified. The full browser
suite uses a freshly seeded isolated database on port 18085; its former disposable
test records were replaced, not real records.

The previous [unified-booking report](2026-09-13-unified-bookings.md) records the
race-enabled PostgreSQL integration, offline, privacy, rounding and financial-lock
verification. This follow-up changes HTML/CSS only, not Go business logic, schema,
dependencies or pricing. Real external SMTP/S3 delivery, hardware passkeys, other
browser engines and production deployment are not claimed here.

Impeccable's Operate/polish guidance kept the fix within the existing Werkblatt
interface and bounded verification to an initial batch and one confirmation.
Graphify's historical index was not a source of current implementation claims:
reflection hit a write-permission error and the query was stopped after producing
no output. No graph rebuild or LLM extraction was performed (0 extraction tokens).
