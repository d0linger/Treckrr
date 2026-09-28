package backup

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Online archive budget (OPS-05). The fixed 128 MiB allowance was sized for the
// shipped 768 MiB container: an online restore holds the ciphertext, the
// plaintext and a scratch copy at once, which peaks around 550 MiB at 128 MiB.
// A container started with a smaller memory limit kept the same allowance and
// would be OOM-killed by a dump the budget still admitted.
//
// The budget is therefore derived from the cgroup memory limit with the same
// 1:6 ratio as the shipped pairing (768 MiB / 6 = 128 MiB). It only ever LOWERS
// the default: a larger container keeps 128 MiB, so an existing deployment
// behaves exactly as before, and without a readable limit (no cgroup, no limit,
// non-Linux) nothing changes either. Offline CLI runs set Options.MaxBytes
// explicitly and are not affected.
const (
	defaultOnlineBudget = maxS3ObjectBytes
	minOnlineBudget     = 16 << 20
	budgetLimitRatio    = 6
)

// cgroupLimitFiles are the memory limit files of cgroup v2 and v1.
var cgroupLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

var onlineBudget = sync.OnceValue(func() int64 {
	limit := readMemoryLimit(cgroupLimitFiles...)
	budget := budgetForLimit(limit)
	if budget < defaultOnlineBudget {
		slog.Info("backup: online archive budget lowered to fit the container memory limit",
			"limit_bytes", limit, "budget_bytes", budget)
	}
	return budget
})

// OnlineBudget is the in-memory archive allowance of the running server, shared
// by scheduled backups, restores and the restore upload limit.
func OnlineBudget() int64 { return onlineBudget() }

// budgetForLimit maps a container memory limit to the archive budget. A limit of
// zero means "unknown or unlimited" and keeps the default.
func budgetForLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultOnlineBudget
	}
	budget := limit / budgetLimitRatio
	switch {
	case budget >= defaultOnlineBudget:
		return defaultOnlineBudget
	case budget < minOnlineBudget:
		return minOnlineBudget
	}
	return budget
}

// readMemoryLimit returns the first readable memory limit in bytes, or zero when
// none is set. cgroup v2 writes "max" for no limit; cgroup v1 reports a huge
// page-aligned number instead, which is treated the same way.
func readMemoryLimit(paths ...string) int64 {
	for _, p := range paths {
		raw, err := os.ReadFile(p) // #nosec G304 G703 -- fixed kernel cgroup paths
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(raw))
		if v == "" || v == "max" {
			return 0
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 || n >= 1<<60 {
			return 0
		}
		return n
	}
	return 0
}
