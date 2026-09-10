package main

import "testing"

func TestBackupCLIMaxBytes(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  int64
		valid bool
	}{
		{"", 0, true}, {"1048576", 1 << 20, true}, {"17179869184", 16 << 30, true},
		{"0", 0, false}, {"-1", 0, false}, {"1048575", 0, false}, {"17179869185", 0, false}, {"9223372036854775808", 0, false}, {"oops", 0, false},
	} {
		n, err := backupCLIMaxBytes(tc.raw)
		if (err == nil) != tc.valid || (tc.valid && n != tc.want) {
			t.Fatalf("%q: n=%d err=%v", tc.raw, n, err)
		}
	}
}
