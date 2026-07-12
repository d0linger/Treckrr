## 2026-07-06 - [Cookie Security Centralization]
*Vulnerability:* Inconsistent or missing security attributes (`Secure`, `HttpOnly`, `SameSite`) on various cookies throughout the application, leading to multiple `gosec` G124 warnings.
*Learning:* Directly using `http.SetCookie` in multiple places makes it easy to forget security best practices for each new cookie. Centralizing cookie creation in a helper method ensures consistent defaults.
*Prevention:* Always use the `(s *Server) setCookie` helper method instead of `http.SetCookie` directly to ensure `Secure`, `SameSite`, and `Path` attributes are correctly applied based on the application configuration.

## 2026-07-07 - [Inconsistent CSV Sanitization]
*Vulnerability:* CSV Injection (Formula Injection) in the audit trail export. While the main billing export was protected, the audit log export lacked sanitization for user-controlled fields.
*Learning:* Security mitigations must be applied to all export paths, not just the primary ones. Attackers can use usernames or audit details to trigger malicious formulas in spreadsheet software.
*Prevention:* Always use the `csvSafe` helper when exporting user-provided text to CSV files. Ensure that any new export functionality follows the established sanitization pattern.

## 2026-07-08 - [Distributed Brute-Force Risk in TOTP Login]
*Vulnerability:* The TOTP verification step (second step of login) was only protected by IP-based rate limiting. An attacker with a compromised password could use a botnet to brute-force the 6-digit TOTP code across many IPs.
*Learning:* IP-based rate limiting is insufficient for secondary authentication factors. Once a user is identified (e.g., after a successful password check), per-user rate limiting must be applied to all subsequent sensitive steps.
*Prevention:* Apply per-user rate limiting (e.g., using `sensitiveBlocked`) to the TOTP/recovery-code verification flow. Failures should be recorded against the specific user account to mitigate distributed attacks.
