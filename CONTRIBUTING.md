# Contributing to Treckrr

Thanks for your interest in improving Treckrr! Contributions of all sizes are
welcome — bug fixes, features, docs, tests.

## Getting started

Fork the repo and clone your fork. The fastest way to run the full stack:

```bash
cp .env.example .env          # then edit the secrets
docker compose up -d --build  # http://localhost:8080
```

For a local Go workflow you need Go ≥ 1.26.6 (the `go.mod` language floor; CI
and the image build on 1.27.3) and a reachable PostgreSQL:

```bash
export DATABASE_URL="postgres://treckrr:treckrr@localhost:5432/treckrr?sslmode=disable"
export SESSION_SECRET="$(openssl rand -hex 32)"   # 32 chars minimum, enforced at startup
export ADMIN_USERNAME=admin
export ADMIN_PASSWORD=dev-admin-password
go run ./cmd/treckrr
```

## Project layout

The code is a standard Go layout under `internal/`: `server` (HTTP handlers and
middleware), `store` (all SQL), `db` (pool and migrations), `calc` (the cost
model), `web` (templates and embedded assets), plus `auth`, `backup`, `mail`,
`pdf`, `totp` and `bankimport`. Notable points:

- HTML templates and all CSS/JS/icons live in `internal/web` and are embedded
  into the binary via `go:embed`. **After changing them you must rebuild the
  Docker image** (`docker compose up -d --build`) — a plain restart won't pick
  up template changes.
- SQL migrations are embedded in `internal/db/migrations` and run automatically
  on start. Add a new numbered file; never edit an already-released migration.
- The cost model lives in `internal/calc` and is unit-tested against the
  original spreadsheet values — keep those tests green.
- No CDNs: bundle any new asset locally and keep the Content-Security-Policy
  intact.

## Before you open a pull request

Run the fast checks first:

```bash
gofmt -l .          # must print nothing
go vet -tags=integration ./...
go test ./...       # unit-only; does not replace the database-backed CI suite
golangci-lint run --build-tags=integration   # if installed locally
```

For the full race/integration suite, use a **separate disposable PostgreSQL
server**, never the existing development or production database. The Docker
`make test` target expects that server on the `treckrr-itest` network and a user
allowed to create its owned scratch databases:

```bash
make test TEST_DATABASE_URL='<isolated disposable test database URL>' TEST_INVOICE_MIGRATION_DATABASES=1 PG_MAJOR=16
# Use PG_MAJOR=18 with a matching disposable PG18 server.
```

The explicit migration opt-in is required: without it, `make test` fails before
running tests instead of silently skipping historical-preservation/rollback
checks. `make check` requires the same variables.

For the old/new financial comparison and same-database upgrade probe, run
`make test-parity` (or `node .github/scripts/billing-parity.mjs`). This runner owns
and cleans up new isolated Docker resources, requires no database URL, and needs
the pre-migration commit `f85ca89377f22797dc66f65c37ae37943e18c18d` in local git
history. Retain that commit in repository history or a permanent reference: CI
tries a fixed-SHA fetch if checkout lacks it, but cannot recover an object that
the server no longer retains. The fetch does not restore checkout credentials;
if an anonymous fetch is unavailable (for example, for a private repository), it
fails clearly rather than changing permissions or substituting a newer baseline.
Reports remain under ignored `tmp/billing-parity/`.

CI runs the parity gate on pull requests and requires parity plus browser success
before promoting a production image. Existing standalone E2E check names remain
unchanged; publishing events also run their own browser gate. A separate weekly
or manually dispatched **Billing allocation fuzz** workflow explores randomized
allocation cases for 45 seconds without a database. None of these additions
uploads screenshots or generated comparison reports.

Guidelines:

- Keep changes focused; one logical change per PR.
- Match the surrounding style (naming, comments, error handling). Wrap errors
  with context (`fmt.Errorf("...: %w", err)`).
- Add or update tests for behaviour changes, especially in `calc` and `store`.
- Update the README / docs when you change configuration, routes or features.
- Write clear commit messages (imperative mood, e.g. "Add payment history view").
- Keep mockups, screenshots, private review reports, and generated test output in
  ignored local directories such as `tmp/`; never force-add them. Browser tests
  use semantic layout/accessibility checks rather than committed screenshot
  baselines. Existing README images, application icons, and synthetic test fixtures
  are intentional repository inputs, not cleanup targets.

## Reporting bugs & requesting features

Open a GitHub issue with steps to reproduce (for bugs) or a short rationale (for
features). For **security** issues, follow [SECURITY.md](SECURITY.md) instead of
opening a public issue.

## License

By contributing you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
