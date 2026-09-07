package backup_test

import (
	"os/exec"
	"testing"
)

// requirePGTools skips when pg_dump/pg_restore are absent.
//
// These are the only tests in the tree that shell out to the PostgreSQL client
// binaries: the rehearsal restores a dump, and the rotation has to produce real
// dumps to re-encrypt. The CI image ships the server as a service container but
// not the client tools, so without this guard adding these tests would have
// turned CI red for a missing binary rather than a broken behavior. A skip is
// honest here — the test genuinely cannot run — and the message says what to
// install.
func requirePGTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH; install the postgresql-client package to run this test", bin)
		}
	}
}
