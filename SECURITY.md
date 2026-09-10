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
- **TOTP** with encrypted seeds at rest (AES-GCM, purpose-separated key), replay
  protection and hashed single-use recovery codes; **WebAuthn passkeys**,
- hashed session tokens, sliding and absolute expiry, list/revoke controls, and
  transactional password/session rotation; PostgreSQL-backed attempt admission
  before password/MFA verification,
- role-based access (administrator / editor / read-only),
- an **audit trail** plus request logging,
- a strict **Content-Security-Policy** (all assets served locally, no CDNs;
  `object-src 'none'`, `frame-ancestors 'none'`, and `upgrade-insecure-requests`
  over HTTPS) plus hardened HTTP security headers — `X-Content-Type-Options`,
  `X-Frame-Options: DENY`, `Referrer-Policy`, `Cross-Origin-Opener-Policy` and
  `Cross-Origin-Resource-Policy` (`same-origin`), `X-Permitted-Cross-Domain-Policies: none`,
  a restrictive `Permissions-Policy`, and **HSTS** over HTTPS,
- `HttpOnly`, `SameSite=Lax` session cookies (`Secure` and `__Host-` behind HTTPS),
- HMAC-bound CSRF tokens for authenticated mutations and a seeded login-CSRF flow,
- account deactivation that revokes credentials and sessions while retaining the
  user identity and append-only audit history,
- encrypted local/S3 backups with validation, restore rehearsal and key rotation.

## Hardening checklist for operators

- [ ] Set a strong, unique `SESSION_SECRET` (`openssl rand -hex 32`).
- [ ] Change `ADMIN_PASSWORD` and `POSTGRES_PASSWORD` from the defaults.
- [ ] Run behind a TLS-terminating reverse proxy; set `COOKIE_SECURE=true`.
      If forwarding headers are needed, enable `TRUST_PROXY` and configure
      `TRUSTED_PROXIES` for the actual proxy peers. Do not trust public clients'
      forwarding headers or expose an alternative unprotected HTTP route.
- [ ] Do **not** expose the database port or the app's `HOST_PORT` publicly.
- [ ] Configure the built-in backup schedule and persistent backup directory;
      keep encryption-key escrow outside the application host and test a restore.
      For S3, use a dedicated bucket/prefix and least-privilege credentials.
- [ ] Follow the [restore runbook](README.md) when restoring; stop other writers
      and keep the maintenance gate closed until reconciliation succeeds.
- [ ] Protect database and log access. Use separate migration/maintenance and
      runtime roles where deployed, and an independently protected audit archive
      when owner-resistant tamper evidence or legal holds are required.
- [ ] Keep the stack updated (Dependabot PRs, rebuild the image regularly).

## Known limitations

- This is a shared, single-tenant ledger: roles control write/admin access, not
  per-neighbor confidentiality.
- The audit trigger prevents ordinary updates/deletes, not a database owner or
  superuser. Credential/privilege changes use transactional audits; some other
  events remain best-effort and failures must be monitored.
- Default retention uses event creation time and retains business events through
  seven complete subsequent calendar years. Tax-period exceptions, longer duties
  and legal holds need an operator retention policy; this is not a compliance
  certification or a legal-hold system.
- Revocation blocks subsequent authentication; already-running requests may
  finish. Retired usernames remain reserved to preserve historical identity.
