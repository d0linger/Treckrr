# Upgrade validation

Use this procedure for the production upgrade. The local source-build Compose
stack is development; a healthy local container is not production acceptance.

## Rehearse against a production backup

1. Record the running production image digest, PostgreSQL major version and last
   `schema_migrations.name`. Keep the existing secrets and persistent volumes.
2. Restore a fresh encrypted backup onto an isolated PostgreSQL server. Prove that
   it actually restores; an archive listing alone is insufficient. Block outbound
   mail, S3 and other external effects in the rehearsal application.
3. Capture the old application's read-only reports on the restored data: totals
   per year and neighbor, net/VAT/gross invoice amounts, payments, credits,
   outstanding balances, invoice numbers and frozen document hashes. Include
   issued, canceled, credited, partly paid and closed-year cases. Save the exports
   and compare the same reports after upgrading the isolated copy.
4. Run the stored-row fingerprint procedure below before and after migration.
   Investigate every difference. New schema objects and invoice-source mappings
   can be intended; changing an old booking, payment or invoice amount is not.
5. Exercise login/MFA, booking, invoice/PDF, payment and export flows on synthetic
   accounts in that isolated copy. Do not use a real customer for a write test.

## Compare the existing business rows

Stop all applications, schedulers and other writers for the comparison window.
Use an existing read-only database connection, supplied through your protected
PostgreSQL service/connection configuration. Do not put passwords in shell history.

The application runs its maintenance loop immediately after startup, including
due recurring bookings. Blocking browser access does not pause these jobs. Use a
migration-only harness on the isolated restored copy for a strict migration
comparison, then start the application for acceptance. During a real cutover,
record and inspect legitimate scheduler writes separately; do not dismiss a
fingerprint difference or promise byte-identical data across a normal app boot.

Before upgrading, generate a fixed projection of the old schema:

```sh
psql -XqAt -f .github/scripts/generate-business-fingerprint.sql > fingerprint.sql
psql -XqAt -f fingerprint.sql > before.txt
```

After upgrading, use **the same generated file**, without regenerating it:

```sh
psql -XqAt -f fingerprint.sql > after.txt
diff -u before.txt after.txt
```

Each line contains a table name, row count and SHA-256 over every existing column
of every row, sorted independently of physical row order. The generated queries
use a consistent, read-only transaction. Added columns are excluded by the old
projection; dropped/renamed old columns or tables fail the query. The generator
covers the explicit business-table list in its source and only tables that exist
before the upgrade. Review that list when adding business tables. Newly created
tables, default values and calculated report behavior need separate validation.
Authentication state, audit sealing, notification/mail processing and backup
status are operational changes, outside this business-row fingerprint.

The schema guard does not prove that a restore contains the production data.
Compare restored row counts and fingerprints with a baseline captured from the
live system before the upgrade. Two empty/schema-only snapshots can match each
other and still be an incomplete restore. Without that live baseline, report
restore completeness as unverified; do not require every business table to be
nonempty, since empty invoices or payments can be legitimate.

For an upgrade from before migration 0081, review the intentional reminder and
closing-checklist changes separately: invoice debt is separated from other
account debt, and an old annual sent marker does not establish that a particular
invoice was sent. The billing parity gate checks these exact differences.

## Production cutover

- Require green unit/race/integration, browser, screenshot and billing parity
  gates for the exact release. The pinned pre-0081 baseline is `f85ca89`; the
  parity runner uses synthetic isolated databases, not production records.
- Schedule a maintenance window; stop every old app instance and external
  writer. Take a final encrypted backup, copy it off the host and retain its key.
  Record the final old-data fingerprints and reports before starting the upgrade.
- Use `docker-compose.ghcr.yml` with a verified immutable release image, never an
  unreviewed moving `latest`. Migrations are automatic and forward-only; the
  rollout through 0095 includes new schema and neutral price-rule defaults.
- Configure HTTPS, `COOKIE_SECURE=true`, `ALLOW_INSECURE_HTTP=false`,
  `TRUST_PROXY=true` and the actual proxy CIDR in `TRUSTED_PROXIES`. Restrict direct
  app access; the database must not be publicly published. Set `RP_ID` and
  `RP_ORIGIN` to the real public host and HTTPS origin.
- Preserve `ENCRYPTION_SECRET` (including its effective fallback to
  `SESSION_SECRET`) and backup keys. Do not rotate them as part of this upgrade.
  Keep `ADMIN_PASSWORD_RESET=false`; provision unique production credentials.
- Leave `DEMO_MODE` off. Check SMTP, encrypted backup schedules, off-host storage,
  restore rehearsal, storage capacity and monitoring/alerts. BMD/RZL presets need
  a real import acceptance check with the intended accounting system.
- Prepare MFA/passkey recovery before enforcing the policy. Accounts with a
  required factor and only a passkey must use that passkey to sign in.
- Before reopening access, compare the final fingerprints and reports, confirm
  `/healthz` and `/readyz`, startup/migration logs, login and audit-chain status.
  Verify that no snapshot-backfill or TOTP migration errors were logged.
- Roll back using the retained matching backup and old image if needed. A simple
  old-image restart does not undo migrations. Keep writers stopped during recovery;
  restoring the pre-upgrade backup after reopening would discard subsequent work.

PostgreSQL documents consistent logical backups in [pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html).
The separate production configuration follows [Docker's production guidance](https://docs.docker.com/compose/how-tos/production/).
