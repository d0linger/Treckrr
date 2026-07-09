# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report privately via GitHub's **[Security Advisories](https://github.com/d0linger/Treckrr/security/advisories/new)**
("Report a vulnerability"), or by email to the maintainer listed on the GitHub
profile. Include:

- affected version / commit,
- steps to reproduce or a proof of concept,
- impact assessment if you have one.

You can expect an acknowledgement within a few days. Please allow reasonable time
for a fix before any public disclosure.

## Supported versions

This is a small self-hosted project; only the latest `main` receives fixes.
Always run the most recent build.

## Security features

Treckrr ships with:

- password hashing with **bcrypt**, a password policy and forced password change,
- **TOTP** two-factor authentication,
- session management (list/revoke active sessions) and **login rate limiting**,
- role-based access (administrator / editor / read-only),
- an **audit trail** plus request logging,
- a strict **Content-Security-Policy** (all assets served locally, no CDNs;
  `object-src 'none'`, `frame-ancestors 'none'`, and `upgrade-insecure-requests`
  over HTTPS) plus hardened HTTP security headers — `X-Content-Type-Options`,
  `X-Frame-Options: DENY`, `Referrer-Policy`, `Cross-Origin-Opener-Policy` and
  `Cross-Origin-Resource-Policy` (`same-origin`), `X-Permitted-Cross-Domain-Policies: none`,
  a restrictive `Permissions-Policy`, and **HSTS** over HTTPS,
- `HttpOnly`, `SameSite=Lax` session cookies (`Secure` behind HTTPS).

## Hardening checklist for operators

- [ ] Set a strong, unique `SESSION_SECRET` (`openssl rand -hex 32`).
- [ ] Change `ADMIN_PASSWORD` and `POSTGRES_PASSWORD` from the defaults.
- [ ] Run behind a TLS-terminating reverse proxy; set `COOKIE_SECURE=true` or
      `TRUST_PROXY=true`.
- [ ] Do **not** expose the database port or the app's `HOST_PORT` publicly.
- [ ] Enable automatic backups (`--profile backup`) and test a restore.
- [ ] Keep the stack updated (Dependabot PRs, rebuild the image regularly).

## Known limitations

- The TOTP secret is currently stored unencrypted in the database; protect
  database access and backups accordingly.
- CSRF protection relies on `SameSite=Lax` cookies rather than per-request
  tokens.

Contributions improving these are welcome.
