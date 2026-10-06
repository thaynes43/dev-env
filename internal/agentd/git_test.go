package agentd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitTestEnv isolates git from the pod's own config: its own HOME and global
// config, no system config, and a fixed identity.
func gitTestEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// gitRun runs git for a test fixture.
func gitRun(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitFixture is a tiny origin: a bare repo <root>/remote/<repo> with one
// commit on main, served to partial clones (uploadpack.allowFilter).
type gitFixture struct {
	env    []string
	root   string
	remote string // the bare repo
	seed   string // a clone used to push more commits
}

func newGitFixture(t *testing.T, repo string) gitFixture {
	t.Helper()
	root := t.TempDir()
	env := gitTestEnv(filepath.Join(root, "fixture-home"))
	if err := os.MkdirAll(filepath.Join(root, "fixture-home"), 0o755); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(root, "remote", repo)
	gitRun(t, env, root, "init", "-q", "--bare", "-b", "main", remote)
	gitRun(t, env, remote, "config", "uploadpack.allowFilter", "true")
	seed := filepath.Join(root, "seed")
	gitRun(t, env, root, "clone", "-q", remote, seed)
	writeFile(t, filepath.Join(seed, "README.md"), "hello\n")
	gitRun(t, env, seed, "add", "README.md")
	gitRun(t, env, seed, "commit", "-q", "-m", "first")
	gitRun(t, env, seed, "push", "-q", "origin", "HEAD:main")
	gitRun(t, env, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return gitFixture{env: env, root: root, remote: remote, seed: seed}
}

// settings returns Settings for a session home that clones from the fixture,
// and a runner whose git sees only the fixture's environment.
func (g gitFixture) settings(t *testing.T) (Settings, ExecRunner) {
	t.Helper()
	home := filepath.Join(g.root, "session-home")
	s := testSettings(t, home)
	s.RemoteBase = "file://" + filepath.Join(g.root, "remote")
	s.TokenWait = 0
	env := gitTestEnv(home)
	return s, ExecRunner{BaseEnv: env}
}

// noSleep makes retries instant for one test.
func noSleep(t *testing.T) {
	t.Helper()
	old := sleepFunc
	sleepFunc = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	t.Cleanup(func() { sleepFunc = old })
}
