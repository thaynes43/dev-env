package controller

// Shared fixtures of the HOME expiry's tests (DESIGN-025 unit H5): an
// in-process pods/exec that runs the real agentd history export and archive
// over t.TempDir(), as cmd/agentd prints them, and a fake rescuer.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

const testShelfPod = "dev-env-shelf-0"

// agentdExec plays pods/exec for the history commands. The hold pod (any pod
// but the shelf, in the agent container) runs agentd's real export over home;
// the shelf pod runs the real archive at archive.Root.
type agentdExec struct {
	mu      sync.Mutex
	home    string
	shared  string
	ghToken string
	archive agentd.HistoryArchive
	calls   []string
	// fail makes a command ("plan", "stream", "put", "verify") fail as an
	// exec that could not run.
	fail map[string]error
	// before runs before a command, once: a test changes the world there.
	before map[string]func()
}

// newAgentdExec builds a HOME for session with one file of each history root,
// and the provider logins beside them (excluded, never exported), a GitHub
// token outside HOME and an empty archive on a device other than HOME's
// stand-in, as agentd's mount check asks.
func newAgentdExec(t *testing.T, session string) *agentdExec {
	t.Helper()
	base := t.TempDir()
	x := &agentdExec{home: filepath.Join(base, "home"), shared: filepath.Join(base, "shared"), ghToken: filepath.Join(base, "creds", "gh_token")}
	writeExpiryFile(t, x.ghToken, "ghs_TEST_ONLY_TOKEN")
	for rel, content := range map[string]string{
		".claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666.jsonl": `{"type":"user","message":"hello"}` + "\n",
		".claude/history.jsonl":     `{"display":"a prompt"}` + "\n",
		".claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"sk-ant-oat-TEST-ONLY"}}`,
		".codex/sessions/2026/10/12/rollout-2026-10-12T01-00-00-abc.jsonl": `{"type":"session_meta"}` + "\n",
		".codex/auth.json":                          `{"tokens":{"access_token":"codex-TEST-ONLY"}}`,
		".agentd/launch.json":                       `{"record":"launch"}`,
		".agentd/decisions/019a-thread.json":        `{"state":"Delivered"}`,
		"work/" + session + ".log":                  "the session's log\n",
		"repos/haynes-ops/README.md":                "code goes through the Git rescue\n",
		".claude/file-history/0b6f6c1e/3f9a1c2e@v1": "SECRET_FROM_DOTENV=1\n",
		".agentd/managed-output/inv-1/0.txt":        "assistant text\n",
	} {
		writeExpiryFile(t, filepath.Join(x.home, filepath.FromSlash(rel)), content)
	}
	if err := os.MkdirAll(x.shared, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "archive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	x.archive = agentd.HistoryArchive{Root: root, Home: otherDevice(t, root), SharedDir: x.shared, Now: time.Now}
	return x
}

func writeExpiryFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// otherDevice is a directory on another device than dir: agentd's archive
// refuses to run on the volume it calls HOME (D-48's check), and the test
// archive lives in t.TempDir().
func otherDevice(t *testing.T, dir string) string {
	t.Helper()
	var d syscall.Stat_t
	if err := syscall.Stat(dir, &d); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"/proc", "/sys", "/dev"} {
		var o syscall.Stat_t
		if syscall.Stat(c, &o) == nil && o.Dev != d.Dev {
			return c
		}
	}
	t.Skip("no directory on another device than the test's temporary directory")
	return ""
}

func (x *agentdExec) settings() agentd.Settings {
	return agentd.Settings{Home: x.home, StateDir: filepath.Join(x.home, ".agentd"), SharedDir: x.shared, GHTokenFile: x.ghToken}
}

func (x *agentdExec) called() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.calls...)
}

// entries lists the archive's entry directories.
func (x *agentdExec) entries(t *testing.T) []string {
	t.Helper()
	var out []string
	matches, err := filepath.Glob(filepath.Join(x.archive.Root, protocol.HistoryArchiveEntries, "*", "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		rel, _ := filepath.Rel(x.archive.Root, m)
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

// Run implements shelf.Executor as cmd/agentd's historyCtl answers.
func (x *agentdExec) Run(_ context.Context, _, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(cmd) < 4 || cmd[0] != "agentd" || cmd[1] != "ctl" {
		return fmt.Errorf("unexpected command %q", cmd)
	}
	x.mu.Lock()
	x.calls = append(x.calls, cmd[3])
	failure := x.fail[cmd[3]]
	hook := x.before[cmd[3]]
	delete(x.before, cmd[3])
	x.mu.Unlock()
	if hook != nil {
		hook()
	}
	if failure != nil {
		return failure
	}
	shelfSide := cmd[2] == "history-archive"
	if shelfSide != (pod == testShelfPod && container == shelf.Container) || !shelfSide && container != ContainerName {
		return fmt.Errorf("%s run in %s/%s", strings.Join(cmd[2:4], " "), pod, container)
	}
	fs := flag.NewFlagSet("ctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o agentd.HistoryExportOptions
	var planSHA, archivedBy, uid, sum string
	fs.StringVar(&o.Session, "session", "", "")
	fs.StringVar(&o.ClaudeConfigDir, "claude-config-dir", "", "")
	fs.StringVar(&o.CodexHome, "codex-home", "", "")
	fs.StringVar(&o.Rescue, "rescue", "", "")
	fs.StringVar(&planSHA, "plan-sha256", "", "")
	fs.StringVar(&archivedBy, "archived-by", "", "")
	fs.StringVar(&uid, "session-uid", "", "")
	fs.StringVar(&sum, "content-sha256", "", "")
	if err := fs.Parse(cmd[4:]); err != nil {
		return err
	}
	fail := func(w io.Writer, err error) error {
		var he *protocol.HistoryError
		if !errors.As(err, &he) {
			he = protocol.HistoryErrorf(protocol.HistoryErrIO, "", "%v", err)
		}
		data, _ := protocol.CanonicalJSON(he)
		_, _ = w.Write(append(data, '\n'))
		return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1}
	}
	print := func(v any) error {
		data, err := protocol.CanonicalJSON(v)
		if err != nil {
			return err
		}
		_, err = stdout.Write(append(data, '\n'))
		return err
	}
	switch cmd[3] {
	case "plan":
		plan, err := agentd.PlanHistoryExport(x.settings(), o)
		if err != nil {
			return fail(stdout, err)
		}
		return print(plan)
	case "stream":
		if err := agentd.StreamHistoryExport(x.settings(), o, planSHA, stdout); err != nil {
			return fail(stderr, err)
		}
		return nil
	case "put":
		res, err := x.archive.Put(stdin, archivedBy)
		if err != nil {
			return fail(stdout, err)
		}
		return print(res)
	case "verify":
		res, err := x.archive.Verify(uid, sum)
		if err != nil {
			return fail(stdout, err)
		}
		return print(res)
	}
	return fmt.Errorf("unexpected command %q", cmd)
}

// shelfPodObject is a Running, Ready shelf pod for shelf.Shelf.Pod to find.
func shelfPodObject(ns string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: testShelfPod, Namespace: ns, UID: "shelf-uid", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: shelf.Container, Image: "synthetic.invalid/agent:shelf"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}

// expiryRescuer answers every rescue as CleanAndPushed for the Session, or
// with the report the test sets, and counts the pods it ran in.
type expiryRescuer struct {
	mu     sync.Mutex
	report func(*corev1.Pod) (protocol.RescueReport, error)
	pods   []string
}

func (f *expiryRescuer) Rescue(_ context.Context, pod *corev1.Pod) (protocol.RescueReport, error) {
	f.mu.Lock()
	f.pods = append(f.pods, string(pod.UID))
	report := f.report
	f.mu.Unlock()
	if report != nil {
		return report(pod)
	}
	return cleanRescue(pod.Name, "20261111-1200"), nil
}

func (f *expiryRescuer) ranIn() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pods)
}

// cleanRescue is agentd's report for a HOME whose one clone is clean and
// pushed.
func cleanRescue(session, stamp string) protocol.RescueReport {
	return protocol.RescueReport{Session: session, Stamp: stamp, OK: true, CleanAndPushed: true,
		Agent: &protocol.AgentStop{}, Repos: []protocol.RepoRescue{{Path: "/home/dev/repos/haynes-ops", Fetched: true}}}
}
