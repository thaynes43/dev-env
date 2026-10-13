package shelf

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const historyUID = "6f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"

func historyShelf(t *testing.T, e *fakeExec) *HistoryReader {
	t.Helper()
	c := newClient(t, shelfPod("shelf-1", time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), corev1.PodRunning, true))
	return &HistoryReader{Shelf: &Shelf{Reader: c, Exec: e, Namespace: ns}}
}

// The reads run agentd's history-archive commands in the shelf with exactly
// the arguments given, and decode their answers.
func TestHistoryReaderRunsTheArchiveReads(t *testing.T) {
	ctx := context.Background()
	sum := strings.Repeat("a", 64)

	e := &fakeExec{out: `{"project":"dev-env","items":[{"sessionUID":"` + historyUID + `","archivedAt":"2026-11-12T01:00:00Z","contentSHA256":"` + sum + `"}],"next":"c"}` + "\n"}
	h := historyShelf(t, e)
	list, err := h.List(ctx, "dev-env", 20, "2026-11/x.json")
	if err != nil || len(list.Items) != 1 || list.Items[0].SessionUID != historyUID || list.Next != "c" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if e.cmds[0] != "agentd ctl history-archive list --project dev-env --limit 20 --after 2026-11/x.json" || e.pods[0] != ns+"/shelf-1/"+Container {
		t.Errorf("list ran %q in %q", e.cmds[0], e.pods[0])
	}
	if _, err := h.List(ctx, "dev-env", 5, ""); err != nil || e.cmds[1] != "agentd ctl history-archive list --project dev-env --limit 5" {
		t.Errorf("list without a cursor ran %q: %v", e.cmds[1], err)
	}

	e = &fakeExec{out: `{"contentSHA256":"` + sum + `","project":"dev-env","fileCount":3,"excludedCount":1,"offset":2,"files":[],"excluded":[{"root":"claude","path":"file-history","class":"user_copy"}]}`}
	h = historyShelf(t, e)
	m, err := h.Manifest(ctx, historyUID, sum, 2, 1)
	if err != nil || m.Project != "dev-env" || m.FileCount != 3 || len(m.Excluded) != 1 {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	if e.cmds[0] != "agentd ctl history-archive manifest --session-uid "+historyUID+" --offset 2 --limit 1 --content-sha256 "+sum {
		t.Errorf("manifest ran %q", e.cmds[0])
	}
	if _, err := h.Manifest(ctx, historyUID, "", 0, 0); err != nil || e.cmds[1] != "agentd ctl history-archive manifest --session-uid "+historyUID+" --offset 0 --limit 0" {
		t.Errorf("the newest entry's header ran %q: %v", e.cmds[1], err)
	}

	e = &fakeExec{out: `{"contentSHA256":"` + sum + `","file":"claude/x.jsonl","fileSHA256":"` + sum + `","bytes":3,"offset":0,"next":3,"eof":true,"data":"YWJj"}`}
	h = historyShelf(t, e)
	p, err := h.Read(ctx, historyUID, sum, "claude/x.jsonl", 0)
	if err != nil || string(p.Data) != "abc" || !p.EOF || p.Next != 3 {
		t.Fatalf("read: %+v %v", p, err)
	}
	if e.cmds[0] != "agentd ctl history-archive read --session-uid "+historyUID+" --content-sha256 "+sum+" --file claude/x.jsonl --offset 0" {
		t.Errorf("read ran %q", e.cmds[0])
	}
}

// agentd's refusal is its one JSON object on exit 1, and comes back as a
// *protocol.HistoryError with its code. Anything else is an error of its own:
// no shelf is ErrNoShelf, and a failed exec, another exit status or an answer
// that is not JSON is never mistaken for a refusal.
func TestHistoryReaderErrors(t *testing.T) {
	ctx := context.Background()
	exit := func(code int) error { return utilexec.CodeExitError{Err: errors.New("command terminated"), Code: code} }
	for _, tc := range []struct {
		name string
		e    *fakeExec
		code string
	}{
		{"not found", &fakeExec{out: `{"error":"not_found","message":"no entry"}` + "\n", err: exit(1)}, protocol.HistoryErrNotFound},
		{"corrupt", &fakeExec{out: `{"error":"corrupt","message":"x"}`, err: exit(1)}, protocol.HistoryErrCorrupt},
		{"no archive", &fakeExec{out: `{"error":"no_archive","message":"x"}`, err: exit(1)}, protocol.HistoryErrNoArchive},
	} {
		_, err := historyShelf(t, tc.e).Manifest(ctx, historyUID, "", 0, 0)
		var he *protocol.HistoryError
		if !errors.As(err, &he) || he.Code != tc.code {
			t.Errorf("%s: %v, want the refusal %s", tc.name, err, tc.code)
		}
	}
	for _, tc := range []struct {
		name string
		e    *fakeExec
	}{
		{"usage", &fakeExec{errOut: "usage", err: exit(2)}},
		{"exit 1 without a refusal", &fakeExec{out: "garbage", err: exit(1)}},
		{"exec failed", &fakeExec{err: errors.New("connection refused")}},
		{"not JSON", &fakeExec{out: "garbage"}},
		{"too long", &fakeExec{out: strings.Repeat("x", maxOutput+1)}},
	} {
		_, err := historyShelf(t, tc.e).Manifest(ctx, historyUID, "", 0, 0)
		var he *protocol.HistoryError
		if err == nil || errors.As(err, &he) {
			t.Errorf("%s: %v, want an error that is no refusal", tc.name, err)
		}
	}
	h := &HistoryReader{Shelf: &Shelf{Reader: newClient(t), Exec: &fakeExec{}, Namespace: ns}}
	if _, err := h.List(ctx, "dev-env", 1, ""); !errors.Is(err, ErrNoShelf) {
		t.Errorf("no shelf: %v", err)
	}
	if _, err := (&HistoryReader{}).List(ctx, "dev-env", 1, ""); err == nil {
		t.Error("a reader without a shelf answered")
	}
}
