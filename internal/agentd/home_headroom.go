package agentd

import (
	"log/slog"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	homeHeadroomEvery   = 30 * time.Second
	homeHeadroomTimeout = 3 * time.Second
)

// homeHeadroomMonitor is a stub in the RED commit.
type homeHeadroomMonitor struct {
	statfs   func(string, *syscall.Statfs_t) error
	timeout  time.Duration
	inflight atomic.Bool
}

func (m *homeHeadroomMonitor) Sample(home string, now time.Time, log *slog.Logger) *protocol.HomeHeadroom {
	return nil
}

// SampleHomeHeadroom is a stateless one-shot sample for `agentd ctl status`.
func SampleHomeHeadroom(home string, now time.Time) *protocol.HomeHeadroom { return nil }
