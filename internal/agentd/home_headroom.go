package agentd

import (
	"errors"
	"log/slog"
	"math"
	"math/bits"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// HOME headroom (designs/home-headroom-monitor.md). One statfs(2) of the
// filesystem that holds $HOME, never a directory walk: its cost does not grow
// with the number of files, and a file that appears or vanishes cannot change
// or break it. The result carries measured numbers and a closed state word,
// not a path, an error text or an action.
const (
	// homeHeadroomEvery is the shortest gap between two real samples. The
	// heartbeat is 60 s, so in practice there is one sample per beat.
	homeHeadroomEvery = 30 * time.Second
	// homeHeadroomTimeout bounds one statfs. A hung mount reports
	// unreadable/timeout and the heartbeat goes on.
	homeHeadroomTimeout = 3 * time.Second

	// Source defaults, not a sizing claim: percent of the filesystem that is
	// still available (bytes: what this user can use; inodes: free).
	bytesLowPercent        = 15
	bytesExhaustedPercent  = 3
	inodesLowPercent       = 10
	inodesExhaustedPercent = 2
)

// homeHeadroomMonitor samples HOME at a bounded cadence and logs only state
// changes. The zero value is ready to use.
type homeHeadroomMonitor struct {
	// statfs and timeout are test seams; nil and zero select the syscall and
	// homeHeadroomTimeout.
	statfs  func(string, *syscall.Statfs_t) error
	timeout time.Duration

	// inflight is true while a statfs call is running, including one that
	// outlived its timeout. At most one is ever outstanding.
	inflight atomic.Bool

	mu     sync.Mutex
	have   bool
	last   time.Time
	cur    protocol.HomeHeadroom
	logged string // state/reason last logged; "" before the first sample
}

// SampleHomeHeadroom is a one-shot sample with no cadence or log, for
// `agentd ctl status`.
func SampleHomeHeadroom(home string, now time.Time) *protocol.HomeHeadroom {
	var m homeHeadroomMonitor
	return m.Sample(home, now, nil)
}

// Sample returns the newest observation of home's filesystem. It samples when
// none exists, when homeHeadroomEvery has passed, or when the clock has gone
// backwards; otherwise it returns the cached one. The result is a copy.
func (m *homeHeadroomMonitor) Sample(home string, now time.Time, log *slog.Logger) *protocol.HomeHeadroom {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.have || now.Before(m.last) || now.Sub(m.last) >= homeHeadroomEvery {
		m.cur = m.measure(home, now.UTC())
		m.have, m.last = true, now
		m.note(log)
	}
	out := m.cur
	if m.cur.Bytes != nil {
		b := *m.cur.Bytes
		out.Bytes = &b
	}
	if m.cur.Inodes != nil {
		i := *m.cur.Inodes
		out.Inodes = &i
	}
	return &out
}

type statfsResult struct {
	stat syscall.Statfs_t
	err  error
}

// read runs one bounded statfs. timedOut is true when the call did not return
// in time, or when an earlier hung call has still not returned.
func (m *homeHeadroomMonitor) read(home string) (res statfsResult, timedOut bool) {
	if !m.inflight.CompareAndSwap(false, true) {
		return res, true
	}
	statfs := m.statfs
	if statfs == nil {
		statfs = syscall.Statfs
	}
	wait := m.timeout
	if wait <= 0 {
		wait = homeHeadroomTimeout
	}
	done := make(chan statfsResult, 1)
	go func() {
		var r statfsResult
		r.err = statfs(home, &r.stat)
		m.inflight.Store(false)
		done <- r
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res = <-done:
		return res, false
	case <-timer.C:
		return res, true
	}
}

func (m *homeHeadroomMonitor) measure(home string, now time.Time) protocol.HomeHeadroom {
	h := protocol.HomeHeadroom{Version: protocol.HomeHeadroomVersion, ObservedAt: now}
	res, timedOut := m.read(home)
	if timedOut || res.err != nil {
		h.State, h.Reason = protocol.HeadroomUnreadable, headroomErrReason(res.err, timedOut)
		return h
	}
	st := res.stat
	if total, avail, ok := byteFigures(st); ok {
		h.Bytes = &protocol.HeadroomFigure{State: classifyHeadroom(avail, total, bytesLowPercent, bytesExhaustedPercent), Total: total, Available: avail}
	}
	if st.Files > 0 && st.Ffree <= st.Files {
		h.Inodes = &protocol.HeadroomFigure{State: classifyHeadroom(st.Ffree, st.Files, inodesLowPercent, inodesExhaustedPercent), Total: st.Files, Available: st.Ffree}
	}
	h.State = protocol.HeadroomOK
	for _, f := range []*protocol.HeadroomFigure{h.Bytes, h.Inodes} {
		switch {
		case f == nil:
			if h.State == protocol.HeadroomOK {
				h.State = protocol.HeadroomUnknown
			}
		case f.State == protocol.HeadroomExhausted:
			h.State = protocol.HeadroomExhausted
		case f.State == protocol.HeadroomLow && h.State != protocol.HeadroomExhausted:
			h.State = protocol.HeadroomLow
		}
	}
	if h.State == protocol.HeadroomUnknown {
		switch {
		case h.Bytes == nil && h.Inodes == nil:
			h.Reason = protocol.HeadroomReasonBothInvalid
		case h.Bytes == nil:
			h.Reason = protocol.HeadroomReasonBytesInvalid
		default:
			h.Reason = protocol.HeadroomReasonInodesInvalid
		}
	}
	return h
}

// byteFigures converts blocks to bytes with the fragment size (else the block
// size) and Bavail, as df does, and refuses figures that contradict each other
// or overflow.
func byteFigures(st syscall.Statfs_t) (total, avail uint64, ok bool) {
	unit := st.Frsize
	if unit <= 0 {
		unit = st.Bsize
	}
	if unit <= 0 || st.Blocks == 0 || st.Bfree > st.Blocks || st.Bavail > st.Bfree || st.Blocks > math.MaxUint64/uint64(unit) {
		return 0, 0, false
	}
	return st.Blocks * uint64(unit), st.Bavail * uint64(unit), true
}

// classifyHeadroom compares avail/total with the percentages using exact
// 128-bit products, so no filesystem size overflows or rounds.
func classifyHeadroom(avail, total uint64, lowPercent, exhaustedPercent uint64) string {
	switch {
	case belowPercent(avail, total, exhaustedPercent):
		return protocol.HeadroomExhausted
	case belowPercent(avail, total, lowPercent):
		return protocol.HeadroomLow
	}
	return protocol.HeadroomOK
}

// belowPercent reports avail*100 < total*percent.
func belowPercent(avail, total, percent uint64) bool {
	ah, al := bits.Mul64(avail, 100)
	th, tl := bits.Mul64(total, percent)
	return ah < th || ah == th && al < tl
}

// headroomErrReason maps a statfs failure to a closed word. The error text is
// dropped on purpose: it names the path.
func headroomErrReason(err error, timedOut bool) string {
	switch {
	case timedOut:
		return protocol.HeadroomReasonTimeout
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return protocol.HeadroomReasonPermission
	case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENOTDIR):
		return protocol.HeadroomReasonNotFound
	case errors.Is(err, syscall.EIO), errors.Is(err, syscall.ESTALE), errors.Is(err, syscall.ENOTCONN), errors.Is(err, syscall.ETIMEDOUT):
		return protocol.HeadroomReasonIO
	}
	return protocol.HeadroomReasonOther
}

// note logs one line when the state or reason changes. A healthy first sample
// and repeats are silent.
func (m *homeHeadroomMonitor) note(log *slog.Logger) {
	key := m.cur.State + "/" + m.cur.Reason
	prev := m.logged
	m.logged = key
	if log == nil || key == prev || prev == "" && m.cur.State == protocol.HeadroomOK {
		return
	}
	attrs := []any{"state", m.cur.State}
	if m.cur.Reason != "" {
		attrs = append(attrs, "reason", m.cur.Reason)
	}
	if b := m.cur.Bytes; b != nil {
		attrs = append(attrs, "totalBytes", b.Total, "availableBytes", b.Available)
	}
	if i := m.cur.Inodes; i != nil {
		attrs = append(attrs, "totalInodes", i.Total, "availableInodes", i.Available)
	}
	if m.cur.State == protocol.HeadroomOK {
		log.Info("HOME headroom recovered", attrs...)
		return
	}
	log.Warn("HOME headroom", attrs...)
}
