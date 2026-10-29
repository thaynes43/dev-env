package agentd

import (
	"log/slog"
	"math"
	"syscall"
	"time"
)

const tmpUsageEvery = 5 * time.Minute

// tmpUsageMonitor samples the filesystem containing /tmp. A disk-backed
// EmptyDir's kubelet eviction threshold is NOT a quota visible to statfs:
// these figures cover the backing filesystem, potentially shared with peers.
// Only the supervisor loop calls check, so it needs no separate goroutine.
type tmpUsageMonitor struct {
	checked bool
	last    time.Time
	warned  bool
	// statfs is the test seam; nil selects the real filesystem syscall.
	statfs func(string, *syscall.Statfs_t) error
}

func (m *tmpUsageMonitor) check(now time.Time, log *slog.Logger) {
	if m.checked && now.Sub(m.last) < tmpUsageEvery {
		return
	}
	m.checked, m.last = true, now
	statfs := m.statfs
	if statfs == nil {
		statfs = syscall.Statfs
	}
	var stat syscall.Statfs_t
	if err := statfs("/tmp", &stat); err != nil {
		return // Unknown is not recovery; preserve warning suppression.
	}
	if stat.Bsize <= 0 || stat.Blocks == 0 || stat.Bfree > stat.Blocks || stat.Blocks > math.MaxUint64/uint64(stat.Bsize) {
		return
	}
	capacity := stat.Blocks * uint64(stat.Bsize)
	used := (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
	if float64(used) <= 0.75*float64(capacity) {
		m.warned = false
		return
	}
	if !m.warned {
		log.Warn("/tmp backing filesystem usage exceeds 75%", "usedBytes", used, "capacityBytes", capacity)
		m.warned = true
	}
}
