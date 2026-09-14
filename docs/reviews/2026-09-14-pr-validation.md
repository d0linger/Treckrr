# PR validation: 174, 175, 177, 179, 180, 181

Reviewed against local `dev` at `ed3bc8c` on 2026-09-14. All six PRs were open;
174, 175, 177, and 179 were drafts. GitHub check runs had no failure or pending
conclusion at the final head check (19 checks each for the drafts; 16 each for
the dependency PRs, including intentionally skipped/neutral checks).

Changes are applied locally to `dev`, not merged between branches. No push,
GitHub PR-state changes, deployment, or production-data access is part of this work.

## Decisions

| PR | Reviewed head | Decision and adaptation |
| --- | --- | --- |
| [174](https://github.com/d0linger/Treckrr/pull/174) | `fce689e853de` | Apply company decimal field limits, moved ahead of all parsing, including VAT. Reject oversized submissions without replacing existing settings with zero. |
| [175](https://github.com/d0linger/Treckrr/pull/175) | `21114cc3c6f5` | Apply step-up credential limits, corrected to bcrypt's 72 **bytes**, not 72 Unicode characters. Reject before database access/admission. Keep the existing password policy and pending-2FA validation. |
| [177](https://github.com/d0linger/Treckrr/pull/177) | `c8c5392e965e` | Apply 50-character date limits to payment edits and installments. Preserve existing date fallback behavior for ordinary legacy requests. |
| [179](https://github.com/d0linger/Treckrr/pull/179) | `a927e4405d8f` | Apply decimal/date limits to legacy labor and travel bookings before person/company lookups. Preserve master-data rates, overrides, rounding, and booking locks. |
| [180](https://github.com/d0linger/Treckrr/pull/180) | `6084d6a9f2ba` | Apply all eight Go dependency updates with the pgx connection/backup compatibility correction below. No Go language-floor or schema change. |
| [181](https://github.com/d0linger/Treckrr/pull/181) | `93fd3c4ba54e` | Apply Playwright 1.63.0 and its reviewed lockfile. Correct the login test's clock setup race exposed by the full run. CI's Node 22 satisfies its Node requirement. |

The security PR descriptions overstate the remaining unbounded-input exposure:
the current app already limits request bodies and bounds decimal parsing before
calling the decimal library. These changes provide explicit field validation,
avoid silent fallback writes, and reject oversized step-up input before admission;
they are not evidence of an independently reproduced unbounded DoS.

## pgx compatibility correction

[pgx 5.11 URI parsing](https://github.com/jackc/pgx/releases/tag/v5.11.0) follows
libpq rather than HTML form query semantics: a literal `+` is not a space.
Rewriting the original DSN with `net/url.Values.Encode` could therefore turn an
encoded password/option space into a different value.

- Database connections now let pgx parse the original DSN, add missing timeout
  defaults on the parsed configuration, and open the pool from that configuration.
  Explicit operator values, credentials, driver options, and both DSN forms survive.
- Backup command preparation preserves raw URI components while moving every
  password assignment into the child environment. Literal/encoded plus signs,
  spaces, hash characters, duplicate parameters, IPv6, and multiple hosts are
  covered. Parser errors do not echo credentials.
- Restore rehearsal replaces only the target database and removes both database
  query aliases, so neither can override the generated scratch target.

This additional adaptation follows the database skill's dependency review.
No SQL, migration, invoice calculation, or data-conversion change was required.

Deployment note: query-string spaces must be written as `%20`, and literal
plus signs may be written as `%2B` to avoid ambiguity. A DSN that previously
used raw `+` to mean a space needs that configuration correction before
deploying pgx 5.11. Production connection settings were not inspected or changed.

## Validation

Validation is run against fresh, disposable databases under the Docker project
`treckrr-pr-validation`. The browser fixture is served at
`http://localhost:18089`; existing local and production applications are untouched.

- Go 1.27.1 unit/race tests: 628 test/subtest results passed (212 top-level
  tests), 94 database-dependent tests skipped
  without a database URL; no failures. The tagged database matrix is checked
  separately below.
- Targeted input-limit regressions cover all affected fields, 72-byte ASCII and
  UTF-8 password boundaries, rejected submissions before database access,
  accepted payment/date boundaries, and saved company values.
- `golangci-lint v2.13.1`: zero issues, including the configured security checks.
- `deadcode v0.49.0 -test -tags=integration ./...`: no unreachable functions.
- `govulncheck v1.6.0`: zero reachable symbol or imported-package vulnerabilities.
  It reports [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) at module level:
  the unmaintained `x/crypto/openpgp` packages, which Treckrr does not import.
  This advisory applies to all versions, including the old dependency; it has
  no version-only fix and does not justify dropping bcrypt or this update.
- `npm ci --ignore-scripts`: exact lockfile installation, zero npm audit findings.
- Login-animation suite: all 17 tests passed on three consecutive runs (51/51).
- Complete Playwright 1.63.0 / Chromium 153 suite: **93/93 passed**, no retries
  or skipped tests. Includes light/dark accessibility, mobile/keyboard behavior,
  forms, all unified-booking variants, invoices/payments, offline replay, and
  the continuous login animation.
- Gitleaks 8.24.3 staged scan (`protect --staged --redact`): no leaks.
  A preliminary contextless stdin scan flagged only old/new public WebAuthn
  `go.sum` checksums; the file-aware scan applies the normal lockfile rules.
- Module download/checksum verification, `go mod tidy -diff`, tagged `go vet`,
  `go build ./...`, and the production Dockerfile build all passed.

- PostgreSQL 16.15 and 18.6, with matching client tools and Go 1.27.1:
  **845 test/subtest results passed on each** (327 top-level tests), zero
  failures, **62.9% statement coverage** on each. Runs use
  `-race -tags=integration -p 1 -count=1 -shuffle=on`.
  Each run skips only `TestVerifyS3ObjectRejectsBadIntegration`, because no
  external S3 test endpoint is configured. Database, backup/restore/rehearsal,
  auth, company, payment, and labor/travel integration checks pass.
- Final focused server/database/backup regressions passed again after fixture
  and formatting adjustments.

The initial browser run passed 92/93 tests. The remaining test failed in fixture
setup, before loading the login page: it installed a running clock and then
attempted to pause at the same timestamp after that timestamp had passed.
Install now starts before the fixed pause target, following
[Playwright's clock guidance](https://playwright.dev/docs/clock#test-polling).
The animation implementation and its continuous-motion assertions are unchanged.

A subsequent browser launch attempt could not start because the executable in
the synced workspace was replaced by `*.exe-chunking-*` files. Validation uses
a Treckrr-specific Windows temporary browser cache outside that folder; no
global browser or synchronization settings were changed.
