package controller

// Synthetic measurements only: nice -n19, GOMAXPROCS=2, -p1, -parallel1 and
// shared flock. Prepare and run in separate processes/cgroups; a fresh process
// alone is not evidence of cold file caches. Nothing here contacts Kubernetes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/thaynes43/dev-env/internal/testenv"
)

type relayMeasureShape struct {
	Files            int `json:"files"`
	Excluded         int `json:"excluded"`
	PathBytes        int `json:"requestedPathBytes"`
	PlanBytes        int `json:"canonicalPlanBytesIncludingNewline"`
	PathPaddingBytes int `json:"pathPaddingBytes"`
}

var relayMeasureAt = time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)

func relayMeasureFile(i, pathBytes int) protocol.HistoryFile {
	sum := strings.Repeat("ab", 32)
	return protocol.HistoryFile{Root: protocol.HistoryRootClaude, Path: fmt.Sprintf("projects/%0*d.jsonl", max(pathBytes-15, 8), i), Bytes: 1000, Mode: 0o600, ModTime: relayMeasureAt, SHA256: sum, Blocks: []string{sum}, BlocksSHA256: protocol.HistoryBlocksSHA256([]string{sum})}
}
func relayMeasureExcluded(i, pathBytes int) protocol.HistoryExcluded {
	return protocol.HistoryExcluded{Root: protocol.HistoryRootClaude, Path: fmt.Sprintf("file-history/%0*d", max(pathBytes-15, 8), i), Class: protocol.HistoryClassUserCopy, Bytes: 10}
}
func relayMeasurePlan(files, pathBytes int) (protocol.HistoryExportPlan, error) {
	if files < 1 || files > protocol.MaxHistoryExportFiles || pathBytes < 23 || pathBytes > protocol.MaxHistoryPathBytes {
		return protocol.HistoryExportPlan{}, errors.New("synthetic file count or path length out of bounds")
	}
	size, err := relayMeasureSizer(pathBytes)
	if err != nil {
		return protocol.HistoryExportPlan{}, err
	}
	if size(files) > historyPlanOutputMax {
		return protocol.HistoryExportPlan{}, errors.New("synthetic shape exceeds the production plan cap before construction")
	}
	p := protocol.HistoryExportPlan{Format: protocol.HistoryExportPlanFormat, Session: "measure", Roots: []protocol.HistoryRoot{{Name: protocol.HistoryRootClaude, Dir: ".claude"}}, Files: []protocol.HistoryFile{}, Excluded: []protocol.HistoryExcluded{}, Bytes: int64(files) * 1000}
	for i := range files {
		p.Files = append(p.Files, relayMeasureFile(i, pathBytes))
	}
	for i := range files / 10 {
		p.Excluded = append(p.Excluded, relayMeasureExcluded(i, pathBytes))
	}
	return p, nil
}

func relayMeasureSizer(pathBytes int) (func(int) int, error) {
	if pathBytes < 23 || pathBytes > protocol.MaxHistoryPathBytes {
		return nil, errors.New("synthetic path length out of bounds")
	}
	one := protocol.HistoryExportPlan{Format: protocol.HistoryExportPlanFormat, Session: "measure",
		Roots: []protocol.HistoryRoot{{Name: protocol.HistoryRootClaude, Dir: ".claude"}}, Files: []protocol.HistoryFile{}, Excluded: []protocol.HistoryExcluded{}}
	fb, err := protocol.CanonicalJSON(relayMeasureFile(0, pathBytes))
	if err != nil {
		return nil, err
	}
	eb, err := protocol.CanonicalJSON(relayMeasureExcluded(0, pathBytes))
	if err != nil {
		return nil, err
	}
	base, err := protocol.CanonicalJSON(one)
	if err != nil {
		return nil, err
	}
	return func(n int) int {
		x := len(base) + 1 + len(strconv.Itoa(n*1000)) - 1
		if n > 0 {
			x += n*(len(fb)+1) - 1
		}
		if e := n / 10; e > 0 {
			x += e*(len(eb)+1) - 1
		}
		return x
	}, nil
}

// Count search uses constant-size arithmetic, not repeated construction of
// large plans. Bounded path padding then reaches the exact target plus newline.
func exactRelayMeasurePlan(target, pathBytes int) ([]byte, relayMeasureShape, error) {
	if target <= 0 || target > historyPlanOutputMax {
		return nil, relayMeasureShape{}, errors.New("fixture target must fit the production 16MiB cap")
	}
	size, err := relayMeasureSizer(pathBytes)
	if err != nil {
		return nil, relayMeasureShape{}, err
	}
	lo, hi := 0, protocol.MaxHistoryExportFiles
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if size(mid) <= target {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	p, err := relayMeasurePlan(lo, pathBytes)
	if err != nil {
		return nil, relayMeasureShape{}, err
	}
	padding := target - size(lo)
	remaining := padding
	for i := len(p.Files) - 1; i >= 0 && remaining > 0; i-- {
		n := min(remaining, protocol.MaxHistoryPathBytes-len(p.Files[i].Path))
		p.Files[i].Path = strings.TrimSuffix(p.Files[i].Path, ".jsonl") + strings.Repeat("x", n) + ".jsonl"
		remaining -= n
	}
	for i := len(p.Excluded) - 1; i >= 0 && remaining > 0; i-- {
		n := min(remaining, protocol.MaxHistoryPathBytes-len(p.Excluded[i].Path))
		p.Excluded[i].Path += strings.Repeat("x", n)
		remaining -= n
	}
	if remaining != 0 {
		return nil, relayMeasureShape{}, errors.New("requested shape has insufficient path padding capacity")
	}
	b, err := protocol.CanonicalJSON(p)
	if err != nil {
		return nil, relayMeasureShape{}, err
	}
	b = append(b, '\n')
	if len(b) != target {
		return nil, relayMeasureShape{}, fmt.Errorf("fixture accounting: got %d, want %d", len(b), target)
	}
	return b, relayMeasureShape{Files: lo, Excluded: lo / 10, PathBytes: pathBytes, PlanBytes: len(b), PathPaddingBytes: padding}, nil
}

// MODE=prepare seals ROOT/plan.json and metadata without running a relay.
// If prepare fails after creating ROOT, it leaves the unsealed ROOT as evidence.
// Neither prepare nor reuse accepts that ROOT. Any separately authorized future
// attempt needs a new ROOT; there is no automatic retry or cleanup.
// MODE=run reuses that private fixture. BYTES requests an exact canonical size
// (including newline); FILES retains the earlier inline shape experiment.
// MODE=selftest runs only the bounded prerequisite checks, not a measurement.
func TestHistoryRelayMeasure(t *testing.T) {
	mode := os.Getenv("DEV_ENV_HISTORY_RELAY_MEASURE_MODE")
	if mode == "selftest" {
		testHistoryRelayMeasurementFixtures(t)
		return
	}
	if mode != "" && mode != "prepare" && mode != "run" {
		t.Fatal("mode must be prepare, run or selftest")
	}
	root := os.Getenv("DEV_ENV_HISTORY_RELAY_MEASURE_ROOT")
	if mode == "run" && root == "" {
		t.Fatal("run requires a frozen synthetic fixture ROOT")
	}
	var preparation *testenv.MeasureFixture
	if mode == "prepare" {
		var err error
		preparation, err = testenv.CreateMeasureFixture(root, "relay")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = preparation.Close() }()
	}
	var line []byte
	var shape relayMeasureShape
	inputSource := "inline construction: warm setup is included in total process/cgroup peak"
	if root != "" && mode != "prepare" {
		f, err := testenv.OpenMeasureFixture(root, "relay")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if err := f.Metadata(&shape); err != nil {
			t.Fatal(err)
		}
		line, err = f.Read("plan.json", historyPlanOutputMax)
		if err != nil || shape.PlanBytes != len(line) || shape.Files < 1 || shape.Files > protocol.MaxHistoryExportFiles || shape.PathBytes < 23 || shape.PathBytes > protocol.MaxHistoryPathBytes {
			t.Fatalf("invalid frozen fixture: %v", err)
		}
		inputSource = "frozen fixture: prior-process construction excluded; load/hash/setup included in total peak"
	} else {
		pathBytes := relayMeasureEnvInt(t, "DEV_ENV_HISTORY_RELAY_MEASURE_PATH", 120)
		target := relayMeasureEnvInt(t, "DEV_ENV_HISTORY_RELAY_MEASURE_BYTES", 0)
		if mode == "prepare" && target == 0 {
			target = historyPlanOutputMax
		}
		if target > 0 {
			var err error
			line, shape, err = exactRelayMeasurePlan(target, pathBytes)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			files := relayMeasureEnvInt(t, "DEV_ENV_HISTORY_RELAY_MEASURE_FILES", 0)
			if files == 0 {
				t.Skip("set frozen ROOT, exact BYTES, or synthetic FILES")
			}
			p, err := relayMeasurePlan(files, pathBytes)
			if err != nil {
				t.Fatal(err)
			}
			line, err = protocol.CanonicalJSON(p)
			if err != nil {
				t.Fatal(err)
			}
			line = append(line, '\n')
			shape = relayMeasureShape{Files: files, Excluded: files / 10, PathBytes: pathBytes, PlanBytes: len(line)}
		}
		if len(line) > historyPlanOutputMax {
			t.Fatal("fixture exceeds production plan cap; no PlanMax override")
		}
		if mode == "prepare" {
			if err := preparation.Seal(shape, map[string][]byte{"plan.json": line}); err != nil {
				t.Fatal(err)
			}
			t.Logf("prepared synthetic fixture: %+v; no relay executed", shape)
			return
		}
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		t.Fatal("fixture lacks canonical newline")
	}
	measureHistoryRelay(t, line, shape, inputSource)
}
func relayMeasureEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	s := os.Getenv(name)
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		t.Fatalf("%s must be a positive integer", name)
	}
	return n
}

func measureRelay(line []byte, putBytes *atomic.Int64) (*ExecHistoryRelay, *v1alpha1.AgentSession) {
	s, _, hold := guardSession(v1alpha1.HomeExpiryExporting)
	s.Status.Rescue = &v1alpha1.RescueStatus{Stamp: "20261111-1200", Result: v1alpha1.RescueCleanAndPushed, PodUID: string(hold.UID)}
	s.Status.PrivateHistory.ExecutorTerminal.ObservedAt = metav1.NewTime(relayMeasureAt)
	sum := strings.Repeat("ab", 32)
	r := &ExecHistoryRelay{Exec: &scriptedExec{answers: map[string]func(io.Reader, io.Writer, io.Writer) error{
		"plan": func(_ io.Reader, out, _ io.Writer) error {
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
	}}, Shelf: &shelf.Shelf{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(shelfPodObject(hold.Namespace)).Build(), Namespace: hold.Namespace}, Slots: shelf.NewExecSlots(1)} // Production plan ceiling, unchanged.
	return r, s
}
func measureHistoryRelay(t *testing.T, line []byte, shape relayMeasureShape, inputSource string) {
	t.Helper()
	var putBytes atomic.Int64
	relay, s := measureRelay(line, &putBytes)
	_, _, hold := guardSession(v1alpha1.HomeExpiryExporting)
	runtime.GC()
	debug.FreeOSMemory()
	before := testenv.ReadMeasureMemory()
	heapBefore := liveHeap()
	stop, done := make(chan struct{}), make(chan struct{})
	var peakHeap atomic.Uint64
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if h := liveHeap(); h > peakHeap.Load() {
					peakHeap.Store(h)
				}
			}
		}
	}()
	start := time.Now()
	_, err := relay.Archive(context.Background(), hold, HistoryExportArgs{Session: s.Name}, historyContent(s))
	close(stop)
	<-done
	after := testenv.ReadMeasureMemory()
	receipt, marshalErr := json.Marshal(struct {
		Shape          relayMeasureShape     `json:"shape"`
		Input          string                `json:"input"`
		Before         testenv.MeasureMemory `json:"before"`
		After          testenv.MeasureMemory `json:"after"`
		RSSDeltaKiB    int64                 `json:"rssDeltaKiB"`
		HeapDeltaBytes uint64                `json:"peakHeapAboveBaselineBytes"`
		ContentBytes   int64                 `json:"contentBytes"`
		Wall           string                `json:"wall"`
	}{shape, inputSource, before, after, after.RSSKiB - before.RSSKiB, peakHeap.Load() - min(peakHeap.Load(), heapBefore), putBytes.Load(), time.Since(start).String()})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Log(string(receipt))
	if err != nil {
		t.Fatal(err)
	}
}
func liveHeap() uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()
}

func TestHistoryRelayMeasurementFixtures(t *testing.T) { testHistoryRelayMeasurementFixtures(t) }
func testHistoryRelayMeasurementFixtures(t *testing.T) {
	t.Run("prepare-freeze-reuse-without-envtest", func(t *testing.T) {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		parent := t.TempDir()
		root := filepath.Join(parent, "relay")
		cmd := exec.Command(exe, "-test.run=^TestHistoryRelayMeasure$", "-test.count=1", "-test.parallel=1", "-test.v")
		cmd.Dir = parent // No ../templates or envtest assets in this directory.
		cmd.Env = []string{"GOMAXPROCS=2", "DEV_ENV_HISTORY_RELAY_MEASURE_ONLY=1", "DEV_ENV_HISTORY_RELAY_MEASURE_MODE=prepare", "DEV_ENV_HISTORY_RELAY_MEASURE_BYTES=8192", "DEV_ENV_HISTORY_RELAY_MEASURE_ROOT=" + root}
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "no relay executed") {
			t.Fatalf("isolated preparation: %s: %v", out, err)
		}
		f, err := testenv.OpenMeasureFixture(root, "relay")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		line, err := f.Read("plan.json", historyPlanOutputMax)
		if err != nil || len(line) != 8192 {
			t.Fatalf("frozen small plan: %d: %v", len(line), err)
		}
		var shape relayMeasureShape
		if err := f.Metadata(&shape); err != nil || shape.PlanBytes != len(line) || shape.PathBytes != 120 {
			t.Fatalf("shape metadata: %+v: %v", shape, err)
		}
		var putBytes atomic.Int64
		r, s := measureRelay(line, &putBytes)
		_, _, hold := guardSession(v1alpha1.HomeExpiryExporting)
		if _, err := r.Archive(context.Background(), hold, HistoryExportArgs{Session: s.Name}, historyContent(s)); err != nil || putBytes.Load() == 0 {
			t.Fatalf("frozen input was not accepted by the real relay with fake exec: %v", err)
		}
	})
	t.Run("exact-small-shapes", func(t *testing.T) {
		for _, pathBytes := range []int{120, 1000, 1024} {
			target := 128 << 10
			if pathBytes == 1024 {
				size, err := relayMeasureSizer(pathBytes)
				if err != nil {
					t.Fatal(err)
				}
				target = size(32) // A maximal-path small shape need not have spare padding.
			}
			line, shape, err := exactRelayMeasurePlan(target, pathBytes)
			if err != nil || len(line) != target || shape.PlanBytes != len(line) || shape.PathBytes != pathBytes {
				t.Fatalf("shape %d: %+v: %v", pathBytes, shape, err)
			}
			var p protocol.HistoryExportPlan
			if err := json.Unmarshal(line, &p); err != nil {
				t.Fatal(err)
			}
			if eq, err := protocol.CanonicalEqual(p, line[:len(line)-1]); err != nil || !eq {
				t.Fatalf("noncanonical fixture: %v", err)
			}
			for _, f := range p.Files {
				if len(f.Path) > protocol.MaxHistoryPathBytes {
					t.Fatal("padding exceeded path bound")
				}
			}
		}
		if _, _, err := exactRelayMeasurePlan(16384, 1000); err == nil {
			t.Fatal("shape with insufficient padding capacity was silently stretched past the path bound")
		}
		if _, err := relayMeasurePlan(protocol.MaxHistoryExportFiles, 1024); err == nil {
			t.Fatal("oversized shape was not rejected before constructing it")
		}
	})
	t.Run("default-cap-plus-one", func(t *testing.T) {
		var count atomic.Int64
		r, s := measureRelay(nil, &count)
		if r.planMax() != 16<<20 || r.PlanMax != 0 {
			t.Fatal("measurement bypasses production cap")
		}
		r.Exec.(*scriptedExec).answers["plan"] = func(_ io.Reader, out, _ io.Writer) error {
			_, err := io.CopyN(out, repeatMeasureByte{}, int64(historyPlanOutputMax)+1)
			return err
		}
		_, _, hold := guardSession(v1alpha1.HomeExpiryExporting)
		_, err := r.Archive(context.Background(), hold, HistoryExportArgs{Session: s.Name}, historyContent(s))
		if !errors.Is(err, errPlanTooLarge) || count.Load() != 0 {
			t.Fatalf("cap+1 did not refuse before put: %v", err)
		}
	})
	t.Run("gate-refuses-misuse", func(t *testing.T) {
		if on, err := historyMeasurementOnly("", "anything"); on || err != nil {
			t.Fatal("ordinary envtest gate changed")
		}
		if on, err := historyMeasurementOnly("1", "^TestHistoryRelayMeasure$"); !on || err != nil {
			t.Fatal("exact opt-in refused")
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cases := [][]string{
			{"-test.run=TestHistoryRelayMeasure"},
			{"-test.run=^TestHistoryRelayMeasure$/child"},
			{"-test.run=.*"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.list=.*"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.bench=.*"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.fuzz=.*"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.skip=.*"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.count=2"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.count=0"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.parallel=2"},
			{"-test.run=^TestHistoryRelayMeasure$", "-test.cpu=1,2"},
		}
		for _, args := range cases {
			args = append(args, "-test.parallel=1")
			if strings.Contains(strings.Join(args, " "), "-test.parallel=2") {
				args = args[:len(args)-1]
			}
			cmd := exec.Command(exe, args...)
			cmd.Env = []string{"DEV_ENV_HISTORY_RELAY_MEASURE_ONLY=1", "GOMAXPROCS=2"}
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "history measurement:") {
				t.Fatalf("misuse did not exit before envtest: %s: %v", out, err)
			}
		}
		cmd := exec.Command(exe, "-test.run=^TestHistoryRelayMeasure$", "-test.count=1", "-test.parallel=1")
		cmd.Env = []string{"DEV_ENV_HISTORY_RELAY_MEASURE_ONLY=0", "GOMAXPROCS=2"}
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "requires -test.run=^TestHistoryRelayMeasure$") {
			t.Fatalf("invalid opt-in did not refuse before envtest: %s: %v", out, err)
		}
	})
}

type repeatMeasureByte struct{}

func (repeatMeasureByte) Read(p []byte) (int, error) { clear(p); return len(p), nil }
