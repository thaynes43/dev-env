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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskclaim"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type inboxArchiveGetFake struct {
	result protocol.CoordArchiveGetResult
	err    error
	calls  int
	after  func()
}

func (f *inboxArchiveGetFake) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	if f.after != nil {
		f.after()
	}
	if q.Resource != "inboxmessages" || q.Name == "" || q.UID != "" || q.ResourceVersion != "" || q.SHA256 != "" {
		return protocol.CoordArchiveGetResult{}, errors.New("unexpected inbox archive query")
	}
	return f.result, f.err
}

func archivedInboxGetResult(t *testing.T, m *coord.InboxMessage) protocol.CoordArchiveGetResult {
	t.Helper()
	m.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"}
	raw, err := protocol.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "inboxmessages", Kind: "InboxMessage", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: m.Namespace, Name: m.Name, UID: string(m.UID), ResourceVersion: m.ResourceVersion, Project: m.Spec.Project, RecipientUID: m.Spec.Recipient.UID, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	if _, err := protocol.VerifyCoordArchiveObject(manifest, bytes.NewReader(raw)); err != nil {
		t.Fatalf("fixture full object invalid: %v", err)
	}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)}, Object: raw}
}

func inboxArchiveGetRig(t *testing.T) (*fixture, string, string, *coord.InboxMessage, *inboxArchiveGetFake, string) {
	t.Helper()
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender, recipient := claimSession(t, f, "archive-sender", humanSA), claimSession(t, f, "archive-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-get"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	m, err := f.srv.Inbox.Get(context.Background(), decode[apiv1.InboxResult](t, w).Message.Name)
	if err != nil {
		t.Fatal(err)
	}
	a := &inboxArchiveGetFake{result: archivedInboxGetResult(t, m)}
	return f, sender, recipient, m, a, apiv1.InboxPath + "/" + m.Name + "?project=sample"
}

func TestInboxArchiveGetRedAcceptedBaseAndLiveWins(t *testing.T) {
	f, _, recipient, m, archive, path := inboxArchiveGetRig(t)
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	// This is the accepted-base counterexample: before the GET fallback is wired,
	// deleting the live object leaves a 404 despite this verified archive object.
	w := f.do(http.MethodGet, path, recipient, nil)
	if w.Code != http.StatusOK || archive.calls != 0 || decode[apiv1.InboxMessage](t, w).Text != m.Spec.Text {
		t.Fatalf("live message must win: status=%d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	w = f.do(http.MethodGet, path, recipient, nil)
	got := decode[apiv1.InboxMessage](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(archive.result.Manifest.ArchivedAt) || got.Text != m.Spec.Text {
		t.Fatalf("archive fallback = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
}

func TestInboxArchiveGetDefaultOffAclAndImmutableProjection(t *testing.T) {
	f, _, recipient, m, archive, path := inboxArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodGet, path, recipient, nil)
	if w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("default off = %d calls=%d", w.Code, archive.calls)
	}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	f.srv.InboxCoordinatorReadProjects = []string{"sample"}
	for _, token := range []string{tokCoordinator, recipient} {
		w = f.do(http.MethodGet, path, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("authorized archived read = %d %s", w.Code, w.Body.String())
		}
	}
	got := decode[apiv1.InboxMessage](t, w)
	want := expectedInboxArchiveMetadata(m)
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &want); err != nil {
		t.Fatal(err)
	}
	got.Archived, got.ArchivedAt = false, nil
	if !reflect.DeepEqual(got.InboxMetadata, want) || got.Text != m.Spec.Text {
		t.Fatalf("immutable projection differs: got=%#v want=%#v", got, want)
	}
	f.srv.InboxCoordinatorReadProjects = nil
	before := archive.calls
	if w = f.do(http.MethodGet, path, tokCoordinator, nil); w.Code != http.StatusNotFound || archive.calls != before {
		t.Fatalf("unscoped coordinator = %d calls=%d", w.Code, archive.calls)
	}
}

func TestInboxArchiveGetRefusesOnlyAfterGenuineLiveNotFound(t *testing.T) {
	for _, mode := range []string{"live-error", "archive-missing", "corrupt", "wrong-recipient", "busy"} {
		t.Run(mode, func(t *testing.T) {
			f, _, recipient, m, archive, path := inboxArchiveGetRig(t)
			if mode != "live-error" {
				if err := f.c.Delete(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
			want, calls := http.StatusServiceUnavailable, 1
			switch mode {
			case "live-error":
				f.srv.Inbox.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errors.New("live unavailable")
				}})
				want, calls = http.StatusServiceUnavailable, 0
			case "archive-missing":
				archive.err = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing")
				want = http.StatusNotFound
			case "corrupt":
				archive.result.Object = []byte(`{}`)
			case "wrong-recipient":
				m.Spec.Recipient.UID = "11111111-1111-4111-8111-111111111111"
				archive.result = archivedInboxGetResult(t, m)
				want = http.StatusNotFound
			case "busy":
				release, ok := f.srv.ShelfSlots.TryTake()
				if !ok {
					t.Fatal("slot")
				}
				defer release()
				want, calls = http.StatusServiceUnavailable, 0
			}
			w := f.do(http.MethodGet, path, recipient, nil)
			if w.Code != want || archive.calls != calls || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s = %d calls=%d body=%s", mode, w.Code, archive.calls, w.Body.String())
			}
		})
	}
}

func TestInboxArchiveGetDoesNotEnableAcknowledgementFallback(t *testing.T) {
	f, sender, recipient, m, archive, path := inboxArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	ackPath := strings.Split(path, "?")[0] + "/ack?project=sample"
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("ack GET = %d calls=%d", w.Code, archive.calls)
	}
	if w := f.do(http.MethodPost, ackPath, recipient, strings.NewReader(`{}`)); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("ack POST = %d calls=%d", w.Code, archive.calls)
	}
	if w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-get-next")); w.Code != http.StatusCreated || archive.calls != 0 {
		t.Fatalf("publish = %d calls=%d", w.Code, archive.calls)
	}
}

// Construct the wire oracle independently of the production projection.
func expectedInboxArchiveMetadata(m *coord.InboxMessage) apiv1.InboxMetadata {
	p := m.Spec.Provenance
	out := apiv1.InboxMetadata{Name: m.Name, UID: string(m.UID), Project: m.Spec.Project, Key: m.Spec.Key,
		Sender:     apiv1.TaskClaimOwner{Namespace: m.Spec.Sender.Namespace, Name: m.Spec.Sender.Name, UID: m.Spec.Sender.UID},
		Recipient:  apiv1.TaskClaimOwner{Namespace: m.Spec.Recipient.Namespace, Name: m.Spec.Recipient.Name, UID: m.Spec.Recipient.UID},
		Actor:      apiv1.TaskClaimActor{Kind: m.Spec.Actor.Kind, ServiceAccount: m.Spec.Actor.ServiceAccount, PodName: m.Spec.Actor.PodName, PodUID: m.Spec.Actor.PodUID},
		Provenance: apiv1.TaskClaimProvenance{CatalogRevision: p.CatalogRevision, RulesRevision: p.RulesRevision, GitHub: p.GitHub, DefaultBranch: p.DefaultBranch, InitialPinState: p.InitialPinState, ReportedHead: p.ReportedHead},
		Repository: m.Spec.Repository, Provider: m.Spec.Provider, RequestedModel: m.Spec.RequestedModel, CreatedAt: m.CreationTimestamp.Time}
	if p.ReportedAt != nil {
		at := p.ReportedAt.Time
		out.Provenance.ReportedAt = &at
	}
	return out
}

func TestInboxArchiveGetFullObjectRefusals(t *testing.T) {
	for _, mode := range []string{"namespace", "recipient-manifest", "uid-manifest", "revision-manifest", "name-manifest", "resource", "api-version", "kind", "zero-receipt", "nonutc-receipt", "noncanonical", "digest", "size", "empty-text", "invalid-model", "invalid-actor", "zero-created", "deleted", "deterministic-name", "foreign-project", "recipient-namespace", "recipient-name"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, m, a, path := inboxArchiveGetRig(t)
			if err := f.c.Delete(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			f.srv.CoordinationArchiveReads = true
			f.srv.CoordinationArchive = a
			f.srv.ShelfSlots = shelf.NewExecSlots(1)
			want := 503
			switch mode {
			case "namespace":
				m.Namespace = "other"
				a.result = archivedInboxGetResult(t, m)
			case "recipient-manifest":
				a.result.Manifest.RecipientUID = "11111111-1111-4111-8111-111111111111"
			case "uid-manifest":
				a.result.Manifest.UID = "11111111-1111-4111-8111-111111111111"
			case "revision-manifest":
				a.result.Manifest.ResourceVersion = "different-revision"
			case "name-manifest":
				a.result.Manifest.Name = inbox.MessageName(m.Spec.Sender, "different-key")
			case "resource":
				a.result.Manifest.Resource = "publishedcontexts"
			case "api-version":
				a.result.Manifest.APIVersion = "different/v1"
			case "kind":
				a.result.Manifest.Kind = "PublishedContext"
			case "zero-receipt":
				a.result.Manifest.ArchivedAt = time.Time{}
			case "nonutc-receipt":
				a.result.Manifest.ArchivedAt = a.result.Manifest.ArchivedAt.In(time.FixedZone("other", 3600))
			case "noncanonical":
				a.result.Object = append([]byte(" "), a.result.Object...)
			case "digest":
				a.result.Manifest.Object.SHA256 = strings.Repeat("0", 64)
			case "size":
				a.result.Manifest.Object.Bytes++
			case "empty-text":
				m.Spec.Text = ""
				a.result = archivedInboxGetResult(t, m)
			case "invalid-model":
				m.Spec.RequestedModel = ""
				a.result = archivedInboxGetResult(t, m)
			case "invalid-actor":
				m.Spec.Actor.Kind = "coordinator"
				a.result = archivedInboxGetResult(t, m)
			case "zero-created":
				m.CreationTimestamp = metav1.Time{}
				a.result = archivedInboxGetResult(t, m)
			case "deleted":
				stamp := metav1.Now()
				m.DeletionTimestamp = &stamp
				a.result = archivedInboxGetResult(t, m)
			case "deterministic-name":
				m.Name = inbox.MessageName(m.Spec.Sender, "different-key")
				a.result = archivedInboxGetResult(t, m)
			case "recipient-namespace":
				m.Spec.Recipient.Namespace = "other"
				a.result = archivedInboxGetResult(t, m)
				want = 404
			case "recipient-name":
				m.Spec.Recipient.Name = "other-recipient"
				a.result = archivedInboxGetResult(t, m)
				want = 404
			case "foreign-project":
				m.Spec.Project = "other"
				a.result = archivedInboxGetResult(t, m)
				want = 404
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != want || a.calls != 1 || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, a.calls, w.Body.String())
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func TestInboxArchiveGetCallerFenceAndAbsentSender(t *testing.T) {
	for _, mode := range []string{"absent-sender", "deleting-original", "replaced-before", "replaced-after", "pod-after", "foreign-live"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, m, a, path := inboxArchiveGetRig(t)
			f.srv.CoordinationArchiveReads = true
			f.srv.CoordinationArchive = a
			f.srv.ShelfSlots = shelf.NewExecSlots(1)
			want, calls := 200, 1
			if mode != "foreign-live" {
				if err := f.c.Delete(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			replace := func() {
				session := f.session("archive-recipient")
				session.UID = "replacement-session"
				if err := f.c.Update(context.Background(), session); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "absent-sender":
				removeInboxSession(t, f, "archive-sender")
				stamp := metav1.NewTime(time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC))
				m.Spec.Provenance.ReportedAt = &stamp
				m.Spec.Provenance.ReportedHead = strings.Repeat("b", 40)
				a.result = archivedInboxGetResult(t, m)
			case "deleting-original":
				session := f.session("archive-recipient")
				session.Finalizers = []string{"fixture-retain"}
				if err := f.c.Update(context.Background(), session); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), session); err != nil {
					t.Fatal(err)
				}
			case "replaced-before":
				replace()
				want, calls = 404, 0
			case "replaced-after":
				a.after = replace
				want = 404
			case "pod-after":
				a.after = func() { resumeInboxPod(t, f, reader) }
				want = 404
			case "foreign-live":
				m.Spec.Project = "other"
				m.Labels[taskclaim.ProjectLabel] = "other"
				if err := f.c.Update(context.Background(), m); err != nil {
					t.Fatal(err)
				}
				want, calls = 404, 0
			}
			w := f.do(http.MethodGet, path, reader, nil)
			if w.Code != want || a.calls != calls {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, a.calls, w.Body.String())
			}
			if want == 200 {
				got := decode[apiv1.InboxMessage](t, w)
				expected := expectedInboxArchiveMetadata(m)
				wire, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(wire, &expected); err != nil {
					t.Fatal(err)
				}
				if got.ArchivedAt == nil || !got.ArchivedAt.Equal(a.result.Manifest.ArchivedAt) || !got.Archived {
					t.Fatal("missing receipt")
				}
				got.Archived = false
				got.ArchivedAt = nil
				if !reflect.DeepEqual(got.InboxMetadata, expected) || got.Text != m.Spec.Text {
					t.Fatalf("attribution changed: %#v %#v", got, expected)
				}
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func TestInboxArchiveGetUnavailableCancellationAndErrorMapping(t *testing.T) {
	for _, mode := range []string{"nil-reader", "nil-slots", "busy", "cancel-before", "cancel-after", "no-archive", "corrupt", "ambiguous", "other", "plain-error", "not-found"} {
		t.Run(mode, func(t *testing.T) {
			f, _, reader, m, a, path := inboxArchiveGetRig(t)
			if err := f.c.Delete(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			f.srv.CoordinationArchiveReads = true
			f.srv.CoordinationArchive = a
			f.srv.ShelfSlots = shelf.NewExecSlots(1)
			ctx := context.Background()
			want, calls := 503, 1
			switch mode {
			case "nil-reader":
				f.srv.CoordinationArchive = nil
				calls = 0
			case "nil-slots":
				f.srv.ShelfSlots = nil
				calls = 0
			case "busy":
				release, ok := f.srv.ShelfSlots.TryTake()
				if !ok {
					t.Fatal("slot")
				}
				defer release()
				bounded, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
				ctx = bounded
				calls = 0
			case "cancel-before":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				calls = 0
			case "cancel-after":
				cancelled, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = cancelled
				a.after = cancel
			case "plain-error":
				a.err = errors.New("transport failure")
			case "not-found":
				a.err = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing")
				want = 404
			default:
				a.err = protocol.HistoryErrorf(mode, "", "refusal")
			}
			req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+reader)
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, req)
			if w.Code != want || a.calls != calls || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s = %d calls=%d %s", mode, w.Code, a.calls, w.Body.String())
			}
			if f.srv.ShelfSlots != nil && mode != "busy" {
				assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
			}
		})
	}
}

type inboxArchiveGetExecutor struct {
	body  []byte
	calls int
	name  string
}

func (e *inboxArchiveGetExecutor) Run(_ context.Context, ns, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if ns != sessionNS || pod != "archive-shelf" || container != shelf.Container || strings.Join(cmd, " ") != "agentd ctl coord-archive get --resource inboxmessages --name "+e.name {
		return errors.New("unexpected typed inbox Get selector")
	}
	_, err := stdout.Write(e.body)
	return err
}
func TestInboxArchiveGetTypedShelfProjection(t *testing.T) {
	f, _, reader, m, a, path := inboxArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(a.result)
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "archive-shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	if err = f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	exec := &inboxArchiveGetExecutor{body: body, name: m.Name}
	f.srv.CoordinationArchiveReads = true
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	f.srv.CoordinationArchive = &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: f.c, Exec: exec, Namespace: sessionNS}}
	w := f.do(http.MethodGet, path, reader, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := decode[apiv1.InboxMessage](t, w)
	if exec.calls != 1 || a.calls != 0 || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(a.result.Manifest.ArchivedAt) || got.Text != m.Spec.Text {
		t.Fatalf("typed: %#v calls=%d", got, exec.calls)
	}
	assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
}

// These fixtures configure exactly one slot; verify that one is returned.
func assertInboxArchiveGetSlot(t *testing.T, slots *shelf.ExecSlots) {
	t.Helper()
	release, ok := slots.TryTake()
	if !ok {
		t.Fatal("configured archive slot leaked")
	}
	defer release()
	extra, ok := slots.TryTake()
	if ok {
		extra()
		t.Fatal("one-slot fixture gained capacity")
	}
}

func TestInboxArchiveGetInvalidNameAndProjectNeverReachShelf(t *testing.T) {
	f, _, reader, m, a, path := inboxArchiveGetRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchiveReads = true
	f.srv.CoordinationArchive = a
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	for _, target := range []string{apiv1.InboxPath + "/invalid-message?project=sample", strings.Replace(path, "project=sample", "project=other", 1)} {
		w := f.do(http.MethodGet, target, reader, nil)
		if w.Code != 404 || a.calls != 0 {
			t.Fatalf("invalid scope/name = %d calls=%d %s", w.Code, a.calls, w.Body.String())
		}
	}
	assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
}
