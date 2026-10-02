# Treckrr consolidation and refactoring plan

Date: 2026-10-01

Scope: current `dev` branch

Status: implementation in progress; safety-net and booking-core consolidation completed without schema changes

## Implementation progress

Completed on `dev` on 2026-10-01:

- Added a PostgreSQL characterization test proving standard and quick entry
  freeze the same rig identity, labels, hourly rate, and rounded cost.
- Added typed equipment rate breakdowns while retaining the existing calculator
  functions as compatibility entry points.
- Added `internal/money` as the shared exact-decimal line-amount primitive and
  migrated equivalent calculations across models, handlers, imports, person and
  travel surcharges, entry groups, and recurring materialization.
- Centralized catalog resolution and immutable equipment snapshot assembly for
  standard and quick-entry workflows.
- Replaced the booking parser's six positional return values with a typed draft
  and introduced one application-layer create boundary over the existing atomic
  `Entry`-group and `LedgerBooking` persistence adapters.
- Protected explicitly agreed free-equipment rates from catalog recalculation.
- Applied exact machine-hours storage validation consistently to V2, legacy/
  offline, and quick-entry paths.
- Stabilized the recurring schedule integration test across local and UTC time
  zones by comparing PostgreSQL dates as calendar days.
- Consolidated the dashboard's basis, neighbour, booking, year-close, and
  settlement state into one live workflow strip, with direct booking entry from
  each writable open-year neighbour row.
- Added compact in-page navigation for booking, own services, counterclaims,
  and payments on the long neighbour account page without changing its forms,
  calculations, or permissions.

Deliberately unchanged:

- No database migration, historical rewrite, table merge, route change, or URL
  change was introduced.
- Own work, incoming counterclaims, payments, recurring snapshots, and issued
  invoice snapshots retain their separate persistence and locking semantics.
- The unified booking form, quick entry, copy workflow, recurring bookings,
  accounting CSV exports, PDF documents, and e-invoice output remain available.

Deferred to later, separately reviewed slices:

- Typed HTML/PDF statement view models and document assemblers.
- Native XLSX output and a dedicated Maschinenring/steuer export profile.
- Standalone named templates detached from a neighbor and schedule; current
  quick entry, copy, and recurring rules already cover the existing workflows.
- Diesel-price adjustments. This requires an effective-dated business rule and
  immutable booking snapshots; a live external price must never rewrite history.
- Removal of `Machine.HourlyRate`. Templates invoke it reflectively, so it must
  remain a compatibility projection until machine template data is typed.

## Executive summary

Treckrr is no longer a small CRUD application. It is a mature operational system with 221 registered HTTP routes, 48 server-rendered templates, approximately 18,800 non-test lines in `internal/server`, and approximately 13,600 non-test lines in `internal/store`. The underlying accounting safeguards are stronger than the apparent UI complexity: exact decimal arithmetic is centralized, billing years reference versioned price bases, bookings retain price snapshots, closed years lock business records, payments remain recordable after closure, and invoice snapshots preserve tax-relevant history.

The main issue is therefore not an incorrect cost model. It is accumulated orchestration complexity:

- The daily path (basis → neighbor → booking → year close → payment) competes visually with specialist modules.
- One HTTP layer coordinates parsing, catalog lookup, pricing, persistence, audit, idempotency, and redirect behavior.
- Current and compatibility-era booking representations coexist across `Entry`, `LedgerEntry`, `LedgerBooking`, recurring snapshots, quick entry, and import.
- The same business operation has several input paths whose validation and resolution overlap.
- The frontend has a coherent token system, but page-specific CSS and shell behavior have accumulated in one large stylesheet and one large application script.

The recommended direction is a staged modular monolith, not a rewrite and not a SPA migration. Preserve the existing Go templates, PostgreSQL transactions, URLs, and accounting invariants. Extract use-case services and presentation partials behind characterization tests in small, independently reviewable steps.

## Operator priority and target workflow

The current primary operator workflow is:

1. Maintain or select a **Bemessungsgrundlage**.
2. Select a **Nachbar**.
3. Create a **Buchung**.
4. Review and complete the **Jahresabschluss**.
5. Mark the remaining amount as **bezahlt**.

The following modules remain valuable but are secondary or future-oriented for the current operator: Mahnwesen, Buchungen & Filter, Rechnungsjournal, Zahlungs-Import, Wiederkehrend, Personen, Mailausgang, and Betriebsdaten. They should remain available without occupying the same visual hierarchy as the daily workflow.

## Current system map

### Runtime and presentation

- Go `net/http` application with route registration in `internal/server/server.go`.
- Server-rendered Go templates in `internal/web/templates`.
- Shared styling in `internal/web/static/css/app.css` with light/dark tokens, responsive behavior, print rules, and component classes.
- Progressive enhancement through focused scripts plus the shared `internal/web/static/js/app.js`.
- PWA/offline booking support, service worker, and local replay protection.

### Core business flow

| Stage | Main model | Main server/store surfaces | Important invariant |
|---|---|---|---|
| Basis | `PriceBase`, `LoadLevel`, `Tractor`, `Machine`, `Gespann` | `bases.go`, `prices.go`, `gespanne.go`, corresponding store files | A billing year uses one basis; locked bases remain stable. |
| Year | `BillingYear`, year-neighbor membership | `years.go`, `dashboard.go`, `store/years.go` | Year and basis are different concepts; closure freezes bookings/documents. |
| Booking | `Entry`, `LedgerEntry`, `LedgerBooking`, `BookingPerson` | `booking_form.go`, `booking_form_view.go`, `entries.go`, `ledger_booking.go` | Own work and incoming counterclaims must remain semantically distinct. |
| Pricing | exact `decimal.Decimal` functions | `internal/calc`, booking resolvers, `store/recalc.go` | Stored totals and component values are rounded deterministically. |
| Settlement | ledger rows, payments, invoices | `entries.go`, `payments_handlers.go`, `beleg.go`, store payment/invoice files | Payment is independent from year closure and invoice snapshots are immutable. |
| Closure | year status and closing checks | `year_closing.go`, `store/year_closing.go` | Checks inform; the operator remains responsible for closing. |

### Existing strengths to preserve

- `internal/calc` is small, exact, and well-tested. It is the correct center for tractor, machine, rig, and cost arithmetic.
- The `PriceBase`/`BillingYear` split correctly models a rate basis that can span multiple billing years.
- Bookings store snapshots rather than silently changing whenever catalog prices change.
- Incoming work is not mixed into own turnover or equipment utilization.
- Payments remain possible after a year is closed, matching the real workflow.
- Account locks, idempotency keys, audit records, and frozen invoice documents already protect consequential operations.
- Go templates keep the runtime small and work without a client framework.
- The visual system already has semantic tokens, dark mode, print handling, visible focus, reduced-motion handling, and coarse-pointer touch floors.

## Functional overlap and consolidation opportunities

### 1. Booking creation has multiple orchestration paths

Evidence:

- `parseBookingV2` is the current shared parser, but equipment bookings still delegate to `resolveEntryFromForm`.
- Quick entry independently resolves rows and rigs through `createQuickRows` and `buildGespannEntry`.
- Edit/copy presentation is assembled separately by `setEntryBookingForm` and `setLedgerBookingForm`.
- Recurring bookings persist and later rehydrate another snapshot form.
- CSV import is another entry boundary with similar validation expectations.

Risk:

- A catalog, rounding, active-item, or snapshot rule can be fixed in one path and remain stale in another.
- Validation messages and allowed precision can drift.
- Every new booking type multiplies handlers, templates, replay checks, and tests.

Recommended target:

- Introduce an internal application-level `booking` package only after characterization tests cover every current path.
- Define one neutral command such as `BookingDraft` with direction, service lines, date, participant, and source metadata.
- Resolve catalog references through one `CatalogResolver` and calculate through one `Pricer`.
- Keep two persistence adapters: own work writes `Entry` groups; incoming work writes `LedgerBooking`. Do **not** merge those tables merely because the form is shared.
- Make quick entry, recurring execution, CSV import, and the interactive form adapters that all produce the same validated command.

### 2. Pricing arithmetic is centralized, but pricing orchestration is distributed

Evidence:

- `internal/calc` owns the core formula.
- `resolveEntryFromForm`, `buildGespannEntry`, `parseBookingV2`, and `RecalcPreview` each assemble catalog items and decide when a row is priceable.
- `Machine.HourlyRate` duplicates the multiplication also represented by `calc.MachineRate`.
- `LedgerBooking.Total` separately defines line-level rounding for counterclaims.

Risk:

- The formula remains correct while eligibility and component assembly diverge.
- New cost modifiers could be applied to only some creation/recalculation paths.

Recommended target:

- Keep pure arithmetic in `internal/calc`.
- Add a single immutable `RateBreakdown` result containing tractor, machine, person, adjustment, subtotal, and total lines.
- Use that breakdown for previews, persisted snapshots, receipts, PDFs, and recalculation previews.
- Retain line-by-line rounding as an explicit invariant with golden tests before moving code.

### 3. HTTP handlers carry too much use-case responsibility

Evidence:

- `internal/server/server.go` registers 221 routes.
- `internal/server/entries.go` combines page assembly, booking creation/update, ledger mutation, quick entry, copy/edit, account guards, and deletion.
- `internal/server/beleg.go` combines receipt assembly, public shares, PDF/email delivery, invoice issue, cancellation, credit notes, and QR output.

Risk:

- Small behavior changes require understanding unrelated delivery and rendering paths.
- Error mapping, audit wording, and redirect decisions become inseparable from transactions.
- Large files attract compatibility branches because no narrower boundary exists.

Recommended target:

- First split files within the existing packages; do not create new packages just to reduce file length.
- Then extract use-case services around stable transaction boundaries:
  - `BookingService`
  - `SettlementService`
  - `YearCloseService`
  - `DocumentService`
- Keep handlers responsible for HTTP parsing, authorization, calling one use case, flash/error mapping, and redirect/render only.
- Group route registration by domain using functions such as `registerBookingRoutes`, without changing any URL.

### 4. Presentation models are assembled ad hoc

Evidence:

- Handlers populate generic `pageData` maps.
- Receipt, neighbor, dashboard, and edit views each derive overlapping labels and totals.
- Both `Entry` and `LedgerBooking` need display descriptions and calculation details.

Risk:

- Templates depend on implicit keys and runtime-only contracts.
- Renaming or adding a field is difficult to verify statically.
- PDF and HTML views can drift.

Recommended target:

- Introduce typed view models one page family at a time.
- Start with the daily workflow: dashboard, neighbor detail, booking form, year close.
- Build HTML and PDF from the same typed statement/receipt assembler.
- Preserve German UI strings at the presentation boundary; keep domain errors language-neutral.

### 5. Domain models have accumulated compatibility concerns

Evidence:

- `models.go` contains billing, authentication, payment, equipment, company, and invoice types.
- `LedgerBooking` retains explicit deprecated fields for historical snapshots.
- `NeighborEquipment` remains for export/erasure compatibility only.

Risk:

- Compatibility fields look like valid choices for new development.
- Developers must know historical context to avoid writing new data into retired shapes.

Recommended target:

- Split `models.go` into files by domain while keeping package `models` and exported APIs unchanged.
- Mark write-prohibited compatibility fields with constructors/validation that prevent new usage.
- Document which models are catalog data, mutable operational data, immutable snapshots, and compatibility-only records.
- Postpone any package split until dependencies and template reflection references are mapped.

### 6. Frontend assets need source-level modularization

Evidence:

- `app.css` contains the design tokens, shell, component primitives, page-specific modules, print rules, and later cascade overrides.
- `app.js` owns unrelated shell, feedback, dialog, navigation, search, capture, and sharing behavior.
- Domain-specific scripts already exist, demonstrating that incremental separation is feasible.

Risk:

- Later overrides can unintentionally undo earlier component rules.
- A visual fix requires searching a large cascade rather than editing a clear component layer.
- Shared submit behavior is sensitive to handler interaction.

Recommended target:

- Keep one delivered CSS asset, but author it in ordered source layers: tokens, reset/base, shell, components, workflow pages, utilities, responsive, print.
- Concatenate/minify during the existing build or generation step; do not add a runtime bundler solely for this.
- Split `app.js` by responsibility while retaining CSP-safe external files and the same DOM contracts.
- Add browser tests for every shared submit or modal interaction before moving it.

## Data model and parameter recommendations

### Retain

- `PriceBase` as the version boundary for published prices.
- `BillingYear` as the operator's work unit.
- Immutable booking and invoice snapshots.
- Exact decimal arithmetic and explicit rounding.
- Separation of payments from year status.
- Separation of own services from incoming neighbor counterclaims.

### Clarify

- Define one documented vocabulary: catalog rate, booked rate, self-cost, invoice amount, ledger amount, paid amount, and remaining amount.
- Expose a typed calculation breakdown rather than re-deriving labels from flattened fields.
- Treat units as controlled values with an explicit custom-unit escape hatch.
- Model adjustments as named lines, not hidden modifiers of an hourly rate.

### Avoid

- Do not replace snapshots with live catalog joins.
- Do not merge `payments` into ledger postings; their audit and settlement semantics differ.
- Do not merge own and incoming bookings into a single persistence table without a proven migration and reporting model.
- Do not add floating-point calculations or client-authoritative totals.

## UI/UX consolidation concept

### Navigation

- Keep the four bottom/desktop-rail destinations as the permanent information architecture: Übersicht, Jahre, Nachbarn, Grundlagen.
- Mirror that daily workflow at the top of the drawer.
- Put specialist modules behind “Weitere Funktionen”.
- Automatically expand a disclosure when the active page is inside it, so deep links never hide context.
- Keep account, notifications, user administration, audit, and backup visible because they are operational controls rather than ordinary feature destinations.

### Dashboard

- Preserve total cost and hours as the first scan target.
- Collapse reporting/export/import links under “Auswertungen & Daten”.
- Present year state and payment totals as one “Jahresablauf” block.
- Keep “Jahr abschließen” as the dominant action while a year is open.
- Make an outstanding balance visibly actionable: the control must say “Als bezahlt markieren”, not rely on a tooltip attached to “Offen”.

### Booking capture

- Preserve the existing unified booking form and direction-first flow.
- Keep optional details progressively disclosed.
- Prefer remembered defaults per operator only after defining how shared devices and offline replay should behave.
- On mobile, keep the primary save action and required fields within one thumb-driven vertical path.

### Tables and lists

- Continue using card-like rows for mobile and data tables only where column comparison is the task.
- Keep money right-aligned and tabular.
- Use one semantic status grammar across tags, chips, and payment actions: positive/complete, in progress, open/action required, credit, voided.
- Never rely on color alone; retain explicit German labels.

### Forms

- Continue the shared `.field`, `.input`, `.select`, `.btn`, and `.disclosure` primitives.
- Standardize optional details under native `details` elements.
- Keep destructive controls out of primary action rows and require explicit confirmation.
- Preserve 44px targets on narrow/coarse-pointer devices.

## Feature assessment

Several suggested “missing” features already exist and should be consolidated rather than rebuilt:

| Idea | Current state | Recommendation |
|---|---|---|
| Recurring work templates | Implemented under `Wiederkehrend` | Keep secondary until usage increases; reuse the future booking command service. |
| Multi-machine combinations | Implemented as `Gespanne` | Keep as a catalog composition; improve search/favorites before adding another combination model. |
| Tax/accounting export | Rechnungsjournal, CSV/accounting profiles, e-invoice support exist | Consolidate export entry points and documentation before adding another export. |
| Payment import | Implemented with batches and reversals | Keep as an advanced workflow; do not surface in the daily path. |
| Offline field capture | Implemented with replay/idempotency | Preserve as a core non-functional requirement in every booking refactor. |

Recommended future additions:

1. **Versioned fuel adjustment (“Dieselpreis-Joker”)**
   Add an explicit, effective-dated adjustment to a price basis or rate breakdown. Operators must preview and deliberately apply it. Never fetch a live price and silently recalculate historical bookings.

2. **Operator favorites**
   Allow a small set of neighbors, rigs, or tasks to appear first in booking capture. Keep it per user/device and compatible with offline operation.

3. **Basis change impact preview**
   Before editing an unlocked basis, show affected open years/bookings and the difference that recalculation would produce.

4. **Close-and-settle cockpit**
   Build on the existing closing checklist and dashboard payment states to provide a guided sequence, without making non-blocking checks mandatory.

5. **Typed export profiles**
   Consolidate accounting outputs behind named profiles with a preview and validation summary rather than adding standalone export buttons.

## Visual changes implemented in this step

- Reordered the drawer around the daily workflow.
- Moved the marked low-frequency modules into “Weitere Funktionen”.
- Moved Mailausgang and Betriebsdaten into “Erweiterte Verwaltung”.
- Preserved every route and automatically opens the containing section on active specialist pages.
- Collapsed dashboard reporting/import actions under “Auswertungen & Daten”.
- Introduced a clearer “Jahresablauf” grouping for year status, closure, and payment totals.
- Changed the completed-year open-balance control from a status-looking pill into an explicit “Als bezahlt markieren” action.
- Harmonized status tags with the existing mono, bordered semantic-chip design.
- Added narrow-screen behavior so the year actions and payment action use the available width.

No handler, query, calculation, migration, authorization rule, or persistence operation changed.

## UI implementation audit

| Dimension | Score | Evidence |
|---|---:|---|
| Accessibility | 4/4 | Semantic landmarks, labels, focus management, skip link, reduced-motion handling, and automated axe coverage already exist. |
| Performance | 3/4 | No frontend framework or large runtime; shared assets are lean, but the monolithic CSS/JS sources make targeted optimization harder. |
| Responsive design | 4/4 | Mobile-first shell, bottom navigation, desktop rail, safe areas, horizontal table regions, and coarse-pointer target floors. |
| Theming | 3/4 | Strong semantic light/dark tokens; some literals remain for deliberate fixed surfaces and print contexts. |
| Implementation integrity | 3/4 | The Werkblatt identity is coherent; source layering and duplicate status primitives remain consolidation opportunities. |
| **Total** | **17/20** | **Good; architecture and information hierarchy are the next constraints, not visual identity.** |

The mechanical detector reported one generic “root clips positioned child” warning. It is not accepted as a demonstrated defect: root clipping is part of the current off-canvas containment and no affected menu, tooltip, or popover was identified.

## Recommended delivery sequence

### Phase 0 — visual consolidation (implemented here)

- Navigation hierarchy, dashboard action disclosure, workflow grouping, payment-action clarity, responsive polish.
- Risk: low; template/CSS only.

### Phase 1 — safety net and contracts

- Add characterization tests for interactive, quick, recurring, and imported bookings using the same scenario matrix.
- Document rounding, snapshot, account-direction, closure, and payment invariants.
- Introduce typed view models for dashboard and neighbor detail without changing behavior.
- Risk: low to medium; no schema change.

### Phase 2 — booking application service

- Extract catalog resolution, validation, pricing breakdown, and persistence commands.
- Migrate one input adapter at a time: interactive form, quick entry, recurring, import.
- Keep the legacy path until every adapter and replay case is green.
- Risk: medium to high; requires explicit implementation approval and full integration/browser validation.

### Phase 3 — settlement and document assemblers

- Extract account balance/settlement orchestration.
- Build HTML/PDF/email content from shared typed document models.
- Keep invoice snapshots and account locks unchanged.
- Risk: high because tax documents and frozen history are involved.

### Phase 4 — source modularization

- Split `models.go`, route registration, CSS source layers, and shared JavaScript by responsibility without changing delivered contracts.
- Remove compatibility code only after production data inventory proves it is no longer required.
- Risk: medium; mostly structural but broad.

### Phase 5 — optional product extensions

- Versioned fuel adjustment, favorites, basis-impact preview, and close/settle cockpit.
- Each feature should be a separate decision and PR.

## Production impact and manual steps

For the visual implementation in this change:

- Database impact: none.
- Migration impact: none.
- Configuration impact: none.
- Data rewrite: none.
- URL/API impact: none.
- Authorization impact: none.
- Manual production step: none beyond the normal application deployment.

The future refactoring phases are proposals only. They must be implemented as small behavior-preserving changes with explicit review before any schema or accounting behavior is touched.
