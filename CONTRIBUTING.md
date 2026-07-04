# Contributing to Treckrr

Thanks for your interest in improving Treckrr! Contributions of all sizes are
welcome — bug fixes, features, docs, tests.

## Getting started

Fork the repo and clone your fork. The fastest way to run the full stack:

```bash
cp .env.example .env          # then edit the secrets
docker compose up -d --build  # http://localhost:8080
```

For a local Go workflow (Go ≥ 1.26 and a reachable PostgreSQL), see the
[Development section of the README](README.md#development).

## Project layout

The code is a standard Go layout under `internal/` (see the README's
Architecture section). Notable points:

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

Run the same checks CI runs:

```bash
gofmt -l .          # must print nothing
go vet ./...
go test ./...       # the CI also runs with -race
golangci-lint run   # if installed locally
```

Guidelines:

- Keep changes focused; one logical change per PR.
- Match the surrounding style (naming, comments, error handling). Wrap errors
  with context (`fmt.Errorf("...: %w", err)`).
- Add or update tests for behaviour changes, especially in `calc` and `store`.
- Update the README / docs when you change configuration, routes or features.
- Write clear commit messages (imperative mood, e.g. "Add payment history view").

## Reporting bugs & requesting features

Open a GitHub issue with steps to reproduce (for bugs) or a short rationale (for
features). For **security** issues, follow [SECURITY.md](SECURITY.md) instead of
opening a public issue.

## License

By contributing you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
