<div align="center">

<img src="internal/web/static/icons/favicon.svg" width="104" alt="Treckrr logo">

# Treckrr

**Self-hosted cost-sharing ledger for agricultural machinery** — neighbours book each
other's tractors and implements, Treckrr prices the work from a shared rate basis and
issues tax-correct Austrian invoices. Go, PostgreSQL, Docker.

[![CI](https://github.com/d0linger/treckrr/actions/workflows/ci.yml/badge.svg)](https://github.com/d0linger/treckrr/actions/workflows/ci.yml)
[![Security](https://github.com/d0linger/treckrr/actions/workflows/security.yml/badge.svg)](https://github.com/d0linger/treckrr/actions/workflows/security.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-e8763a.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-1.26.6+-00ADD8?logo=go&logoColor=white)
![PWA](https://img.shields.io/badge/PWA-offline--capable-2f6f4f)
![Self-hosted](https://img.shields.io/badge/self--hosted-Docker-2496ED?logo=docker&logoColor=white)

</div>

Treckrr is for a **Maschinengemeinschaft**: a handful of neighbouring farms that share
machinery and settle up once a year. Bookings are priced from one agreed rate basis, so
nobody argues about the hourly rate, and the year closes with a frozen invoice per
neighbour that satisfies § 11 UStG.

> The application UI is in **German**; this README is in English.
>
> All assets are served **locally** — no CDNs, no tracking, strict CSP. Your data stays
> on your machine.

---

## 📸 Screenshots

> The screenshots follow your theme automatically — **light on GitHub's light theme, dark on its dark theme**.

<table>
<tr>
<td align="center"><b>Anmelden</b></td>
<td align="center"><b>Übersicht</b></td>
<td align="center"><b>Beleg</b></td>
<td align="center"><b>Grundlage</b></td>
</tr>
<tr>
<td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/img/login-m-dark.png"><img alt="Sign-in with password or passkey" src="docs/img/login-m-light.png" width="200"></picture></td>
<td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/img/dashboard-m-dark.png"><img alt="Year overview with per-neighbour balances" src="docs/img/dashboard-m-light.png" width="200"></picture></td>
<td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/img/beleg-m-dark.png"><img alt="Receipt with itemised bookings, payments and open balance" src="docs/img/beleg-m-light.png" width="200"></picture></td>
<td><picture><source media="(prefers-color-scheme: dark)" srcset="docs/img/grundlagen-m-dark.png"><img alt="Rate basis with tractors, load levels and implements" src="docs/img/grundlagen-m-light.png" width="200"></picture></td>
</tr>
</table>

<p align="center"><b>Jahresübersicht</b> — every neighbour's hours, cost and payment state for the billing year, with batch invoicing and CSV export</p>
<p align="center"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/img/dashboard-dark.png"><img alt="Year overview: total cost, per-neighbour balances and payment state" src="docs/img/dashboard-light.png" width="760"></picture></p>

---

## ✨ Features

- **Bookings** priced from a shared rate basis — tractor PS × load level, or per unit/area — with implements, rigs ("Gespanne"), receipt photos, recurring series, quick capture, copy-from-existing, a duplicate warning, and storno that voids without deleting. A helper from the Personenstamm can ride along with a rig booking — on the form, in the quick-entry table and in a series — as linked man-hours priced from their own rate.
- **Rate bases** per year: tractors, load levels, implements and rigs. Compare two bases side by side, and lock one so its prices stop moving.
- **Recalculation**: after a rate change, preview every affected booking old → new and apply it in one step. Bookings on an already-issued invoice are left alone.
- **Billing years** per neighbour with ledger positions, reversible payment cancellations, carry-forward between years, carrying members over from last year, plus archiving and GDPR anonymisation.
- **Invoices** frozen at issue time (§ 11 UStG snapshot) with sequential numbering, storno, credit notes, PDF, EPC QR code and optional e-mail with send tracking. **Sammel-Festschreibung** issues a whole year at once, showing per neighbour what would be issued, blocked or skipped.
- **Austrian VAT modes**: Kleinunternehmer (no VAT shown), flat-rate § 22, or standard taxation — company data, IBAN and payment terms flow into every document.
- **Public receipt links** — expiring, revocable, hash-stored tokens so a neighbour can view their invoice without an account.
- **Account statement** per neighbour across all years, as a page and as PDF.
- **Dunning** (Mahnwesen): overdue list, printable reminder, PDF, EPC QR, CSV export.
- **Import/export**: CSV bookings with preview, bank statement payment matching, per-neighbour GDPR export, year and neighbour CSV.
- **Authentication**: password + optional TOTP with recovery codes, passkeys (WebAuthn, user verification required), roles (admin / editor / viewer), and session management with per-device revocation.
- **Append-only audit log** enforced by a database trigger, filterable and exportable, with staggered retention (1 year operational, 7 years for § 132 BAO records).
- **Encrypted backups** (AES-256-GCM) on a schedule, optional off-box S3 target, guarded GUI restores and an offline CLI recovery workflow.
- **PWA** with offline booking capture that replays under the user who captured it when the connection returns.
- Full-text search with typo tolerance, statistics per year and across all years, light/dark theme, Prometheus metrics (opt-in).

Server-rendered Go templates with vanilla JS — no frontend build step, no CDN, strict CSP.

## 🚀 Quickstart (Docker Compose)

```bash
git clone https://github.com/d0linger/treckrr.git && cd treckrr
cp .env.example .env
```

Generate the secrets and put them in `.env`:

```bash
openssl rand -hex 32   # SESSION_SECRET
openssl rand -hex 32   # BACKUP_ENCRYPTION_KEY (optional, enables backups)
```

Set at minimum **SESSION_SECRET**, **ADMIN_PASSWORD** and **POSTGRES_PASSWORD** — and put the same database password inside **DATABASE_URL**. The app refuses to start while any of them still holds the documented placeholder value. The local source-build profile explicitly enables direct HTTP for development; do not reuse that setting in production.

```bash
docker compose up -d
```

Open **http://localhost:8080** and log in as `admin`. You are forced to change the bootstrap password on first login.

**Prebuilt image instead of building locally** (multi-arch, amd64 + arm64):

```bash
TRUSTED_PROXIES=10.0.0.5/32 \
docker compose -f docker-compose.ghcr.yml up -d
```

The GHCR profile is the production/TLS path: it forces Secure cookies, enables
proxy trust, and requires `TRUSTED_PROXIES`. Pin a release rather than tracking
`latest` with `TRECKRR_TAG=1.4`.

---

## ⚙️ Configuration / Environment Variables

| Variable | Description | Default | Required |
| --- | --- | --- | --- |
| **DATABASE_URL** | Postgres DSN (URL or keyword/value form) | — | Yes |
| **SESSION_SECRET** | Signing key, min. 32 chars, not the placeholder | — | Yes |
| **ADMIN_PASSWORD** | Bootstrap admin password, changed at first login | — | Yes |
| **POSTGRES_PASSWORD** | Database password; must match `DATABASE_URL` | — | Yes |
| **POSTGRES_USER** / **POSTGRES_DB** | Database role and name | `treckrr` | No |
| **ADMIN_USERNAME** | Bootstrap admin login name | `admin` | No |
| **APP_PORT** | Port inside the container | `8080` | No |
| **HOST_PORT** | Published port on the host | `8080` | No |
| **HOST_BIND** | Host interface to bind; `127.0.0.1` only reaches a proxy on the host itself | `0.0.0.0` | No |
| **COOKIE_SECURE** | Force the `Secure` cookie flag | `true` (local Compose overrides to `false`) | No |
| **ALLOW_INSECURE_HTTP** | Explicit direct-HTTP opt-in for local development only | `false` (local Compose sets `true`) | No |
| **TRUST_PROXY** | Honour `X-Forwarded-For` / `-Proto` from a reverse proxy | `false` | No |
| **TRUSTED_PROXIES** | Comma-separated CIDRs allowed to set forwarded headers | — | No |
| **ENCRYPTION_SECRET** | Data-at-rest key for TOTP secrets; pin to the OLD value before rotating `SESSION_SECRET` | `SESSION_SECRET` | No |
| **RP_ID** / **RP_ORIGIN** | WebAuthn relying party host and origin; must match the browser URL | `localhost` / `http://localhost:8080` | No |
| **ADMIN_PASSWORD_RESET** | Break-glass: reset the admin password on next boot | `false` | No |
| **BACKUP_ENCRYPTION_KEY** | Min. 16 chars; empty disables backups entirely | — | No |
| **BACKUP_DIR** / **BACKUP_STATUS_FILE** | Dump directory and status file | `/backups` | No |
| **BACKUP_KEEP** | Dumps to retain — seeds the GUI value on first boot only, after that Admin → Backup wins | `7` | No |
| **BACKUP_ENCRYPTION_KEY_OLD** | Previous key, for `rotate-key` (see below) | — | No |
| **BACKUP_REHEARSE_URL** | Postgres URL allowed to create/drop a scratch DB; enables real restore rehearsals | — | No |
| **S3_ENDPOINT** / **S3_BUCKET** | Off-box backup target; empty disables it | — | No |
| **S3_KEEP** | Objects to keep in the bucket, 0 = all; seeds the GUI value on first boot only | `0` | No |
| **S3_ACCESS_KEY** / **S3_SECRET_KEY** / **S3_PREFIX** | S3 credentials and mandatory installation-unique key prefix when S3 is enabled | — | No |
| **S3_USE_SSL** | TLS for the S3 endpoint | `true` | No |
| **SMTP_HOST** / **SMTP_FROM** | E-mail delivery; both must be set to enable it | — | No |
| **SMTP_PORT** / **SMTP_USER** / **SMTP_PASSWORD** | SMTP credentials | `587` | No |
| **SMTP_STARTTLS** | Use STARTTLS | `true` | No |
| **METRICS_TOKEN** | Min. 16 chars; enables `GET /metrics` behind a bearer token | — | No |
| **LOG_FORMAT** / **LOG_LEVEL** | `text`\|`json`, `debug`\|`info`\|`warn`\|`error` | `text` / `info` | No |

### Rotating the backup key

Changing `BACKUP_ENCRYPTION_KEY` on its own orphans every existing dump: new
backups use the new key, older ones can no longer be opened. Rotate instead:

```bash
# Stop every app instance/scheduler before rotating; keep the database running.
docker compose stop app
# BACKUP_ENCRYPTION_KEY = new key, BACKUP_ENCRYPTION_KEY_OLD = previous key.
# Use docker-compose.ghcr.yml as the base instead when that is your deployment.
docker compose -f docker-compose.yml -f compose.rotate-key.yml run --rm --no-deps app rotate-key
```

Every **local** dump is re-encrypted, verified as restorable **with the new key**, and only
then replaced atomically. A dump that fails verification is left untouched and
reported, so a partial run degrades to "some files still use the old key" — never
to an unreadable archive. The command is resumable: run it again after fixing
whatever failed. Remove `BACKUP_ENCRYPTION_KEY_OLD` from the deployment environment
after success, then recreate the app with its new key. **Do not destroy the old
key**: S3, off-host copies and object versions are not rotated by this command.
Keep a versioned recovery-key record in separate secure storage for as long as
any archive still needs it. Rehearse a representative retained remote archive
with its original key before retiring any key. The previous key is deliberately
mapped only by the CLI override, not into the long-running app.

The same applies to `ENCRYPTION_SECRET` (TOTP secrets at rest): pin it to the old
value before rotating `SESSION_SECRET`, as the table above notes.

### Rehearsing a restore

The backup panel's "Restore getestet" used to be stamped by a table-of-contents
read — that proves the file is a well-formed archive, not that it loads. Set
`BACKUP_REHEARSE_URL` to a Postgres URL that may create and drop a scratch
database, then:

```bash
docker compose run --rm app rehearse-restore
```

It restores the newest dump into a unique `treckrr_rehearsal_<random>` database, checks the applied
migrations and the money tables, drops the scratch database and reports timings.
Only this stamps `restore_tested`; the cheap per-backup check now reports itself
separately as `archive_verified`.

Use a dedicated rehearsal server/role with CREATE DATABASE permission, not a
production database owner. The scratch name is unique per invocation; existing
databases are never dropped to make room. Status-file persistence failures make
the command fail even when the restore drill itself succeeded.

The backup schedule is a cron expression set in the admin Backup panel, not an environment variable.

---

## 💾 Prerequisites & Data Volumes

- **Docker** with Compose v2. No Go, Node or Postgres needed on the host — everything builds and runs in containers.
- **RAM**: the compose files cap the app at 768 MB and Postgres at 1 GB, so keep ~1.8 GB free. Idle use is roughly 15 MB and 31 MB.
- **Port 8080** published on the host (`HOST_PORT`). The database port is not published.
- Migrations run automatically at startup and are forward-only — **take a backup before upgrading**.

Persist these two named volumes:

| Volume | Contents |
| --- | --- |
| `pgdata` | PostgreSQL data directory — all business data |
| `backups` | Encrypted dumps and `status.json` |

Backups next to the database are not backups: set the **S3_\*** variables, or sync the `backups` volume to another machine.

**Restore** is available to administrators in the Backup panel and as an offline
CLI command. Both require typed confirmation. For the CLI, stop **all** app
instances and external schedulers first; leave PostgreSQL running:

```bash
docker compose stop app  # 120-second stop grace, not a forced kill
docker compose run --rm --no-deps app restore --test /backups/<file.dump.enc>
docker compose run --rm --no-deps app restore /backups/<file.dump.enc>
# Only after restore + reconciliation succeed and recovery checks pass:
docker compose up -d app
```

Serving binaries hold a database advisory lease; the CLI refuses a restore while
a participating app is running. The GUI drains local requests and background
maintenance, admits only one restore, and refuses while another participating
instance is present. **First upgrade:** older binaries do not hold this lease;
stop every older instance before relying on the safeguard. These leases do not
replace fencing/stop procedures for other writers or a database connection loss.
A failed or uncertain GUI restore/reconcile keeps maintenance enabled; inspect
logs and complete recovery before restarting. A successful explicit restore
migrates the schema and invalidates all restored sessions and WebAuthn challenges.
The restore transaction itself omits their archived rows, so a crash immediately
after the restore commit cannot resurrect a revoked login.
Review restored passwords, disabled accounts, recovery credentials and revoked
share links against post-backup security changes before reopening access.

Online archives and dump output are capped at **128 MiB**, with one memory-heavy
backup operation admitted per app process. This also bounds S3 downloads and
verification; an oversized archive is rejected without deleting it. Older larger
archives remain recoverable via an offline CLI process with explicitly provisioned
RAM/tmp space and `BACKUP_CLI_MAX_BYTES` (bytes, 1 MiB–16 GiB). Pass this only to that
process (for Compose, `run -e BACKUP_CLI_MAX_BYTES ...` with the value supplied in
the environment), and raise that one-off container's memory/tmpfs limits in a
reviewed local override. Raising this variable alone does **not** allocate RAM.
The current whole-archive format needs several times the archive size plus the
64 MiB key-derivation workspace. Do not run overlapping CLI jobs in a live app's
memory budget.

S3 requires a normalized, installation-unique prefix. New objects carry a stable
ownership marker and are created conditionally, so a collision is reported rather
than overwritten. Retention is limited to owned flat `treckrr-*.dump.enc` objects
under that prefix and never deletes objects younger than 24 hours (or with unknown
dates). Existing untagged objects remain readable but are never auto-pruned.
`S3_KEEP=0` keeps everything. Scope the S3 credentials to the same prefix.
Retention env values seed the GUI settings only on first boot; existing GUI values
remain authoritative.

### Verified release images

The release workflow publishes a candidate, scans that exact digest for both
amd64 and arm64, attaches per-platform SBOM artifacts, and promotes the same
manifest index without rebuilding only after the gates pass. Repository names
are lowercased for registry use. Tags (including `sha-*`) remain mutable aliases;
pin production to the digest shown in the workflow summary. Serialized promotion
rejects superseded branch refs; operators must still publish semantic-version
tags in order so a historical release does not move a minor-version alias back.

The weekly supply-chain job rescans the published digest and, when configured,
repository variable `PRODUCTION_IMAGE_DIGEST` (`sha256:...`, the multi-arch index)
without rebuilding. Update that variable after each deployment. An unset value
reports unknown deployed-image coverage; scanning `latest` is not deployment
inventory. Candidate tags from failed gates must never be selected for deployment.

For local core Go checks, `make check` requires an isolated `TEST_DATABASE_URL`
on the `treckrr-itest` network and matching `PG_MAJOR=16` or `18`; it runs race and
tagged integration tests serially. `make test-unit` is explicitly unit-only.
Security scanners, browser tests, image builds and registry promotion remain
separate CI jobs, not claims made by the Make target.

---

## 🧮 Cost calculation

A booking is priced from the rate basis, not typed in by hand. The tractor
contributes its power at the load level chosen for the job; every implement on the
rig adds its working width:

```
tractor rate  =  PS            ×  cost per PS   (of the load level)
machine rate  =  working width ×  cost per AB
rig rate      =  tractor rate  +  Σ machine rates
booking cost  =  hours         ×  rig rate
```

Worked example — a 130 PS tractor at load level *mittel* (0.36 €/PS) pulling a 3.0 m
power harrow (14.50 €/m), for 4.5 hours:

```
tractor   130 × 0.36  =  46.80 €/h
harrow    3.0 × 14.50 =  43.50 €/h
rig                   =  90.30 €/h
booking   4.5 × 90.30 = 406.35 €
```

Jobs that aren't billed by the hour use a **unit** instead — hectares, cubic
metres, a flat charge — with quantity × unit price.

Every step is exact decimal arithmetic rounded to two places, never binary
floating point, so totals reconcile to the cent. The cost model lives in
`internal/calc` and is unit-tested against the values from the original
spreadsheet the app replaced.

Changing a rate later does **not** silently reprice past bookings. They keep the
price they were booked at; the **Recalculation** view shows every affected booking
old → new so the change is applied deliberately.

---

## 👥 Roles & permissions

Every account has exactly one role. There is no per-object sharing — this is a
single-tenant app where everyone who signs in sees the whole ledger.

| Role | Can | Cannot |
| --- | --- | --- |
| **admin** | everything, plus users, company data, audit log and backups | — |
| **editor** | create and edit bookings, neighbours, rate bases, invoices, payments | reach anything under `/admin` |
| **viewer** | read everything, export CSV/PDF | any change except to their own account (password, 2FA, passkeys, sessions) |

A viewer's write attempt is refused server-side, not just hidden in the UI. An
admin can reset another user's password or 2FA; both end that user's sessions
immediately.

---

## 📈 Health, readiness & metrics

| Endpoint | Auth | Purpose |
| --- | --- | --- |
| `GET /livez` | public | Process is alive. **No database call** — a DB outage must not make an orchestrator kill a healthy container. |
| `GET /readyz` | public | App **and** database reachable. Returns 503 during a restore. |
| `GET /healthz` | public | Legacy alias of `/readyz`. |
| `GET /metrics` | Bearer token | Prometheus text format. Only registered when **METRICS_TOKEN** is set and at least 16 characters. |

The container's own `HEALTHCHECK` polls `/healthz` every 30 s.

Metrics cover process state (`treckrr_uptime_seconds`, `go_goroutines`,
`go_memstats_*`) and the connection pool — `treckrr_db_connections_open`,
`_in_use`, `_idle`, `_max_open`, plus `treckrr_db_wait_total` and
`treckrr_db_wait_seconds_total`. Those two are the ones to alert on: they only
move when requests are queuing for a connection.

```bash
curl -H "Authorization: Bearer $METRICS_TOKEN" http://localhost:8080/metrics
```

---

## 🌐 Running behind a reverse proxy

Forward the standard headers and tell the app to trust them:

```
proxy_set_header Host              $host;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

```bash
COOKIE_SECURE=true
ALLOW_INSECURE_HTTP=false
TRUST_PROXY=true
TRUSTED_PROXIES=10.0.0.5/32        # the proxy's address — see below
RP_ID=treckrr.example.org          # host only, no scheme
RP_ORIGIN=https://treckrr.example.org
```

- **Set TRUSTED_PROXIES.** The app refuses to start with `TRUST_PROXY=true` and
  an empty allow-list. Restrict it to your proxy, or set
  `HOST_BIND=127.0.0.1` — but only when the proxy is installed natively on this
  host or runs with `network_mode: host`. A proxy in its own container on a bridge
  network cannot reach the host's loopback; put it on the same Docker network as
  the app and point it at `app:8080` instead, publishing no host port at all.
- **X-Forwarded-For is read right-to-left.** The right-most entry is the address
  the proxy actually observed; earlier ones are client-supplied. This assumes
  exactly one trusted hop.
- **RP_ID / RP_ORIGIN must match the browser's URL**, or passkeys silently fail.
- **Cookies** fail closed to `Secure` and get the `__Host-` prefix. The only
  plain-HTTP exception is the explicit local-development combination
  `COOKIE_SECURE=false`, `TRUST_PROXY=false`, `ALLOW_INSECURE_HTTP=true`.
- **Let the app own HSTS.** It already sends
  `max-age=31536000; includeSubDomains` over HTTPS. If the proxy adds a second
  header, browsers process only the first (RFC 6797 §8.1) and the other is dead.

---

## 🛡️ Rootless & hardened deployment

The shipped Compose file is hardened by default; nothing below needs to be
switched on.

| | app | db |
| --- | --- | --- |
| User | non-root, UID **10001** | postgres (UID 70) |
| Root filesystem | `read_only: true` | writable (data dir) |
| Capabilities | `cap_drop: ALL` | default set |
| `no-new-privileges` | yes | yes |
| Memory / PID limit | 768 MB / 128 | 1 GB / 256 |
| Writable paths | `tmpfs /tmp`, capped at 320 MB | `pgdata` volume |

The app writes nothing to disk — state is in PostgreSQL, logs go to stdout — so
the root filesystem stays read-only. `/tmp` is a **capped** tmpfs because
multipart uploads spill there, and a tmpfs is RAM: uncapped, a large upload
writes its way into host memory instead of failing cleanly.

The database port is **not published**. The app port is, on `HOST_BIND`
(default `0.0.0.0`); set it to `127.0.0.1` if your proxy is local.

Everything works the same under rootless Docker or Podman.

---

## 🔒 Security

- **Passwords**: bcrypt. A failed login runs a dummy comparison so a wrong
  username costs the same time as a wrong password.
- **Sessions**: 256-bit random tokens, stored only as SHA-256 hashes. Sliding
  30-day expiry with a hard 90-day cap, revocable per device.
- **Second factor**: TOTP with one-time recovery codes, seeds encrypted at rest
  under a key derived separately from the session secret. Passkeys (WebAuthn)
  require user verification, and each ceremony is server-side and single-use.
  Known edge (go-webauthn 0.18): a client that returns *unsolicited* extension
  outputs fails the ceremony by design; password + TOTP remain available as the
  fallback, so a login is never lost — if a passkey suddenly stops working after
  a browser update, that is the first thing to check.
- **Rate limits** on login by IP *and* by target account, on the 2FA step, on
  password step-up, and on passkey challenge creation — all in PostgreSQL, so
  they survive a restart.
- **CSRF**: every state-changing request carries an HMAC token bound to the
  session cookie; the login form has its own seeded token.
- **Headers**: strict CSP with no `unsafe-inline`, `frame-ancestors 'none'`,
  nosniff, `Referrer-Policy: same-origin`, COOP/CORP same-origin, HSTS over
  HTTPS. No CDNs — every asset is embedded in the binary.
- **Audit log** is append-only, enforced by a database trigger rather than
  application code.
- **Financial history is protected by the database**: neighbours and billing
  years carrying bookings, payments, ledger entries or invoices cannot be
  deleted (`ON DELETE RESTRICT`). GDPR erasure is pseudonymisation, which keeps
  the § 132 BAO records intact.
- **Backups** are AES-256-GCM encrypted with a key held separately from the
  session secret, and can be validated before a restore.

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

---

## 🔁 CI / CD

| Workflow | What it does |
| --- | --- |
| **CI** | `go vet`, `go test -race` against a real PostgreSQL service, `go build`, module verification and a tidiness check, golangci-lint |
| **Security** | gosec static scan and govulncheck, weekly as well as on push |
| **Supply chain** | Trivy over the repository *and* over the built image (the Alpine layer nothing else inspects), CycloneDX SBOM generated from the image |
| **E2E** | Playwright smoke test: login → booking → Beleg |
| **DeadCode** | unreachable-function detection |
| **GODep / GitSecret** | dependency review on PRs, gitleaks secret scanning |
| **Docker** | multi-arch image (amd64 + arm64) to GHCR — **gated on the tests passing**, so a red build publishes nothing |

Every action is pinned to a full commit SHA, base images are pinned by digest,
and workflow tokens are least-privilege (`packages: write` only on the job that
pushes).

---

## 📄 License / Contributing

MIT — see [LICENSE](LICENSE). Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md) for the build and test commands, and [SECURITY.md](SECURITY.md) for reporting vulnerabilities privately.
