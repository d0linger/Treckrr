package db

import "testing"

// TestWithTimeouts pins the DSN rewriting: the guards must be added when absent,
// an operator's own values must win, and anything unparseable must pass through
// untouched — a hardening default may never stop a working deployment booting.
func TestWithTimeouts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // substrings that must be present
		same bool     // must be returned unchanged
	}{
		{
			name: "adds both guards to a plain URL DSN",
			in:   "postgres://db:5432/treckrr?sslmode=disable",
			want: []string{"statement_timeout=30s", "idle_in_transaction_session_timeout=60s", "sslmode=disable"},
		},
		{
			name: "keeps an operator's own statement_timeout",
			in:   "postgres://db:5432/treckrr?statement_timeout=5s",
			want: []string{"statement_timeout=5s"},
		},
		{
			name: "postgresql:// scheme is handled too",
			in:   "postgresql://db:5432/treckrr",
			want: []string{"statement_timeout=30s"},
		},
		{
			name: "keyword/value DSN gets both defaults",
			in:   "host=db user=treckrr dbname=treckrr sslmode=disable",
			want: []string{
				"host=db", "user=treckrr", "sslmode=disable",
				"statement_timeout=30s", "idle_in_transaction_session_timeout=60s",
			},
		},
		{
			name: "keyword/value DSN keeps an operator's own value",
			in:   "host=db user=treckrr statement_timeout=5s",
			want: []string{"statement_timeout=5s", "idle_in_transaction_session_timeout=60s"},
		},
		{
			// Unparseable by either route: pass it through rather than mangling a
			// DSN the driver might still accept.
			name: "garbage passes through untouched",
			in:   "=not a dsn=",
			same: true,
		},
		{
			name: "unknown scheme passes through untouched",
			in:   "mysql://db:3306/treckrr",
			same: true,
		},
	}
	for _, c := range cases {
		got := withTimeouts(c.in)
		if c.same {
			if got != c.in {
				t.Errorf("%s: withTimeouts(%q) = %q, want it unchanged", c.name, c.in, got)
			}
			continue
		}
		for _, w := range c.want {
			if !contains(got, w) {
				t.Errorf("%s: withTimeouts(%q) = %q, missing %q", c.name, c.in, got, w)
			}
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
