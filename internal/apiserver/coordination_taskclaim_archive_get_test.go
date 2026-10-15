package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

type taskClaimArchiveGetFake struct {
	legacyResult *protocol.CoordArchiveGetResult
	legacyCalls  int
	result       protocol.CoordArchiveGetResult
	calls        int
	query        protocol.CoordArchiveGetQuery
	err          error
	after        func()
}

func (f *taskClaimArchiveGetFake) Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.legacyCalls++
	if f.legacyResult != nil {
		return *f.legacyResult, nil
	}
	return protocol.CoordArchiveGetResult{}, context.Canceled
}

func (f *taskClaimArchiveGetFake) GetItem(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	f.query = q
	if f.after != nil {
		f.after()
	}
	if q.Resource != "taskclaims" || q.Name == "" || q.ResourceVersion != "" || q.SHA256 != "" {
		return protocol.CoordArchiveGetResult{}, context.Canceled
	}
	return f.result, f.err
}

func archivedTaskClaimGetResult(t *testing.T, claim *coord.TaskClaim, at time.Time) protocol.CoordArchiveGetResult {
	t.Helper()
	claim.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "TaskClaim"}
	raw, err := protocol.CanonicalJSON(claim)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "taskclaims", Kind: "TaskClaim", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: claim.Namespace, Name: claim.Name, UID: string(claim.UID), ResourceVersion: claim.ResourceVersion, Project: claim.Spec.Identity.Project, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	if _, err := protocol.VerifyCoordArchiveObject(manifest, bytes.NewReader(raw)); err != nil {
		t.Fatalf("fixture full object invalid: %v", err)
	}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: at}, Object: raw}
}

func TestTaskClaimArchiveGetRedAcceptedBase(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	owner, reader := claimSession(t, f, "archive-owner", humanSA), claimSession(t, f, "archive-reader", clientSA)
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, owner, claimRequest("issue:329"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	claim, err := f.srv.TaskClaims.Get(context.Background(), decode[apiv1.TaskClaimResult](t, w).Claim.Name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &taskClaimArchiveGetFake{result: archivedTaskClaimGetResult(t, claim, time.Date(2026, 10, 15, 21, 0, 0, 0, time.UTC))}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	if w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+claim.Name+"?project=sample", reader, nil); w.Code != http.StatusOK || archive.calls != 0 {
		t.Fatalf("live control = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, apiv1.TaskClaimsPath+"/"+claim.Name+"?project=sample", reader, nil)
	if w.Code != http.StatusOK || archive.calls != 1 {
		t.Fatalf("accepted archive fallback = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
}

func taskClaimArchiveGetRig(t *testing.T) (*fixture, string, string, *coord.TaskClaim, *taskClaimArchiveGetFake, string) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	owner, reader := claimSession(t, f, "archive-owner", humanSA), claimSession(t, f, "archive-reader", clientSA)
	w := f.do(http.MethodPost, apiv1.TaskClaimsPath, owner, claimRequest("issue:329"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	claim, err := f.srv.TaskClaims.Get(context.Background(), decode[apiv1.TaskClaimResult](t, w).Claim.Name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &taskClaimArchiveGetFake{result: archivedTaskClaimGetResult(t, claim, time.Date(2026, 10, 15, 19, 15, 0, 0, time.UTC))}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	return f, owner, reader, claim, archive, apiv1.TaskClaimsPath + "/" + claim.Name + "?project=sample"
}

func TestTaskClaimArchiveGetPreservesImmutableAttributionWithoutOwnerJoin(t *testing.T) {
	f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	removeTaskClaimArchiveOwner(t, f, claim.Spec.Owner.Name)
	recreated := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: claim.Spec.Owner.Namespace, Name: claim.Spec.Owner.Name, UID: "replacement-owner"}}
	if err := f.c.Create(context.Background(), recreated); err != nil {
		t.Fatal(err)
	}
	ownerReads := 0
	f.srv.Live = ownerPoisoningClient(f.c, claim.Spec.Owner.Namespace, claim.Spec.Owner.Name, &ownerReads)
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK || archive.calls != 1 || archive.query != (protocol.CoordArchiveGetQuery{Resource: "taskclaims", Name: claim.Name}) {
		t.Fatalf("archive get = %d calls=%d query=%#v body=%s", w.Code, archive.calls, archive.query, w.Body.String())
	}
	got := decode[apiv1.TaskClaim](t, w)
	want := expectedArchivedTaskClaim(t, claim, archive.result.Manifest.ArchivedAt)
	if !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(archive.result.Manifest.ArchivedAt) || got.ArchivedAt.Location() != time.UTC || got.OwnerState != "Unknown" || got.StopState != "Unknown" {
		t.Fatalf("archive receipt/states = %#v", got)
	}
	if ownerReads != 0 || got.Actor.PodName == f.auth[reader].PodName || got.Actor.PodUID == f.auth[reader].PodUID {
		t.Fatalf("archived owner/caller attribution was consulted or rewritten: ownerReads=%d got=%#v caller=%#v", ownerReads, got.Actor, f.auth[reader])
	}
	got = normalizeTaskClaimUTC(t, got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("immutable task claim changed: got=%#v want=%#v", got, want)
	}
	assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
}

// Construct this oracle from immutable archived data, not the production
// projection. Marshal/unmarshal forces the same API wire normalization while
// retaining every asserted field and UTC receipt/timestamp semantics.
func expectedArchivedTaskClaim(t *testing.T, claim *coord.TaskClaim, receipt time.Time) apiv1.TaskClaim {
	t.Helper()
	p := claim.Spec.Provenance
	receipt = receipt.UTC()
	want := apiv1.TaskClaim{
		Name: claim.Name, UID: string(claim.UID), CreatedAt: claim.CreationTimestamp.UTC(),
		Identity:   apiv1.TaskClaimIdentity{Project: claim.Spec.Identity.Project, Repository: claim.Spec.Identity.Repository, TaskKey: claim.Spec.Identity.TaskKey},
		Owner:      apiv1.TaskClaimOwner{Namespace: claim.Spec.Owner.Namespace, Name: claim.Spec.Owner.Name, UID: claim.Spec.Owner.UID},
		Actor:      apiv1.TaskClaimActor{Kind: claim.Spec.Actor.Kind, ServiceAccount: claim.Spec.Actor.ServiceAccount, PodName: claim.Spec.Actor.PodName, PodUID: claim.Spec.Actor.PodUID},
		Provenance: apiv1.TaskClaimProvenance{CatalogRevision: p.CatalogRevision, RulesRevision: p.RulesRevision, GitHub: p.GitHub, DefaultBranch: p.DefaultBranch, InitialPinState: p.InitialPinState, ReportedHead: p.ReportedHead},
		OwnerState: "Unknown", StopState: "Unknown", Archived: true, ArchivedAt: &receipt,
	}
	if p.ReportedAt != nil {
		reported := p.ReportedAt.UTC()
		want.Provenance.ReportedAt = &reported
	}
	if want.ArchivedAt == nil || want.ArchivedAt.Location() != time.UTC || want.CreatedAt.Location() != time.UTC || (want.Provenance.ReportedAt != nil && want.Provenance.ReportedAt.Location() != time.UTC) {
		t.Fatal("saved immutable source was not normalized to UTC before wire comparison")
	}
	return normalizeTaskClaimUTC(t, want)
}

func normalizeTaskClaimUTC(t *testing.T, claim apiv1.TaskClaim) apiv1.TaskClaim {
	t.Helper()
	wire, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &claim); err != nil {
		t.Fatal(err)
	}
	claim.CreatedAt = claim.CreatedAt.UTC()
	if claim.Provenance.ReportedAt != nil {
		at := claim.Provenance.ReportedAt.UTC()
		claim.Provenance.ReportedAt = &at
	}
	if claim.ArchivedAt != nil {
		at := claim.ArchivedAt.UTC()
		claim.ArchivedAt = &at
	}
	return claim
}

func removeTaskClaimArchiveOwner(t *testing.T, f *fixture, name string) {
	t.Helper()
	s := f.session(name)
	if err := f.c.Delete(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s = f.session(name)
	s.Finalizers = nil
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestTaskClaimArchiveGetDefaultOffLiveAndOptionalUID(t *testing.T) {
	f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
	archived := claim.DeepCopy()
	archived.UID, archived.ResourceVersion = "11111111-1111-4111-8111-111111111111", "archived-revision"
	archive.result = archivedTaskClaimGetResult(t, archived, time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC))
	if w := f.do(http.MethodGet, path, reader, nil); w.Code != http.StatusOK || archive.calls != 0 || decode[apiv1.TaskClaim](t, w).UID != string(claim.UID) {
		t.Fatalf("default live = %d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
	if w := f.do(http.MethodGet, path+"&uid="+string(claim.UID), reader, nil); w.Code != http.StatusOK || archive.calls != 0 || decode[apiv1.TaskClaim](t, w).UID != string(claim.UID) {
		t.Fatalf("matching live UID = %d calls=%d", w.Code, archive.calls)
	}
	f.srv.CoordinationArchiveReads = false
	if w := f.do(http.MethodGet, path+"&uid="+string(archived.UID), reader, nil); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("defaultoff oldUID=%d calls=%d", w.Code, archive.calls)
	}
	f.srv.CoordinationArchiveReads = true
	w := f.do(http.MethodGet, path+"&uid="+string(archived.UID), reader, nil)
	if w.Code != http.StatusOK || archive.calls != 1 || archive.query.UID != string(archived.UID) || !decode[apiv1.TaskClaim](t, w).Archived || decode[apiv1.TaskClaim](t, w).UID != string(archived.UID) {
		t.Fatalf("other live UID did not select archive: %d calls=%d query=%#v", w.Code, archive.calls, archive.query)
	}
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads = false
	w = f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusNotFound || archive.calls != 1 {
		t.Fatalf("default off = %d calls=%d", w.Code, archive.calls)
	}
}

func TestTaskClaimArchiveGetRefusesInvalidInputBeforeArchive(t *testing.T) {
	f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"&uid=", "&uid=not-a-uid", "&uid=11111111-1111-4111-8111-111111111111&uid=22222222-2222-4222-8222-222222222222", "&project=sample", "&unexpected=x", ";uid=x"} {
		w := f.do(http.MethodGet, path+query, reader, nil)
		if w.Code != http.StatusBadRequest || archive.calls != 0 {
			t.Fatalf("invalid query %q = %d calls=%d %s", query, w.Code, archive.calls, w.Body.String())
		}
	}
	assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
}

func TestTaskClaimArchiveGetRefusalsAndCallerFence(t *testing.T) {
	for _, mode := range []string{"not-found", "plain", "corrupt", "wrong-uid", "foreign-project", "cancel-after", "scope-after", "caller-session-after", "caller-pod-after"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
			if err := f.c.Delete(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			want := http.StatusServiceUnavailable
			switch mode {
			case "not-found":
				archive.err, want = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing"), http.StatusNotFound
			case "plain":
				archive.err = errors.New("transport failed")
			case "corrupt":
				archive.result.Object = []byte(`{}`)
			case "wrong-uid":
				archive.result.Manifest.UID = "11111111-1111-4111-8111-111111111111"
			case "foreign-project":
				foreign := claim.DeepCopy()
				foreign.Spec.Identity.Project = "other"
				archive.result = archivedTaskClaimGetResult(t, foreign, archive.result.Manifest.ArchivedAt)
				// A TaskClaim name binds its project, so a foreign project cannot
				// be a coherent object for this route and must fail closed.
			case "cancel-after":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				archive.after = cancel
				req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+reader)
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, req)
				if w.Code != want || archive.calls != 1 {
					t.Fatalf("%s = %d calls=%d", mode, w.Code, archive.calls)
				}
				assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
				return
			case "scope-after":
				archive.after = func() { f.srv.TaskClaimReadProjects = nil }
				want = http.StatusForbidden
			case "caller-session-after":
				archive.after = func() {
					s := f.session("archive-reader")
					s.UID = "replacement-caller"
					if err := f.c.Update(context.Background(), s); err != nil {
						t.Fatal(err)
					}
				}
				want = http.StatusForbidden
			case "caller-pod-after":
				archive.after = func() { resumeInboxPod(t, f, reader) }
				want = http.StatusForbidden
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != want || archive.calls != 1 || strings.Contains(w.Body.String(), "PRIVATE_PROMPT_MUST_NOT_LEAK") {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

type taskClaimGetOnlyFake struct{ calls int }

func (f *taskClaimGetOnlyFake) Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	return protocol.CoordArchiveGetResult{}, errors.New("legacy Get must not be used")
}

func TestTaskClaimArchiveGetUnavailableAndForeignLiveNeverFallback(t *testing.T) {
	for _, mode := range []string{"get-only", "nil-reader", "nil-slots", "busy", "cancel-before", "foreign-live"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
			if err := f.c.Delete(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			want := http.StatusServiceUnavailable
			switch mode {
			case "get-only":
				f.srv.CoordinationArchive = &taskClaimGetOnlyFake{}
			case "nil-reader":
				f.srv.CoordinationArchive = nil
			case "nil-slots":
				f.srv.ShelfSlots = nil
			case "busy":
				release, ok := f.srv.ShelfSlots.TryTake()
				if !ok {
					t.Fatal("slot")
				}
				defer release()
				bounded, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
				ctx = bounded
			case "cancel-before":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "foreign-live":
				// Build a coherent foreign identity/name pair. A foreign object
				// reached through its own deterministic route is uniformly hidden.
				foreignSpec := claim.Spec
				foreignSpec.Identity.Project = "other"
				foreign, _, err := f.srv.TaskClaims.Create(context.Background(), foreignSpec)
				if err != nil {
					t.Fatal(err)
				}
				path = apiv1.TaskClaimsPath + "/" + foreign.Name + "?project=sample"
				want = http.StatusNotFound
			}
			req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+reader)
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, req)
			if w.Code != want || archive.calls != 0 {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			if legacy, ok := f.srv.CoordinationArchive.(*taskClaimGetOnlyFake); ok && legacy.calls != 0 {
				t.Fatalf("forbidden legacy Get fallback called %d times", legacy.calls)
			}
			if f.srv.ShelfSlots != nil && mode != "busy" {
				assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
			}
		})
	}
}

type taskClaimArchiveGetExecutor struct {
	body  []byte
	calls int
	name  string
}

func (e *taskClaimArchiveGetExecutor) Run(_ context.Context, ns, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if ns != sessionNS || pod != "archive-shelf" || container != shelf.Container || strings.Join(cmd, " ") != "agentd ctl coord-archive get-item --resource taskclaims --name "+e.name {
		return errors.New("unexpected typed TaskClaim item selector")
	}
	_, err := stdout.Write(e.body)
	return err
}

func TestTaskClaimArchiveGetUsesTypedItemShelfTransport(t *testing.T) {
	f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(archive.result)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "archive-shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	exec := &taskClaimArchiveGetExecutor{body: body, name: claim.Name}
	f.srv.CoordinationArchive = &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: f.c, Exec: exec, Namespace: sessionNS}}
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusOK || exec.calls != 1 || !decode[apiv1.TaskClaim](t, w).Archived {
		t.Fatalf("typed item shelf = %d calls=%d %s", w.Code, exec.calls, w.Body.String())
	}
	assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
}

func assertTaskClaimArchiveGetSlot(t *testing.T, slots *shelf.ExecSlots) {
	t.Helper()
	release, ok := slots.TryTake()
	if !ok {
		t.Fatal("archive slot leaked")
	}
	defer release()
	if extra, ok := slots.TryTake(); ok {
		extra()
		t.Fatal("archive slot capacity changed")
	}
}

func ownerPoisoningClient(backing client.WithWatch, namespace, name string, reads *int) client.Reader {
	return interceptor.NewClient(backing, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
		if key.Namespace == namespace && key.Name == name {
			*reads++
			return errors.New("archived TaskClaim must not join its owner Session")
		}
		return c.Get(ctx, key, object, opts...)
	}})
}

func TestTaskClaimArchiveGetMissingProjectKeepsACLStatus(t *testing.T) {
	f, _, reader, claim, archive, _ := taskClaimArchiveGetRig(t)
	path := apiv1.TaskClaimsPath + "/" + claim.Name
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusForbidden || archive.calls != 0 {
		t.Fatalf("missingproject changedACL=%d calls=%d %s", w.Code, archive.calls, w.Body.String())
	}
}

func assertTaskClaimGetNoWrites(t *testing.T, f *fixture) {
	t.Helper()
	attempts := 0
	fail := func() error { attempts++; return errors.New("TaskClaim GET attempted a write") }
	f.srv.TaskClaims.Client = interceptor.NewClient(f.c, interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return fail() },
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error { return fail() },
		Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return fail()
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error { return fail() },
		DeleteAllOf: func(context.Context, client.WithWatch, client.Object, ...client.DeleteAllOfOption) error {
			return fail()
		},
	})
	t.Cleanup(func() {
		if attempts != 0 {
			t.Errorf("TaskClaim GET attempted %d writes", attempts)
		}
	})
}

func TestTaskClaimArchiveGetFullObjectRefusals(t *testing.T) {
	modes := []string{"manifest-api", "manifest-resource", "manifest-project", "manifest-version", "digest", "canonical", "namespace", "identity-name", "owner", "actor", "provenance", "reported-head", "created", "deleted", "zero-receipt", "nonutc-receipt", "valid-wrong-selected-uid", "unexpected-text", "typed-invalid", "typed-ambiguous"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
			if err := f.c.Delete(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			saved := claim.DeepCopy()
			switch mode {
			case "namespace":
				saved.Namespace = "other"
			case "identity-name":
				saved.Spec.Identity.TaskKey = "issue:other"
			case "owner":
				saved.Spec.Owner.UID = ""
			case "actor":
				saved.Spec.Actor.Kind = "invalid"
			case "provenance":
				saved.Spec.Provenance.InitialPinState = "invalid"
			case "reported-head":
				saved.Spec.Provenance.ReportedHead = "bad"
			case "created":
				saved.CreationTimestamp = metav1.Time{}
			case "deleted":
				at := metav1.NewTime(time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC))
				saved.DeletionTimestamp = &at
			case "valid-wrong-selected-uid":
				saved.UID = "11111111-1111-4111-8111-111111111111"
				path += "&uid=" + string(claim.UID)
			}
			archive.result = archivedTaskClaimGetResult(t, saved, archive.result.Manifest.ArchivedAt)
			switch mode {
			case "manifest-api":
				archive.result.Manifest.APIVersion = "other/v1"
			case "manifest-resource":
				archive.result.Manifest.Resource = "inboxmessages"
			case "manifest-project":
				archive.result.Manifest.Project = "other"
			case "manifest-version":
				archive.result.Manifest.ResourceVersion = "other"
			case "digest":
				archive.result.Manifest.Object.SHA256 = strings.Repeat("0", 64)
			case "canonical":
				archive.result.Object = append([]byte("{ "), archive.result.Object[1:]...)
				if _, err := protocol.VerifyCoordArchiveObject(archive.result.Manifest.CoordArchiveManifest, bytes.NewReader(archive.result.Object)); err != nil {
					t.Fatalf("canonicalfixture lacksvaliddigest: %v", err)
				}
			case "zero-receipt":
				archive.result.Manifest.ArchivedAt = time.Time{}
			case "nonutc-receipt":
				archive.result.Manifest.ArchivedAt = archive.result.Manifest.ArchivedAt.In(time.FixedZone("fixture", 3600))
			case "unexpected-text":
				var raw map[string]any
				if err := json.Unmarshal(archive.result.Object, &raw); err != nil {
					t.Fatal(err)
				}
				raw["spec"].(map[string]any)["text"] = "PRIVATE_PROMPT_MUST_NOT_LEAK"
				object, err := protocol.CanonicalJSON(raw)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(object)
				archive.result.Object = object
				archive.result.Manifest.Object = protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(object))}
				if _, err := protocol.VerifyCoordArchiveObject(archive.result.Manifest.CoordArchiveManifest, bytes.NewReader(object)); err != nil {
					t.Fatal(err)
				}
			case "typed-invalid":
				archive.err = protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "refused")
			case "typed-ambiguous":
				archive.err = protocol.HistoryErrorf(protocol.HistoryErrAmbiguous, "", "refused")
			}
			assertTaskClaimGetNoWrites(t, f)
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != http.StatusServiceUnavailable || archive.calls != 1 || archive.legacyCalls != 0 || strings.Contains(w.Body.String(), "PRIVATE_PROMPT_MUST_NOT_LEAK") {
				t.Fatalf("%s=%d item=%d legacy=%d %s", mode, w.Code, archive.calls, archive.legacyCalls, w.Body.String())
			}
			assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func TestTaskClaimArchiveGetScopesOwnersAndFirstReceipt(t *testing.T) {
	for _, mode := range []string{"absent-owner", "coordinator", "unscoped-coordinator", "foreign-archive", "first-vs-revision", "caller-deleted", "pod-deleted"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
			if err := f.c.Delete(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			want, calls := http.StatusOK, 1
			ownerReads := 0
			switch mode {
			case "absent-owner":
				removeTaskClaimArchiveOwner(t, f, claim.Spec.Owner.Name)
			case "coordinator":
				reader = tokCoordinator
			case "unscoped-coordinator":
				reader = tokCoordinator
				f.srv.TaskClaimReadProjects = []string{"other"}
				want, calls = http.StatusForbidden, 0
			case "foreign-archive":
				foreign := claim.DeepCopy()
				foreign.Spec.Identity.Project = "other"
				foreign.Name = taskclaim.Name(foreign.Spec.Identity)
				archive.result = archivedTaskClaimGetResult(t, foreign, archive.result.Manifest.ArchivedAt)
				path = apiv1.TaskClaimsPath + "/" + foreign.Name + "?project=sample"
				want = http.StatusNotFound
			case "first-vs-revision":
				legacy := archive.result
				legacy.Manifest.ArchivedAt = legacy.Manifest.ArchivedAt.Add(2 * time.Hour)
				archive.legacyResult = &legacy
			case "caller-deleted":
				archive.after = func() { removeTaskClaimArchiveOwner(t, f, "archive-reader") }
				want = http.StatusForbidden
			case "pod-deleted":
				archive.after = func() {
					id := f.auth[reader]
					pod := new(corev1.Pod)
					if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: id.PodName}, pod); err != nil {
						t.Fatal(err)
					}
					if err := f.c.Delete(context.Background(), pod); err != nil {
						t.Fatal(err)
					}
				}
				want = http.StatusForbidden
			}
			f.srv.Live = ownerPoisoningClient(f.c, claim.Spec.Owner.Namespace, claim.Spec.Owner.Name, &ownerReads)
			assertTaskClaimGetNoWrites(t, f)
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != want || archive.calls != calls || archive.legacyCalls != 0 || ownerReads != 0 || strings.Contains(w.Body.String(), "PRIVATE_PROMPT_MUST_NOT_LEAK") {
				t.Fatalf("%s=%d item=%d legacy=%d owner=%d %s", mode, w.Code, archive.calls, archive.legacyCalls, ownerReads, w.Body.String())
			}
			if want == http.StatusOK {
				got := normalizeTaskClaimUTC(t, decode[apiv1.TaskClaim](t, w))
				expected := expectedArchivedTaskClaim(t, claim, archive.result.Manifest.ArchivedAt)
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("%s fullsavedclaim changed: got=%#v want=%#v", mode, got, expected)
				}
			}
			assertTaskClaimArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func TestTaskClaimArchiveGetLiveErrorNeverFallsBack(t *testing.T) {
	f, _, reader, claim, archive, path := taskClaimArchiveGetRig(t)
	f.srv.TaskClaims.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if key.Name == claim.Name {
			return errors.New("PRIVATE_PROMPT_MUST_NOT_LEAK")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != http.StatusServiceUnavailable || archive.calls != 0 || archive.legacyCalls != 0 || strings.Contains(w.Body.String(), "PRIVATE_PROMPT_MUST_NOT_LEAK") {
		t.Fatalf("liveerror=%d archive=%d legacy=%d %s", w.Code, archive.calls, archive.legacyCalls, w.Body.String())
	}
}
