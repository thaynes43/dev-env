package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

const (
	histSample = "11111111-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	histOther  = "22222222-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	histNone   = "33333333-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	histAbsent = "44444444-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
)

var histArchivedAt = time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)

func histSum(uid string) string { return strings.Repeat(uid[:1], 64) }

// fakeHistory is the shelf's archive: three entries, of the project sample,
// the project other and no project. It records every call, so a test can
// show that a refused caller never reached the shelf.
type fakeHistory struct {
	calls   []string
	listErr error
	manErr  error
	readErr error
	list    protocol.HistoryList
}

func (h *fakeHistory) entry(uid string) (protocol.HistoryManifestPage, bool) {
	project := map[string]string{histSample: "sample", histOther: "other", histNone: ""}
	p, ok := project[uid]
	if !ok {
		return protocol.HistoryManifestPage{}, false
	}
	sum := histSum(uid)
	return protocol.HistoryManifestPage{
		ContentSHA256: sum,
		Entry:         protocol.HistoryEntry{Format: protocol.HistoryArchiveEntryFormat, ContentSHA256: sum, ArchivedAt: histArchivedAt, ArchivedBy: "dev-env-operator"},
		Session:       protocol.HistorySession{Namespace: sessionNS, Name: "s-" + uid[:1], UID: uid},
		Project:       p, Provider: "claude", HomeUID: "home-" + uid[:1],
		Terminal: protocol.HistoryTerminal{PodUID: "pod-" + uid[:1], Phase: "Failed", Reason: "DeadlineExceeded", ObservedAt: histArchivedAt.Add(-31 * 24 * time.Hour)},
		Rescue:   protocol.HistoryRescue{Stamp: "20261012-0102", Result: "CleanAndPushed"},
		Roots:    []protocol.HistoryRoot{{Name: "claude", Dir: ".claude"}},
		Bytes:    3, FileCount: 2, ExcludedCount: 1,
	}, true
}

func (h *fakeHistory) List(_ context.Context, project string, limit int, after string) (protocol.HistoryList, error) {
	h.calls = append(h.calls, fmt.Sprintf("list %s %d %s", project, limit, after))
	return h.list, h.listErr
}

func (h *fakeHistory) Manifest(_ context.Context, uid, content string, offset, limit int) (protocol.HistoryManifestPage, error) {
	h.calls = append(h.calls, fmt.Sprintf("manifest %s %s %d %d", uid, content, offset, limit))
	if h.manErr != nil {
		return protocol.HistoryManifestPage{}, h.manErr
	}
	p, ok := h.entry(uid)
	if !ok || (content != "" && content != p.ContentSHA256) {
		return protocol.HistoryManifestPage{}, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "no entry")
	}
	p.Offset = offset
	p.Files, p.Excluded = []protocol.HistoryFile{}, []protocol.HistoryExcluded{}
	all := []string{"claude/projects/a.jsonl", "claude/history.jsonl", "excluded"}
	for i := offset; i < min(offset+limit, len(all)); i++ {
		if i < 2 {
			root, path, _ := strings.Cut(all[i], "/")
			p.Files = append(p.Files, protocol.HistoryFile{Root: root, Path: path, Bytes: 3, SHA256: histSum(uid), BlocksSHA256: histSum(uid)})
		} else {
			p.Excluded = append(p.Excluded, protocol.HistoryExcluded{Root: "claude", Path: "file-history", Class: protocol.HistoryClassUserCopy, Bytes: 9})
		}
	}
	if limit > 0 && offset+limit < len(all) {
		p.Next = offset + limit
	}
	return p, nil
}

func (h *fakeHistory) Read(_ context.Context, uid, content, file string, offset int64) (protocol.HistoryReadResult, error) {
	h.calls = append(h.calls, fmt.Sprintf("read %s %s %s %d", uid, content, file, offset))
	if h.readErr != nil {
		return protocol.HistoryReadResult{}, h.readErr
	}
	return protocol.HistoryReadResult{ContentSHA256: content, File: file, FileSHA256: histSum(uid), Bytes: 3, Offset: offset, Next: 3, EOF: true, Data: []byte("abc")[offset:]}, nil
}

// historyFixture: reads on, the payload scope for sample, a live Session of
// sample, and the coordinator.
func historyFixture(t *testing.T) (*fixture, *fakeHistory, string) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	f.srv.ContextReadProjects = []string{"sample"}
	h := &fakeHistory{}
	f.srv.HistoryReads, f.srv.History = true, h
	return f, h, contextSession(t, f, "reader", clientSA)
}

func TestHistoryPeersShareProjectsAndProjectless(t *testing.T) {
	f, h, reader := historyFixture(t)
	peer := contextSession(t, f, "peer", humanSA)
	projectless := f.session("peer")
	delete(projectless.Annotations, v1alpha1.AnnotationProjectSnapshot)
	if err := f.c.Update(context.Background(), projectless); err != nil {
		t.Fatal(err)
	}
	// History read authority does not depend on payload-publication projects or
	// a saved project snapshot. Both callers use the existing Session SA.
	f.srv.ContextReadProjects = nil
	for _, tok := range []string{reader, peer} {
		for _, uid := range []string{histOther, histNone} {
			w := f.do(http.MethodGet, apiv1.HistoryEntryPath(uid), tok, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("shared manifest: %d", w.Code)
			}
			manifest := decode[apiv1.HistoryManifest](t, w)
			w = f.do(http.MethodGet, apiv1.HistoryFilePath(uid)+"?path=claude/history.jsonl&offset=0&content="+manifest.ContentSHA256, tok, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("shared bytes: %d", w.Code)
			}
			page := decode[apiv1.HistoryFilePage](t, w)
			if manifest.ContentSHA256 != histSum(uid) || page.ContentSHA256 != manifest.ContentSHA256 || string(page.Data) != "abc" {
				t.Fatal("peers did not retrieve the same preserved bytes and digest")
			}
		}
	}
	for _, project := range []string{"other", "third", ""} {
		h.list = protocol.HistoryList{Project: project, Items: []protocol.HistorySummary{}}
		w := f.do(http.MethodGet, apiv1.HistoryPath+"?project="+project, reader, nil)
		if w.Code != http.StatusOK || decode[apiv1.HistoryList](t, w).Project != project {
			t.Fatalf("shared project index %q: %d", project, w.Code)
		}
	}
}

func TestHistorySharedReadsKeepLiveCallerFences(t *testing.T) {
	for _, change := range []string{"session-pod-replaced", "session-replaced", "coordinator-replaced", "client-unconfigured", "no-live", "anonymous"} {
		t.Run(change, func(t *testing.T) {
			f, h, reader := historyFixture(t)
			tok := reader
			switch change {
			case "client-unconfigured":
				f.srv.Policy.Clients = nil
				tok = tokClient
			case "no-live":
				f.srv.Live = nil
				tok = tokClient
			case "anonymous":
				tok = ""
			default:
				if change == "coordinator-replaced" {
					tok = tokCoordinator
				}
				podReads := 0
				f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if err := c.Get(ctx, key, obj, opts...); err != nil {
						return err
					}
					if pod, ok := obj.(*corev1.Pod); ok {
						podReads++
						if change == "session-pod-replaced" || change == "coordinator-replaced" && podReads == 2 {
							pod.UID = "replacement-pod"
						}
					}
					if sess, ok := obj.(*v1alpha1.AgentSession); ok && change == "session-replaced" {
						sess.UID = "replacement-session"
					}
					return nil
				}})
			}
			w := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), tok, nil)
			if change == "anonymous" {
				wantError(t, w, http.StatusUnauthorized, apiv1.CodeUnauthenticated)
			} else {
				wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
			}
			if len(h.calls) != 0 {
				t.Fatal("refused identity reached the shelf")
			}
		})
	}
}

func TestHistoryReadsOffAnswer404(t *testing.T) {
	f, h, reader := historyFixture(t)
	f.srv.HistoryReads = false
	for _, tok := range []string{reader, tokCoordinator, tokClient, tokHuman} {
		for _, path := range []string{apiv1.HistoryPath + "?project=sample", apiv1.HistoryEntryPath(histSample), apiv1.HistoryFilePath(histSample) + "?path=claude/x&offset=0"} {
			w := f.do(http.MethodGet, path, tok, nil)
			wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("reads off reached the shelf: %v", h.calls)
	}
}

func TestHistoryListChecksTrustedCallerBeforeTheShelf(t *testing.T) {
	f, h, reader := historyFixture(t)
	h.list = protocol.HistoryList{Project: "sample", Next: "2026-11/20261112T010000.000000000Z-" + histSample + "-1111111111111111.json", Items: []protocol.HistorySummary{
		{SessionUID: histSample, Session: "s-1", Provider: "claude", ArchivedAt: histArchivedAt, ContentSHA256: histSum(histSample), Bytes: 3, Files: 2,
			Terminal: &protocol.HistoryTerminal{PodUID: "p", Phase: "Failed", ObservedAt: histArchivedAt}},
		{SessionUID: histOther, ArchivedAt: histArchivedAt, ContentSHA256: histSum(histOther), Error: protocol.HistoryErrCorrupt},
	}}
	for _, tok := range []string{reader, tokCoordinator, tokClient} {
		w := f.do(http.MethodGet, apiv1.HistoryPath+"?project=sample", tok, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		got := decode[apiv1.HistoryList](t, w)
		if !got.Archived || got.Project != "sample" || len(got.Items) != 2 || got.Continue != h.list.Next {
			t.Fatalf("list: %+v", got)
		}
		if it := got.Items[0]; !it.Archived || !it.ArchivedAt.Equal(histArchivedAt) || it.Project != "sample" || it.Session != "s-1" || it.Files != 2 || it.Terminal == nil || it.Terminal.Phase != "Failed" {
			t.Errorf("item: %+v", it)
		}
		if it := got.Items[1]; !it.Archived || it.Error != apiv1.HistoryCorrupt || it.Provider != "" || it.Terminal != nil {
			t.Errorf("a corrupt item: %+v", it)
		}
	}
	if h.calls[0] != "list sample 20 " {
		t.Errorf("the default page: %q", h.calls[0])
	}
	w := f.do(http.MethodGet, apiv1.HistoryPath+"?project=sample&limit=100&continue="+url.QueryEscape(h.list.Next), reader, nil)
	if w.Code != http.StatusOK || h.calls[len(h.calls)-1] != "list sample 100 "+h.list.Next {
		t.Errorf("a later page: %d %v", w.Code, h.calls)
	}

	// Human and unknown identities gain no history permission.
	calls := len(h.calls)
	for _, tc := range []struct{ tok, project string }{
		{tokHuman, "sample"}, {tokStranger, "sample"},
	} {
		w := f.do(http.MethodGet, apiv1.HistoryPath+"?project="+tc.project, tc.tok, nil)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s reading %q: %d %s, want 403", tc.tok, tc.project, w.Code, w.Body.String())
		}
	}
	if len(h.calls) != calls {
		t.Errorf("a refused list reached the shelf: %v", h.calls[calls:])
	}
	for _, q := range []string{"limit=0", "limit=101", "limit=x", "continue=../../x", "continue=" + strings.Repeat("a", 200)} {
		w := f.do(http.MethodGet, apiv1.HistoryPath+"?project=sample&"+q, reader, nil)
		wantError(t, w, http.StatusBadRequest, apiv1.CodeBadRequest)
	}

	// The shelf's failures are 503, never 404.
	for _, err := range []error{
		protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "x"),
		protocol.HistoryErrorf(protocol.HistoryErrIO, "", "x"),
		shelf.ErrNoShelf,
		errors.New("exec failed"),
	} {
		h.listErr = err
		w := f.do(http.MethodGet, apiv1.HistoryPath+"?project=sample", reader, nil)
		wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	}
}

func TestHistoryManifestIsSharedAndPaged(t *testing.T) {
	f, h, reader := historyFixture(t)
	w := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample)+"?limit=2", reader, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest: %d %s", w.Code, w.Body.String())
	}
	m := decode[apiv1.HistoryManifest](t, w)
	if !m.Archived || !m.ArchivedAt.Equal(histArchivedAt) || m.ContentSHA256 != histSum(histSample) || m.Project != "sample" || m.Session.UID != histSample ||
		m.Terminal.Phase != "Failed" || m.FileCount != 2 || m.ExcludedCount != 1 || len(m.Files) != 2 || len(m.Excluded) != 0 || m.Continue != "2" {
		t.Fatalf("page 1: %+v", m)
	}
	if m.Files[0].Root != "claude" || m.Files[0].Path != "projects/a.jsonl" || m.Files[0].SHA256 == "" {
		t.Errorf("file: %+v", m.Files[0])
	}
	w = f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample)+"?limit=2&continue=2&content="+histSum(histSample), reader, nil)
	m = decode[apiv1.HistoryManifest](t, w)
	if w.Code != http.StatusOK || len(m.Excluded) != 1 || m.Excluded[0].Class != protocol.HistoryClassUserCopy || m.Continue != "" || m.Offset != 2 {
		t.Fatalf("page 2: %d %+v", w.Code, m)
	}
	if h.calls[0] != "manifest "+histSample+"  0 2" || h.calls[1] != "manifest "+histSample+" "+histSum(histSample)+" 2 2" {
		t.Errorf("calls: %q", h.calls)
	}
	if w := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), reader, nil); w.Code != http.StatusOK || h.calls[2] != "manifest "+histSample+"  0 100" {
		t.Errorf("the default page: %d %q", w.Code, h.calls[2])
	}

	// Existing trusted readers can read another project or no project. Only
	// an absent history is hidden; payload publication configuration is separate.
	f.srv.ContextReadProjects = nil
	for _, tc := range []struct{ tok, uid string }{
		{reader, histOther}, {reader, histNone}, {tokCoordinator, histNone}, {tokClient, histOther},
	} {
		w := f.do(http.MethodGet, apiv1.HistoryEntryPath(tc.uid), tc.tok, nil)
		if w.Code != http.StatusOK || decode[apiv1.HistoryManifest](t, w).Session.UID != tc.uid {
			t.Errorf("shared manifest: %d", w.Code)
		}
	}
	for _, tok := range []string{reader, tokCoordinator, tokClient} {
		wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath(histAbsent), tok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	}

	// Untrusted, human and no-longer-live callers are refused before the shelf.
	calls := len(h.calls)
	for _, tok := range []string{tokHuman, tokStranger} {
		wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), tok, nil), http.StatusNotFound, apiv1.CodeNotFound)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "reader"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), reader, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if len(h.calls) != calls {
		t.Errorf("a caller without scope reached the shelf: %v", h.calls[calls:])
	}
	pod.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"limit=0", "limit=1001", "continue=-1", "continue=x", "content=abc"} {
		wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample)+"?"+q, reader, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	}
	wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath("not-a-uid"), reader, nil), http.StatusBadRequest, apiv1.CodeBadRequest)

	// The shelf's failures are 503 for every caller in scope, never 404.
	for _, err := range []error{
		protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "x"),
		protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "x"),
		protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "x"),
		shelf.ErrNoShelf,
		errors.New("exec failed"),
	} {
		h.manErr = err
		wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), reader, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	}
	// A corrupt entry remains unavailable for every trusted reader, regardless
	// of its project or the directory named by agentd's refusal.
	for _, tc := range []struct {
		path string
		tok  string
		want int
	}{
		{"v1/other/" + histSample + "/0123456789abcdef", reader, http.StatusServiceUnavailable},
		{"v1/_none/" + histSample + "/0123456789abcdef", reader, http.StatusServiceUnavailable},
		{"v1/_none/" + histSample + "/0123456789abcdef", tokCoordinator, http.StatusServiceUnavailable},
		{"v1/sample/" + histSample + "/0123456789abcdef", reader, http.StatusServiceUnavailable},
		{"v1/other/" + histSample + "/0123456789abcdef", tokCoordinator, http.StatusServiceUnavailable},
		{"v1/other/" + histOther + "/0123456789abcdef", reader, http.StatusServiceUnavailable},
		{"", reader, http.StatusServiceUnavailable},
	} {
		h.manErr = protocol.HistoryErrorf(protocol.HistoryErrCorrupt, tc.path, "content.json does not match its entry")
		for _, path := range []string{apiv1.HistoryEntryPath(histSample), apiv1.HistoryFilePath(histSample) + "?path=claude/x&offset=0"} {
			if w := f.do(http.MethodGet, path, tc.tok, nil); w.Code != tc.want {
				t.Errorf("a corrupt entry at %q read by %s at %s: %d, want %d", tc.path, tc.tok, path, w.Code, tc.want)
			}
		}
	}
	f.srv.ContextReadProjects = []string{"sample"}
	h.manErr = nil
	f.srv.History = nil
	wantError(t, f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), reader, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
}

func TestHistoryFilePageChecksTrustedCallerFirst(t *testing.T) {
	f, h, reader := historyFixture(t)
	path := apiv1.HistoryFilePath(histSample) + "?path=claude/projects/a.jsonl&offset=1"
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("file: %d %s", w.Code, w.Body.String())
	}
	p := decode[apiv1.HistoryFilePage](t, w)
	if !p.Archived || !p.ArchivedAt.Equal(histArchivedAt) || string(p.Data) != "bc" || p.Offset != 1 || p.Next != 3 || !p.EOF ||
		p.Path != "claude/projects/a.jsonl" || p.ContentSHA256 != histSum(histSample) || p.SessionUID != histSample || p.FileSHA256 == "" {
		t.Fatalf("page: %+v", p)
	}
	// The newest entry's digest first, from the header only, then the read
	// pinned to it.
	if len(h.calls) != 2 || h.calls[0] != "manifest "+histSample+"  0 0" || h.calls[1] != "read "+histSample+" "+histSum(histSample)+" claude/projects/a.jsonl 1" {
		t.Fatalf("calls: %q", h.calls)
	}
	if !strings.Contains(w.Body.String(), `"data":"YmM="`) {
		t.Errorf("data is not base64: %s", w.Body.String())
	}

	// Shared files keep the same content pin and bytes across project buckets.
	f.srv.ContextReadProjects = nil
	h.calls = nil
	for _, uid := range []string{histOther, histNone} {
		w := f.do(http.MethodGet, apiv1.HistoryFilePath(uid)+"?path=claude/projects/a.jsonl&offset=0", reader, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("shared file: %d", w.Code)
		}
		got := decode[apiv1.HistoryFilePage](t, w)
		if string(got.Data) != "abc" || got.SessionUID != uid || got.ContentSHA256 != histSum(uid) {
			t.Errorf("shared file lost its content pin: %+v", got)
		}
	}
	calls := len(h.calls)
	wantError(t, f.do(http.MethodGet, apiv1.HistoryFilePath(histAbsent)+"?path=claude/projects/a.jsonl&offset=0", reader, nil), http.StatusNotFound, apiv1.CodeNotFound)
	if len(h.calls) != calls+1 || !strings.HasPrefix(h.calls[calls], "manifest ") {
		t.Error("an absent history reached the file read")
	}

	// In scope, the read's own refusals: a missing file is 404, an offset
	// past the end is 400, a damaged block is 503.
	h.readErr = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "claude/x", "the entry has no such file")
	wantError(t, f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/x&offset=0", reader, nil), http.StatusNotFound, apiv1.CodeNotFound)
	h.readErr = protocol.HistoryErrorf(protocol.HistoryErrInvalid, "claude/x", "offset outside")
	wantError(t, f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/x&offset=9", reader, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	h.readErr = protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "claude/x", "block 0 differs")
	wantError(t, f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/x&offset=0", reader, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	h.readErr = nil

	for _, q := range []string{"", "path=claude/x", "path=claude/x&offset=-1", "path=../x&offset=0", "path=other/x&offset=0", "path=claude&offset=0",
		"path=claude/x&offset=0&content=zz", "path=claude/" + strings.Repeat("a", 1100) + "&offset=0"} {
		wantError(t, f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?"+q, reader, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	}

	// A read with the content named reads that entry.
	h.calls = nil
	w = f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/history.jsonl&offset=0&content="+histSum(histSample), reader, nil)
	if w.Code != http.StatusOK || h.calls[0] != "manifest "+histSample+" "+histSum(histSample)+" 0 0" {
		t.Errorf("named content: %d %q", w.Code, h.calls)
	}
}

// Every read takes one of the replica's shelf exec slots, shared with the HOME
// expiry's relay. None free: 503, and the shelf is not asked.
func TestHistoryReadsTakeAShelfSlot(t *testing.T) {
	f, h, reader := historyFixture(t)
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	release, ok := f.srv.ShelfSlots.TryTake()
	if !ok {
		t.Fatal("slot")
	}
	for _, path := range []string{apiv1.HistoryPath + "?project=sample", apiv1.HistoryEntryPath(histSample), apiv1.HistoryFilePath(histSample) + "?path=claude/x&offset=0"} {
		wantError(t, f.do(http.MethodGet, path, reader, nil), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	}
	if len(h.calls) != 0 {
		t.Errorf("a read without a slot reached the shelf: %v", h.calls)
	}
	release()
	if w := f.do(http.MethodGet, apiv1.HistoryEntryPath(histSample), reader, nil); w.Code != http.StatusOK {
		t.Errorf("with the slot back: %d", w.Code)
	}
	// The slot is given back after each read.
	if w := f.do(http.MethodGet, apiv1.HistoryFilePath(histSample)+"?path=claude/x&offset=0", reader, nil); w.Code != http.StatusOK {
		t.Errorf("a second read: %d", w.Code)
	}
}
