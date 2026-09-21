Method: dual-agent (A: /root/design_critique_a · B: /root/ui_evidence_b)

# Treckrr all-page critique — 2026-09-22

Target: `internal/web/templates`, all 40 templates and shared CSS/JavaScript.
Baseline: `a2eebac`, dev. Mode: operate. The user requested critique and polish
together; the bounded fixes below are within that standing scope.

## Overall assessment

**29/40 — Good.** Preserve the authored Werkblatt identity: agricultural symbols,
technical ledger typography, tabular money, German task language, and restrained
green/orange hierarchy. The opportunity is clearer recovery and task grouping,
not a new appearance. Scores are source-based; independent agents could not
open a browser because the exposed Chrome/iab inventory was empty. Rendered
verification is performed separately with the repository's Playwright setup.

## Nielsen scorecard

| Heuristic | Score / 4 | Evidence |
| --- | ---: | --- |
| Visibility of status | 3 | Busy submissions, live totals, payment/year states; short-lived success feedback. |
| Match with real world | 3 | Explicit debt direction and familiar units; invoice guidance uses implementation jargon. |
| User control | 3 | Back/cancel, voiding and recovery; actionable Undo disappears after four seconds. |
| Consistency | 3 | Shared shell and native controls; quick-entry labels and legacy focus states lag behind. |
| Error prevention | 3 | Invoice checks and confirmation dialogs; closing warnings can be clipped. |
| Recognition over recall | 3 | Visible directions and summaries; secondary action groups require excess scanning. |
| Flexibility/efficiency | 3 | Quick entry, copies, bulk actions, shortcuts and recurring bookings. |
| Minimalist design | 3 | Functional typography and surfaces; a few oversized groups of peer actions. |
| Error recovery | 2 | Generic field errors and invoice correction link to the wrong editing surface. |
| Help/documentation | 3 | Contextual instructions and explanations before irreversible changes. |

## Strengths

- Booking direction and live costs explain who owes whom; optional details are
  progressively disclosed (`booking_form.html`).
- Invoice confirmation and year-closing checks separate daily entry from
  consequential commitments (`invoice_confirm.html`, `year_closing.html`).
- The shared shell already provides a skip link, current navigation, named
  scroll regions, focus-managed dialogs and a mobile 44px control floor.

## Priorities for this polish

1. **P1: Name quick-entry controls, including their row.** `neighbor.html:41,48`
   has unnamed date/hour fields. Gespann/person labels also need row context.
   Keep cloned rows in `capture.js` correctly named.
2. **P2: Route invoice correction to the actual neighbour editor.**
   `invoice_confirm.html:50` sends “Nachbardaten” to the booking ledger instead
   of the editor in `neighbors_manage.html`. Supply a stable target and open its
   relevant disclosure. Replace “Snapshot”/hash jargon with plain German.
3. **P2: Keep actionable feedback available.** `app.js:619–634` expires Undo
   after four seconds. Keep actionable toasts until explicit dismissal.
4. **P2: Wrap year-closing warnings.** `year_closing.html:24–28` uses the
   single-line `.list__sub` style; details/names can be hidden on phones.
   Change this context only, preserving compact list metadata elsewhere.
5. **P2: Group secondary actions by task.** `layout.html:125–155` presents
   seven ordinary and four administrator destinations as peers. `beleg.html`
   presents up to eight document actions. Introduce quiet group headings for
   navigation, document output, and delivery without removing capabilities.
6. **P2: Repair dark-theme foreground/background pairings.** The skip link,
   completed onboarding marker and offline count use white on light dark-theme
   accents (source ratios 1.71:1, 2.63:1, 2.83:1). Use the matching foreground
   token/semantic solid surface (`app.css:1647,1654,2178`).
7. **P2: Expose legacy ledger radio focus.** `app.css:721` hides the native
   radio; provide a `:focus-visible + span` ring in `ledger_edit.html`.

Minor: replace generic numeric/type errors with actionable German messages,
while preserving domain-specific custom messages and existing help associations.

## Cognitive load and personas

Load is moderate. Chunking, minimal choices, and single focus are the main
concerns, concentrated in drawer and document actions. Preserve the existing
progressive disclosure in booking filters, machine costs and contact details.

- **Jordan (first-time operator):** the invoice remedy points to the wrong
  surface. Plain-language fixed-invoice guidance will help.
- **Sam (keyboard/assistive technology):** repeated unnamed controls, invisible
  legacy radio focus, and disappearing Undo are the strongest friction points.
- **Casey (interrupted mobile use):** wrapping financial warnings and persistent
  Undo improve safe resumption; grouped secondary actions reduce scanning.

The emotional journey is credible at entry and reassuring around debt previews
and invoice checks. Recovery links, clipped warnings, and expiring actions are
the valleys to address before changing the visual identity.

## Detector adjudication

B ran the detector once. Output truncation means an exact baseline total cannot
be certified (at least 37 fragment-color advisories and 10 layout findings were
visible). The final structured scan counted 48 contextual advisories/warnings;
their adjudication is recorded in the [implementation review](2026-09-22-pr184-193.md).

- Black text advisories on standalone Go fragments omit the shared theme CSS;
  they are not evidence of black text in the rendered dark interface.
- Blueprint grid and semantic accent rails are intentional DESIGN.md identity.
- Toast shadow is an overlay; bottom-sheet corner rounding is intentional.
- Root horizontal clipping protects the off-canvas drawer; no clipped popover
  was demonstrated.
- Dark summary/rate cards explicitly use dark green backgrounds: white text
  there is valid. Backup segmented controls already have the 44px mobile floor.

No overlay was injected; manual browser surfaces were unavailable. No detector
advisory is accepted without checking the actual cascade and UI context.

## Coverage

Authentication/offline, account security/profile, dashboard, neighbour
management/detail/history, booking/list/edit/attachments, ledger/payment edits,
recurring work, years/bases/prices/rigs/persons, invoices/batch issuance/receipts,
year closing/dunning/journal, both imports, statistics/comparison/recalculation,
users/audit/company/backup, and shared shell/partials.

Questions skipped: the user has already requested polish of all reviewed pages.
Broader ideas (reordering the dashboard's main action, favourite document
actions) are deferred because they need usage evidence, not cosmetic judgement.

## Post-polish verification

All seven priorities and the minor error-message improvement were implemented.
The scorecard above remains the baseline, not an unmeasured post-polish score.
Independent before/after Playwright evidence covered 24 route/theme/width
combinations plus expanded quick-entry, drawer, skip focus and invoice-repair
states. The automated page matrix covered 35 authenticated routes at desktop
and phone widths in both themes. See the implementation review for final test
results and remaining tool/coverage limitations.
