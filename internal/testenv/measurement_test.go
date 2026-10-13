package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMeasureFixturePrivateFrozenInputs(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "fixture")
	f, err := CreateMeasureFixture(root, "small")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Seal(map[string]int{"target": 12}, map[string][]byte{"plan.json": []byte("synthetic\n")}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := CreateMeasureFixture(root, "small"); err == nil {
		t.Fatal("prepare overwrote an existing root")
	}
	f, err = OpenMeasureFixture(root, "small")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := f.Read("plan.json", 64)
	if err != nil || string(b) != "synthetic\n" {
		t.Fatalf("frozen reuse: %q: %v", b, err)
	}
	if _, err := f.Read("plan.json", 2); err == nil {
		t.Fatal("input size bound ignored")
	}
	if _, err := f.Read("../plan.json", 64); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := OpenMeasureFixture(root, "other-kind"); err == nil {
		t.Fatal("wrong fixture kind accepted")
	}
	if err := os.Chmod(filepath.Join(root, "plan.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plan.json"), []byte("changed!!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "plan.json"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read("plan.json", 64); err == nil {
		t.Fatal("frozen digest mismatch accepted")
	}
}

func TestMeasureFixtureRefusesUnsafeRootsAndLinks(t *testing.T) {
	parent := t.TempDir()
	if _, err := CreateMeasureFixture("relative", "small"); err == nil {
		t.Fatal("relative root accepted")
	}
	if _, err := CreateMeasureFixture(os.Getenv("HOME"), "small"); err == nil {
		t.Fatal("HOME accepted")
	}
	if err := os.Symlink(parent, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMeasureFixture(filepath.Join(parent, "link", "new"), "small"); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	root := filepath.Join(parent, "fixture")
	f, err := CreateMeasureFixture(root, "small")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Seal(nil, map[string][]byte{"plan.json": []byte("synthetic")}); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "plan.json"), filepath.Join(root, "extra-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read("plan.json", 64); err == nil {
		t.Fatal("multiply linked fixture file accepted")
	}
	if err := os.Remove(filepath.Join(root, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("extra-link", filepath.Join(root, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read("plan.json", 64); err == nil {
		t.Fatal("symlink fixture file accepted")
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMeasureFixture(root, "small"); err == nil {
		t.Fatal("nonprivate fixture root accepted")
	}
}

func TestMeasureSelfCgroupDoesNotFallbackToParent(t *testing.T) {
	mount := []byte("1 0 0:1 /slice /sys/fs/cgroup rw - cgroup2 cgroup rw\n")
	if got, err := selfMeasureCgroup([]byte("0::/slice/container\n"), mount); err != nil || got != "/sys/fs/cgroup/container" {
		t.Fatalf("own cgroup: %q: %v", got, err)
	}
	for _, own := range []string{"0::/elsewhere\n", "0::/../../slice\n", "1:memory:/slice\n"} {
		if got, err := selfMeasureCgroup([]byte(own), mount); err == nil || got != "" {
			t.Fatalf("unresolved own path guessed parent: %q: %v", got, err)
		}
	}
}
