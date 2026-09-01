package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/web"
)

// pageData is the map passed to templates. Handlers add page-specific keys.
type pageData map[string]any

// newPage returns page data pre-filled with common fields (user, flash, nav).
func (s *Server) newPage(w http.ResponseWriter, r *http.Request, title, active string) pageData {
	u := userFromCtx(r)
	p := pageData{
		"Title":    title,
		"Active":   active,
		"User":     u,
		"BasePath": r.URL.Path,
		"Theme":    s.themeFromCookie(r),
		"CSRF":     s.csrfToken(r),
	}
	// Backup-health dot in the header, for everyone who can act on it (admins +
	// Erfasser, never Nur-Leser). Cheap status-file read; the template decides
	// what each role sees (admins get the manage link).
	if u != nil && u.CanWrite() {
		p["BackupHealth"] = s.backupHealth()
	}
	if msg, kind, undoURL := s.readFlash(w, r); msg != "" {
		p["FlashMessage"] = msg
		p["FlashKind"] = kind
		if undoURL != "" {
			p["FlashUndo"] = undoURL
		}
	}
	return p
}

// serverError logs the underlying error with a short context tag and answers
// with a generic 500. Handlers should prefer this over a bare http.Error so a
// production failure leaves a diagnosable trace (which call failed, and why)
// without leaking internals to the client.
func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	slog.Error("internal error", "what", sanitizeLog(what), "err", sanitizeLog(errMsg))
	writeErrorPage(w, http.StatusInternalServerError, "Interner Fehler",
		"Es ist ein unerwarteter Fehler aufgetreten. Bitte versuche es erneut.")
}

// handleNotFound is the mux fallback for any path that no route matches; it
// renders the branded 404 instead of net/http's plain-text default.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeErrorPage(w, http.StatusNotFound, "Seite nicht gefunden",
		"Diese Seite existiert nicht oder wurde verschoben.")
}

// badRequest renders the branded error page for a 400 on a user-facing HTML route
// (a manipulated URL, a stale form, a missing/invalid parameter) instead of
// net/http's bare plain-text default. Do NOT use it on JSON/fetch API endpoints —
// those must keep a plain status the client-side code can parse.
func (s *Server) badRequest(w http.ResponseWriter, msg string) {
	if strings.TrimSpace(msg) == "" {
		msg = "Die Anfrage war ungültig."
	}
	writeErrorPage(w, http.StatusBadRequest, "Ungültige Anfrage", msg)
}

// errorPageTmpl is a self-contained, brand-consistent error document: it links
// the app stylesheet (so fonts/colors match) and follows the system light/dark
// preference via prefers-color-scheme. Standalone so it needs neither a session
// nor the full page layout, and works for authenticated and anonymous requests.
var errorPageTmpl = template.Must(template.New("errpage").Parse(
	`<!doctype html><html lang="de"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>{{.Title}} · Treckrr</title>` +
		`<link rel="stylesheet" href="/static/css/app.css?v={{.Version}}"></head>` +
		`<body class="errpage-body"><main class="errpage"><div class="errpage__card">` +
		`<div class="errpage__code">{{.Status}}</div>` +
		`<h1 class="errpage__title">{{.Title}}</h1>` +
		`<p class="errpage__msg">{{.Msg}}</p>` +
		`<a class="btn btn--primary" href="/">Zur Übersicht</a>` +
		`</div></main></body></html>`))

func writeErrorPage(w http.ResponseWriter, status int, title, msg string) {
	var buf bytes.Buffer
	if err := errorPageTmpl.Execute(&buf, map[string]any{
		"Status": status, "Title": title, "Msg": msg, "Version": web.AssetVersion(),
	}); err != nil { // never happens for this fixed template
		http.Error(w, title, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// render executes the named page template's "layout" into the response.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	tpl, ok := s.templates[page]
	if !ok {
		slog.Error("unknown template", "page", sanitizeLog(page))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if _, exists := data["AssetVersion"]; !exists {
		data["AssetVersion"] = web.AssetVersion()
	}
	// Render to a buffer first so a template error does not emit a half page.
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("render failed", "page", sanitizeLog(page), "err", sanitizeLog(err.Error()))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	// Inject a hidden CSRF token into every POST form of the rendered page so
	// templates stay token-agnostic; validated by the csrf middleware.
	out := injectCSRFField(buf.Bytes(), s.csrfToken(r))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(out)
}

// ---- Flash messages (cookie based) --------------------------------------

// The flash cookie is HMAC-signed, because its contents are rendered into the
// page AND can arm an action. layout.html turns FlashUndo into a POST form, and
// injectCSRFField then stamps a VALID CSRF token into that form — so an
// attacker-written flash cookie would be a one-click, CSRF-valid state change to
// any same-origin path, plus an attacker-chosen message to talk the user into
// clicking it. Signing means only this server can mint one; the __Host- prefix
// (setCookie) additionally stops a sibling host under the same registrable domain
// from writing the cookie at all. The baked-in expiry bounds replay of a captured
// cookie, since MaxAge is enforced by the client and an attacker holds the jar.
const flashTTL = 30 * time.Second

// signFlash returns "base64url(payload).hex(hmac)". The "flash:" context prefix
// binds the MAC to this use, mirroring the "csrf:" / "2fa:" prefixes elsewhere.
func (s *Server) signFlash(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("flash:" + payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

// maxFlashCookieLen bounds the raw cookie input so an oversized value cannot
// drive needless base64 decoding and HMAC work on every page render.
const maxFlashCookieLen = 2048

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, kind, msg string) {
	s.setFlashUndo(w, r, kind, msg, "")
}

// setFlashUndo is setFlash with an optional undo action: the toast renders a
// "Rückgängig" POST button targeting undoURL (only same-origin absolute paths).
func (s *Server) setFlashUndo(w http.ResponseWriter, r *http.Request, kind, msg, undoURL string) {
	payload := strconv.FormatInt(time.Now().Add(flashTTL).Unix(), 10) +
		"|" + kind + "|" + url.QueryEscape(msg)
	if undoURL != "" {
		payload += "|" + url.QueryEscape(undoURL)
	}
	s.setCookie(w, r, &http.Cookie{
		Name:   flashCookie,
		Value:  s.signFlash(payload),
		MaxAge: int(flashTTL.Seconds()),
	})
}

// readFlash returns the flash message, kind and optional undo URL, clearing the
// cookie. A missing, oversized, unsigned, forged or expired cookie yields no
// flash at all. The undo URL is additionally only honored when it is a
// same-origin absolute path.
func (s *Server) readFlash(w http.ResponseWriter, r *http.Request) (msg, kind, undoURL string) {
	c, err := s.cookie(r, flashCookie)
	if err != nil || c.Value == "" {
		return "", "", ""
	}
	s.setCookie(w, r, &http.Cookie{Name: flashCookie, Value: "", MaxAge: -1})
	payload, ok := s.verifyFlash(c.Value)
	if !ok {
		return "", "", ""
	}
	// exp | kind | msg [| undoURL]
	parts := strings.SplitN(payload, "|", 4)
	if len(parts) < 3 {
		return "", "", ""
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", ""
	}
	decoded, err := url.QueryUnescape(parts[2])
	if err != nil {
		return "", "", ""
	}
	if len(parts) == 4 {
		if u, err := url.QueryUnescape(parts[3]); err == nil &&
			strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") &&
			!strings.Contains(u, "\\") { // "/\\evil.com" is read as "//evil.com" by browsers
			undoURL = u
		}
	}
	return decoded, parts[1], undoURL
}

// verifyFlash checks the signature and returns the raw payload.
func (s *Server) verifyFlash(value string) (string, bool) {
	if len(value) > maxFlashCookieLen {
		return "", false
	}
	enc, sig, ok := strings.Cut(value, ".")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("flash:" + string(raw)))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
		return "", false
	}
	return string(raw), true
}

// ---- Form helpers -------------------------------------------------------

// pathID parses the {id} path value as int64.
func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// formInt64FromPath parses a named path value (e.g. a second {pid}) as int64.
func formInt64FromPath(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// formInt64 parses a form field as int64 (0 on empty/invalid).
func formInt64(r *http.Request, name string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue(name)), 10, 64)
	return v
}

// formInt parses a form field as a platform int, treating an empty or invalid
// value as 0 (mirroring formInt64). strconv.Atoi yields an int directly, so the
// value never passes through a lossy int64->int narrowing on 32-bit builds.
func formInt(r *http.Request, name string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
	return v
}

// formInt64Ptr returns a pointer to the parsed id, or nil when empty/zero.
func formInt64Ptr(r *http.Request, name string) *int64 {
	v := formInt64(r, name)
	if v == 0 {
		return nil
	}
	return &v
}

// formDecimal parses a form field as an exact decimal, accepting both "," and
// "." as the decimal separator (German users type commas). Empty/invalid -> 0.
func formDecimal(r *http.Request, name string) decimal.Decimal {
	return parseGermanDecimal(r.FormValue(name))
}

// maxDecimalLen bounds the input decimal parsing will even attempt. Building the
// mantissa runs through big.Int, whose base-10 parse is superlinear: measured at
// 95 µs for 100 digits, 32 ms for 100k, and 2.7 SECONDS for a megabyte — which
// limitBody's 1 MiB ceiling allows in a single field. Every real amount, rate,
// quantity or width fits in a handful of characters, so anything past this is not
// a number a person typed.
//
// A length cap alone is not enough, because SCIENTIFIC NOTATION packs an enormous
// value into a handful of characters: "1e1000000" is nine, sails past this limit,
// and parses cheaply — the cost lands on whatever touches the value afterwards.
// Measured on decimal v1.4.0: at exponent 1e6, Round(2) takes 60 ms, String() —
// which the SQL driver calls to store it — 207 ms, and GreaterThan(), i.e. the
// range check meant to REJECT the value, 58 ms. The exponent is an int32, so
// twelve characters reach 1e2147483647 and those figures grow superlinearly.
// Nobody types an exponent into a Betrag or a Stundenzahl, so the notation is
// refused outright, before any decimal operation runs on it.
const maxDecimalLen = 32

// parseGermanDecimal parses a raw string as an exact decimal, accepting "," or
// "." as the decimal separator. Empty/invalid -> 0.
//
// The length guard sits HERE rather than at the call sites because the expensive
// parse is here: twelve form fields across six handlers reach this function, and
// a per-field check would have to be remembered twelve times and again for every
// field added later. Over-long input is treated as invalid, which is the same
// answer these callers already get for anything unparseable.
func parseGermanDecimal(raw string) decimal.Decimal {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), ",", ".")
	if len(raw) > maxDecimalLen || strings.ContainsAny(raw, "eE") {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// maxFormListLen bounds the number of items parsed from repeated form fields.
const maxFormListLen = 100

// formInt64List collects repeated form values under name as int64s.
func formInt64List(r *http.Request, name string) []int64 {
	var ids []int64
	for _, v := range r.Form[name] {
		if len(ids) >= maxFormListLen {
			break
		}
		v = strings.TrimSpace(v)
		if len(v) > maxDecimalLen {
			continue
		}
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// formMachineIDs collects repeated "machine_ids" checkbox values.
func formMachineIDs(r *http.Request) []int64 {
	return formInt64List(r, "machine_ids")
}

// redirect issues a see-other redirect (post/redirect/get).
func redirect(w http.ResponseWriter, r *http.Request, target string) {
	http.Redirect(w, r, target, http.StatusSeeOther)
}
