package backup_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A configured integration database promises real backup coverage. Missing
// clients must fail that run; only a unit-only run may skip these tests.
func requirePGTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("TEST_DATABASE_URL") != "" {
				t.Fatalf("%s not in PATH; install matching PostgreSQL clients when TEST_DATABASE_URL is set", bin)
			}
			t.Skipf("%s not in PATH; install the postgresql-client package to run this test", bin)
		}
	}
}

func TestRequirePGTools(t *testing.T) {
	if os.Getenv("TRECKRR_PGTOOLS_SUBPROCESS") == "1" {
		requirePGTools(t)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		dsn      string
		wantFail bool
	}{
		{name: "unit run can skip missing tools"},
		{name: "configured database requires tools", dsn: "postgres://unused", wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// #nosec G204 -- run this test executable, never a caller-supplied command.
			cmd := exec.CommandContext(
				t.Context(),
				executable,
				"-test.run=^TestRequirePGTools$",
				"-test.v",
			)
			cmd.Env = append(os.Environ(),
				"TRECKRR_PGTOOLS_SUBPROCESS=1",
				"TEST_DATABASE_URL="+tt.dsn,
				"PATH="+t.TempDir(),
			)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantFail {
				t.Fatalf("exit error = %v, want failure %v; output:\n%s", err, tt.wantFail, out)
			}
			if !strings.Contains(string(out), "pg_dump not in PATH") {
				t.Fatalf("missing prerequisite diagnostic:\n%s", out)
			}
		})
	}
}
