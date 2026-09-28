package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBudgetForLimitOnlyLowers pins OPS-05: the shipped 768 MiB container keeps
// exactly the old 128 MiB, a smaller one gets proportionally less, and no limit
// or a larger one never raises the allowance.
func TestBudgetForLimitOnlyLowers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int64
		want  int64
	}{
		{"unknown or unlimited", 0, 128 << 20},
		{"shipped 768 MiB", 768 << 20, 128 << 20},
		{"larger container", 4 << 30, 128 << 20},
		{"512 MiB container", 512 << 20, (512 << 20) / 6},
		{"tiny container keeps the floor", 32 << 20, 16 << 20},
	} {
		if got := budgetForLimit(tc.limit); got != tc.want {
			t.Errorf("%s: budgetForLimit(%d) = %d, want %d", tc.name, tc.limit, got, tc.want)
		}
	}
}

// TestReadMemoryLimitFormats covers cgroup v2 ("max" / bytes) and v1 (a huge
// number for "unlimited"), plus the missing-file fallback to the next path.
func TestReadMemoryLimitFormats(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	v2max := write("v2max", "max\n")
	v2 := write("v2", "805306368\n")
	v1unlimited := write("v1", "9223372036854771712\n")
	junk := write("junk", "not-a-number")
	missing := filepath.Join(dir, "missing")

	for _, tc := range []struct {
		name  string
		paths []string
		want  int64
	}{
		{"v2 no limit", []string{v2max}, 0},
		{"v2 limit", []string{v2}, 805306368},
		{"v1 unlimited", []string{v1unlimited}, 0},
		{"unparsable", []string{junk}, 0},
		{"falls through a missing file", []string{missing, v2}, 805306368},
		{"nothing readable", []string{missing}, 0},
	} {
		if got := readMemoryLimit(tc.paths...); got != tc.want {
			t.Errorf("%s: readMemoryLimit = %d, want %d", tc.name, got, tc.want)
		}
	}
}
