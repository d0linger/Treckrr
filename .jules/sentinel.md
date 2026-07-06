## 2026-07-06 - [Cookie Security Centralization]
*Vulnerability:* Inconsistent or missing security attributes (`Secure`, `HttpOnly`, `SameSite`) on various cookies throughout the application, leading to multiple `gosec` G124 warnings.
*Learning:* Directly using `http.SetCookie` in multiple places makes it easy to forget security best practices for each new cookie. Centralizing cookie creation in a helper method ensures consistent defaults.
*Prevention:* Always use the `(s *Server) setCookie` helper method instead of `http.SetCookie` directly to ensure `Secure`, `SameSite`, and `Path` attributes are correctly applied based on the application configuration.
