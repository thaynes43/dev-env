package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type contextArchiveFake struct {
	result protocol.CoordArchiveGetResult
	err    error
	calls  int
	after  func()
}

func (f *contextArchiveFake) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	if f.after != nil {
		f.after()
	}
	if q.Resource != "publishedcontexts" || q.Name == "" {
		return protocol.CoordArchiveGetResult{}, errors.New("unexpected query")
	}
	return f.result, f.err
}

func archivedContextResult(t *testing.T, p *coord.PublishedContext) protocol.CoordArchiveGetResult {
	t.Helper()
	p.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "PublishedContext"}
	raw, err := protocol.CanonicalJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	m := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "publishedcontexts", Kind: "PublishedContext", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), ResourceVersion: p.ResourceVersion, Project: p.Spec.Project, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: m, ArchivedAt: time.Date(2026, 10, 15, 15, 0, 0, 0, time.UTC)}, Object: raw}
}

func TestContextArchivePublishedContextFallbackAndLiveFence(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	source, reader := contextSession(t, f, "source", humanSA), contextSession(t, f, "reader", clientSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	published := decode[apiv1.PublishContextResult](t, w)
	p, err := f.srv.PublishedContexts.Get(context.Background(), published.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &contextArchiveFake{result: archivedContextResult(t, p)}
	f.srv.CoordinationArchiveReads = true
	f.srv.CoordinationArchive = archive
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	path := apiv1.PublishedContextsPath + "/" + p.Name + "?project=sample"
	// A live object remains byte-for-byte the live projection and never asks shelf.
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK || archive.calls != 0 || decode[apiv1.PublishedContext](t, w).Metadata.Archived {
		t.Fatalf("live read = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"archived"`) || strings.Contains(w.Body.String(), `"archivedAt"`) {
		t.Fatal("live wire shape gained archive fields")
	}
	if err := f.c.Delete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, path, reader, nil)
	got := decode[apiv1.PublishedContext](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || !got.Metadata.Archived || got.Metadata.ArchivedAt == nil || got.Text != contextReq().Text || got.Metadata.ContentSHA256 != apiv1.ContextHash(got.Text) || got.Metadata.Attribution.Provider != p.Spec.Attribution.Provider || got.Metadata.Attribution.RequestedModel != p.Spec.Attribution.RequestedModel {
		t.Fatalf("archived read = %d calls=%d %#v", w.Code, archive.calls, got)
	}
}

func TestContextArchiveMissingAndForeignDoNotLeak(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	reader := contextSession(t, f, "reader", clientSA)
	archive := &contextArchiveFake{err: protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "absent")}
	f.srv.CoordinationArchive = archive
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	name := apiv1.ContextName("absent-uid", "result", "missing")
	path := apiv1.PublishedContextsPath + "/" + name + "?project=sample"
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("default off = %d calls=%d", w.Code, archive.calls)
	}
	f.srv.CoordinationArchiveReads = true
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 1 {
		t.Fatalf("archive miss = %d calls=%d", w.Code, archive.calls)
	}
	// A malformed foreign live object is masked before archive fallback.
	foreign := &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: name}}
	foreign.Spec.Project = "other"
	if err := f.c.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 1 {
		t.Fatalf("foreign mask = %d calls=%d", w.Code, archive.calls)
	}
}

func TestContextArchiveRefusesCorruptResult(t *testing.T) {
	f := newFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	reader := contextSession(t, f, "reader", clientSA)
	archive := &contextArchiveFake{result: protocol.CoordArchiveGetResult{Object: []byte(`{}`)}}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	name := apiv1.ContextName("absent-uid", "result", "corrupt")
	w := f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+name+"?project=sample", reader, nil)
	if w.Code != http.StatusServiceUnavailable || archive.calls != 1 {
		t.Fatalf("corrupt archive = %d calls=%d", w.Code, archive.calls)
	}
}

// All records and identities in this rig exist only in the fake API.
func contextArchiveRig(t *testing.T) (*fixture, string, *coord.PublishedContext, *contextArchiveFake, string) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	source := contextSession(t, f, "source", humanSA)
	reader := contextSession(t, f, "reader", clientSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	name := decode[apiv1.PublishContextResult](t, w).Metadata.Name
	p, err := f.srv.PublishedContexts.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &contextArchiveFake{result: archivedContextResult(t, p)}
	if err := f.c.Delete(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	return f, reader, p, archive, apiv1.PublishedContextsPath + "/" + name + "?project=sample"
}

func assertContextArchiveSlots(t *testing.T, slots *shelf.ExecSlots) {
	t.Helper()
	one, ok := slots.TryTake()
	if !ok {
		t.Fatal("first shared token leaked")
	}
	defer one()
	two, ok := slots.TryTake()
	if !ok {
		t.Fatal("second shared token leaked")
	}
	defer two()
	if extra, ok := slots.TryTake(); ok {
		extra()
		t.Fatal("pool capacity changed")
	}
}

func TestContextArchiveErrorIdentityAndScopeMatrix(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mutate        func(*coord.PublishedContext)
		code          string
		transport     bool
		wrongResource bool
		want          int
	}{
		{name: "not found", code: protocol.HistoryErrNotFound, want: 404},
		{name: "no archive", code: protocol.HistoryErrNoArchive, want: 503},
		{name: "corrupt", code: protocol.HistoryErrCorrupt, want: 503},
		{name: "ambiguous", code: protocol.HistoryErrAmbiguous, want: 503},
		{name: "transport", transport: true, want: 503},
		{name: "wrong resource", wrongResource: true, want: 503},
		{name: "foreign project", mutate: func(p *coord.PublishedContext) { p.Spec.Project = "other" }, want: 404},
		{name: "foreign namespace", mutate: func(p *coord.PublishedContext) { p.Namespace = "other" }, want: 503},
		{name: "wrong name", mutate: func(p *coord.PublishedContext) { p.Name = apiv1.ContextName("different", "result", "different") }, want: 503},
		{name: "wrong spec key with valid hash", mutate: func(p *coord.PublishedContext) { p.Spec.Identity.Key = "different-key" }, want: 503},
		{name: "invalid immutable text", mutate: func(p *coord.PublishedContext) { p.Spec.Text = "invalid-hash" }, want: 503},
		{name: "wrong UID", mutate: func(p *coord.PublishedContext) { p.UID = "bad" }, want: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, reader, p, archive, path := contextArchiveRig(t)
			if tc.mutate != nil {
				tc.mutate(p)
				archive.result = archivedContextResult(t, p)
			}
			if tc.wrongResource {
				archive.result.Manifest.Resource = "taskclaims"
			}
			if tc.code != "" {
				archive.err = protocol.HistoryErrorf(tc.code, "", "fixture")
			}
			if tc.transport {
				archive.err = errors.New("fixture transport unavailable")
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != tc.want || archive.calls != 1 || strings.Contains(w.Body.String(), contextReq().Text) {
				t.Fatalf("refusal %d calls%d: %s", w.Code, archive.calls, w.Body.String())
			}
			assertContextArchiveSlots(t, f.srv.ShelfSlots)
		})
	}
}

func TestContextArchiveCallerFencesBeforeShelf(t *testing.T) {
	for _, mode := range []string{"unscoped", "wrong project", "client", "stale pod", "deleting pod", "suspended session"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, _, archive, path := contextArchiveRig(t)
			switch mode {
			case "unscoped":
				f.srv.ContextReadProjects = nil
			case "wrong project":
				path = strings.Replace(path, "project=sample", "project=other", 1)
			case "client":
				reader = tokClient
			case "stale pod":
				id := f.auth[reader]
				id.PodUID = "replaced-pod"
				f.auth[reader] = id
			case "deleting pod":
				var pod corev1.Pod
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "reader"}, &pod); err != nil {
					t.Fatal(err)
				}
				pod.Finalizers = []string{"fixture/hold"}
				if err := f.c.Update(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
			case "suspended session":
				s := f.session("reader")
				s.Spec.OperatingMode = "Suspended"
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != http.StatusForbidden || archive.calls != 0 || strings.Contains(w.Body.String(), contextReq().Text) {
				t.Fatalf("fence %d calls%d %s", w.Code, archive.calls, w.Body.String())
			}
		})
	}
}

func TestContextArchiveSourceAbsentCoordinatorAndSharedPool(t *testing.T) {
	f, reader, p, archive, path := contextArchiveRig(t)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "source"}, &pod); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	s := f.session("source")
	s.Finalizers = nil
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if key.Name == "source" {
			t.Fatal("archived read joined absent source")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	archive.after = func() {
		extra, ok := f.srv.ShelfSlots.TryTake()
		if !ok {
			t.Fatal("Get holds more than one token")
		}
		defer extra()
		if unexpected, ok := f.srv.ShelfSlots.TryTake(); ok {
			unexpected()
			t.Fatal("Get holds no token")
		}
	}
	for _, token := range []string{reader, tokCoordinator} {
		w := f.do(http.MethodGet, path, token, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		got := decode[apiv1.PublishedContext](t, w)
		if got.Text != p.Spec.Text || got.Metadata.ContentSHA256 != apiv1.ContextHash(p.Spec.Text) || got.Metadata.Attribution.Provider != p.Spec.Attribution.Provider || got.Metadata.Attribution.RequestedModel != p.Spec.Attribution.RequestedModel || got.Metadata.Attribution.ObservedModelState != "Unknown" || !got.Metadata.Archived || got.Metadata.ArchivedAt == nil || !got.Metadata.ArchivedAt.Equal(archive.result.Manifest.ArchivedAt) {
			t.Fatalf("projection: %#v", got)
		}
		assertContextArchiveSlots(t, f.srv.ShelfSlots)
	}
}

func TestContextArchiveLiveErrorsAndUnavailableDependencies(t *testing.T) {
	for _, mode := range []string{"live error", "nil pool", "nil archive", "slot deadline"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, _, archive, path := contextArchiveRig(t)
			switch mode {
			case "live error":
				f.srv.PublishedContexts.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errors.New("live unavailable")
				}})
			case "nil pool":
				f.srv.ShelfSlots = nil
			case "nil archive":
				f.srv.CoordinationArchive = nil
			case "slot deadline":
				one, _ := f.srv.ShelfSlots.TryTake()
				defer one()
				two, _ := f.srv.ShelfSlots.TryTake()
				defer two()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				if release, err := f.srv.coordinationArchiveSlot(ctx); err == nil {
					release()
					t.Fatal("busy pool ignored short parent deadline")
				}
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) || archive.calls != 0 {
					t.Fatal("deadline not propagated")
				}
				return
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != 503 || archive.calls != 0 {
				t.Fatalf("unavailable %d calls%d %s", w.Code, archive.calls, w.Body.String())
			}
		})
	}
}

func TestContextArchiveCancelAfterSuccessfulClient(t *testing.T) {
	f, reader, _, archive, path := contextArchiveRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	archive.after = cancel
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+reader)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code != 503 || archive.calls != 1 || strings.Contains(w.Body.String(), contextReq().Text) {
		t.Fatalf("cancelled payload %d calls%d %s", w.Code, archive.calls, w.Body.String())
	}
	assertContextArchiveSlots(t, f.srv.ShelfSlots)
}

// Only an in-process stdout writer; no Kubernetes exec or subprocess is used.
type contextArchiveExecutor struct {
	body  []byte
	calls int
}

func (e *contextArchiveExecutor) Run(_ context.Context, ns, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if ns != sessionNS || pod != "archive-shelf" || container != shelf.Container || len(cmd) < 7 || strings.Join(cmd[:6], " ") != "agentd ctl coord-archive get --resource publishedcontexts" {
		return errors.New("unexpected synthetic exec route")
	}
	_, err := stdout.Write(e.body)
	return err
}
func TestContextArchiveTypedShelfClientRoundTripAndCorruption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%v", corrupt), func(t *testing.T) {
			f, reader, _, archive, path := contextArchiveRig(t)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "archive-shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			if err := f.c.Create(context.Background(), pod); err != nil {
				t.Fatal(err)
			}
			result := archive.result
			if corrupt {
				result.Object = []byte(strings.Replace(string(result.Object), contextReq().Text, "CHANGED_SYNTHETIC_TEXT", 1))
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			exec := &contextArchiveExecutor{body: body}
			f.srv.CoordinationArchive = &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: f.c, Exec: exec, Namespace: sessionNS}}
			w := f.do(http.MethodGet, path, reader, nil)
			if corrupt {
				if w.Code != 503 || strings.Contains(w.Body.String(), "SYNTHETIC_TEXT") || strings.Contains(w.Body.String(), contextReq().Text) {
					t.Fatalf("transport corruption %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != 200 || decode[apiv1.PublishedContext](t, w).Text != contextReq().Text {
				t.Fatalf("transport roundtrip %d %s", w.Code, w.Body.String())
			}
			if exec.calls != 1 || archive.calls != 0 {
				t.Fatal("typed client did not own fake exec route")
			}
			assertContextArchiveSlots(t, f.srv.ShelfSlots)
		})
	}
}
