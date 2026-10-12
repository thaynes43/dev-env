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

func shortStop(t *testing.T, grace time.Duration) {
	t.Helper()
	oldGrace, oldDelay := stopGrace, waitDelay
	stopGrace, waitDelay = grace, grace+200*time.Millisecond
	t.Cleanup(func() { stopGrace, waitDelay = oldGrace, oldDelay })
}

// A stopped command ends within the allowance the shared Git queue covers.
func TestExecRunnerStopFitsTheSharedGitHeadroom(t *testing.T) {
	if stopGrace >= waitDelay || sharedGitAdminBudget-sharedGitPrepareBudget <= waitDelay {
		t.Fatalf("stopGrace %v, waitDelay %v and the queue's headroom %v do not nest", stopGrace, waitDelay, sharedGitAdminBudget-sharedGitPrepareBudget)
	}
}

func TestExecRunnerTimeLimitKillsTheGroup(t *testing.T) {
	for _, mode := range []string{"stops on SIGTERM", "ignores SIGTERM"} {
		t.Run(mode, func(t *testing.T) {
			shortStop(t, 200*time.Millisecond)
			// The grandchild keeps stdout open; only a group signal and
			// WaitDelay end Run. An ignored SIGTERM is inherited, so only the
			// group SIGKILL after stopGrace ends that one.
			script := "sleep 30 & sleep 30"
			if mode == "ignores SIGTERM" {
				script = "trap '' TERM; " + script
			}
			start := time.Now()
			_, err := runBounded(context.Background(), ExecRunner{}, 100*time.Millisecond, Cmd{Name: "sh", Args: []string{"-c", script}})
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("Run took %v after its limit", took)
			}
			if err == nil || !strings.Contains(err.Error(), "no answer within 100ms") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// The time limit asks the group to stop before it kills it, so a command's
// own cleanup runs. The grace is far longer than the test may take: only the
// SIGTERM handler can write the marker.
func TestExecRunnerTimeLimitSendsSIGTERMFirst(t *testing.T) {
	shortStop(t, time.Minute)
	marker := filepath.Join(t.TempDir(), "cleaned")
	_, err := runBounded(context.Background(), ExecRunner{}, 100*time.Millisecond, Cmd{Name: "sh", Args: []string{"-c", `trap 'echo done > "$0"; exit 143' TERM; sleep 30 & wait`, marker}})
	if err == nil {
		t.Fatal("the time limit did not end the command")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "done\n" {
		t.Fatalf("SIGTERM handler did not run: %q %v", data, err)
	}
}

// awaitFixtureFile polls for a file a blocked command creates. It bounds a test
// fixture's wait; no assertion depends on how long it takes.
func awaitFixtureFile(t *testing.T, path string, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !exists(path) {
		select {
		case <-done:
			t.Fatalf("command ended before %s appeared", path)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A blob:none clone fetches missing blobs from inside a checkout, while Git
// holds index.lock (merge) or a half-made worktree (worktree add). A time
// limit that ends there must leave neither behind: Git's own SIGTERM handlers
// remove them, a SIGKILL would not (DESIGN-007, recoverable advance).
func TestExecRunnerTimeLimitLetsGitRemoveItsLocks(t *testing.T) {
	for _, mode := range []string{"merge", "worktree add"} {
		t.Run(mode, func(t *testing.T) {
			shortStop(t, time.Minute)
			g := newGitFixture(t, "demo")
			clone := filepath.Join(g.root, "clone")
			gitRun(t, g.env, g.root, "clone", "-q", "--filter=blob:none", "file://"+g.remote, clone)
			first := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			pushBootstrapChange(t, g)
			gitRun(t, g.env, clone, "fetch", "-q", "origin")
			// The next lazy fetch blocks until the time limit ends it.
			started := filepath.Join(g.root, "lazy-fetch-started")
			gitRun(t, g.env, clone, "config", "remote.origin.uploadpack", "touch '"+started+"'; sleep 60; git-upload-pack")
			anchor := filepath.Join(g.root, "anchor")
			args := []string{"-C", clone, "merge", "--ff-only", "origin/main"}
			held := filepath.Join(clone, ".git", "index.lock")
			if mode == "worktree add" {
				args = []string{"-C", clone, "worktree", "add", "--detach", anchor, "origin/main"}
				held = anchor
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var runErr error
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, runErr = ExecRunner{BaseEnv: g.env}.Run(ctx, Cmd{Name: "git", Args: args})
			}()
			awaitFixtureFile(t, started, finished)
			if !exists(held) {
				t.Fatalf("%s is not held during the lazy fetch", held)
			}
			cancel()
			<-finished
			if runErr == nil {
				t.Fatal("the time limit did not end Git")
			}
			if exists(held) || exists(filepath.Join(clone, ".git", "worktrees", "anchor")) {
				t.Fatalf("Git left %s behind", held)
			}
			if gitRun(t, g.env, clone, "rev-parse", "HEAD") != first {
				t.Fatal("the interrupted step moved HEAD")
			}
			// Nothing is stranded: the same step succeeds on the next try.
			gitRun(t, g.env, clone, "config", "--unset", "remote.origin.uploadpack")
			gitRun(t, g.env, clone, args[2:]...)
		})
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
