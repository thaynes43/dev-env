package agentd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTmpUsageWarningThresholdAndCadence(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	stat := syscall.Statfs_t{Blocks: 100, Bfree: 25, Bavail: 25, Bsize: 4096}
	var readErr error
	calls := 0
	m := tmpUsageMonitor{statfs: func(path string, got *syscall.Statfs_t) error {
		calls++
		if path != "/tmp" {
			t.Fatalf("statfs path = %q", path)
		}
		*got = stat
		return readErr
	}}
	now := time.Time{} // A zero clock must still rate-limit the first attempt.
	m.check(now, log)
	if calls != 1 || out.Len() != 0 {
		t.Fatalf("exactly 75%%: calls=%d, log=%s", calls, &out)
	}
	stat.Bfree, stat.Bavail = 24, 24
	m.check(now.Add(tmpUsageEvery-time.Nanosecond), log)
	if calls != 1 {
		t.Fatal("sampled sooner than five minutes")
	}
	now = now.Add(tmpUsageEvery)
	m.check(now, log)
	if calls != 2 || !strings.Contains(out.String(), `"usedBytes":311296`) || !strings.Contains(out.String(), `"capacityBytes":409600`) || !strings.Contains(out.String(), `"availableBytes":98304`) {
		t.Fatalf("high usage did not log measured bytes: calls=%d, log=%s", calls, &out)
	}
	warnOnce := out.String()
	for _, err := range []error{nil, errors.New("statfs failed"), nil} {
		readErr = err
		now = now.Add(tmpUsageEvery)
		m.check(now, log)
		if out.String() != warnOnce {
			t.Fatalf("high usage or an error repeated the warning: %s", &out)
		}
	}
	stat.Bfree, stat.Bavail = 50, 50
	now = now.Add(tmpUsageEvery)
	m.check(now, log)
	stat.Bfree, stat.Bavail = 0, 0
	now = now.Add(tmpUsageEvery)
	m.check(now, log)
	if got := strings.Count(out.String(), `"level":"WARN"`); got != 2 {
		t.Fatalf("recovery should rearm the warning, got %d: %s", got, &out)
	}
}

func TestTmpUsageFragmentSizeAndAvailableSpace(t *testing.T) {
	for _, sizes := range []struct {
		name         string
		fragmentSize int64
		blockSize    int64
	}{
		{"fragment size", 512, 4096},
		{"fragment without block size", 512, 0},
		{"zero fragment fallback", 0, 512},
		{"negative fragment fallback", -1, 512},
	} {
		t.Run(sizes.name, func(t *testing.T) {
			var out bytes.Buffer
			m := tmpUsageMonitor{statfs: func(_ string, got *syscall.Statfs_t) error {
				// 70 used, 20 available, 10 reserved: df reports >75% used.
				*got = syscall.Statfs_t{Blocks: 100, Bfree: 30, Bavail: 20, Frsize: sizes.fragmentSize, Bsize: sizes.blockSize}
				return nil
			}}
			m.check(time.Now(), slog.New(slog.NewJSONHandler(&out, nil)))
			for _, want := range []string{`"usedBytes":35840`, `"capacityBytes":51200`, `"availableBytes":10240`} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %s in log: %s", want, &out)
				}
			}
		})
	}
}

func TestTmpUsageUnknownSamples(t *testing.T) {
	for name, stat := range map[string]syscall.Statfs_t{
		"zero capacity":          {},
		"no block size":          {Blocks: 100},
		"negative block size":    {Blocks: 100, Bsize: -1},
		"free exceeds total":     {Blocks: 100, Bfree: 101, Bsize: 4096},
		"available exceeds free": {Blocks: 100, Bfree: 25, Bavail: 26, Bsize: 4096},
		"overflow":               {Blocks: math.MaxUint64, Bsize: 4096},
		"fragment overflow":      {Blocks: math.MaxUint64, Frsize: 4096, Bsize: 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			m := tmpUsageMonitor{warned: true, statfs: func(_ string, got *syscall.Statfs_t) error {
				*got = stat
				return nil
			}}
			m.check(time.Now(), slog.New(slog.NewTextHandler(&out, nil)))
			if out.Len() != 0 || !m.warned {
				t.Fatalf("invalid sample logged or rearmed: %s, warned=%v", &out, m.warned)
			}
		})
	}
	// Failed calls also consume the five-minute sample slot.
	calls := 0
	m := tmpUsageMonitor{statfs: func(_ string, _ *syscall.Statfs_t) error {
		calls++
		return errors.New("unavailable")
	}}
	now := time.Now()
	m.check(now, nil)
	m.check(now.Add(time.Minute), nil)
	if calls != 1 {
		t.Fatalf("statfs failure retried too soon: %d calls", calls)
	}
}

func TestDaemonChecksTmpWithoutHeartbeat(t *testing.T) {
	var out bytes.Buffer
	d := Daemon{Log: slog.New(slog.NewTextHandler(&out, nil)), Interval: time.Hour, Poll: time.Hour, writerRefused: true}
	d.tmpUsage.statfs = func(_ string, got *syscall.Statfs_t) error {
		*got = syscall.Statfs_t{Blocks: 100, Bsize: 4096}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.supervise(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/tmp backing filesystem usage exceeds 75%") {
		t.Fatalf("no warning with heartbeats disabled: %s", &out)
	}
}
