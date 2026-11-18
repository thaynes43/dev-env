package agentd

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The container's cgroup namespace places its own v2 memory controller here.
// Older/v1 hosts, unlimited budgets and read failures report unknown (absent).
// There is no process inspection, native RPC, command or log.
func sampleCodexHostMemory(now time.Time, read func(string) ([]byte, error)) *protocol.CodexHostMemory {
	values := [2]int64{}
	for i, name := range []string{"memory.current", "memory.max"} {
		raw, err := read("/sys/fs/cgroup/" + name)
		if err != nil || len(raw) > 32 {
			return nil
		}
		s := strings.TrimSpace(string(raw))
		if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil
		}
		values[i], err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil
		}
	}
	stat, err := read("/sys/fs/cgroup/memory.stat")
	if err != nil || len(stat) > 64<<10 {
		return nil
	}
	var inactive int64
	found := false
	for line := range strings.Lines(string(stat)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.IndexFunc(fields[1], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil
		}
		if fields[0] == "inactive_file" {
			if found {
				return nil
			}
			inactive, found = n, true
		}
	}
	if !found {
		return nil
	}
	workingSet := max(int64(0), values[0]-inactive)
	m := &protocol.CodexHostMemory{CurrentBytes: workingSet, LimitBytes: values[1], ObservedAt: now.UTC()}
	if !m.Valid() {
		return nil
	}
	return m
}

func codexHostMemory(sess protocol.Session, now time.Time) *protocol.CodexHostMemory {
	if sess.Agent != protocol.AgentCodex || sess.Mode != protocol.ModeRemote {
		return nil
	}
	return sampleCodexHostMemory(now, os.ReadFile)
}
