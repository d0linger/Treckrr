// Package web embeds the HTML templates and static assets and provides the
// template rendering helpers used by the HTTP handlers.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// StaticFS returns the embedded static asset filesystem rooted at "static".
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// assetVersion is a short content hash over all embedded static files. It is
// appended to asset URLs (?v=...) so a new build busts browser/service-worker
// caches automatically.
var assetVersion = computeAssetVersion()

// AssetVersion returns the current static-asset content hash.
func AssetVersion() string { return assetVersion }

func computeAssetVersion() string {
	h := sha256.New()
	_ = fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = h.Write([]byte(path))
		_, _ = h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// sharedTemplates are layout/partials parsed into every page set.
var sharedTemplates = []string{"templates/layout.html", "templates/partials.html"}

// Templates parses each page against the shared layout and partials, returning
// a set keyed by page name (the file's base name without extension, e.g.
// "dashboard"). Rendering a page executes its "layout" template.
func Templates() (map[string]*template.Template, error) {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	shared := map[string]bool{}
	for _, s := range sharedTemplates {
		shared[s] = true
	}

	pages := map[string]*template.Template{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		path := "templates/" + e.Name()
		if shared[path] {
			continue
		}
		files := append(append([]string{}, sharedTemplates...), path)
		t, err := template.New("layout.html").Funcs(funcMap()).ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".html")
		pages[name] = t
	}
	return pages, nil
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"money": Money,
		"num":   Num,
		"date":  Date,
		"dateInput": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("2006-01-02")
		},
		"dict": dict,
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"contains": func(ids []int64, id int64) bool {
			for _, v := range ids {
				if v == id {
					return true
				}
			}
			return false
		},
		"deref": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"emptyIDs": func() []int64 { return nil },
	}
}

// Money formats a value as German-style currency, e.g. 1234.5 -> "1.234,50 €".
func Money(v decimal.Decimal) string {
	return formatDecimal(v, 2) + " €"
}

// Num formats a number with up to three decimals, German style, no trailing zeros.
func Num(v decimal.Decimal) string {
	s := formatDecimal(v, 3)
	// Trim trailing zeros after the decimal comma.
	if strings.Contains(s, ",") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ",")
	}
	return s
}

// Date formats a date as dd.mm.yyyy.
func Date(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("02.01.2006")
}

// formatDecimal renders v with the given decimals using "." as thousands
// separator and "," as decimal separator (de-DE).
func formatDecimal(v decimal.Decimal, decimals int32) string {
	s := v.StringFixed(decimals)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i+1:]
	}
	var b strings.Builder
	n := len(intPart)
	for i, c := range intPart {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func dict(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict requires an even number of arguments")
	}
	m := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		m[key] = values[i+1]
	}
	return m, nil
}
