package controller

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// TestHistoryRelayMeasure measures the operator's memory for one relay
// (DESIGN-025 5.4): the plan read from the hold pod, its decode and canonical
// check, content.json and the copy put reads. It is skipped unless
// DEV_ENV_HISTORY_RELAY_MEASURE_FILES names how many files the synthetic plan
// lists; DEV_ENV_HISTORY_RELAY_MEASURE_PATH sets each path's length (default
// 120 bytes). Every file has one block, and the plan excludes a tenth as many
// names. The relay takes any plan size here (PlanMax), so the result shows
// what historyPlanOutputMax must stay under. Run it alone, under nice and
// GOMAXPROCS=2, one package at a time: it allocates hundreds of MiB.
func TestHistoryRelayMeasure(t *testing.T) {
	files, _ := strconv.Atoi(os.Getenv("DEV_ENV_HISTORY_RELAY_MEASURE_FILES"))
	if files <= 0 {
		t.Skip("set DEV_ENV_HISTORY_RELAY_MEASURE_FILES to measure the relay's memory")
	}
	pathLen, _ := strconv.Atoi(os.Getenv("DEV_ENV_HISTORY_RELAY_MEASURE_PATH"))
	if pathLen <= 0 {
		pathLen = 120
	}
	at := time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)
	plan := protocol.HistoryExportPlan{Format: protocol.HistoryExportPlanFormat, Session: "measure",
		Roots: []protocol.HistoryRoot{{Name: protocol.HistoryRootClaude, Dir: ".claude"}}}
	sum := strings.Repeat("ab", 32)
	for i := range files {
		name := fmt.Sprintf("projects/%0*d.jsonl", max(pathLen-15, 8), i)
		plan.Files = append(plan.Files, protocol.HistoryFile{Root: protocol.HistoryRootClaude, Path: name, Bytes: 1000, Mode: 0o600, ModTime: at,
			SHA256: sum, Blocks: []string{sum}})
	}
	for i := range files / 10 {
		plan.Excluded = append(plan.Excluded, protocol.HistoryExcluded{Root: protocol.HistoryRootClaude, Path: fmt.Sprintf("file-history/%0*d", max(pathLen-15, 8), i),
			Class: protocol.HistoryClassUserCopy, Bytes: 10})
	}
	plan.Bytes = int64(files) * 1000
	line, err := protocol.CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	plan = protocol.HistoryExportPlan{}

	s, _, hold := guardSession(v1alpha1.HomeExpiryExporting)
	s.Status.Rescue = &v1alpha1.RescueStatus{Stamp: "20261111-1200", Result: v1alpha1.RescueCleanAndPushed, PodUID: string(hold.UID)}
	s.Status.PrivateHistory.ExecutorTerminal.ObservedAt = metav1.NewTime(at)
	var putBytes atomic.Int64
	relay := &ExecHistoryRelay{Exec: &scriptedExec{answers: map[string]func(io.Reader, io.Writer, io.Writer) error{
		"plan": func(_ io.Reader, out, _ io.Writer) error {
			// Written in exec-sized frames, as the stream protocol delivers it.
			for rest := line; len(rest) > 0; {
				n := min(len(rest), 32<<10)
				if _, err := out.Write(rest[:n]); err != nil {
					return err
				}
				rest = rest[n:]
			}
			return nil
		},
		"stream": func(io.Reader, io.Writer, io.Writer) error { return nil },
		"put": func(in io.Reader, out, _ io.Writer) error {
			n, err := io.Copy(io.Discard, in)
			putBytes.Store(n)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, `{"bytes":0,"contentSHA256":"%s","entry":"v1/_none/x/abababababababab","files":0,"gitFiles":0,"status":"stored"}`+"\n", sum)
			return err
		},
	}}, Shelf: &shelf.Shelf{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(shelfPodObject(hold.Namespace)).Build(), Namespace: hold.Namespace},
		Slots: shelf.NewExecSlots(1), PlanMax: 1 << 30}

	runtime.GC()
	debug.FreeOSMemory()
	rssBefore := procStatusKiB(t, "VmRSS")
	resetPeak := os.WriteFile("/proc/self/clear_refs", []byte("5"), 0) == nil
	heapBefore := liveHeap()
	stop := make(chan struct{})
	var peakHeap atomic.Uint64
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				if h := liveHeap(); h > peakHeap.Load() {
					peakHeap.Store(h)
				}
			}
		}
	}()
	start := time.Now()
	if _, err := relay.Archive(context.Background(), hold, HistoryExportArgs{Session: s.Name}, historyContent(s)); err != nil {
		t.Fatal(err)
	}
	close(stop)
	elapsed := time.Since(start)
	peak := "unknown (clear_refs refused)"
	if resetPeak {
		peak = fmt.Sprintf("%.1f MiB", float64(procStatusKiB(t, "VmHWM")-rssBefore)/1024)
	}
	t.Logf("files %d, path %d B: plan %.1f MiB, content.json %.1f MiB; peak heap above the start %.1f MiB, peak RSS above the start %s; %s",
		files, pathLen, float64(len(line))/(1<<20), float64(putBytes.Load())/(1<<20),
		float64(peakHeap.Load()-min(peakHeap.Load(), heapBefore))/(1<<20), peak, elapsed.Round(time.Millisecond))
}

// liveHeap is the bytes of heap objects, live or not yet swept.
func liveHeap() uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()
}

// procStatusKiB reads one kB field of /proc/self/status.
func procStatusKiB(t *testing.T, field string) int64 {
	t.Helper()
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Skip("no /proc/self/status:", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if name, rest, ok := strings.Cut(sc.Text(), ":"); ok && name == field {
			v, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Skip("no " + field + " in /proc/self/status")
	return 0
}
