package backup

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func restoreList(ctx context.Context, archive string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pg_restore", "--list", archive) // #nosec G204 -- service-owned temporary file
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, remaining: 16 << 20}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("restore authentication filter: %w", err)
	}
	return excludeRestoredSessions(out.Bytes()), nil
}

func excludeRestoredSessions(toc []byte) []byte {
	lines := strings.Split(string(toc), "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		// <id>; <catalog oid> <object oid> TABLE DATA public <table> <owner>
		if len(fields) >= 7 && fields[3] == "TABLE" && fields[4] == "DATA" && fields[5] == "public" &&
			(fields[6] == "sessions" || fields[6] == "webauthn_ceremonies") {
			lines[i] = "; " + line
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
