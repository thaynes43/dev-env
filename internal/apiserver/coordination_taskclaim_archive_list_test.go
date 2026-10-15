package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

type taskClaimArchiveListFake struct {
	list      protocol.CoordArchiveListResult
	err       error
	calls     int
	query     protocol.CoordArchiveListQuery
	afterList func()
}

func (f *taskClaimArchiveListFake) Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	return protocol.CoordArchiveGetResult{}, errors.New("unexpected task claim archive get")
}

func (f *taskClaimArchiveListFake) List(_ context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	f.calls++
	f.query = q
	if f.afterList != nil {
		f.afterList()
	}
	return f.list, f.err
}

func archivedTaskClaimListItem(t *testing.T, claim *coord.TaskClaim) protocol.CoordArchiveListItem {
	t.Helper()
	claim.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "TaskClaim"}
	raw, err := protocol.CanonicalJSON(claim)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "taskclaims", Kind: "TaskClaim", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: claim.Namespace, Name: claim.Name, UID: string(claim.UID), ResourceVersion: claim.ResourceVersion, Project: claim.Spec.Identity.Project, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	return protocol.CoordArchiveListItem{UID: string(claim.UID), Manifest: &protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC)}, Object: raw}
}

func taskClaimArchiveListRig(t *testing.T) (*fixture, string, *coord.TaskClaim, *taskClaimArchiveListFake) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	owner, reader := claimSession(t, f, "archive-owner", humanSA), claimSession(t, f, "archive-reader", clientSA)
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, owner, claimRequest("issue:311"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	name := decode[apiv1.TaskClaimResult](t, w).Claim.Name
	claim, err := f.srv.TaskClaims.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	archive := &taskClaimArchiveListFake{}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	return f, reader, claim, archive
}

func TestTaskClaimArchiveListPagingDefaultOffAndNoOwnerJoin(t *testing.T) {
	f, reader, claim, archive := taskClaimArchiveListRig(t)
	item := archivedTaskClaimListItem(t, claim)
	query := protocol.CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 1}
	next, err := query.Cursor("20261015T170000.000000000Z-" + string(claim.UID) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{item}, Next: next}
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
		if key.Namespace == claim.Spec.Owner.Namespace && key.Name == claim.Spec.Owner.Name {
			t.Fatal("archived task claim list consulted owner Session")
		}
		return c.Get(ctx, key, object, opts...)
	}})
	path := apiv1.TaskClaimsPath + "?project=sample&limit=1"
	if w := f.do(http.MethodGet, path, reader, nil); w.Code != http.StatusOK || archive.calls != 0 || len(decode[apiv1.TaskClaimList](t, w).Items) != 0 {
		t.Fatalf("live list = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	w := f.do(http.MethodGet, path+"&archived=only", reader, nil)
	got := decode[apiv1.TaskClaimList](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || archive.query != query || len(got.Items) != 1 || !got.Items[0].Archived || got.Items[0].ArchivedAt == nil || got.Items[0].OwnerState != "Unknown" || got.Items[0].StopState != "Unknown" || got.Continue == "" {
		t.Fatalf("archived page = %d calls=%d query=%#v body=%s", w.Code, archive.calls, archive.query, w.Body.String())
	}
	wantMetadata := claimMetadata(claim)
	if got.Items[0].Owner != wantMetadata.Owner || got.Items[0].Actor != wantMetadata.Actor || !reflect.DeepEqual(got.Items[0].Provenance, wantMetadata.Provenance) {
		t.Fatal("archive view lost immutable attribution")
	}
	archive.list = protocol.CoordArchiveListResult{}
	if w = f.do(http.MethodGet, path+"&archived=only&continue="+got.Continue, reader, nil); w.Code != http.StatusOK || archive.calls != 2 || archive.query.After != next {
		t.Fatalf("continued page = %d calls=%d query=%#v", w.Code, archive.calls, archive.query)
	}
	f.srv.CoordinationArchiveReads = false
	if w = f.do(http.MethodGet, path+"&archived=only", reader, nil); w.Code != http.StatusNotFound || archive.calls != 2 {
		t.Fatalf("default-off archive list = %d calls=%d", w.Code, archive.calls)
	}
}

func TestTaskClaimArchiveListRefusesInvalidInputsAndMetadata(t *testing.T) {
	for _, mode := range []string{"query", "unsupported", "malformed-project", "foreign", "corrupt", "namespace", "uid", "valid-uid-mismatch", "revision-mismatch", "resource", "project", "name", "deterministic-name", "spec", "validspec", "digest", "timestamp", "zero-created", "noncanonical", "text", "failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, claim, archive := taskClaimArchiveListRig(t)
			path := apiv1.TaskClaimsPath + "?project=sample&archived=only"
			want, calls := http.StatusServiceUnavailable, 1
			item := archivedTaskClaimListItem(t, claim)
			switch mode {
			case "query":
				path += "&limit=1&limit=1"
				want, calls = http.StatusBadRequest, 0
			case "unsupported":
				path = strings.Replace(path, "archived=only", "archived=all", 1)
				want, calls = http.StatusBadRequest, 0
			case "malformed-project":
				path = strings.Replace(path, "project=sample", "project=bad!", 1)
				want, calls = http.StatusForbidden, 0
			case "foreign":
				path = apiv1.TaskClaimsPath + "?project=other&archived=only"
				want, calls = http.StatusForbidden, 0
			case "corrupt":
				archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: string(claim.UID), Error: protocol.HistoryErrCorrupt}}}
				want = http.StatusOK
			case "namespace":
				item.Manifest.Namespace = "other"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "uid":
				item.UID, item.Manifest.UID = "bad", "bad"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "valid-uid-mismatch":
				item.UID, item.Manifest.UID = "99999999-9999-9999-9999-999999999999", "99999999-9999-9999-9999-999999999999"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "revision-mismatch":
				item.Manifest.ResourceVersion = "different-valid-revision"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "resource":
				item.Manifest.Resource = "publishedcontexts"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "project":
				claim.Spec.Identity.Project = "other"
				claim.Name = taskclaim.Name(claim.Spec.Identity)
				archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
			case "name":
				item.Manifest.Name = "task-claim." + strings.Repeat("a", 64)
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "spec":
				claim.Spec.Identity.TaskKey = "not-a-task-key"
				archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
			case "deterministic-name":
				other := claim.Spec.Identity
				other.TaskKey = "issue:9999"
				claim.Name = taskclaim.Name(other)
				archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
			case "validspec":
				claim.Spec.Provenance.InitialPinState = "NotUnknown"
				archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
			case "digest":
				item.Manifest.Object.SHA256 = "invalid"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "timestamp":
				item.Manifest.ArchivedAt = time.Time{}
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "zero-created":
				claim.CreationTimestamp = metav1.Time{}
				archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
			case "noncanonical":
				item.Object = append([]byte(" \n"), item.Object...)
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "text":
				var object map[string]any
				if json.Unmarshal(item.Object, &object) != nil {
					t.Fatal("fixture")
				}
				object["spec"].(map[string]any)["text"] = "must-not-leak"
				item.Object, _ = protocol.CanonicalJSON(object)
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "failure":
				archive.err = errors.New("shelf unavailable")
			case "cancelled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+reader)
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, req)
				if w.Code != http.StatusServiceUnavailable || archive.calls != 0 {
					t.Fatalf("cancelled = %d calls=%d", w.Code, archive.calls)
				}
				assertContextArchiveSlots(t, f.srv.ShelfSlots)
				return
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != want || archive.calls != calls || strings.Contains(w.Body.String(), "must-not-leak") {
				t.Fatalf("%s = %d calls=%d body=%s", mode, w.Code, archive.calls, w.Body.String())
			}
			assertContextArchiveSlots(t, f.srv.ShelfSlots)
		})
	}
}

func TestTaskClaimArchiveListCursorBindsRouteProjectLimitAndProgress(t *testing.T) {
	f, reader, claim, archive := taskClaimArchiveListRig(t)
	query := protocol.CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 1}
	position := "20261015T170000.000000000Z-" + string(claim.UID) + ".json"
	after, err := query.Cursor(position)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []contextArchiveListCursor{{Route: apiv1.PublishedContextsPath, Project: "sample", Limit: 1}, {Route: apiv1.TaskClaimsPath, Project: "other", Limit: 1}, {Route: apiv1.TaskClaimsPath, Project: "sample", Limit: 2}} {
		outer, err := encodeContextArchiveListCursor(after, scope)
		if err != nil {
			t.Fatal(err)
		}
		w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only&continue="+outer, reader, nil)
		if w.Code != http.StatusBadRequest || archive.calls != 0 {
			t.Fatalf("foreign cursor = %d calls=%d", w.Code, archive.calls)
		}
	}
	outer, err := encodeContextArchiveListCursor(after, contextArchiveListCursor{Route: apiv1.TaskClaimsPath, Project: "sample", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	archive.list.Next = after
	w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only&continue="+outer, reader, nil)
	if w.Code != http.StatusServiceUnavailable || archive.calls != 1 {
		t.Fatalf("nonprogress cursor = %d calls=%d", w.Code, archive.calls)
	}
}

func TestTaskClaimArchiveListLimitsAndCallerFence(t *testing.T) {
	f, reader, claim, archive := taskClaimArchiveListRig(t)
	archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
	path := apiv1.TaskClaimsPath + "?project=sample&archived=only"
	for _, request := range []struct{ token, suffix string }{{reader, ""}, {tokCoordinator, ""}, {reader, "&limit=20"}} {
		w := f.do(http.MethodGet, path+request.suffix, request.token, nil)
		if w.Code != http.StatusOK || archive.query.Limit != 20 || len(decode[apiv1.TaskClaimList](t, w).Items) != 1 {
			t.Fatalf("default/max/coordinator = %d query=%#v %s", w.Code, archive.query, w.Body.String())
		}
	}
	for _, suffix := range []string{"&limit=0", "&limit=21", "&limit=100", "&limit=bad", "&archived=only", "&unexpected=value", "&continue=live-cursor"} {
		w := f.do(http.MethodGet, path+suffix, reader, nil)
		if w.Code != http.StatusBadRequest || archive.calls != 3 {
			t.Fatalf("invalid query %s = %d calls=%d", suffix, w.Code, archive.calls)
		}
	}
	for _, token := range []string{tokHuman, tokClient, tokStranger, ""} {
		w := f.do(http.MethodGet, path, token, nil)
		if (w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized) || archive.calls != 3 {
			t.Fatalf("unauthorized caller = %d calls=%d", w.Code, archive.calls)
		}
	}
	for _, mode := range []string{"replaced-session", "deleting-pod", "missing-grant"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, _, archive := taskClaimArchiveListRig(t)
			if mode == "replaced-session" {
				s := f.session("archive-reader")
				s.UID = "replacement-session"
				if err := f.c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			} else if mode == "deleting-pod" {
				var pod corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: "archive-reader"}, &pod); err != nil {
					t.Fatal(err)
				}
				pod.Finalizers = []string{"fixture-retain"}
				if err := f.c.Update(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), &pod); err != nil {
					t.Fatal(err)
				}
			} else {
				f.srv.TaskClaimReadProjects = []string{"other"}
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != http.StatusForbidden || archive.calls != 0 {
				t.Fatalf("stale/deleting caller = %d calls=%d", w.Code, archive.calls)
			}
		})
	}
}

func TestTaskClaimArchiveListDecodedProgressAndCorruptContinuation(t *testing.T) {
	f, reader, claim, archive := taskClaimArchiveListRig(t)
	query := protocol.CoordArchiveListQuery{Resource: "taskclaims", Project: "sample", Limit: 1}
	after, err := query.Cursor("20261015T180000.000000000Z-" + string(claim.UID) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	outer, err := encodeContextArchiveListCursor(after, contextArchiveListCursor{Route: apiv1.TaskClaimsPath, Project: "sample", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(after)
	if err != nil {
		t.Fatal(err)
	}
	alias := base64.RawURLEncoding.EncodeToString(append([]byte(" "), raw...))
	for _, mode := range []string{"alternate", "newer", "older"} {
		next, want := alias, http.StatusServiceUnavailable
		if mode != "alternate" {
			at := "20261015T190000.000000000Z-"
			if mode == "older" {
				at, want = "20261015T170000.000000000Z-", http.StatusOK
			}
			next, err = query.Cursor(at + string(claim.UID) + ".json")
			if err != nil {
				t.Fatal(err)
			}
		}
		archive.list = protocol.CoordArchiveListResult{Next: next}
		w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only&continue="+outer, reader, nil)
		if w.Code != want {
			t.Fatalf("%s = %d %s", mode, w.Code, w.Body.String())
		}
	}
	for _, scope := range []contextArchiveListCursor{{Route: apiv1.TaskClaimsPath, Project: "sample", Limit: 1, Recipient: "99999999-9999-9999-9999-999999999999"}, {Route: apiv1.TaskClaimsPath, Project: "sample", Limit: 1}} {
		inner := after
		if scope.Recipient == "" {
			inner = "invalid-inner"
		}
		invalid, err := encodeContextArchiveListCursor(inner, scope)
		if err != nil {
			t.Fatal(err)
		}
		before := archive.calls
		w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only&continue="+invalid, reader, nil)
		if w.Code != http.StatusBadRequest || archive.calls != before {
			t.Fatalf("cursor binding = %d calls=%d", w.Code, archive.calls)
		}
	}
	archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{{UID: string(claim.UID), Error: protocol.HistoryErrCorrupt}}, Next: after}
	w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only", reader, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	page := decode[apiv1.TaskClaimList](t, w)
	if len(page.Items) != 0 || page.Continue == "" {
		t.Fatal("corrupt row lost usable continuation")
	}
	archive.list = protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only&continue="+page.Continue, reader, nil)
	if w.Code != http.StatusOK || len(decode[apiv1.TaskClaimList](t, w).Items) != 1 {
		t.Fatalf("usable later page = %d %s", w.Code, w.Body.String())
	}
}

func TestTaskClaimArchiveListPreservesLiveProjectionAndBounds(t *testing.T) {
	f, reader, claim, _ := taskClaimArchiveListRig(t)
	owner := f.session("archive-owner")
	owner.Status.Phase = "Running"
	if err := f.c.Status().Update(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if live, archived := f.srv.claimView(context.Background(), claim), claimMetadata(claim); live.OwnerState != "Running" || archived.OwnerState != "Unknown" || live.Archived || live.ArchivedAt != nil {
		t.Fatal("live owner join or immutable projection changed")
	}
	var observed int64
	f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		var options client.ListOptions
		for _, opt := range opts {
			opt.ApplyToList(&options)
		}
		observed = options.Limit
		return c.List(ctx, list, opts...)
	}})
	for _, suffix := range []string{"", "&limit=100"} {
		w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample"+suffix, reader, nil)
		if w.Code != http.StatusOK || observed != 100 {
			t.Fatalf("live list limit = %d observed=%d", w.Code, observed)
		}
	}
}

func TestTaskClaimArchiveListSharedSlotBoundaries(t *testing.T) {
	for _, mode := range []string{"no-reader", "get-only", "no-slots", "busy", "cancel-after-list"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, _, archive := taskClaimArchiveListRig(t)
			ctx := context.Background()
			var held []func()
			wantCalls := 0
			switch mode {
			case "no-reader":
				f.srv.CoordinationArchive = nil
			case "get-only":
				f.srv.CoordinationArchive = &contextArchiveFake{}
			case "no-slots":
				f.srv.ShelfSlots = nil
			case "busy":
				for n := 0; n < 2; n++ {
					release, ok := f.srv.ShelfSlots.TryTake()
					if !ok {
						t.Fatal("fixture could not take shared slots")
					}
					held = append(held, release)
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			case "cancel-after-list":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				archive.afterList = cancel
				wantCalls = 1
			}
			req := httptest.NewRequest(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&archived=only", nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+reader)
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, req)
			for _, release := range held {
				release()
			}
			if w.Code != http.StatusServiceUnavailable || archive.calls != wantCalls {
				t.Fatalf("%s = %d calls=%d", mode, w.Code, archive.calls)
			}
			if f.srv.ShelfSlots != nil {
				assertContextArchiveSlots(t, f.srv.ShelfSlots)
			}
		})
	}
}

func TestTaskClaimArchiveListResponseBounds(t *testing.T) {
	for _, mode := range []string{"too-many", "too-large", "corrupt-payload", "unknown-error", "missing-manifest"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, claim, archive := taskClaimArchiveListRig(t)
			item := archivedTaskClaimListItem(t, claim)
			switch mode {
			case "too-many":
				for n := 0; n < 21; n++ {
					archive.list.Items = append(archive.list.Items, item)
				}
			case "too-large":
				claim.Annotations = map[string]string{"fixture": strings.Repeat("x", 220<<10)}
				for n := 0; n < 20; n++ {
					row := claim.DeepCopy()
					row.UID = types.UID(fmt.Sprintf("11111111-1111-1111-1111-%012d", n))
					row.Spec.Identity.TaskKey = fmt.Sprintf("issue:%d", 1000+n)
					row.Name = taskclaim.Name(row.Spec.Identity)
					valid := archivedTaskClaimListItem(t, row)
					if _, _, err := f.srv.archivedTaskClaimMetadata(*valid.Manifest, valid.Object); err != nil {
						t.Fatalf("oversize-page fixture row is individually invalid: %v", err)
					}
					archive.list.Items = append(archive.list.Items, valid)
				}
				envelope, err := protocol.CanonicalJSON(archive.list)
				if err != nil || len(envelope) <= maxTaskClaimArchiveListBytes {
					t.Fatal("fixture does not exceed only the whole-page byte bound")
				}
			case "corrupt-payload":
				item.Error = protocol.HistoryErrCorrupt
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "unknown-error":
				item.Error = "unknown"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "missing-manifest":
				item.Manifest = nil
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			}
			w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&archived=only", reader, nil)
			if w.Code != http.StatusServiceUnavailable || archive.calls != 1 {
				t.Fatalf("%s = %d calls=%d", mode, w.Code, archive.calls)
			}
			assertContextArchiveSlots(t, f.srv.ShelfSlots)
		})
	}
}

type taskClaimArchiveListExecutor struct {
	body  []byte
	calls int
}

func (e *taskClaimArchiveListExecutor) Run(_ context.Context, ns, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if ns != sessionNS || pod != "archive-shelf" || container != shelf.Container || len(cmd) < 8 || strings.Join(cmd[:6], " ") != "agentd ctl coord-archive list --resource taskclaims" {
		return errors.New("unexpected synthetic task claim list route")
	}
	_, err := stdout.Write(e.body)
	return err
}

func TestTaskClaimArchiveListTypedShelfProjection(t *testing.T) {
	f, reader, claim, fake := taskClaimArchiveListRig(t)
	body, err := json.Marshal(protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "archive-shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	exec := &taskClaimArchiveListExecutor{body: body}
	f.srv.CoordinationArchive = &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: f.c, Exec: exec, Namespace: sessionNS}}
	w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample&limit=1&archived=only", reader, nil)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	got := decode[apiv1.TaskClaimList](t, w)
	if exec.calls != 1 || fake.calls != 0 || len(got.Items) != 1 || !got.Items[0].Archived || got.Items[0].OwnerState != "Unknown" {
		t.Fatalf("typed list = %#v calls=%d", got, exec.calls)
	}
}
