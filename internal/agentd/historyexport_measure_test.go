package agentd

import (
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// TestHistoryExportMeasure measures DESIGN-025 15's "Measured in H4": the CPU
// time and memory of a plan, a stream piped into put (as the operator relays
// it), and a verify, over DEV_ENV_HISTORY_MEASURE_MIB MiB of history in files
// of 32 MiB. It is skipped unless that variable is set, and it is never part
// of CI. Run it alone, under the CPU rule:
//
//	DEV_ENV_HISTORY_MEASURE_MIB=1024 nice -n 19 env GOMAXPROCS=2 \
//	  go test -p 1 -count=1 -run TestHistoryExportMeasure -v ./internal/agentd/
func TestHistoryExportMeasure(t *testing.T) {
	mib, _ := strconv.Atoi(os.Getenv("DEV_ENV_HISTORY_MEASURE_MIB"))
	if mib <= 0 {
		t.Skip("set DEV_ENV_HISTORY_MEASURE_MIB to measure")
	}
	r := newHistoryRig(t)
	rng := rand.NewChaCha8([32]byte{1})
	buf := make([]byte, 1<<20)
	const perFile = 32
	for i := 0; i*perFile < mib; i++ {
		p := r.path(fmt.Sprintf(".codex/sessions/2026/10/12/rollout-%04d.jsonl", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < perFile && i*perFile+j < mib; j++ {
			_, _ = rng.Read(buf)
			if _, err := f.Write(buf); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ar := newArchiveRig(t, r)

	measure := func(name string, f func()) {
		runtime.GC()
		var before, after syscall.Rusage
		var m0, m1 runtime.MemStats
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &before)
		runtime.ReadMemStats(&m0)
		start := time.Now()
		f()
		wall := time.Since(start)
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &after)
		runtime.ReadMemStats(&m1)
		cpu := time.Duration(after.Utime.Nano()-before.Utime.Nano()) + time.Duration(after.Stime.Nano()-before.Stime.Nano())
		t.Logf("%-14s %4d MiB: wall %8s, cpu %8s, allocated %6.1f MiB, heap sys %6.1f MiB, peak RSS so far %6.1f MiB",
			name, mib, wall.Round(time.Millisecond), cpu.Round(time.Millisecond),
			float64(m1.TotalAlloc-m0.TotalAlloc)/(1<<20), float64(m1.HeapSys)/(1<<20), float64(after.Maxrss)/1024)
	}

	plan := r.plan(t)
	measure("plan", func() { plan = r.plan(t) })
	c := testContent(t, plan, "dev-env", testSessionUID)
	contentLine, err := protocol.CanonicalJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	contentLine = append(contentLine, '\n')
	var sum string
	measure("stream+put", func() {
		pr, pw := io.Pipe()
		go func() {
			_, _ = pw.Write(contentLine)
			pw.CloseWithError(StreamHistoryExport(r.s, r.o, c.PlanSHA256, pw))
		}()
		res, err := ar.a.Put(pr, "measure")
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		sum = res.ContentSHA256
	})
	measure("verify", func() {
		if _, err := ar.a.Verify(testSessionUID, sum); err != nil {
			t.Fatalf("verify: %v", err)
		}
	})
}
