package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const rcGeneration = "..2026_10_30_13_40_00.123456789"

func projectRCTarget(t *testing.T, config string) string {
	t.Helper()
	dir := filepath.Join(config, "codex")
	if err := os.Mkdir(filepath.Join(dir, rcGeneration), 0700); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, rcGeneration, "rc-target.json")
	if err := os.Rename(filepath.Join(dir, "rc-target.json"), leaf); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rcGeneration, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/rc-target.json", filepath.Join(dir, "rc-target.json")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodexRCProjectedPacketQualifiesAndRotates(t *testing.T) {
	s, sess, d, deps, count := rcControlFixture(t)
	dir := projectRCTarget(t, s.ConfigDir)
	rec, err := codexRCControl(context.Background(), s, sess, "preflight", d, deps)
	if err != nil || rec.State != "qualified" || *count != 0 {
		t.Fatalf("projection refused: %+v %v", rec, err)
	}
	next := "..2026_10_30_13_41_00.987654321"
	if err := os.Mkdir(filepath.Join(dir, next), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, next, "rc-target.json"), []byte(`{"changed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, filepath.Join(dir, "..new")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..new"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	rec, err = codexRCControl(context.Background(), s, sess, "start", d, deps)
	if err != nil || rec.State != "refused" || *count != 0 || exists(s.statePath(codexRCOnceFile)) {
		t.Fatalf("stale packet used: %+v %v", rec, err)
	}
}

func TestCodexRCTargetProjectionRejectsAdversarialPaths(t *testing.T) {
	for _, mode := range []string{"absolute key", "traversal key", "absolute data", "traversal data", "non timestamp", "symlink generation", "symlink leaf", "directory leaf", "FIFO leaf", "oversize", "missing data", "symlink config", "symlink ancestor"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, d, deps, count := rcControlFixture(t)
			dir := projectRCTarget(t, s.ConfigDir)
			key, data, gen := filepath.Join(dir, "rc-target.json"), filepath.Join(dir, "..data"), filepath.Join(dir, rcGeneration)
			replace := func(path, target string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "absolute key":
				replace(key, filepath.Join(gen, "rc-target.json"))
			case "traversal key":
				replace(key, "../codex/..data/rc-target.json")
			case "absolute data":
				replace(data, gen)
			case "traversal data":
				replace(data, "../codex/"+rcGeneration)
			case "non timestamp":
				replace(data, "unexpected")
			case "symlink generation":
				if err := os.Rename(gen, gen+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(gen+"-moved", gen); err != nil {
					t.Fatal(err)
				}
			case "symlink leaf":
				replace(filepath.Join(gen, "rc-target.json"), key)
			case "directory leaf":
				if err := os.Remove(filepath.Join(gen, "rc-target.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(gen, "rc-target.json"), 0700); err != nil {
					t.Fatal(err)
				}
			case "FIFO leaf":
				if err := os.Remove(filepath.Join(gen, "rc-target.json")); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(filepath.Join(gen, "rc-target.json"), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(filepath.Join(gen, "rc-target.json"), []byte(strings.Repeat("x", 8193)), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing data":
				if err := os.Remove(data); err != nil {
					t.Fatal(err)
				}
			case "symlink ancestor":
				if err := os.Rename(s.ConfigDir, s.ConfigDir+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(s.ConfigDir+"-moved", s.ConfigDir); err != nil {
					t.Fatal(err)
				}
			case "symlink config":
				if err := os.Rename(dir, dir+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-moved", dir); err != nil {
					t.Fatal(err)
				}
			}
			rec, err := codexRCControl(context.Background(), s, sess, "start", d, deps)
			if err != nil || rec.State != "refused" || *count != 0 || exists(s.statePath(codexRCOnceFile)) {
				t.Fatalf("unsafe packet used: %+v %v", rec, err)
			}
		})
	}
}
