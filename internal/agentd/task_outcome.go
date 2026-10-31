package agentd

import (
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// readTaskOutcome is called only at initial task exit. O_NONBLOCK prevents a
// task-created FIFO from hanging agentd; O_NOFOLLOW refuses a symlink leaf.
func readTaskOutcome(stateDir string, l Launch) *protocol.TaskOutcome {
	if l.TUI || l.Resume || !confirmedInitialUsage(l, stateDir) {
		return nil
	}
	path := filepath.Join(filepath.Dir(stateDir), "work", l.Session+".outcome.json")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > protocol.MaxTaskOutcomeBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, protocol.MaxTaskOutcomeBytes+1))
	if err != nil {
		return nil
	}
	record, err := protocol.DecodeTaskOutcomeRecord(data)
	if err != nil {
		return nil
	}
	return &protocol.TaskOutcome{SessionUID: l.UsageSource.SessionUID, PodUID: l.UsageSource.PodUID, Record: *record}
}
