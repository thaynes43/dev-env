package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/thaynes43/dev-env/internal/testenv"
)

type exportMeasureFixture struct {
	TargetBytes                  int64  `json:"totalExportedFileBytes"`
	BaseBytes                    int64  `json:"baseFixtureExportedBytes"`
	Files                        int    `json:"exportedFiles"`
	Session                      string `json:"session"`
	SessionUID                   string `json:"sessionUID"`
	PlanSHA256                   string `json:"planSHA256"`
	ContentSHA256                string `json:"contentSHA256"`
	PlanLineBytes                int    `json:"planLineBytes"`
	ContentLineBytes             int    `json:"contentLineBytes"`
	RequiresSyntheticSharedMount bool   `json:"requiresSyntheticSharedMount"`
}

// MIB denotes TOTAL exported file bytes, including every existing fixture file.
// MODE=prepare seals a new private ROOT, without measuring execution. A later
// MODE=plan or stream reuses it; each phase needs its own Job/cgroup and cache
// evidence for cold acceptance. With no MODE this remains an explicitly warm
// combined exploratory check, never the deployment-sized cold measurement.
// CPU rule: nice -n19, GOMAXPROCS=2, -p1, -parallel1 and shared flock. No stress,
// wide parallel tests, cache drops or live HOME/claim mounts are authorized here.
func TestHistoryExportMeasure(t *testing.T) {
	mode := os.Getenv("DEV_ENV_HISTORY_MEASURE_MODE")
	if mode != "" && mode != "prepare" && mode != "plan" && mode != "stream" {
		t.Fatal("mode must be prepare, plan or stream")
	}
	root := os.Getenv("DEV_ENV_HISTORY_MEASURE_ROOT")
	if mode == "plan" || mode == "stream" {
		r, meta, _, _, err := loadExportMeasureFixture(root)
		if err != nil {
			t.Fatal(err)
		}
		// A reused fixture is for the production CLI/mount behavior, without the
		// in-process test rig's mount-check replacements.
		if err := sharedIsMounted(r.s.SharedDir, r.s.Home); err != nil {
			t.Fatalf("fixture needs its own synthetic shared mount: %v", err)
		}
		measureExportPhase(t, mode, func() {
			if mode == "plan" {
				p := r.plan(t)
				sum, _, err := HistoryPlanSHA256(p)
				if err != nil || sum != meta.PlanSHA256 || p.Bytes != meta.TargetBytes {
					t.Fatalf("frozen fixture changed: %v", err)
				}
			} else if err := StreamHistoryExport(r.s, r.o, meta.PlanSHA256, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
		t.Log("single phase only; fresh process is not cold-cache evidence; stream here discards output, not a shelf resource measurement")
		return
	}
	raw := os.Getenv("DEV_ENV_HISTORY_MEASURE_MIB")
	if raw == "" {
		t.Skip("set total fixture MIB, or select a prepared plan/stream phase")
	}
	mib, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || mib <= 0 || mib > protocol.MaxHistoryExportBytes/(1<<20) {
		t.Fatal("MIB must be positive and no more than the production 2048MiB ceiling")
	}
	if mode == "" {
		if root != "" {
			t.Fatal("a persistent fixture ROOT requires an explicit prepare, plan or stream mode")
		}
		root = filepath.Join(t.TempDir(), "fixture")
	}
	r, meta, plan, content, err := prepareExportMeasureFixture(t, root, mib<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("payload %d bytes (base fixture %d), plan %d bytes, content line %d bytes; manifest/tar/block/index overhead is separate from the 2GiB file-byte ceiling", meta.TargetBytes, meta.BaseBytes, meta.PlanLineBytes, meta.ContentLineBytes)
	if mode == "prepare" {
		t.Log("synthetic fixture prepared; no resource measurement executed")
		return
	}
	t.Log("warm combined exploratory check: fixture construction and earlier hashing are included in lifetime process/cgroup peaks; not cold acceptance")
	ar := newArchiveRig(t, r)
	measureExportPhase(t, "plan", func() { plan = r.plan(t) })
	var result protocol.HistoryPutResult
	measureExportPhase(t, "stream+put (same process)", func() {
		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := pw.Write(content); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			_ = pw.CloseWithError(StreamHistoryExport(r.s, r.o, meta.PlanSHA256, pw))
		}()
		var err error
		result, err = ar.a.Put(pr, "synthetic-measure")
		_ = pr.Close()
		<-done
		if err != nil {
			t.Fatal(err)
		}
	})
	measureExportPhase(t, "verify", func() {
		if _, err := ar.a.Verify(meta.SessionUID, result.ContentSHA256); err != nil {
			t.Fatal(err)
		}
	})
	var storedBytes int64
	if err := filepath.WalkDir(ar.a.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			storedBytes += fi.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("archive regular-file bytes %d; stored metadata/block/index overhead %d (logical sizes, not allocated disk blocks); tar framing not stored", storedBytes, storedBytes-plan.Bytes)
}

func exportMeasureRemaining(target, base int64) (int64, error) {
	if target <= 0 || target > protocol.MaxHistoryExportBytes || base < 0 || base > target {
		return 0, fmt.Errorf("requested total %d does not fit base fixture %d and production file-byte ceiling %d", target, base, protocol.MaxHistoryExportBytes)
	}
	return target - base, nil
}

func prepareExportMeasureFixture(t *testing.T, root string, target int64) (*historyRig, exportMeasureFixture, protocol.HistoryExportPlan, []byte, error) {
	t.Helper()
	// Check the ceiling before creating files; preparation never overwrites a root.
	if _, err := exportMeasureRemaining(target, 0); err != nil {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
	}
	fixture, err := testenv.CreateMeasureFixture(root, "export")
	if err != nil {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
	}
	defer func() { _ = fixture.Close() }()
	r := newHistoryRigAt(t, root)
	base := r.plan(t).Bytes
	remaining, err := exportMeasureRemaining(target, base)
	if err != nil {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
	}
	rng := rand.NewChaCha8([32]byte{1})
	buf := make([]byte, 1<<20)
	for i := 0; remaining > 0; i++ {
		n := min(remaining, int64(32<<20))
		p := r.path(fmt.Sprintf(".codex/sessions/2026/10/12/measure-%04d.jsonl", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
		}
		left := n
		for left > 0 {
			chunk := buf[:min(left, int64(len(buf)))]
			_, _ = rng.Read(chunk)
			if _, err := f.Write(chunk); err != nil {
				_ = f.Close()
				return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
			}
			left -= int64(len(chunk))
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
		}
		remaining -= n
	}
	plan := r.plan(t)
	if plan.Bytes != target {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, fmt.Errorf("total fixture payload %d, want %d", plan.Bytes, target)
	}
	planSHA, planJSON, err := HistoryPlanSHA256(plan)
	if err != nil {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
	}
	contentJSON, err := protocol.CanonicalJSON(testContent(t, plan, "dev-env", testSessionUID))
	if err != nil {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
	}
	contentSum := sha256.Sum256(contentJSON)
	planJSON = append(planJSON, '\n')
	contentJSON = append(contentJSON, '\n')
	meta := exportMeasureFixture{TargetBytes: target, BaseBytes: base, Files: len(plan.Files), Session: testHistorySession, SessionUID: testSessionUID, PlanSHA256: planSHA, ContentSHA256: hex.EncodeToString(contentSum[:]), PlanLineBytes: len(planJSON), ContentLineBytes: len(contentJSON), RequiresSyntheticSharedMount: true}
	if err := fixture.Seal(meta, map[string][]byte{"plan.json": planJSON, "content.json": contentJSON}); err != nil {
		return nil, exportMeasureFixture{}, protocol.HistoryExportPlan{}, nil, err
	}
	return r, meta, plan, contentJSON, nil
}

func loadExportMeasureFixture(root string) (*historyRig, exportMeasureFixture, []byte, []byte, error) {
	f, err := testenv.OpenMeasureFixture(root, "export")
	if err != nil {
		return nil, exportMeasureFixture{}, nil, nil, err
	}
	defer func() { _ = f.Close() }()
	var meta exportMeasureFixture
	if err := f.Metadata(&meta); err != nil {
		return nil, meta, nil, nil, err
	}
	plan, err := f.Read("plan.json", protocol.MaxHistoryContentBytes*2)
	if err != nil {
		return nil, meta, nil, nil, err
	}
	content, err := f.Read("content.json", protocol.MaxHistoryContentBytes+1)
	if err != nil {
		return nil, meta, nil, nil, err
	}
	if meta.Session != testHistorySession || meta.SessionUID != testSessionUID || meta.TargetBytes <= 0 || meta.TargetBytes > protocol.MaxHistoryExportBytes || meta.BaseBytes > meta.TargetBytes || len(plan) != meta.PlanLineBytes || len(content) != meta.ContentLineBytes || len(plan) == 0 || len(content) == 0 || plan[len(plan)-1] != '\n' || content[len(content)-1] != '\n' {
		return nil, meta, nil, nil, errors.New("invalid synthetic export metadata")
	}
	ps, cs := sha256.Sum256(plan[:len(plan)-1]), sha256.Sum256(content[:len(content)-1])
	if hex.EncodeToString(ps[:]) != meta.PlanSHA256 || hex.EncodeToString(cs[:]) != meta.ContentSHA256 {
		return nil, meta, nil, nil, errors.New("frozen canonical digest mismatch")
	}
	home := filepath.Join(root, "home")
	creds := filepath.Join(root, "creds", "gh_token")
	for _, p := range []string{home, creds} {
		if err := noSymlinkComponents(p); err != nil {
			return nil, meta, nil, nil, err
		}
	}
	r := &historyRig{home: home, s: Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), SharedDir: filepath.Join(home, ".shared"), GHTokenFile: creds}, o: HistoryExportOptions{Session: testHistorySession, ClaudeConfigDir: ".claude", CodexHome: ".codex"}}
	return r, meta, plan, content, nil
}

func measureExportPhase(t *testing.T, name string, run func()) {
	t.Helper()
	runtime.GC()
	before := testenv.ReadMeasureMemory()
	var r0, r1 syscall.Rusage
	var m0, m1 runtime.MemStats
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r0)
	runtime.ReadMemStats(&m0)
	start := time.Now()
	defer func() {
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r1)
		runtime.ReadMemStats(&m1)
		receipt, err := json.Marshal(struct {
			Phase          string                `json:"phase"`
			Before         testenv.MeasureMemory `json:"before"`
			After          testenv.MeasureMemory `json:"after"`
			Wall           string                `json:"wall"`
			CPU            string                `json:"cpu"`
			AllocatedBytes uint64                `json:"allocatedBytes"`
		}{name, before, testenv.ReadMeasureMemory(), time.Since(start).String(), (time.Duration(r1.Utime.Nano()-r0.Utime.Nano()) + time.Duration(r1.Stime.Nano()-r0.Stime.Nano())).String(), m1.TotalAlloc - m0.TotalAlloc})
		if err != nil {
			t.Error(err)
		} else {
			t.Log(string(receipt))
		}
	}()
	run()
}

func TestHistoryExportMeasurementFixtures(t *testing.T) {
	t.Run("total-accounting", func(t *testing.T) {
		if n, err := exportMeasureRemaining(protocol.MaxHistoryExportBytes, 12345); err != nil || n != protocol.MaxHistoryExportBytes-12345 {
			t.Fatalf("2GiB accounting: %d %v", n, err)
		}
		for _, pair := range [][2]int64{{protocol.MaxHistoryExportBytes + 1, 0}, {10, 11}, {-1, 0}} {
			if _, err := exportMeasureRemaining(pair[0], pair[1]); err == nil {
				t.Fatal("invalid size accepted")
			}
		}
	})
	t.Run("prepare-freeze-reuse", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "fixture")
		r, meta, plan, content, err := prepareExportMeasureFixture(t, root, 6<<20)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Bytes != 6<<20 || meta.TargetBytes != plan.Bytes || meta.BaseBytes <= 0 {
			t.Fatalf("base files not counted: %+v", meta)
		}
		reused, saved, line, savedContent, err := loadExportMeasureFixture(root)
		if err != nil {
			t.Fatal(err)
		}
		if saved != meta || string(savedContent) != string(content) || len(line) != meta.PlanLineBytes || reused.s.Home != r.s.Home {
			t.Fatal("frozen fixture changed on reuse")
		}
		sum, _, err := HistoryPlanSHA256(reused.plan(t))
		if err != nil || sum != meta.PlanSHA256 {
			t.Fatalf("reused synthetic HOME differs: %v", err)
		}
		if _, _, _, _, err := prepareExportMeasureFixture(t, root, 6<<20); err == nil {
			t.Fatal("preexisting fixture overwritten")
		}
		// Frozen control-file tampering must refuse before any HOME walk.
		if err := os.Chmod(filepath.Join(root, "plan.json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := loadExportMeasureFixture(root); err == nil {
			t.Fatal("mutable control file accepted")
		}
	})
}
