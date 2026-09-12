# Page-by-page UI overhaul — 2026-09-11

## Scope and method

The review mapped every HTML route to its handler and template, then exercised 44 distinct rendered surfaces and states. Each surface was captured and checked in three scenarios: desktop light (1440 × 900), mobile light (390 × 844), and desktop dark (1440 × 900), for 132 page/scenario captures in total.

Every run checked the response and final URL, document title and heading structure, landmarks, forms, tables, duplicate IDs, accessible labels and image alternatives, page and element overflow, interactive target sizing, console and network errors, WCAG 2.1 A/AA Axe findings, and basic load timing. Eight contact sheets and the full-height screenshots of the longest or changed mobile pages were reviewed manually.

The initial 129-result matrix used the local development dataset. Because it contained no issued invoice that could render a valid reminder letter, an isolated temporary database was subsequently seeded with a synthetic invoice. The valid letter was then captured and checked for accessibility, heading structure, and overflow in all three scenarios. Its invalid-parameter and missing-record states remain separate inventory rows. No production or local business data was changed to manufacture a screenshot.

## Page inventory

| # | Surface/state | Route exercised | Result |
|---:|---|---|---|
| 1 | Login | `/login` | 200 |
| 2 | Offline | `/offline` | 200 |
| 3 | Not found | `/missing-page` | Branded 404 |
| 4 | Invalid request | `/neighbors/1/mahnung` | Branded 400 |
| 5 | Dashboard | `/?year=1` | 200 |
| 6 | Neighbor detail | `/neighbors/1?year=2` | 200 |
| 7 | Statement/invoice | `/neighbors/1/beleg?year=2` | 200 |
| 8 | Neighbor history | `/neighbors/1/overview` | 200 |
| 9 | Invoice confirmation | `/neighbors/1/invoice/confirm?year=2` | 200 |
| 10 | Missing reminder record | `/neighbors/1/mahnung?year=1&stufe=0` | Branded 404 |
| 11 | Neighbor recalculation | `/neighbors/1/recalc?year=2` | 200 |
| 12 | Bookings | `/buchungen?year=2` | 200 |
| 13 | Edit booking | `/entries/1/edit` | 200 |
| 14 | Copy booking | `/entries/1/copy` | 200 |
| 15 | Edit ledger item | `/ledger/1/edit` | 200 |
| 16 | Copy ledger item | `/ledger/1/copy` | 200 |
| 17 | Edit payment | `/payments/3/edit` | 200 |
| 18 | Copy payment | `/payments/3/copy` | 200 |
| 19 | Year statistics | `/stats?year=2` | 200 |
| 20 | All-years statistics | `/stats/all` | 200 |
| 21 | Neighbor management | `/neighbors` | 200 |
| 22 | Archived neighbors | `/neighbors?scope=archiviert` | 200 |
| 23 | People | `/personen` | 200 |
| 24 | Dunning overview | `/mahnwesen?year=1` | 200 |
| 25 | Invoice journal | `/rechnungsjournal?year=1` | 200 |
| 26 | Billing years | `/years` | 200 |
| 27 | Year closing | `/years/1/abschluss` | 200 |
| 28 | Batch invoicing | `/years/1/issue-all` | 200 |
| 29 | Year recalculation | `/years/2/recalc` | 200 |
| 30 | Assessment bases | `/bases` | 200 |
| 31 | Prices | `/prices?base=1` | 200 |
| 32 | Price comparison | `/prices/compare?base=1` | 200 |
| 33 | Machine combinations | `/gespanne?base=1` | 200 |
| 34 | Recurring bookings | `/recurring` | 200 |
| 35 | Booking import | `/entries/import?year=2` | 200 |
| 36 | Payment import | `/payments/import` | 200 |
| 37 | User administration | `/admin/users` | 200 |
| 38 | Backup | `/admin/backup` | 200 |
| 39 | Company data | `/admin/company` | 200 |
| 40 | Audit log | `/admin/audit` | 200 |
| 41 | Profile | `/profile` | 200 |
| 42 | Change password | `/account/password` | 200 |
| 43 | Two-factor setup | `/account/2fa` | 200 |
| 44 | Valid reminder letter | `/neighbors/1/mahnung?year=2&stufe=1` | 200; isolated synthetic invoice |

## Changes made

- Restored a single, unambiguous page-level H1 on every authenticated screen while preserving the app-bar title.
- Gave the mobile brand link a stable accessible name and marked active drawer/sub-navigation destinations with `aria-current="page"`.
- Replaced tab semantics on ordinary base-workspace links with an accurately labelled navigation landmark.
- Added an explicit label to the booking photo upload.
- Made horizontally scrollable data tables named, keyboard-focusable regions.
- Increased mobile/coarse-pointer hit areas for back links, workspace navigation, chart drill-down links, and switches.
- Kept long chart labels visibly truncated with ellipses while retaining their 44 px mobile hit areas, correcting a regression identified by the independent finish reviewer.
- Enabled the reminder letter's existing formal document layout: its sender/recipient header and prominent open amount had been hidden by shared invoice-mode styles. Added a rendered-content regression check to the invoice flow.
- Collapsed unusually long active-session histories behind an accessible disclosure while retaining all revoke controls. With 106 seeded sessions, the mobile profile dropped from roughly 11,900 px to 1,413 px.
- Routed browser-facing missing resources through the branded, recoverable 404 page while preserving compact plain responses for non-HTML clients.
- Expanded accessibility and UI-hardening coverage to the previously untested edit/copy, recalculation, invoice-confirmation, assessment-base, price-comparison, password, 2FA, offline, and error surfaces.

## Verification

- Confirmation matrix: 129/129 original renders completed, plus 3/3 valid reminder captures. The reviewer-requested chart correction was recaptured in all three scenarios.
- Serious/critical Axe findings: 0.
- Page-level horizontal overflow: 0.
- Duplicate IDs: 0.
- Missing image alternatives: 0.
- Heading failures: 0; every rendered page has exactly one H1.
- Unexpected console/network failures: 0. The only navigation errors were the three intentionally exercised 400/404 responses in each scenario.
- Slowest measured load: 581 ms (local login); authenticated pages remained below 300 ms in this run.
- `go test ./...`: passed.
- Playwright accessibility and UI-hardening suites: passed. Seed-dependent semantics checks run before the write-based specs and use the payment history that the CI seed guarantees.
- Full Playwright suite: 27/27 passed against a temporary, isolated application/database seeded directly from `.github/workflows/e2e.yml`.
- Final reminder correction: all 3 capture scenarios passed visible-content assertions, Axe, and overflow checks; the updated invoice suite passed 3/3 on a fresh isolated CI fixture.
- Independent finish review: `ship`; chart-label truncation and reminder content omissions both scored resolved. Documentation review found no design-system contract change and preserved the existing design files, including the previously stale sidecar.

An initial full-suite attempt against the local development dataset hit four fixture failures because year 1 was already closed. The company name/address and neighbor address changed by that attempt were restored to the exact prior values recorded in the audit log; its single test payment (ID 18) was soft-deleted through the normal reversible application action. The successful write-based run used the isolated database.
