package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRunner(t *testing.T) {
	r := ExecRunner{BaseEnv: []string{"PATH=/usr/bin:/bin", "A=base"}}
	dir := t.TempDir()
	res, err := r.Run(context.Background(), Cmd{
		Name:  "sh",
		Args:  []string{"-c", `printf '%s %s ' "$A" "$(pwd)"; cat; echo oops >&2`},
		Dir:   dir,
		Env:   []string{"A=over"},
		Stdin: strings.NewReader("in"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "over "+dir+" in" {
		t.Errorf("stdout = %q", got)
	}
	if string(res.Stderr) != "oops\n" {
		t.Errorf("stderr = %q", res.Stderr)
	}

	_, err = r.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo secret-arg >&2; exit 3", "secret-arg"}})
	var ce *CmdError
	if !errors.As(err, &ce) || ce.ExitCode != 3 {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-arg") {
		t.Errorf("Error() carries the arguments: %q", err)
	}

	if _, err := r.Run(context.Background(), Cmd{Name: filepath.Join(dir, "no-such-binary")}); err == nil || ExitCodeOf(err) != -1 {
		t.Errorf("missing binary: %v", err)
	}
	if _, err := r.LookPath("sh"); err != nil {
		t.Errorf("LookPath(sh): %v", err)
	}
}

func TestExecRunnerTimeLimitKillsTheGroup(t *testing.T) {
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
	// The grandchild keeps stdout open; only a group kill and WaitDelay end Run.
	start := time.Now()
	_, err := runBounded(context.Background(), ExecRunner{}, 100*time.Millisecond, Cmd{Name: "sh", Args: []string{"-c", "sleep 30 & sleep 30"}})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Run took %v after its limit", took)
	}
	if err == nil || !strings.Contains(err.Error(), "no answer within 100ms") {
		t.Errorf("err = %v", err)
	}
}

func TestWriteFileAtomicAndSymlinkForce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "f")
	if err := writeFileAtomic(p, []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(data) != "two" || fi.Mode().Perm() != 0o640 {
		t.Errorf("content %q mode %v", data, fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "sub", ".f.agentd-*")); len(left) != 0 {
		t.Errorf("temp files left: %q", left)
	}

	link := filepath.Join(dir, "link")
	for _, target := range []string{"/a", "/b", "/b"} {
		if err := symlinkForce(target, link); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.Readlink(link); got != "/b" {
		t.Errorf("link -> %q", got)
	}
	if err := symlinkForce("/a", filepath.Join(dir, "sub")); err == nil {
		t.Error("replaced a directory")
	}
}
