package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type taskClaimArchiveListFake struct {
	list  protocol.CoordArchiveListResult
	err   error
	calls int
	query protocol.CoordArchiveListQuery
}

func (f *taskClaimArchiveListFake) Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	return protocol.CoordArchiveGetResult{}, errors.New("unexpected task claim archive get")
}

func (f *taskClaimArchiveListFake) List(_ context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	f.calls++
	f.query = q
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
	for _, mode := range []string{"query", "foreign", "corrupt", "namespace", "uid", "name", "spec", "digest", "timestamp", "noncanonical", "text", "failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, reader, claim, archive := taskClaimArchiveListRig(t)
			path := apiv1.TaskClaimsPath + "?project=sample&archived=only"
			want, calls := http.StatusServiceUnavailable, 1
			item := archivedTaskClaimListItem(t, claim)
			switch mode {
			case "query":
				path += "&limit=1&limit=1"
				want, calls = http.StatusBadRequest, 0
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
			case "name":
				item.Manifest.Name = "task-claim." + strings.Repeat("a", 64)
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "spec":
				claim.Spec.Identity.TaskKey = "not-a-task-key"
				archive.list.Items = []protocol.CoordArchiveListItem{archivedTaskClaimListItem(t, claim)}
			case "digest":
				item.Manifest.Object.SHA256 = "invalid"
				archive.list.Items = []protocol.CoordArchiveListItem{item}
			case "timestamp":
				item.Manifest.ArchivedAt = time.Time{}
				archive.list.Items = []protocol.CoordArchiveListItem{item}
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
