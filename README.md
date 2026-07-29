<div align="center">

# Treckrr 🚜

**Bill tractor & machine costs for agricultural neighbourly help — from a phone.**

Treckrr is a mobile-first Progressive Web App that replaces the hand-kept
spreadsheet farmers use to settle shared machine work (*Nachbarschaftshilfe*).
Work is booked per **neighbour** and **year**, priced automatically from a shared
rate list, offset against a running account, and exported as CSV or a shareable
receipt.

[![CI](https://github.com/d0linger/Treckrr/actions/workflows/ci.yml/badge.svg)](https://github.com/d0linger/Treckrr/actions/workflows/ci.yml)
[![Security](https://github.com/d0linger/Treckrr/actions/workflows/security.yml/badge.svg)](https://github.com/d0linger/Treckrr/actions/workflows/security.yml)
![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)
![PWA](https://img.shields.io/badge/PWA-installable-5A0FC8)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

</div>

> [!NOTE]
> **The user interface is German** — the app targets a German-speaking farming
> context. The **code, docs and configuration are English** so the project is easy
> to fork and adapt.

---

## Contents

- [Screenshots](#screenshots)
- [Why Treckrr](#why-treckrr)
- [How billing works](#how-billing-works)
- [Features](#features)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Deployment](#deployment)
- [Architecture](#architecture)
- [Development](#development)
- [Security & CI](#security--ci)
- [License](#license)

---

## Screenshots

<p align="center">
  <img src="docs/img/login.png" width="200" alt="Login — password + passkey">
  <img src="docs/img/dashboard.png" width="200" alt="Billing-year dashboard">
  <img src="docs/img/neighbor.png" width="200" alt="Neighbour — balance & new booking">
  <img src="docs/img/verrechnung.png" width="200" alt="Bookings & running account (Verrechnung)">
</p>
<p align="center">
  <img src="docs/img/beleg.png" width="200" alt="Shareable receipt (Beleg)">
  <img src="docs/img/stats.png" width="200" alt="Statistics — KPIs & charts">
  <img src="docs/img/menu.png" width="200" alt="Side-drawer navigation">
  <img src="docs/img/dashboard-dark.png" width="200" alt="Dark mode">
</p>
<p align="center"><sub>Mobile-first PWA · German UI · light &amp; dark themes · password + TOTP or one-tap passkey login · exact-decimal billing with running-account offset (Verrechnung) and a shareable receipt.</sub></p>

---

## Why Treckrr

Neighbours share expensive machinery: one farmer's tractor and baler does another's
field, hours get jotted down, and once a year somebody totals it all up in a
spreadsheet. That spreadsheet is fragile — a rate changes and last year's numbers
silently move, a formula breaks, and there is no record of who paid what.

Treckrr keeps the same mental model but makes it durable:

- **Prices live in one place** and are reused across years, so you edit a rate once.
- **Every booking freezes its price** at the moment it's made — editing a rate list
  later never rewrites history.
- **All money is exact decimal** (`NUMERIC` end to end), so totals reconcile to the
  cent.
- **It runs on the phone in the field**, installs like an app, and works offline for
  reading.

Self-hosted, single small Go binary, PostgreSQL, no CDNs — every byte of CSS, JS,
font and icon is served from your own host.

---

## How billing works

Two concepts are deliberately kept apart:

| Concept | German | What it is |
|---|---|---|
| **Rate basis** | *Bemessungsgrundlage* | A price list — tractors, machines, load levels and fixed rigs. Published only every few years and **shared by several billing years**. |
| **Billing year** | *Abrechnungsjahr* | A calendar year you create. It **picks one rate basis** and has its **own set of neighbours**. |

Hourly rates come straight from the original spreadsheet:

| Element | Formula |
|---|---|
| Tractor rate | `PS × cost-per-PS/h` (load level *light / medium / heavy*) |
| Machine rate | `working-width × cost-per-width/h` |
| Rig (*Gespann*) rate | tractor rate + Σ machine rates |
| Booking cost | `hours × rig rate` |

Each booking stores a **frozen price snapshot**, so historical exports never change
when a basis is edited later. Alongside the priced work, each neighbour has a
**running account** (*Verrechnung*): free-form offsetting entries — *"I owe you"* or
*"extra charge"* — that combine with the work total into a single **balance**
(*Saldo*). All amounts are computed and stored as exact decimals
(Postgres `NUMERIC` + `shopspring/decimal`), never floats.

```
 Rate basis (2024)  ──picked by──▶  Billing year 2024  ──has──▶  Neighbours
   tractors                             │                          │
   machines                             │                          ├─ bookings (frozen price)
   load levels                          │                          └─ ledger entries (Verrechnung)
   fixed rigs                           ▼                                     │
                                   completed → payment status                 ▼
                                                                         Saldo + receipt (Beleg)
```

---

## Features

**Billing years**
- Create a year, pick its rate basis, add neighbours — existing, brand new, or
  **carried over** from last year with per-neighbour checkboxes.
- Fast year switching; status *in progress* → *completed*.
- Completing a year **locks its bookings** and unlocks a per-neighbour **payment
  status** (*open* → *paid*) with paid/open totals. Years can be reopened.
- **Recalculate** flags bookings whose rate basis changed after they were made.

**Bookings**
- Book a **fixed rig** or a **free manual combination**, with a live rate preview
  driven by the pricing API.
- Create, **edit**, **quick multi-row entry**, **void** (stays visible but stops
  counting; reversible) or delete — with client-side validation.
- Excel-style neighbour overview (date, activity incl. rig detail, hours, cost)
  with totals and a per-activity summary.

**Neighbours & settlement**
- Central, global neighbour list — create/rename once, reused across years.
  Neighbours **with bookings can't be deleted**, only **deactivated / reactivated**;
  their history stays intact.
- **Running account** (*Verrechnung*): add offsetting debits/credits per neighbour
  that roll into the year's **balance**.
- Cross-year **history** per neighbour, including payment history.
- **Printable Beleg** — a compact, screenshot-ready receipt per neighbour/year for
  handing over or messaging.
- **CSV export** per year and per neighbour.

**Rate bases & master data**
- Editable name and "valid-from" year; **clone** a basis into a new one (the source
  stays untouched); delete while unused or **lock** it read-only.
- Manage tractors, machines, load levels and fixed rigs per basis in a dedicated
  workspace.
- Tractors/machines are **deactivatable** (kept for existing bookings), have a custom
  **sort order** and machine **categories** with filtering; rigs show a **cost
  breakdown**, and a **basis comparison** shows the rate delta (%) against another
  basis.

**Reporting** (`/stats`)
- KPIs (revenue, hours, paid/open), locally rendered **bar charts** and
  **sparklines** (per neighbour / activity / tractor — no JS framework) and a
  **year comparison**.

**Accounts, roles & auth**
- **Roles**: administrator, editor, read-only.
- **Login**: password + **TOTP two-factor** (setup QR + one-time **recovery codes**),
  or one-tap **passkeys / WebAuthn** (usernameless, biometric) — password + TOTP
  stay the fallback. Admins can reset a user's 2FA.
- **Sessions**: rolling (stay signed in while active), listable and revocable;
  password/role changes sign out other sessions.

**Platform**
- Installable **PWA** with an offline fallback, **dark mode** (light/dark/auto,
  remembered per device), native `<dialog>` confirmations, and content-hashed assets
  with automatic service-worker cache refresh.
- **Automatic database backups** via an optional Compose profile.

<details>
<summary><strong>Security hardening at a glance</strong></summary>

- **Secrets at rest**: TOTP secrets encrypted (AES-GCM), passwords bcrypt-hashed,
  recovery codes SHA-256-hashed, passkeys store only public keys.
- **Request safety**: CSRF tokens on all state-changing requests, **HSTS** over
  HTTPS, and a strict same-origin **Content-Security-Policy** (no external hosts).
- **Abuse resistance**: rate limiting on login and every sensitive action;
  TOTP replay protection.
- **Audit trail** (`/admin/audit`) with search, action filter and CSV export; every
  request is also logged to stdout.
- **Recoverable access**: the bootstrap admin is reconciled from environment
  variables on every start.

See [SECURITY.md](SECURITY.md) to report a vulnerability.

</details>

---

## Quick start

Requires Docker with Compose.

```bash
# 1. Configure — set at least SESSION_SECRET, ADMIN_PASSWORD, POSTGRES_PASSWORD
cp .env.example .env

# 2. Start (builds the app image, runs PostgreSQL as a standalone container)
docker compose up -d --build

# 3. Open  →  http://localhost:8080   (HOST_PORT from .env)
```

On first start the app runs its schema migrations and provisions the admin user.
The database begins **empty**: create a **rate basis** under **Grundlagen**, then a
**billing year** under **Jahre**, and add neighbours.

### Prebuilt image (GHCR)

A multi-arch image (`linux/amd64`, `linux/arm64`) is published to GitHub Container
Registry on every push to `main` and on `v*` release tags — run it without building:

```bash
docker compose -f docker-compose.ghcr.yml up -d
# pin a version instead of latest:
TRECKRR_TAG=1.2 docker compose -f docker-compose.ghcr.yml up -d
```

Image: `ghcr.io/d0linger/treckrr` (tags: `latest`, `main`, semver from release tags).

---

## Configuration

Copy `.env.example` to `.env` and adjust. The most important variables:

| Variable | Purpose |
|---|---|
| `SESSION_SECRET` | Cookie-signing secret, ≥ 16 chars (`openssl rand -hex 32`). **Change for production.** |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | Bootstrap admin, reconciled on **every** start |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | Database credentials |
| `DATABASE_URL` | Postgres connection string (defaults to the `db` container) |
| `COOKIE_SECURE` | `true` when served over HTTPS directly (or use `TRUST_PROXY`) |
| `TRUST_PROXY` | `true` behind a trusted reverse proxy — derives client IP & scheme from `X-Forwarded-*` |
| `RP_ID` / `RP_ORIGIN` | Passkeys: host (no scheme) and full origin, e.g. `treckrr.example.com` / `https://treckrr.example.com` |
| `APP_PORT` / `HOST_PORT` | Container / host port |
| `BACKUP_INTERVAL` / `BACKUP_KEEP` | Interval and retention of automatic backups |

> [!IMPORTANT]
> The admin password is reconciled from the environment on **every** start, so
> access is always recoverable through your Docker configuration.

---

## Deployment

### Behind a reverse proxy (Nginx Proxy Manager, Traefik, Caddy …)

The app speaks **plain HTTP on port 8080** — the proxy terminates TLS.

1. Set `TRUST_PROXY=true` so real client IPs (audit / rate-limit) and the `Secure`
   cookie flag are derived from `X-Forwarded-For` / `X-Forwarded-Proto`.
   **Only enable when the app is reachable *exclusively* through the proxy** —
   otherwise clients could spoof those headers.
2. Point the proxy at `treckrr-app:8080` (same Docker network) or the host IP, at
   the **domain root** (no sub-path). Websockets are not required.
3. For passkeys, set `RP_ID` / `RP_ORIGIN` to the **public** host.
4. Prefer **not** exposing `HOST_PORT` publicly — only the proxy needs it.
5. Health check endpoint: `GET /healthz`.

**Minimal Nginx example** — the line that matters most is `X-Forwarded-Proto`;
without it the app can't tell it is served over HTTPS, so it never sets `Secure`
cookies or HSTS:

```nginx
server {
    listen 443 ssl;
    server_name treckrr.example.com;
    client_max_body_size 10m;

    location / {
        proxy_pass         http://127.0.0.1:8080;   # or http://treckrr-app:8080 on the same network
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;   # REQUIRED for Secure cookies + HSTS
    }
}
```

With **Traefik** or **Caddy** the `X-Forwarded-*` headers are added automatically —
keep `TRUST_PROXY=true` and route to `treckrr-app:8080`.

<details>
<summary><strong>Deploying with Portainer</strong></summary>

Portainer's **web-editor** stack is processed by the Compose engine running *inside
the Portainer container*, which **cannot see files on the Docker host**. So:

- `env_file: /treckrr/.env` (or any host path) **fails** — Portainer can't read it
  and the deploy returns **HTTP 500**. Host `env_file` paths only work when you run
  `docker compose` **on the host itself**.
- Instead keep `${VAR}` placeholders in the compose file and supply the values in
  Portainer's **Environment variables** panel (Advanced mode accepts a whole `.env`
  at once). One set of variables can feed **both** the `db` and `app` services.
- Values in the panel are literal — no `$`/`#`/quote escaping.

To read a real `.env` **file** from disk instead, don't use the web editor — run the
stack on the host, where `env_file` works: `cd /treckrr && docker compose up -d`.

</details>

<details>
<summary><strong>Automatic backups</strong></summary>

```bash
docker compose --profile backup up -d      # daily pg_dump into ./backups
sh scripts/backup.sh                        # manual dump
sh scripts/restore.sh backups/<file>.dump   # restore
```

</details>

<details>
<summary><strong>Running rootless (rootless Docker / Podman)</strong></summary>

The stack runs under a **rootless** container engine with no changes:

- The app image runs as a **non-root** user (`treckrr`, UID 10001) with a
  **read-only root filesystem**, `no-new-privileges` and only a small `tmpfs` for
  `/tmp`; Postgres uses a named volume; nothing needs privileged ports or
  capabilities.
- On Ubuntu: install rootless Docker (`docker-ce-rootless-extras` + `uidmap`, then
  `dockerd-rootless-setuptool.sh install`), point at the user socket
  (`export DOCKER_HOST=unix:///run/user/$(id -u)/docker.sock`), run
  `loginctl enable-linger "$USER"`, then `docker compose up -d --build`. Runs
  unchanged under **rootless Podman** (`podman compose up`).
- The app listens on **8080** (non-privileged); TLS/443 is terminated by your proxy,
  so no `CAP_NET_BIND_SERVICE` is needed.
- The backup profile bind-mounts `./backups`; under rootless the dumps are owned by a
  mapped sub-UID — retrieve them with `docker compose cp` or make the directory
  writable for the mapping.

</details>

---

## Architecture

A single Go binary serves the API, the embedded templates and the local assets;
PostgreSQL holds the data. No runtime dependencies beyond the two containers.

```
cmd/treckrr        Entry point (HTTP server, graceful shutdown)
internal/config    Configuration from environment
internal/db        Connection pool (pgx) + embedded SQL migrations (auto-run on boot)
internal/models    Domain types
internal/calc      Cost model (exact decimals, unit-tested against the spreadsheet)
internal/auth      bcrypt hashing, session tokens, recovery codes, AES-GCM
internal/totp      RFC 6238 TOTP (pure Go)
internal/store     Database access (incl. passkeys, encrypted TOTP secrets)
internal/server    HTTP routing, middleware, handlers
internal/web       Embedded HTML templates & local assets (CSS / JS / icons / fonts)
```

**Dependencies** are few and audited: `pgx` (Postgres), `x/crypto`,
`shopspring/decimal`, `go-webauthn` and `rsc.io/qr` — everything else is the standard
library.

<details>
<summary><strong>Data model</strong></summary>

- `price_bases` — rate basis (lockable); `year` = "valid from".
- `load_levels`, `tractors`, `machines` — price data per basis.
- `gespanne` (+ `gespann_machines`) — fixed rigs per basis.
- `billing_years` — billing year; references **one** `price_bases`.
- `billing_year_neighbors` — which neighbours participate in a year.
- `neighbors` — global, reused across years.
- `entries` (+ `entry_machines`) — bookings with **frozen** price snapshots.
- `neighbor_ledger` — offsetting account entries (*Verrechnung*) per neighbour/year.
- `users` — accounts, roles and (optional) email.
- `sessions` — rolling login sessions; `login_attempts` — rate-limit counters.
- `webauthn_credentials` — registered passkeys (public keys only);
  `totp_recovery_codes` — hashed one-time recovery codes.
- `audit_log` — security-/data-relevant actions.

Fifteen ordered, embedded migrations run automatically on startup.

</details>

---

## Development

With local Go ≥ 1.26 and a reachable PostgreSQL:

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

See [CONTRIBUTING.md](CONTRIBUTING.md) to get involved.

---

## Security & CI

GitHub workflows under `.github/workflows/`:

- **CI** (`ci.yml`) — `go vet`, tests with the race detector, build, and `golangci-lint`.
- **Security** (`security.yml`) — `gosec` (static analysis) and `govulncheck` (known CVEs).
- **Dependency review** (`dependency-review.yml`) — on pull requests.
- **Gitleaks** (`gitleaks.yml`) — scans the full git history for leaked secrets.
- **DeadCode** (`deadcode.yml`) — fails the build on unreachable functions.
- **Docker** (`docker-publish.yml`) — builds the multi-arch image and pushes it to GHCR.

**Dependabot** keeps Go modules, GitHub Actions and the Docker base image current.
Vulnerability reports: [SECURITY.md](SECURITY.md).

---

## License

[MIT](LICENSE) — free to use, modify and distribute. Built only with free,
license-cost-free tools and libraries.
