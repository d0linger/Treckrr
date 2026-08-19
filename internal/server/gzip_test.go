package server

import "testing"

// TestAcceptsGzip pins the Accept-Encoding parsing: gzip is acceptable only via a
// gzip (or *) token that isn't disabled with q=0; matching is case-insensitive and
// ignores unrelated tokens like x-gzip.
func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate, gzip;q=0.8", true},
		{"GZIP", true},                 // case-insensitive
		{"gzip;q=0", false},            // explicitly refused
		{"gzip;q=0.0", false},          // explicitly refused
		{"gzip; q=0", false},           // whitespace around q
		{"deflate, br", false},         // no gzip token
		{"x-gzip", false},              // unrelated token, must not substring-match
		{"identity", false},            // no gzip
		{"*", true},                    // wildcard acceptable
		{"*;q=0", false},               // wildcard refused
		{"gzip;q=0, *;q=1", false},     // explicit gzip refusal wins over *
		{"br;q=1.0, gzip;q=0.5", true}, // gzip present with positive q
		{"identity;q=1, *;q=0", false}, // only identity/* -> no gzip
	}
	for _, c := range cases {
		if got := acceptsGzip(c.header); got != c.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}
