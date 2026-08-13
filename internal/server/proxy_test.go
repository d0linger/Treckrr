package server

import (
	"net"
	"net/http/httptest"
	"testing"

	"treckrr/internal/config"
)

// TestClientIPProxyAllowlist proves SH-05: a forwarded header is only honored
// when the direct peer is an allowlisted proxy; otherwise the peer address wins.
func TestClientIPProxyAllowlist(t *testing.T) {
	_, net10, _ := net.ParseCIDR("10.0.0.0/8")

	cases := []struct {
		name       string
		trustProxy bool
		allowlist  []*net.IPNet
		remoteAddr string
		xff        string
		want       string
	}{
		{"allowlisted proxy → header trusted", true, []*net.IPNet{net10}, "10.1.2.3:5555", "1.2.3.4", "1.2.3.4"},
		{"non-allowlisted peer → header ignored", true, []*net.IPNet{net10}, "203.0.113.5:5555", "1.2.3.4", "203.0.113.5"},
		{"rightmost hop when chained", true, []*net.IPNet{net10}, "10.0.0.9:5555", "9.9.9.9, 8.8.8.8", "8.8.8.8"},
		{"no allowlist → legacy trust", true, nil, "203.0.113.5:5555", "1.2.3.4", "1.2.3.4"},
		{"trust proxy off → always peer", false, []*net.IPNet{net10}, "10.1.2.3:5555", "1.2.3.4", "10.1.2.3"},
		{"allowlisted but no header → peer", true, []*net.IPNet{net10}, "10.1.2.3:5555", "", "10.1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{TrustProxy: tc.trustProxy, TrustedProxies: tc.allowlist}}
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := s.clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
