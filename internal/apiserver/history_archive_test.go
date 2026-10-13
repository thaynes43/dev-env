package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// archiveExec plays pods/exec into the shelf for the history reads: it runs
// agentd's own archive, as cmd/agentd's history-archive commands answer.
type archiveExec struct {
	archive agentd.HistoryArchive
	calls   []string
}

func (x *archiveExec) Run(_ context.Context, _, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	if pod != "dev-env-shelf-0" || container != shelf.Container || len(cmd) < 4 || cmd[2] != "history-archive" {
		return fmt.Errorf("unexpected exec %v in %s/%s", cmd, pod, container)
	}
	x.calls = append(x.calls, cmd[3])
	fs := flag.NewFlagSet("ctl", flag.ContinueOnError)
	var uid, sum, project, after, file string
	var limit, offset int
	var off64 int64
	fs.StringVar(&uid, "session-uid", "", "")
	fs.StringVar(&sum, "content-sha256", "", "")
	fs.StringVar(&project, "project", "", "")
	fs.StringVar(&after, "after", "", "")
	fs.StringVar(&file, "file", "", "")
	fs.IntVar(&limit, "limit", 0, "")
	if cmd[3] == "read" {
		fs.Int64Var(&off64, "offset", 0, "")
	} else {
		fs.IntVar(&offset, "offset", 0, "")
	}
	if err := fs.Parse(cmd[4:]); err != nil {
		return err
	}
	var out any
	var err error
	switch cmd[3] {
	case "list":
		out, err = x.archive.List(project, limit, after)
	case "manifest":
		out, err = x.archive.Manifest(uid, sum, offset, limit)
	case "read":
		out, err = x.archive.Read(uid, sum, file, off64)
	default:
		return fmt.Errorf("unexpected command %v", cmd)
	}
	if err != nil {
		var he *protocol.HistoryError
		if !errors.As(err, &he) {
			he = protocol.HistoryErrorf(protocol.HistoryErrIO, "", "%v", err)
		}
		data, _ := protocol.CanonicalJSON(he)
		_, _ = stdout.Write(append(data, '\n'))
		return utilexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1}
	}
	data, err := protocol.CanonicalJSON(out)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(data, '\n'))
	return err
}

// otherDevice is a directory on another device than dir, as agentd's mount
// check asks of the archive and HOME.
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

// T1's read (DESIGN-025 15), end to end below the API: agentd's own export
// and archive over t.TempDir(), the shelf reader, and the routes. A Session
// of the project reads the entry's manifest and a file page, archived: true,
// every byte checked; a changed block of the file is 503, never served.
func TestHistoryReadsARealArchivedEntry(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	transcript := `{"type":"user","message":"hello from an archived session"}` + "\n"
	for rel, content := range map[string]string{
		".claude/projects/-home-dev/0b6f6c1e-1111-4222-8333-444455556666.jsonl": transcript,
		".claude/.credentials.json":    `{"claudeAiOauth":{"accessToken":"sk-ant-oat-TEST-ONLY"}}`,
		".claude/file-history/x/y@v1":  "COPY_OF_A_FILE=1\n",
		".codex/sessions/2026/s.jsonl": "{}\n",
		".agentd/launch.json":          `{"record":"launch"}`,
		"work/task-1.log":              "log\n",
		"repos/haynes-ops/README.md":   "code goes through the Git rescue\n",
		"../creds/gh_token":            "ghs_TEST_ONLY",
		"../shared/.keep":              "",
		"../archive/.keep-archive-dir": "",
	} {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(base, "archive", ".keep-archive-dir"))
	settings := agentd.Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), SharedDir: filepath.Join(base, "shared"), GHTokenFile: filepath.Join(base, "creds", "gh_token")}
	opts := agentd.HistoryExportOptions{Session: "task-1", ClaudeConfigDir: ".claude", CodexHome: ".codex"}
	plan, err := agentd.PlanHistoryExport(settings, opts)
	if err != nil {
		t.Fatal(err)
	}
	planSum, _, err := agentd.HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	content := protocol.HistoryContent{Format: protocol.HistoryArchiveFormat,
		Session: protocol.HistorySession{Namespace: sessionNS, Name: "task-1", UID: histSample}, Project: "sample", Provider: protocol.HistoryProviderClaude,
		HomeUID:  "7a1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f",
		Terminal: protocol.HistoryTerminal{PodUID: "8b1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f", Phase: "Failed", Reason: "DeadlineExceeded", ObservedAt: at},
		Rescue:   protocol.HistoryRescue{Stamp: "20261012-0102", Result: protocol.HistoryRescueCleanAndPushed}}
	protocol.HistoryContentFromPlan(&content, plan, planSum)
	line, err := protocol.CanonicalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	var in bytes.Buffer
	in.Write(append(line, '\n'))
	if err := agentd.StreamHistoryExport(settings, opts, planSum, &in); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "archive")
	archive := agentd.HistoryArchive{Root: root, Home: otherDevice(t, root), SharedDir: settings.SharedDir, Now: func() time.Time { return histArchivedAt }}
	put, err := archive.Put(&in, "dev-env-operator")
	if err != nil {
		t.Fatal(err)
	}

	f, _, reader := historyFixture(t)
	shelfPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-shelf-0", Namespace: sessionNS, Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: shelf.Container, Image: "synthetic.invalid/agent:shelf"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err := f.c.Create(context.Background(), shelfPod); err != nil {
		t.Fatal(err)
	}
	x := &archiveExec{archive: archive}
	f.srv.History = &shelf.HistoryReader{Shelf: &shelf.Shelf{Reader: f.c, Exec: x, Namespace: sessionNS}}
	f.srv.ShelfSlots = shelf.NewExecSlots(shelf.DefaultExecSlots)

	w := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample)+"?limit=1000", reader, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest: %d %s", w.Code, w.Body.String())
	}
	m := decode[apiv1.HistoryManifest](t, w)
	if !m.Archived || !m.ArchivedAt.Equal(histArchivedAt) || m.ContentSHA256 != put.ContentSHA256 || m.Project != "sample" || m.Session.Name != "task-1" ||
		m.Terminal.Reason != "DeadlineExceeded" || m.FileCount != len(content.Files) || m.ExcludedCount != len(content.Excluded) || m.Continue != "" {
		t.Fatalf("manifest: %+v", m)
	}
	const name = "claude/projects/-home-dev/0b6f6c1e-1111-4222-8333-444455556666.jsonl"
	files := map[string]apiv1.HistoryFile{}
	for _, file := range m.Files {
		files[file.Root+"/"+file.Path] = file
	}
	excluded := map[string]string{}
	for _, e := range m.Excluded {
		excluded[e.Root+"/"+e.Path] = e.Class
	}
	sum := sha256.Sum256([]byte(transcript))
	if files[name].SHA256 != hex.EncodeToString(sum[:]) || excluded["claude/file-history"] != protocol.HistoryClassUserCopy || excluded["claude/.credentials.json"] != protocol.HistoryClassCredential {
		t.Fatalf("files %v, excluded %v", files, excluded)
	}

	w = f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path="+name+"&offset=0", reader, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("file: %d %s", w.Code, w.Body.String())
	}
	p := decode[apiv1.HistoryFilePage](t, w)
	if !p.Archived || string(p.Data) != transcript || !p.EOF || p.FileSHA256 != hex.EncodeToString(sum[:]) || p.ContentSHA256 != put.ContentSHA256 {
		t.Fatalf("file page: %+v", p)
	}
	// A credential's name is no file path at all, and an excluded name has
	// no file in the entry: neither is ever served.
	w = f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/.credentials.json&offset=0", reader, nil)
	wantError(t, w, http.StatusBadRequest, apiv1.CodeBadRequest)
	w = f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/file-history/x/y@v1&offset=0", reader, nil)
	wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)

	w = f.do(http.MethodGet, apiv1.HistoryPath+"?project=sample", reader, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if l := decode[apiv1.HistoryList](t, w); len(l.Items) != 1 || l.Items[0].SessionUID != histSample || !l.Items[0].Archived || l.Items[0].Files != len(content.Files) {
		t.Fatalf("list: %+v", l)
	}

	// One byte of the file changed in the archive: 503, and no byte of it.
	stored := filepath.Join(root, filepath.FromSlash(put.Entry), protocol.HistoryFilesDir, filepath.FromSlash(name))
	if err := os.WriteFile(stored, []byte("X"+transcript[1:]), 0o600); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path="+name+"&offset=0", reader, nil)
	wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if bytes.Contains(w.Body.Bytes(), []byte("archived session")) {
		t.Fatal("a changed block was served")
	}
}
