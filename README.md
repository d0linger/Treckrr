# Treckrr 🚜

Treckrr is a mobile-first **Progressive Web App** for billing **tractor and
machine costs** in agricultural neighbourly help (*Nachbarschaftshilfe*). It
replaces a hand-maintained spreadsheet: work is booked per **neighbour** and
**year**, priced automatically from a shared rate basis, and exported to CSV.

Written in **Go**, data in **PostgreSQL**, shipped with **Docker**. No CDNs — all
CSS/JS/icons are served locally, and the dependency set is small and audited
(`pgx`, `x/crypto`, `shopspring/decimal`, `go-webauthn`, `rsc.io/qr`).

[![CI](https://github.com/d0linger/Treckrr/actions/workflows/ci.yml/badge.svg)](https://github.com/d0linger/Treckrr/actions/workflows/ci.yml)
[![Security](https://github.com/d0linger/Treckrr/actions/workflows/security.yml/badge.svg)](https://github.com/d0linger/Treckrr/actions/workflows/security.yml)
![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![PWA](https://img.shields.io/badge/PWA-installable-5A0FC8)

> **Note on language:** the user interface is **German** (the app targets a
> German-speaking farming context). The codebase, docs and configuration are in
> English so the project is easy to fork and adapt.

---

## Screenshots

<p align="center">
  <img src="docs/img/login.png" width="240" alt="Login with password + passkey">
  <img src="docs/img/dashboard.png" width="240" alt="Billing-year overview">
  <img src="docs/img/menu.png" width="240" alt="Side menu / drawer">
</p>
<p align="center">
  <img src="docs/img/neighbor.png" width="240" alt="Neighbour bookings">
  <img src="docs/img/stats.png" width="240" alt="Statistics and charts">
  <img src="docs/img/dashboard-dark.png" width="240" alt="Dark mode">
</p>
<p align="center"><sub>Mobile-first PWA · German UI · light &amp; dark themes · password + TOTP or one-tap passkey login · exact-decimal billing.</sub></p>

---

## The cost model

The hourly rates come straight from the original spreadsheet:

| Element | Formula |
|---|---|
| Tractor rate | `PS × cost-per-PS/h` (load level *light / medium / heavy*) |
| Machine rate | `working-width × cost-per-width/h` |
| Rig (*Gespann*) rate | tractor rate + Σ machine rates |
| Booking cost | `hours × rig rate` |

Two concepts are deliberately separated:

- **Rate basis** (*Bemessungsgrundlage*) — the price list, published only every
  few years and **shared by several billing years**. Holds tractors, machines,
  load levels and fixed rigs.
- **Billing year** (*Abrechnungsjahr*) — a calendar year you create yourself. It
  **picks one rate basis** and has its **own set of neighbours**.

Bookings store a **frozen price snapshot**, so historical exports never change
when a basis is edited later. All money is computed and stored as **exact
decimals** (Postgres `NUMERIC` + `shopspring/decimal`) — no floating-point
rounding drift.

---

## Features

**Billing years**
- Create a year, pick its rate basis, add neighbours (existing, new, or carried
  over from the previous year with per-neighbour checkboxes).
- Fast year switching via pills; **status** *in progress* / *completed*.
- Completing a year **locks bookings** (no create/delete) and enables a
  per-neighbour **payment status** (*open* by default → *paid*), with paid/open
  totals. Years can be reopened.

**Neighbours**
- Central management: create/rename globally. Neighbours **with bookings can’t be
  deleted**, only **deactivated / reactivated** — existing bookings stay intact.
- Per-neighbour cross-year history incl. payment history.

**Rate bases & master data**
- Editable name and “valid-from” year; clone values into a new basis (source
  stays unchanged); delete while unused, or lock (freeze) read-only.
- Manage costs and rigs per basis in a workspace with back button and sub-tabs.
- Tractors/machines **deactivatable** (kept for existing bookings), custom
  **sort order**, machine **categories/tags** with filter, rig **cost breakdown**,
  and a **basis comparison** showing the rate diff (%) against another basis.

**Bookings**
- Fixed rig **or** free manual combination, with a live rate preview.
- Create, **edit**, **quick multi-row entry**, **void** (stays visible but no
  longer counts; reversible) or delete; client-side validation.
- Excel-style neighbour overview (date, activity incl. rig detail, hours, cost)
  with totals and a per-activity summary. CSV export per year and per neighbour.

**Reporting** (`/stats`)
- KPIs (revenue, hours, paid/open), locally rendered **bar charts** (per
  neighbour / activity / tractor, no JS framework) and a **year comparison**.

**Security & administration**
- **Roles**: administrator, editor, read-only.
- **Login options**: password + **TOTP two-factor** (with one-time **recovery
  codes** and a setup QR), or one-tap **passkeys / WebAuthn** (usernameless,
  biometric) — passwords/TOTP remain the fallback. Admins can reset a user's 2FA.
- **TOTP secrets are encrypted at rest** (AES-GCM); passwords are bcrypt-hashed,
  recovery codes SHA-256-hashed, and passkeys store only public keys.
- **CSRF protection** on all state-changing requests, **HSTS** over HTTPS, and a
  strict **Content-Security-Policy** (everything served same-origin).
- **Rolling sessions** (stay signed in while active), **session management**
  (list/revoke), sign-out of other sessions on password/role change, and
  **rate limiting** on login and every sensitive action.
- **Audit trail** (`/admin/audit`) with search, action filter and CSV export;
  every request is also logged to stdout.
- Bootstrap admin is provisioned from environment variables on every start.

**Platform**
- Installable **PWA** with offline fallback, **dark mode** (light/dark/auto,
  remembered per device), native `<dialog>` confirmations, content-hashed asset
  versioning with automatic service-worker cache refresh.
- **Automatic database backups** via an optional Compose profile.

---

## Quick start (Docker)

Requires Docker with Compose.

```bash
# 1. Configure
cp .env.example .env
#    Set at least: SESSION_SECRET, ADMIN_PASSWORD, POSTGRES_PASSWORD, DATABASE_URL

# 2. Start (builds the app image, runs PostgreSQL as a standalone container)
docker compose up -d --build

# 3. Open
#    http://localhost:8080   (HOST_PORT from .env)
```

### Prebuilt image (GHCR)

A multi-arch image (`linux/amd64`, `linux/arm64`) is published to GitHub
Container Registry on every push to `main` and on `v*` release tags. To run it
without building locally:

```bash
docker compose -f docker-compose.ghcr.yml up -d
# pin a version instead of latest:
TRECKRR_TAG=1.2 docker compose -f docker-compose.ghcr.yml up -d
```

Image: `ghcr.io/arumes31/treckrr` (tags: `latest`, `main`, semver from release tags).

On first start the app runs schema migrations, provisions the admin user, and
seeds an example **rate basis 2023** (spreadsheet values incl. rigs) plus a
**billing year 2025** with three sample neighbours. Add further years under
**Jahre**.

### Environment variables

| Variable | Purpose |
|---|---|
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | Bootstrap admin (reconciled on every start) |
| `SESSION_SECRET` | Random value, ≥ 16 chars (`openssl rand -hex 32`) |
| `COOKIE_SECURE` | Set `true` behind HTTPS (or use `TRUST_PROXY`) |
| `TRUST_PROXY` | `true` behind a trusted reverse proxy |
| `RP_ID` / `RP_ORIGIN` | Passkeys (WebAuthn): your host (no scheme) and full origin, e.g. `treckrr.example.com` / `https://treckrr.example.com`. Defaults to `localhost` / `http://localhost:8080` for local dev |
| `DATABASE_URL` | Postgres connection (default points at the `db` container) |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | Database credentials |
| `APP_PORT` / `HOST_PORT` | Container / host port |
| `BACKUP_INTERVAL` / `BACKUP_KEEP` | Interval and retention of automatic backups |

> The admin password is reconciled from the environment on **every** start, so
> access is always recoverable via Docker configuration.

### Behind a reverse proxy (Nginx Proxy Manager, Traefik, Caddy …)

The app speaks **plain HTTP on port 8080** — the proxy terminates TLS.

1. In `.env` set `TRUST_PROXY=true` so real client IPs (audit/rate-limit) and
   the `Secure` cookie flag are derived from `X-Forwarded-For` /
   `X-Forwarded-Proto`. **Only enable when the app is reachable *exclusively*
   through the proxy** (otherwise clients could spoof these headers).
2. Point the proxy at `treckrr-app:8080` (same Docker network) or the host IP.
   Websockets are not required. Serve at the **domain root** (no sub-path).
3. Prefer **not** exposing `HOST_PORT` publicly — only the proxy needs access.

### Automatic backups

```bash
docker compose --profile backup up -d      # daily pg_dump into ./backups
sh scripts/backup.sh                        # manual dump
sh scripts/restore.sh backups/<file>.dump   # restore
```

### Running rootless (rootless Docker / Podman)

The stack runs under a **rootless** container engine with no changes:

- The app image runs as a **non-root** user (`treckrr`, UID 10001) with a
  **read-only root filesystem**, `no-new-privileges` and only a small `tmpfs`
  for `/tmp`; Postgres uses a **named volume**, and nothing needs privileged
  ports or capabilities.
- On Ubuntu: install rootless Docker (`docker-ce-rootless-extras` + `uidmap`,
  then `dockerd-rootless-setuptool.sh install`), point at the user socket
  (`export DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock`), run
  `loginctl enable-linger "$USER"` so it survives reboots, then
  `docker compose up -d --build` as usual. It also runs unchanged under
  **rootless Podman** (`podman compose up`).
- The app listens on **8080** (non-privileged); TLS/443 is terminated by your
  reverse proxy (`TRUST_PROXY=true`), so no `CAP_NET_BIND_SERVICE` is required.
- The optional backup profile bind-mounts `./backups`; under rootless the dumps
  are owned by a mapped sub-UID on the host — retrieve them with
  `docker compose cp` or make that directory writable for the mapping.

---

## Architecture

```
cmd/treckrr        Entry point (HTTP server, graceful shutdown)
internal/config    Configuration from environment
internal/db        Connection pool + embedded SQL migrations
internal/models    Domain types
internal/calc      Cost model (exact decimals, unit-tested vs. the spreadsheet)
internal/auth      Password hashing (bcrypt), session tokens, recovery codes, AES-GCM
internal/totp      RFC 6238 TOTP (pure Go)
internal/store     Database access (incl. passkeys, encrypted TOTP secrets)
internal/server    HTTP routing, middleware, handlers
internal/web       Embedded HTML templates & local assets (CSS/JS/icons)
```

### Data model (short)

- `price_bases` — rate basis (lockable); `year` = “valid from”.
- `load_levels`, `tractors`, `machines` — price data per basis.
- `gespanne` (+ `gespann_machines`) — fixed rigs per basis.
- `billing_years` — billing year; references **one** `price_bases`.
- `billing_year_neighbors` — which neighbours participate in a year.
- `neighbors` — global, reused across years.
- `entries` (+ `entry_machines`) — bookings per year with **frozen** price
  snapshots so exports and history stay stable.
- `sessions` — rolling login sessions; `login_attempts` — rate-limit counters.
- `webauthn_credentials` — registered passkeys (public keys only);
  `totp_recovery_codes` — hashed one-time recovery codes.
- `audit_log` — security-/data-relevant actions.

---

## Development

Without Docker (local Go ≥ 1.26 and a reachable PostgreSQL):

```bash
export DATABASE_URL="postgres://treckrr:treckrr@localhost:5432/treckrr?sslmode=disable"
export SESSION_SECRET="dev-secret-please-change-01"
export ADMIN_USERNAME=admin
export ADMIN_PASSWORD=admin123
go mod tidy
go run ./cmd/treckrr
```

Checks:

```bash
go test ./...
go vet ./...
```

---

## CI & security tooling

GitHub workflows under `.github/workflows/`:

- **CI** — `go vet`, tests with the race detector, build, and `golangci-lint`.
- **Security** — `gosec` (static analysis) and `govulncheck` (known CVEs).
- **CodeQL** — semantic code scanning for Go, JavaScript and Actions.
- **Dependency review** — on pull requests.
- **GitSecret** — `gitleaks` scans the full git history for leaked secrets.
- **DeadCode** — `golang.org/x/tools/cmd/deadcode` fails the build on
  unreachable functions.
- **Docker** — builds the multi-arch image and pushes it to GHCR
  (`ghcr.io/arumes31/treckrr`).

**Dependabot** keeps Go modules, GitHub Actions and the Docker base image current.

See [SECURITY.md](SECURITY.md) for how to report vulnerabilities and
[CONTRIBUTING.md](CONTRIBUTING.md) to get involved.

---

## License

[MIT](LICENSE) — free to use, modify and distribute. Only free, license-cost-free
tools and libraries are used.
