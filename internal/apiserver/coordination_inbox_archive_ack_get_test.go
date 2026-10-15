package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type inboxArchiveAckGetFake struct {
	result protocol.CoordArchiveGetResult
	err    error
	calls  int
	after  func()
}

func (f *inboxArchiveAckGetFake) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	f.calls++
	if f.after != nil {
		f.after()
	}
	if q.Resource != "inboxreadacks" || q.Name == "" || q.UID != "" || q.ResourceVersion != "" || q.SHA256 != "" {
		return protocol.CoordArchiveGetResult{}, errors.New("unexpected inbox ack archive query")
	}
	return f.result, f.err
}

type inboxArchiveAckRouter struct {
	message *inboxArchiveGetFake
	ack     *inboxArchiveAckGetFake
}

func (r inboxArchiveAckRouter) Get(ctx context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	if q.Resource == "inboxmessages" {
		return r.message.Get(ctx, q)
	}
	return r.ack.Get(ctx, q)
}

func archivedInboxAckGetResult(t *testing.T, a *coord.InboxReadAck) protocol.CoordArchiveGetResult {
	t.Helper()
	a.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxReadAck"}
	raw, err := protocol.CanonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "inboxreadacks", Kind: "InboxReadAck", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: a.Namespace, Name: a.Name, UID: string(a.UID), ResourceVersion: a.ResourceVersion, Project: a.Spec.Project, RecipientUID: a.Spec.Recipient.UID, MessageUID: a.Spec.MessageUID, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}, ArchivedBy: "fixture"}
	if _, err := protocol.VerifyCoordArchiveObject(manifest, bytes.NewReader(raw)); err != nil {
		t.Fatalf("fixture full ack invalid: %v", err)
	}
	return protocol.CoordArchiveGetResult{Manifest: protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)}, Object: raw}
}

// This compiles on the accepted base because it uses raw HTTP JSON rather than
// the additive archive fields. Before the production fallback is wired it is a
// real RED: the live read-ack NotFound returns 404 despite this valid archive.
func TestInboxArchiveAckGetRedAcceptedBase(t *testing.T) {
	f, _, recipient, m, _, messagePath := inboxArchiveGetRig(t)
	ackPath := messagePath[:len(messagePath)-len("?project=sample")] + "/ack?project=sample"
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	w := f.do(http.MethodGet, ackPath, recipient, nil)
	if w.Code != http.StatusOK || archive.calls != 1 {
		t.Fatalf("accepted-base archive ack fallback = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != inbox.AckName(string(m.UID), m.Spec.Recipient.UID) || got["archived"] != true || got["archivedAt"] != archive.result.Manifest.ArchivedAt.Format(time.RFC3339) {
		t.Fatalf("archived wire receipt = %#v", got)
	}
}

func TestInboxArchiveAckGetDefaultOffLiveWinsAndPostStaysLiveOnly(t *testing.T) {
	f, _, recipient, m, _, messagePath := inboxArchiveGetRig(t)
	ackPath := messagePath[:len(messagePath)-len("?project=sample")] + "/ack?project=sample"
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchive, f.srv.ShelfSlots = archive, shelf.NewExecSlots(1)
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("default off = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	f.srv.CoordinationArchiveReads = true
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusOK || archive.calls != 0 {
		t.Fatalf("live ack must win = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	} else {
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if _, archived := got["archived"]; archived {
			t.Fatalf("live ack marked archived: %#v", got)
		}
	}
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusNotFound || archive.calls != 0 {
		t.Fatalf("POST must remain live-only = %d calls=%d body=%s", w.Code, archive.calls, w.Body.String())
	}
}

func TestInboxArchiveAckGetArchivedMessageLiveAndArchivedAck(t *testing.T) {
	f, _, recipient, m, messageArchive, messagePath := inboxArchiveGetRig(t)
	ackPath := strings.TrimSuffix(messagePath, "?project=sample") + "/ack?project=sample"
	if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	ackArchive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, inboxArchiveAckRouter{message: messageArchive, ack: ackArchive}, shelf.NewExecSlots(1)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusOK || messageArchive.calls != 1 || ackArchive.calls != 0 {
		t.Fatalf("archived message + live ack = %d message=%d ack=%d %s", w.Code, messageArchive.calls, ackArchive.calls, w.Body.String())
	} else {
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if _, marked := got["archived"]; marked {
			t.Fatalf("live ack marked archived: %#v", got)
		}
	}
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, ackPath, recipient, nil); w.Code != http.StatusOK || messageArchive.calls != 2 || ackArchive.calls != 1 {
		t.Fatalf("archived message + archived ack = %d message=%d ack=%d %s", w.Code, messageArchive.calls, ackArchive.calls, w.Body.String())
	} else {
		got := decode[apiv1.InboxReadAck](t, w)
		want := apiv1.InboxReadAck{Name: a.Name, UID: string(a.UID), Project: a.Spec.Project, MessageName: a.Spec.MessageName, MessageUID: a.Spec.MessageUID, Recipient: apiv1.TaskClaimOwner{Namespace: a.Spec.Recipient.Namespace, Name: a.Spec.Recipient.Name, UID: a.Spec.Recipient.UID}, Actor: apiv1.TaskClaimActor{Kind: a.Spec.Actor.Kind, ServiceAccount: a.Spec.Actor.ServiceAccount, PodName: a.Spec.Actor.PodName, PodUID: a.Spec.Actor.PodUID}, CreatedAt: a.CreationTimestamp.Time}
		if !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(ackArchive.result.Manifest.ArchivedAt) || !reflect.DeepEqual(func() apiv1.InboxReadAck { got.Archived, got.ArchivedAt = false, nil; return got }(), want) {
			t.Fatalf("immutable archived ack projection got=%#v want=%#v", got, want)
		}
	}
}

func TestInboxArchiveAckGetEnvelopeAndAvailabilityRefusals(t *testing.T) {
	for _, mode := range []string{"not-found", "typed-error", "namespace", "name", "uid", "message", "project", "digest", "noncanonical", "zero-receipt", "nil-reader", "nil-slots", "busy", "cancel-before", "cancel-after"} {
		t.Run(mode, func(t *testing.T) {
			f, _, recipient, m, _, messagePath := inboxArchiveGetRig(t)
			ackPath := strings.TrimSuffix(messagePath, "?project=sample") + "/ack?project=sample"
			if w := f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			a, err := f.srv.Inbox.ReadAck(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.c.Delete(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			archive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
			f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
			want, calls := http.StatusServiceUnavailable, 1
			switch mode {
			case "not-found":
				archive.err, want = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing"), http.StatusNotFound
			case "typed-error":
				archive.err = protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "corrupt")
			case "namespace":
				archive.result.Manifest.Namespace = "other"
			case "name":
				archive.result.Manifest.Name = inbox.AckName("11111111-1111-4111-8111-111111111111", a.Spec.Recipient.UID)
			case "uid":
				archive.result.Manifest.UID = "11111111-1111-4111-8111-111111111111"
			case "message":
				archive.result.Manifest.MessageUID = "11111111-1111-4111-8111-111111111111"
			case "project":
				archive.result.Manifest.Project = "other"
			case "digest":
				archive.result.Manifest.Object.SHA256 = strings.Repeat("0", 64)
			case "noncanonical":
				archive.result.Object = append([]byte(" "), archive.result.Object...)
			case "zero-receipt":
				archive.result.Manifest.ArchivedAt = time.Time{}
			case "nil-reader":
				f.srv.CoordinationArchive, calls = nil, 0
			case "nil-slots":
				f.srv.ShelfSlots, calls = nil, 0
			case "busy":
				release, ok := f.srv.ShelfSlots.TryTake()
				if !ok {
					t.Fatal("slot")
				}
				defer release()
				calls = 0
			case "cancel-before":
				// The request context itself proves cancellation before slot acquisition.
				req := httptest.NewRequest(http.MethodGet, ackPath, nil)
				req.Header.Set("Authorization", "Bearer "+recipient)
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, req)
				if w.Code != http.StatusServiceUnavailable || archive.calls != 0 {
					t.Fatalf("cancel-before = %d calls=%d", w.Code, archive.calls)
				}
				return
			case "cancel-after":
				req := httptest.NewRequest(http.MethodGet, ackPath, nil)
				req.Header.Set("Authorization", "Bearer "+recipient)
				ctx, cancel := context.WithCancel(req.Context())
				archive.after = cancel
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, req.WithContext(ctx))
				if w.Code != http.StatusServiceUnavailable || archive.calls != 1 {
					t.Fatalf("cancel-after = %d calls=%d", w.Code, archive.calls)
				}
				assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
				return
			}
			w := f.do(http.MethodGet, ackPath, recipient, nil)
			if w.Code != want || archive.calls != calls {
				t.Fatalf("%s = %d calls=%d body=%s", mode, w.Code, archive.calls, w.Body.String())
			}
			if f.srv.ShelfSlots != nil && mode != "busy" {
				assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
			}
		})
	}
}

func archivedAckRig(t *testing.T) (*fixture, string, *coord.InboxMessage, *coord.InboxReadAck, *inboxArchiveAckGetFake, string) {
	t.Helper()
	f, _, recipient, m, _, messagePath := inboxArchiveGetRig(t)
	path := strings.TrimSuffix(messagePath, "?project=sample") + "/ack?project=sample"
	if w := f.do(http.MethodPost, path, recipient, strings.NewReader(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(1)
	return f, recipient, m, a, archive, path
}

// These semantic mutations retain a valid canonical digest and matching
// manifest. They exercise message/recipient binding beyond protocol integrity.
func TestInboxArchiveAckGetValidEnvelopeSemanticRefusals(t *testing.T) {
	for _, mode := range []string{"recipient-namespace", "recipient-name", "recipient-uid", "message-name", "message-uid", "project", "namespace", "invalid-actor", "zero-created", "deleted", "nonutc-receipt"} {
		t.Run(mode, func(t *testing.T) {
			f, token, _, a, archive, path := archivedAckRig(t)
			switch mode {
			case "recipient-namespace":
				a.Spec.Recipient.Namespace = "other"
			case "recipient-name":
				a.Spec.Recipient.Name = "other"
			case "recipient-uid":
				a.Spec.Recipient.UID = "11111111-1111-4111-8111-111111111111"
			case "message-name":
				a.Spec.MessageName = inbox.MessageName(coord.SessionIdentity{Namespace: "other", Name: "other", UID: "11111111-1111-4111-8111-111111111111"}, "other")
			case "message-uid":
				a.Spec.MessageUID = "11111111-1111-4111-8111-111111111111"
			case "project":
				a.Spec.Project = "other"
			case "namespace":
				a.Namespace = "other"
			case "invalid-actor":
				a.Spec.Actor.Kind = "coordinator"
			case "zero-created":
				a.CreationTimestamp = metav1.Time{}
			case "deleted":
				stamp := metav1.NewTime(time.Now().UTC())
				a.DeletionTimestamp = &stamp
			}
			archive.result = archivedInboxAckGetResult(t, a)
			if mode == "nonutc-receipt" {
				archive.result.Manifest.ArchivedAt = archive.result.Manifest.ArchivedAt.In(time.FixedZone("other", 3600))
			}
			w := f.do(http.MethodGet, path, token, nil)
			if w.Code != http.StatusServiceUnavailable || archive.calls != 1 || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s=%d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func TestInboxArchiveAckGetCallerFenceAndStoredActor(t *testing.T) {
	for _, mode := range []string{"coordinator", "unscoped-coordinator", "absent-sender", "deleting-recipient", "absent-recipient", "replaced-before", "replaced-after", "pod-after", "foreign-live"} {
		t.Run(mode, func(t *testing.T) {
			f, token, m, a, archive, path := archivedAckRig(t)
			want, calls := http.StatusOK, 1
			replace := func() {
				sess := f.session("archive-recipient")
				sess.UID = "replacement-session"
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "coordinator":
				token = tokCoordinator
				f.srv.InboxCoordinatorReadProjects = []string{"sample"}
			case "unscoped-coordinator":
				token = tokCoordinator
				want, calls = http.StatusNotFound, 0
			case "absent-sender":
				removeInboxSession(t, f, "archive-sender")
			case "deleting-recipient":
				sess := f.session("archive-recipient")
				sess.Finalizers = []string{"fixture-retain"}
				if err := f.c.Update(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), sess); err != nil {
					t.Fatal(err)
				}
			case "absent-recipient":
				removeInboxSession(t, f, "archive-recipient")
				want, calls = http.StatusNotFound, 0
			case "replaced-before":
				replace()
				want, calls = http.StatusNotFound, 0
			case "replaced-after":
				archive.after = replace
				want = http.StatusNotFound
			case "pod-after":
				archive.after = func() { resumeInboxPod(t, f, token) }
				want = http.StatusNotFound
			case "foreign-live":
				m.Spec.Project = "other"
				m.Labels["coordination.dev-env.haynesops.com/project"] = "other"
				if err := f.c.Update(context.Background(), m); err != nil {
					t.Fatal(err)
				}
				want, calls = http.StatusNotFound, 0
			}
			// A resumed Pod is never substituted for immutable acknowledgement actor.
			if mode == "coordinator" || mode == "absent-sender" {
				a.Spec.Actor.PodName = "original-ack-pod"
				a.Spec.Actor.PodUID = "11111111-1111-4111-8111-111111111111"
				archive.result = archivedInboxAckGetResult(t, a)
			}
			w := f.do(http.MethodGet, path, token, nil)
			if w.Code != want || archive.calls != calls {
				t.Fatalf("%s=%d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			if want == http.StatusOK {
				got := decode[apiv1.InboxReadAck](t, w)
				expected := apiv1.InboxReadAck{Name: a.Name, UID: string(a.UID), Project: a.Spec.Project, MessageName: a.Spec.MessageName, MessageUID: a.Spec.MessageUID, Recipient: apiv1.TaskClaimOwner{Namespace: a.Spec.Recipient.Namespace, Name: a.Spec.Recipient.Name, UID: a.Spec.Recipient.UID}, Actor: apiv1.TaskClaimActor{Kind: a.Spec.Actor.Kind, ServiceAccount: a.Spec.Actor.ServiceAccount, PodName: a.Spec.Actor.PodName, PodUID: a.Spec.Actor.PodUID}, CreatedAt: a.CreationTimestamp.Time}
				wire, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(wire, &expected); err != nil {
					t.Fatal(err)
				}
				if !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(archive.result.Manifest.ArchivedAt) {
					t.Fatal("missing receipt")
				}
				got.Archived, got.ArchivedAt = false, nil
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("stored attribution differs: got=%#v want=%#v", got, expected)
				}
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}
