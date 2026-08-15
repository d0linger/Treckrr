package server

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/web"
)

// pageData is the map passed to templates. Handlers add page-specific keys.
type pageData map[string]any

// newPage returns page data pre-filled with common fields (user, flash, nav).
func (s *Server) newPage(w http.ResponseWriter, r *http.Request, title, active string) pageData {
	p := pageData{
		"Title":    title,
		"Active":   active,
		"User":     userFromCtx(r),
		"BasePath": r.URL.Path,
		"Theme":    themeFromCookie(r),
		"CSRF":     s.csrfToken(r),
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

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, kind, msg string) {
	s.setFlashUndo(w, r, kind, msg, "")
}

// setFlashUndo is setFlash with an optional undo action: the toast renders a
// "Rückgängig" POST button targeting undoURL (only same-origin absolute paths).
func (s *Server) setFlashUndo(w http.ResponseWriter, r *http.Request, kind, msg, undoURL string) {
	val := kind + "|" + url.QueryEscape(msg)
	if undoURL != "" {
		val += "|" + url.QueryEscape(undoURL)
	}
	s.setCookie(w, r, &http.Cookie{Name: flashCookie, Value: val, MaxAge: 30})
}

// readFlash returns the flash message, kind and optional undo URL, clearing the
// cookie. The undo URL is only honored when it is a same-origin absolute path.
func (s *Server) readFlash(w http.ResponseWriter, r *http.Request) (msg, kind, undoURL string) {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return "", "", ""
	}
	s.setCookie(w, r, &http.Cookie{Name: flashCookie, Value: "", MaxAge: -1})
	parts := strings.SplitN(c.Value, "|", 3)
	if len(parts) < 2 {
		return "", "", ""
	}
	decoded, err := url.QueryUnescape(parts[1])
	if err != nil {
		return "", "", ""
	}
	if len(parts) == 3 {
		if u, err := url.QueryUnescape(parts[2]); err == nil &&
			strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
			undoURL = u
		}
	}
	return decoded, parts[0], undoURL
}

// ---- Form helpers -------------------------------------------------------

// pathID parses the {id} path value as int64.
func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
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

// parseGermanDecimal parses a raw string as an exact decimal, accepting "," or
// "." as the decimal separator. Empty/invalid -> 0.
func parseGermanDecimal(raw string) decimal.Decimal {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), ",", ".")
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// formInt64List collects repeated form values under name as int64s.
func formInt64List(r *http.Request, name string) []int64 {
	var ids []int64
	for _, v := range r.Form[name] {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// formMachineIDs collects repeated "machine_ids" checkbox values.
func formMachineIDs(r *http.Request) []int64 {
	var ids []int64
	for _, v := range r.Form["machine_ids"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// redirect issues a see-other redirect (post/redirect/get).
func redirect(w http.ResponseWriter, r *http.Request, target string) {
	http.Redirect(w, r, target, http.StatusSeeOther)
}
