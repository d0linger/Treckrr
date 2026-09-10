# CodeRabbit review validation — 2026-09-10

Validated against complete Treckrr `dev` at `91151d1`, not the partial review
worktrees. The two completed IDE reviews covered 77 core/storage files and
99 web/other files. Both reported **unverified findings**. This record covers
all 33 findings (including one outside-diff finding) and 12 nitpicks.

Result: 41 items addressed, three claims not confirmed, one optional performance
rewrite deferred. Some valid findings use safer alternatives to the proposed
patch; their remaining boundaries are explicit below. No migration was changed.
Review IDs: core `ecf043a8-453b-447b-9790-e82a3f9614fa`,
web `338d3e99-530f-40d8-8902-d1a5cf1a0d25`. Finding IDs below are unique prefixes.

## Core/storage findings

| Finding ID | Location / issue | Disposition and evidence |
| --- | --- | --- |
| `5b14ff25` | `backup/backup.go`: interrupted atomic temporary files | Fixed narrowly scoped cleanup of old regular staging temporary files. Tests preserve recent files, completed backups and unrelated files. |
| `4bb01051` | `bankimport/bankimport.go`: payer name after IBAN | Fixed name-column search; preserved historical import hashes for this header order to prevent duplicate payments on re-import. Regression covers name and hash. |
| `39bed4dc` | `bankimport/bankimport.go`: batch amount mismatch | Split details must sum exactly to credited entry amount. Incomplete/excess batches are skipped rather than misallocated; tests cover both and a matching batch. |
| `52ca30ad` | `config/config.go`: negative S3 retention | Configuration now rejects negative `S3_KEEP`; regression added. |
| `be764cc5` | migration 0042: outbox cascade | Mitigated through transactional parent-deletion guards and visible blocker counts. All retained outbox rows block deletion, including the interval between marking mail sent and recording its history. Concurrency tests cover both parents. See boundary below. |
| `9a96efb9` | migration 0043: dunning-history cascade | Same locked parent/history guard preserves dunning records during application deletion; concurrency tests cover both parents. Existing migrations left intact. |
| `0d0b8f43` | migration 0043: negative fees | Store writes now reject negative fees in company settings, dunning notices and retry-mail metadata. Tests cover these paths. Database constraints remain unchanged; direct SQL is outside this protection. |
| `0142416a` | `pdf/invoice.go`: Skonto without IBAN | Frozen discount terms now render independently of IBAN. Generated fixture checked by extracted text and rendered page. |
| `8ee797b1` | `pdf/mahnung.go`: fee/deadline overlap | Page-space calculation includes every optional row and final text height. Reproducing pagination regression passes; two-page fixture visually inspected. |
| `69dbf67f` | `pdf/mahnung.go`: fee-only payment instructions | Uses total payable, not principal alone. Fee-only fixture contains payment instructions and correct amount; visually inspected. |
| `a627c4aa` | `store/company.go`: omitted invoice start | Zero/omitted start preserves the stored value atomically in SQL, without a racy read-modify-write. Settings regression passes. |
| `13baeb80` | `store/entries.go`: invisible conflicting replay | Not confirmed. PostgreSQL waits for the competing insert; the following READ COMMITTED statement sees the committed row. Added a real blocking/concurrent replay test; genuine missing-row errors are not suppressed. |
| `86b80e4e` | `store/invoice_journal.go`: invented legacy corrections | Only ordinary legacy invoices may rebuild from live bookings. Snapshotless reversals, credits and advances stay unavailable rather than acquiring fabricated invoice amounts. Regression covers all kinds. |
| `bdeb3c6a` | outbox integration tests: global queue interference | Tests now operate in their own random scratch databases, so callbacks cannot process another test's mail. Both lifecycle tests pass. |
| `e4f6a3ca` | `store/persons.go`: delete/check race | Parent lock, history check and deletion now share a transaction. Regression proves an in-flight booking cannot silently lose its helper attribution. |
| `52cd6710` | restore reconciliation test: fixed database name | Random 128-bit scratch database names; cleanup drops only the database created by that invocation. |
| `34a08311` | `store/stats_query.go`: mutable historical machine rates | Corrected misleading contract, UI and CSV: money figures are explicitly current-rate estimates. Exact historical component allocations cannot be recovered from the stored aggregate rig rate. Future booking-time allocations require separate schema/design work. No amounts were guessed or rewritten. |

The delivery-history fix protects application deletion paths, including concurrent
inserts through foreign-key parent locks. It deliberately does **not** rewrite
already-applied migrations or detach queued mail with `SET NULL`, which would
change retry/history semantics. Direct privileged SQL deletion can still invoke
the existing cascades. Database-level constraints would require a new reviewed
migration; this is defense-in-depth beyond the implemented application guards.

## Web and outside-diff findings

| Finding ID | Location / issue | Disposition and evidence |
| --- | --- | --- |
| `54ca28ed` | README configuration table interrupted | Restored all configuration rows to the same table, before backup sections. |
| `36894644` | bank integration test: nil neighbor dereference | Errors/nil checked before reading IBAN or formatting failure output. |
| `31dc62d7` | company: invalid discount silently disables terms | Invalid/out-of-range values now reject the entire save with feedback; tests verify previous settings remain unchanged and valid boundaries/comma decimals work. |
| `3e14462d` | neighbor: invalid payment term silently defaults | Nonempty malformed/out-of-range values reject the save; blank still means company default. Regression verifies no mutation. |
| `b05a22f2` | batch reminders: late failure hides earlier sends | Per-neighbor data/PDF errors increment failure count and continue; summary preserves completed/queued outcomes and advises retrying only affected neighbors. Malformed middle snapshot regression proves first and last recipients queue once. |
| `c9b95489` | test cleanup: literal prefix instead of sweep | Explicit `ithandler` prefix sweep for users and their audit rows; normal cleanup still uses exact usernames, not wildcard interpretation of underscores. |
| `758d7b87` | metrics: missing failed S3 gauge | Matches the actual failed status and emits zero; regression added. |
| `3414d46c` | metrics: invalid ages | Zero/future backup and restore timestamps omitted. Valid timestamps remain nonnegative; regression added. |
| `876ab15c` | statistics: synthetic task drilldown | Unlabeled bookings shown as “Ohne Tätigkeit” without a false exact-match link. A real task named “Sonstige” retains its link; regression covers both. |
| `5d0b950e` | statistics test: allegedly shared price mutation | Not confirmed. `newItEnv` owns and cleans its entire price base through `t.Cleanup`, including fatal exits. Corrected the misleading reset comment; no redundant cleanup added. |
| `7cd72c63` | audit filter test: midnight race | All windows derive from the actual recorded login timestamp, not separate clock reads. |
| `42199194` | offline rejection has no correction path | Added correction editor for rejected single/quick submissions. Original payload retained for export; owner, scope, row count and replay keys immutable. Saves check current queue state and never resurrect discarded/sent items or overwrite a newer edit. Explicit retry required. Browser tests cover both paths and partial-batch deduplication. |
| `f0ef9588` | closed-year document mutation controls | Completion guard applied to advance cancellation, advance/credit creation and invoice corrections. Template tests compare open/closed states with and without an invoice. |
| `35ac129a` | duplicate neighbor empty states | Removed duplicate fallback; template tests cover active, archived and all scopes. |
| `c93ffa2a` | import confirmation count ignores manual matches | Generic confirmation includes selected assignments without claiming the automatic count is final. |
| `d2268f9b` | main fallback forgets S3 retention | Backup fallback now carries configured `S3Keep`. |

Offline correction intentionally cannot move a booking to another neighbor/year
or invent new replay keys: partially accepted submissions would otherwise risk
duplication. Missing master data may still need restoration/reconfiguration.
Export and explicit discard remain available; foreign-owner and unstamped items
remain quarantined. Already accepted rows are not edited by a retry.

## Nitpicks

| Finding ID | Issue | Disposition |
| --- | --- | --- |
| `27607728` | Sequence regex letter concatenated into SQL | Parameterized. Existing callers were constants, so this is hardening, not a confirmed injection exploit. |
| `2432edbe` | Invoice settings scratch DB collision | Uses the same random, invocation-owned scratch database helper. |
| `43f6d479` | Photo-count doc comment misplaced | Attached to the correct function. |
| `e6a3603e` | Recurring-person test claims current year globally | Moved into its own scratch database; no other process's year can be purged. |
| `66225e86` | Incomplete `BelegSend` model | Query now populates ID and both parent IDs as well as channel/time. |
| `355766b9` | Dunning-list doc comments misplaced | Attached to their respective functions. |
| `b1476961` | Unbounded advisory-lock wait | Acquisition bounded to 30 seconds and connection closed on failure. |
| `39c75a61` | E2E button requires year 2025 | Matches the stable action and a four-digit year. |
| `303d3c59` | Fee test accepts any value starting with 5 | Exact `5.00` comparison. |
| `fac8ad67` | SMTP audit error lacks sanitization | Applies the same control-character sanitization as structured logging. This does not redact recipient/host information. |
| `8c8f01f4` | Booking decimal parsing allegedly unbounded | Not confirmed: shared `formDecimal` → `parseGermanDecimalOK` already rejects inputs longer than 32 characters and exponent notation before decimal parsing. |
| `6a412785` | Replace per-invoice regex matching with tokens | Deferred optional optimization. No demonstrated latency regression; token joining must preserve punctuation, boundaries and longest-reference behavior. Benchmark and equivalence tests should precede this rewrite. |

## Verification

- Database-backed Go integration suites use only isolated Treckrr test databases,
  not normal application data. New concurrency/validation tests use the
  `integration` build tag.
- Eleven offline Playwright tests pass, using real scripts/IndexedDB and mocked
  HTTP, including correction, export, repeated replay, ownership isolation and
  transient failures. Mobile correction rendering inspected.
- Invoice-without-IBAN, fee-only reminder and paginated reminder fixtures pass
  extracted-text checks and rendered-page inspection; pagination unit test passes.
- Store database context, row-close and dynamic-SQL scan found no additional
  confirmed defects in the reviewed functions.
- Full `go test -tags=integration -p 1 ./... -count=1 -timeout=300s` passed
  against the isolated PostgreSQL test service (server 43.194s, store 34.457s).
- `go vet -tags=integration ./...` passed; repository-pinned Docker
  golangci-lint v2.13.1 with integration tags reports **0 issues**.
- `git diff --check` passed. No normal service restart, deployment, push or
  branch synchronization.

Parkrr, the global Memory junction, shared settings and detached review worktrees
remain untouched. Project-memory updates use Treckrr's local memory junction.
