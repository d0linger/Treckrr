## 2026-07-06 - [Cookie Security Centralization]
*Vulnerability:* Inconsistent or missing security attributes (`Secure`, `HttpOnly`, `SameSite`) on various cookies throughout the application, leading to multiple `gosec` G124 warnings.
*Learning:* Directly using `http.SetCookie` in multiple places makes it easy to forget security best practices for each new cookie. Centralizing cookie creation in a helper method ensures consistent defaults.
*Prevention:* Always use the `(s *Server) setCookie` helper method instead of `http.SetCookie` directly to ensure `Secure`, `SameSite`, and `Path` attributes are correctly applied based on the application configuration.

## 2026-07-07 - [Inconsistent CSV Sanitization]
*Vulnerability:* CSV Injection (Formula Injection) in the audit trail export. While the main billing export was protected, the audit log export lacked sanitization for user-controlled fields.
*Learning:* Security mitigations must be applied to all export paths, not just the primary ones. Attackers can use usernames or audit details to trigger malicious formulas in spreadsheet software.
*Prevention:* Always use the `csvSafe` helper when exporting user-provided text to CSV files. Ensure that any new export functionality follows the established sanitization pattern.

## 2026-07-08 - [Authentication DoS via Oversized Input]
*Vulnerability:* CPU-exhaustion (DoS) risk during authentication. The account-scoped rate limiter was skipped for usernames over 100 characters. An attacker could bypass the limiter and send extremely long usernames or passwords, triggering database queries and costly dummy bcrypt hashing comparisons on the server.
*Learning:* When designing account-scoped rate limiters, bypassing them for over-limit payloads to prevent "trash entries" in the rate-limit store can leave a back-door where those massive payloads hit the database and expensive bcrypt hashing anyway.
*Prevention:* Always strictly validate and reject oversized inputs (such as usernames > 100 runes or passwords > 72 bytes) at the very beginning of the authentication flow before any database querying or password comparison/dummy-comparison happens.
