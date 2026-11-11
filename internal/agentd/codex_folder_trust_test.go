package agentd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(git, "init", "-q", dir)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + t.TempDir(),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v %s", dir, err, out)
	}
}

func configBytes(t *testing.T, s Settings) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A fresh platform repository is trusted, and the launch then proceeds.
func TestCodexRepoTrustLaunchProceeds(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	got := launchThread(t, f, s, sess, goodThreadRequest())
	if got.Outcome != apiv1.ThreadResponded || len(f.launches) != 1 {
		t.Fatalf("got %s/%+v launches %d", got.Outcome, got.Error, len(f.launches))
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(configBytes(t, s), &doc); err != nil {
		t.Fatal(err)
	}
	projects, _ := doc["projects"].(map[string]any)
	table, _ := projects[cwd].(map[string]any)
	if table["trust_level"] != "trusted" || len(projects) != 1 {
		t.Fatalf("projects %+v", projects)
	}
	before := configBytes(t, s)
	again := launchThread(t, newThreadFake(cwd), s, sess, goodThreadRequest())
	if again.Outcome != apiv1.ThreadResponded || !bytes.Equal(before, configBytes(t, s)) {
		t.Fatalf("second launch %s changed the trusted config", again.Outcome)
	}
}

// A path outside the rule stops at once, before the wrapper and before any
// wait, and writes no config.
func TestCodexRepoTrustRefusesUnqualified(t *testing.T) {
	t.Run("not a repository", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		if err := os.RemoveAll(filepath.Join(cwd, ".git")); err != nil {
			t.Fatal(err)
		}
		f := newThreadFake(cwd)
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "folder_not_qualified" || got.TotalMs != 0 || len(f.launches) != 0 {
			t.Fatalf("got %s/%+v total %d launches %d", got.Outcome, got.Error, got.TotalMs, len(f.launches))
		}
		if _, err := os.Stat(filepath.Join(s.CodexHome, "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("config.toml err %v", err)
		}
	})
	s, _, cwd := threadHost(t)
	outside := []string{
		s.ReposDir(),
		filepath.Dir(s.ReposDir()),
		filepath.Join(cwd, "nested"),
	}
	if err := os.Mkdir(filepath.Join(cwd, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	gitInitRepo(t, other)
	link := s.ClonePath("linked")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	outside = append(outside, other, link)
	for _, path := range outside {
		t.Run(path, func(t *testing.T) {
			home := t.TempDir()
			s.CodexHome = filepath.Join(home, ".codex")
			if err := ensureCodexRepoTrust(context.Background(), s, path); err == nil || !os.IsNotExist(mustStat(filepath.Join(s.CodexHome, "config.toml"))) {
				t.Fatalf("path %s err %v", path, err)
			}
		})
	}
}

func mustStat(path string) error {
	_, err := os.Stat(path)
	return err
}

// An existing config keeps every byte outside the one trust entry.
func TestCodexRepoTrustPreservesOtherKeys(t *testing.T) {
	s, _, cwd := threadHost(t)
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(s.ReposDir(), "other")
	original := []byte("# kept\nmodel = \"gpt-6-astra\"\n\n[mcp_servers.fixture]\ncommand = \"keep\"\n\n[projects.\"" + other + "\"]\ntrust_level = \"trusted\"\nextra = \"stay\"\n\n[projects.\"" + cwd + "\"]\ntrust_level = \"untrusted\"\nnote = \"same\"\n")
	path := filepath.Join(s.CodexHome, "config.toml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexRepoTrust(context.Background(), s, cwd); err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(original, []byte("trust_level = \"untrusted\""), []byte("trust_level = \"trusted\""), 1)
	if got := configBytes(t, s); !bytes.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}

	fresh := Settings{Home: s.Home, CodexHome: filepath.Join(t.TempDir(), ".codex")}
	if err := os.MkdirAll(fresh.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	prefix := []byte("# kept\nmodel = \"gpt-6-astra\"\n\n[projects.\"" + other + "\"]\ntrust_level = \"trusted\"\n")
	dest := filepath.Join(fresh.CodexHome, "config.toml")
	if err := os.WriteFile(dest, prefix, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexRepoTrust(context.Background(), fresh, cwd); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, prefix) {
		t.Fatalf("append rewrote the prefix\n%s", got)
	}
	rest := got[len(prefix):]
	wantRest := []byte("\n[projects.\"" + cwd + "\"]\ntrust_level = \"trusted\"\n")
	if !bytes.Equal(rest, wantRest) {
		t.Fatalf("appended %q", rest)
	}
}

// A config that cannot be parsed stops the launch and is left byte for byte.
func TestCodexRepoTrustInvalidConfigUnchanged(t *testing.T) {
	s, sess, cwd := threadHost(t)
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("model = [\n")
	path := filepath.Join(s.CodexHome, "config.toml")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	f := newThreadFake(cwd)
	got := launchThread(t, f, s, sess, goodThreadRequest())
	if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "codex_config_invalid" || got.TotalMs != 0 || len(f.launches) != 0 {
		t.Fatalf("got %s/%+v total %d launches %d", got.Outcome, got.Error, got.TotalMs, len(f.launches))
	}
	if file, err := os.ReadFile(path); err != nil || !bytes.Equal(file, original) {
		t.Fatalf("config changed: %v %q", err, file)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %+v %v", info, err)
	}

	shapes := [][]byte{
		[]byte("projects = \"nope\"\n"),
		[]byte("[projects]\n\"" + cwd + "\" = \"nope\"\n"),
		[]byte("model = \"keep\"\nprojects = { \"" + cwd + "\" = { trust_level = \"untrusted\" } }\n"),
	}
	for _, shape := range shapes {
		home := filepath.Join(t.TempDir(), ".codex")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		s.CodexHome = home
		file := filepath.Join(home, "config.toml")
		if err := os.WriteFile(file, shape, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ensureCodexRepoTrust(context.Background(), s, cwd); err == nil {
			t.Fatalf("accepted %q", shape)
		}
		if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, shape) {
			t.Fatalf("changed %q -> %q (%v)", shape, got, err)
		}
	}
}

// A path named only in the handoff is not trusted. The clone is.
func TestCodexRepoTrustIgnoresHandoffPath(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	req := goodThreadRequest()
	req.Handoff = "Make a worktree, then summarize the open PRs.\nNotes live in /tmp/not-a-repo."
	got := launchThread(t, f, s, sess, req)
	if got.Outcome != apiv1.ThreadResponded {
		t.Fatalf("got %s/%+v", got.Outcome, got.Error)
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(configBytes(t, s), &doc); err != nil {
		t.Fatal(err)
	}
	projects, _ := doc["projects"].(map[string]any)
	if _, ok := projects[cwd]; !ok || len(projects) != 1 {
		t.Fatalf("projects %+v", projects)
	}
	if _, ok := projects["/tmp/not-a-repo"]; ok {
		t.Fatal("the handoff path was trusted")
	}
}

// thread status resolves the folder and writes nothing.
func TestCodexThreadStatusDoesNotWriteTrust(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	_, _ = observeThread(t, f, s, sess, CodexThreadStatusRequest{ThreadID: newThread})
	if _, err := os.Stat(filepath.Join(s.CodexHome, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("status wrote config: %v", err)
	}
}

// An entry that is already trusted, however it is quoted, is left untouched.
func TestCodexRepoTrustLeavesTrustedEntry(t *testing.T) {
	s, _, cwd := threadHost(t)
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("model = \"gpt-6-astra\"\n[projects.'" + cwd + "']\ntrust_level = 'trusted'\n")
	path := filepath.Join(s.CodexHome, "config.toml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexRepoTrust(context.Background(), s, cwd); err != nil {
		t.Fatal(err)
	}
	if got := configBytes(t, s); !bytes.Equal(got, original) {
		t.Fatalf("rewrote a trusted entry\n%s", got)
	}
}

// A private host rejects a trusted root outside ~/codex/<project> on every
// status read. The launch stops before writing, so that read keeps working.
func TestCodexRepoTrustPrivateHostDoesNotWrite(t *testing.T) {
	s, sess, cwd := threadHost(t)
	s.PrivateCodexHost = true
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("model = \"gpt-6-astra\"\n")
	path := filepath.Join(s.CodexHome, "config.toml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	f := newThreadFake(cwd)
	got := launchThread(t, f, s, sess, goodThreadRequest())
	if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "codex_config_invalid" || got.TotalMs != 0 || len(f.launches) != 0 {
		t.Fatalf("got %s/%+v total %d launches %d", got.Outcome, got.Error, got.TotalMs, len(f.launches))
	}
	if file, err := os.ReadFile(path); err != nil || !bytes.Equal(file, original) {
		t.Fatalf("private host config changed: %v %q", err, file)
	}
}

// A busy lock, or a file that changed after it was read, leaves the config
// byte for byte and does not install the new bytes.
func TestCodexRepoTrustBusyLockAndStaleWrite(t *testing.T) {
	s, _, cwd := threadHost(t)
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("model = \"keep\"\n")
	path := filepath.Join(s.CodexHome, "config.toml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(s.CodexHome, "config.toml.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexRepoTrust(context.Background(), s, cwd); err == nil {
		t.Fatal("a busy lock was accepted")
	}
	if file, err := os.ReadFile(path); err != nil || !bytes.Equal(file, original) {
		t.Fatalf("busy lock wrote config: %v %q", err, file)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()

	if err := writeCodexConfigIfUnchanged(path, []byte("stale"), []byte("model = \"changed\"\n"), 0o600); err == nil {
		t.Fatal("a stale snapshot was written")
	}
	if file, err := os.ReadFile(path); err != nil || !bytes.Equal(file, original) {
		t.Fatalf("stale write changed config: %v %q", err, file)
	}
	if err := ensureCodexRepoTrust(context.Background(), s, cwd); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(configBytes(t, s), &doc); err != nil {
		t.Fatal(err)
	}
	projects, _ := doc["projects"].(map[string]any)
	table, _ := projects[cwd].(map[string]any)
	if table["trust_level"] != "trusted" || doc["model"] != "keep" {
		t.Fatalf("after the lock: %+v", doc)
	}
}
