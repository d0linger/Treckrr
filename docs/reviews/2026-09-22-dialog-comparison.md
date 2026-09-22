# Before / after: all dialogs and pages

Comparison scope: **`a2eebac` → `be401d0`**, the UI polish accompanying the local
review of PRs 184–193. Earlier machine/person-pool and booking-direction fixes
already exist on both sides; this report does not present them as new changes.

## Open the comparison

The generated, machine-local gallery is at
`tmp/dialog-comparison-20260922/view/index.html`.
Its images and `manifest.json` are kept together in that directory. Open the HTML
directly in a browser; it needs neither a running application nor internet access.

Choose a page/dialog, desktop or mobile, and light or dark. The default view fits
both screenshots side by side. Actual-pixel mode and individual image links allow
close inspection; scrolling can be synchronized. On narrow screens the two views
stack with explicit Before/After labels.

## Coverage

The catalogue contains 66 surfaces/states, each captured for both revisions at
1280 × 900 and 390 × 900, in light and dark: **264 matched pairs / 528 screenshots**.
Long pages use full-page captures; selected forms are captured as complete
components, and overlay dialogs retain their viewport context.
Component-only captures temporarily hide the unrelated fixed app navigation so
it does not obscure the middle of a tall form. Full-page/overlay captures retain
the application shell.

All 40 templates are represented through their pages or shared components:

- Authentication, authenticator/backup-code login, setup, password, offline and errors.
- Dashboard, neighbours, history, receipt, bookings, edit/copy, attachments and legacy ledger.
- All four booking types in both directions, with maintained machine/person rates.
- Payments, statistics, pricing, rigs, people, years, bases and recurring work.
- Invoice confirmation with missing details and ready-to-issue states, journal,
  batch issuance, closing, recalculation, dunning and reminder letter.
- Imports, users, backup, company, audit and profile.
- Drawer, search, keyboard help, confirmation variants, offline queue/correction.
- Expanded quick entry, invoice-repair destination, keyboard focus, field errors,
  and Undo after six seconds.

This is an inventory of surfaces and representative states, not every possible
record, validation message or repeated confirmation string. The shared
confirmation component is shown in standard, reason and linked-person variants.

## What to compare first

| Surface | Before | After |
| --- | --- | --- |
| Drawer | Long undifferentiated list | Task-based groups |
| Beleg | Output and delivery actions together | Separate print/save and sharing/delivery groups |
| Quick entry | Narrow fields; missing date/hour accessible names | Readable widths, scroll hint, row-aware names |
| Invoice repair | Link opens booking page | Matching neighbour editor opens with contact details |
| Invoice preview | Snapshot/hash jargon | Plain German explanation |
| Closing checklist | Explanations can ellipsize | Full text wraps |
| Legacy direction focus | Radio focus not visible | Explicit keyboard-focus outline |
| Validation | Generic invalid-value message | Specific numeric/email recovery guidance |
| Undo after six seconds | Action has disappeared | Action stays available |
| Dark skip/status foregrounds | Low-contrast white text | Contrast-safe foregrounds |

Unchanged pages remain in the catalogue and are labelled accordingly. Accessible
names and persistent feedback are partly behavioural changes, so explanatory
notes supplement the resting screenshots. Screenshot hash equality is reported
only where the captured files are actually identical; differing hashes alone do
not establish a design change.

## Reproducibility and safety

Both revisions were exported with `git archive` into ignored task directories;
the active branch was not switched. Each ran on a loopback-only port against its
own disposable PostgreSQL database with the same synthetic seed. No real app
data, email sending or production services were used. Post-login capture blocks
all non-GET/HEAD requests; destructive confirmations are opened, never accepted.

Random setup QR codes are masked in both revisions. Decorative random choices
are seeded. Live session/audit timestamps are not product changes. Form-error and
Undo probes use clearly labelled demo fields/toasts injected into the rendered
page, using each revision's real CSS/JS. Offline states use synthetic IndexedDB
items and never replay a booking.

Reusable capture sources:

- `e2e/review/dialog-comparison-seed.sql` — guarded synthetic seed.
- `e2e/review/dialog-comparison.cjs` — capture catalogue and gallery generation.
- `e2e/review/dialog-comparison.html` — local side-by-side viewer template.
- `e2e/review/verify-dialog-comparison.cjs` — pair/image integrity and viewer checks.

Run the capture only after starting those exact archived revisions on localhost
18080/18081, migrating new `comparison_before` / `comparison_after` databases, and
loading the seed. It uses the existing E2E admin fixture credentials. From the
repository root:

```powershell
node e2e/review/dialog-comparison.cjs
node e2e/review/verify-dialog-comparison.cjs
```

Generated images stay ignored; the catalogue, viewer, seed and methodology are
versioned. No application code is changed by this comparison task.

## Validation

- All 528 captures completed successfully; all 264 pairs have both revisions.
- Every image's PNG header, nonzero dimensions and SHA-256 checksum were verified.
- Gallery search, dialog selection, device/theme selection, actual-pixel mode,
  desktop and mobile rendering passed browser checks, with no JavaScript errors.
- Gallery accessibility check: no serious/critical WCAG A/AA violations.
- Both seed safety guards were tested: non-comparison databases and populated
  comparison databases are rejected before writes.
- Financial fixture counts stayed unchanged: four entries, one payment and one
  issued-invoice fixture in each database.

The initial eight-context capture exhausted browser resources; captures were
successfully repeated with at most two contexts. A crop-only stylesheet attempt
was correctly rejected by the app's CSP. Final component crops instead remove
only unrelated navigation nodes in the disposable capture page; no security
policy is disabled or changed. The intermediate failure manifest is retained in
the ignored task directory for traceability.
