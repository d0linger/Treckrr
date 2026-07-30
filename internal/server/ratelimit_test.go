package server

import "testing"

// TestAccountKey pins the normalization of the account-scoped login-limiter key:
// case-folded and trimmed, so failures against "Admin", "admin" and " admin "
// share one throttle bucket.
func TestAccountKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"admin", "pwuser:admin"},
		{"Admin", "pwuser:admin"},
		{"  Bob ", "pwuser:bob"},
		{"MiXeD", "pwuser:mixed"},
		{"", "pwuser:"},
	}
	for _, c := range cases {
		if got := accountKey(c.in); got != c.want {
			t.Errorf("accountKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
