package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type contextArchiveListFake struct {
	*contextArchiveFake
	list  protocol.CoordArchiveListResult
	err   error
	calls int
	query protocol.CoordArchiveListQuery
}

func (f *contextArchiveListFake) List(_ context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	f.calls++
	f.query = q
	return f.list, f.err
}

func archivedContextListItem(t *testing.T, p *coord.PublishedContext) protocol.CoordArchiveListItem {
	t.Helper()
	result := archivedContextResult(t, p)
	var projected map[string]any
	if json.Unmarshal(result.Object, &projected) != nil {
		t.Fatal("fixture")
	}
	delete(projected["spec"].(map[string]any), "text")
	object, err := protocol.CanonicalJSON(projected)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.CoordArchiveListItem{UID: string(p.UID), Manifest: &result.Manifest, Object: object}
}

func TestContextArchiveListPagingAndDefaultOff(t *testing.T) {
	f, reader, p, get, path := contextArchiveRig(t)
	item := archivedContextListItem(t, p)
	q := protocol.CoordArchiveListQuery{Resource: "publishedcontexts", Project: "sample", Limit: 1}
	next, err := q.Cursor("20261015T150000.000000000Z-" + string(p.UID) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	archive := &contextArchiveListFake{contextArchiveFake: get, list: protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{item}, Next: next}}
	f.srv.CoordinationArchive = archive
	listPath := strings.TrimSuffix(path, "/"+p.Name+"?project=sample") + "?project=sample&limit=1"
	// Default live listing remains untouched and never talks to shelf.
	w := f.do(http.MethodGet, listPath, reader, nil)
	if w.Code != http.StatusOK || archive.calls != 0 || len(decode[apiv1.PublishedContextList](t, w).Items) != 0 {
		t.Fatalf("live list = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	w = f.do(http.MethodGet, listPath+"&archived=only", reader, nil)
	got := decode[apiv1.PublishedContextList](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || archive.query != q || len(got.Items) != 1 || !got.Items[0].Archived || got.Items[0].ArchivedAt == nil || got.Continue == "" || strings.Contains(w.Body.String(), p.Spec.Text) {
		t.Fatalf("archived page = %d calls=%d query=%#v body=%s", w.Code, archive.calls, archive.query, w.Body.String())
	}
	archive.list = protocol.CoordArchiveListResult{}
	w = f.do(http.MethodGet, listPath+"&archived=only&continue="+got.Continue, reader, nil)
	if w.Code != http.StatusOK || archive.calls != 2 || archive.query.After != next || len(decode[apiv1.PublishedContextList](t, w).Items) != 0 {
		t.Fatalf("continued page = %d calls=%d query=%#v", w.Code, archive.calls, archive.query)
	}
}

func TestContextArchiveListRefusalsAndCorruptRows(t *testing.T) {
	f, reader, p, get, path := contextArchiveRig(t)
	archive := &contextArchiveListFake{contextArchiveFake: get}
	f.srv.CoordinationArchive = archive
	listPath := strings.TrimSuffix(path, "/"+p.Name+"?project=sample") + "?project=sample&limit=1&archived=only"
	for _, raw := range []string{"&continue=not-a-cursor", "&archived=all", "&unexpected=x"} {
		w := f.do(http.MethodGet, listPath+raw, reader, nil)
		if w.Code != http.StatusBadRequest || archive.calls != 0 {
			t.Fatalf("invalid query %q = %d calls=%d", raw, w.Code, archive.calls)
		}
	}
	inner, err := encodeContextArchiveListCursor("not-a-shelf-cursor", contextArchiveListCursor{Route: apiv1.PublishedContextsPath, Project: "sample", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodGet, listPath+"&continue="+inner, reader, nil)
	if w.Code != http.StatusBadRequest || archive.calls != 0 {
		t.Fatalf("invalid inner cursor = %d calls=%d", w.Code, archive.calls)
	}
	archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: string(p.UID), Error: protocol.HistoryErrCorrupt}}}
	w = f.do(http.MethodGet, listPath, reader, nil)
	if w.Code != http.StatusOK || archive.calls != 1 || len(decode[apiv1.PublishedContextList](t, w).Items) != 0 {
		t.Fatalf("corrupt omission = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	bad := archivedContextListItem(t, p)
	bad.Manifest.Project = "other"
	archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{bad}}
	w = f.do(http.MethodGet, listPath, reader, nil)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), p.Spec.Text) {
		t.Fatalf("bad metadata disclosed = %d %s", w.Code, w.Body.String())
	}
	f.srv.CoordinationArchiveReads = false
	w = f.do(http.MethodGet, listPath, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 2 {
		t.Fatalf("default off = %d calls=%d", w.Code, archive.calls)
	}
}

func TestContextArchiveListFencesSlotsAndResponseBounds(t *testing.T) {
	for _, mode := range []string{"client", "stale", "deleting", "list error", "bad item", "oversize", "nonadvancing"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, p, get, path := contextArchiveRig(t)
			archive := &contextArchiveListFake{contextArchiveFake: get}
			f.srv.CoordinationArchive = archive
			listPath := strings.TrimSuffix(path, "/"+p.Name+"?project=sample") + "?project=sample&limit=1&archived=only"
			want, calls := http.StatusServiceUnavailable, 1
			switch mode {
			case "client":
				reader, want, calls = tokClient, http.StatusForbidden, 0
			case "stale":
				id := f.auth[reader]
				id.PodUID = "replacement"
				f.auth[reader], want, calls = id, http.StatusForbidden, 0
			case "deleting":
				pod := &corev1.Pod{}
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "reader"}, pod); err != nil {
					t.Fatal("fixture")
				}
				pod.Finalizers = []string{"fixture/hold"}
				if err := f.c.Update(context.Background(), pod); err != nil || f.c.Delete(context.Background(), pod) != nil {
					t.Fatal("fixture")
				}
				want, calls = http.StatusForbidden, 0
			case "list error":
				archive.err = errors.New("unavailable")
			case "bad item":
				archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: string(p.UID), Error: "unknown"}}}
			case "oversize":
				archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: string(p.UID), Error: "unknown", Object: json.RawMessage(`"` + strings.Repeat("x", maxContextArchiveListBytes) + `"`)}}}
			case "nonadvancing":
				q := protocol.CoordArchiveListQuery{Resource: "publishedcontexts", Project: "sample", Limit: 1}
				after, err := q.Cursor("20261015T150000.000000000Z-" + string(p.UID) + ".json")
				if err != nil {
					t.Fatal(err)
				}
				outer, err := encodeContextArchiveListCursor(after, contextArchiveListCursor{Route: apiv1.PublishedContextsPath, Project: "sample", Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				listPath += "&continue=" + outer
				archive.list.Next = after
			}
			w := f.do(http.MethodGet, listPath, reader, nil)
			if w.Code != want || archive.calls != calls || strings.Contains(w.Body.String(), p.Spec.Text) {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			assertContextArchiveSlots(t, f.srv.ShelfSlots)
		})
	}
}

func TestContextArchiveListUnavailableAndCancelledReleaseSharedSlot(t *testing.T) {
	for _, mode := range []string{"nil slot", "busy", "cancelled", "list error"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, p, get, path := contextArchiveRig(t)
			archive := &contextArchiveListFake{contextArchiveFake: get}
			f.srv.CoordinationArchive = archive
			listPath := strings.TrimSuffix(path, "/"+p.Name+"?project=sample") + "?project=sample&limit=20&archived=only"
			var releases []func()
			switch mode {
			case "nil slot":
				f.srv.ShelfSlots = nil
			case "busy":
				for range 2 {
					release, ok := f.srv.ShelfSlots.TryTake()
					if !ok {
						t.Fatal("fixture slots")
					}
					releases = append(releases, release)
				}
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				req := httptest.NewRequest(http.MethodGet, listPath, nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+reader)
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, req)
				if w.Code != http.StatusServiceUnavailable || archive.calls != 0 {
					t.Fatalf("cancelled = %d calls=%d", w.Code, archive.calls)
				}
				assertContextArchiveSlots(t, f.srv.ShelfSlots)
				return
			case "list error":
				archive.err = errors.New("unavailable")
			}
			for _, release := range releases {
				defer release()
			}
			w := f.do(http.MethodGet, listPath, reader, nil)
			if w.Code != http.StatusServiceUnavailable || (mode == "list error" && archive.calls != 1) || (mode != "list error" && archive.calls != 0) {
				t.Fatalf("%s = %d calls=%d", mode, w.Code, archive.calls)
			}
			if mode != "busy" && f.srv.ShelfSlots != nil {
				assertContextArchiveSlots(t, f.srv.ShelfSlots)
			}
		})
	}
}

// contextArchiveListExecutor is an in-process shelf exec boundary. The typed
// shelf client still validates the text-stripped transport projection.
type contextArchiveListExecutor struct {
	body  []byte
	calls int
}

func (e *contextArchiveListExecutor) Run(_ context.Context, ns, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if ns != sessionNS || pod != "archive-shelf" || container != shelf.Container || len(cmd) < 8 || strings.Join(cmd[:6], " ") != "agentd ctl coord-archive list --resource publishedcontexts" {
		return errors.New("unexpected synthetic list exec route")
	}
	_, err := stdout.Write(e.body)
	return err
}

func TestContextArchiveListTypedProjectedRoundTrip(t *testing.T) {
	f, reader, p, get, path := contextArchiveRig(t)
	item := archivedContextListItem(t, p)
	body, err := json.Marshal(protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{item}})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "archive-shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	exec := &contextArchiveListExecutor{body: body}
	f.srv.CoordinationArchive = &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: f.c, Exec: exec, Namespace: sessionNS}}
	listPath := strings.TrimSuffix(path, "/"+p.Name+"?project=sample") + "?project=sample&limit=1&archived=only"
	w := f.do(http.MethodGet, listPath, reader, nil)
	got := decode[apiv1.PublishedContextList](t, w)
	if w.Code != http.StatusOK || exec.calls != 1 || get.calls != 0 || len(got.Items) != 1 || !got.Items[0].Archived || strings.Contains(w.Body.String(), p.Spec.Text) {
		t.Fatalf("typed projected list = %d calls=%d %s", w.Code, exec.calls, w.Body.String())
	}
}
