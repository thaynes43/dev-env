package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/inbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// This uses the accepted live HTTP surface and verified archive envelopes. The
// initial POST proves the live create control; its retry after release is the
// accepted-base RED for the archived retry contract. The poisoned Create is
// installed only after fixture setup, so every archived outcome proves it made
// no write attempt while the ordinary live control remains independently valid.
func TestInboxArchiveAckRetryConfirmedNoWrites(t *testing.T) {
	f, _, recipient, m, messageArchive, messagePath := inboxArchiveGetRig(t)
	path := strings.TrimSuffix(messagePath, "?project=sample") + "/ack?project=sample"
	if w := f.do(http.MethodPost, path, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	a, err := f.srv.Inbox.ReadAck(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	ackArchive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, inboxArchiveAckRouter{message: messageArchive, ack: ackArchive}, shelf.NewExecSlots(1)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	resumeInboxPod(t, f, recipient)
	if a.Spec.Actor.PodUID == f.auth[recipient].PodUID {
		t.Fatal("fixture did not create a resumed recipient executor")
	}
	assertRetryNoWrites(t, f)
	w := f.do(http.MethodPost, path, recipient, bytes.NewBufferString(`{}`))
	if w.Code != http.StatusOK || messageArchive.calls != 1 || ackArchive.calls != 1 {
		t.Fatalf("archived POST retry = %d messageCalls=%d ackCalls=%d body=%s", w.Code, messageArchive.calls, ackArchive.calls, w.Body.String())
	}
	got := decode[apiv1.InboxAckResult](t, w)
	want := apiv1.InboxAckResult{Outcome: "Confirmed", Ack: apiv1.InboxReadAck{Name: a.Name, UID: string(a.UID), Project: a.Spec.Project, MessageName: a.Spec.MessageName, MessageUID: a.Spec.MessageUID, Recipient: apiv1.TaskClaimOwner{Namespace: a.Spec.Recipient.Namespace, Name: a.Spec.Recipient.Name, UID: a.Spec.Recipient.UID}, Actor: apiv1.TaskClaimActor{Kind: a.Spec.Actor.Kind, ServiceAccount: a.Spec.Actor.ServiceAccount, PodName: a.Spec.Actor.PodName, PodUID: a.Spec.Actor.PodUID}, CreatedAt: a.CreationTimestamp.Time, Archived: true, ArchivedAt: &ackArchive.result.Manifest.ArchivedAt}}
	// Compare the wire-normalized independent expected view, including every
	// stored acknowledgement field and its first archive receipt.
	for _, value := range []any{&want} {
		wire, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wire, value); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("archived acknowledgement changed: got=%#v want=%#v", got, want)
	}
}

func TestInboxArchiveAckRetryRefusalsNeverWrite(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		configure func(*fixture, *inboxArchiveGetFake, *inboxArchiveAckGetFake)
		want      int
		messageIO int
		ackIO     int
	}{
		{name: "default-off", body: `{}`, want: http.StatusNotFound},
		{name: "ack-not-found", body: `{}`, want: http.StatusNotFound, messageIO: 1, ackIO: 1, configure: func(f *fixture, _ *inboxArchiveGetFake, ack *inboxArchiveAckGetFake) {
			f.srv.CoordinationArchiveReads = true
			ack.err = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing")
		}},
		{name: "archive-error", body: `{}`, want: http.StatusServiceUnavailable, messageIO: 1, ackIO: 1, configure: func(f *fixture, _ *inboxArchiveGetFake, ack *inboxArchiveAckGetFake) {
			f.srv.CoordinationArchiveReads = true
			ack.err = errors.New("archive failed")
		}},
		{name: "extra-intent", body: `{"extra":true}`, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, recipient, m, messageArchive, messagePath := inboxArchiveGetRig(t)
			path := strings.TrimSuffix(messagePath, "?project=sample") + "/ack?project=sample"
			if w := f.do(http.MethodPost, path, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
				t.Fatal(w.Code, w.Body.String())
			}
			a, err := f.srv.Inbox.ReadAck(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.c.Delete(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			ackArchive := &inboxArchiveAckGetFake{result: archivedInboxAckGetResult(t, a)}
			f.srv.CoordinationArchive, f.srv.ShelfSlots = inboxArchiveAckRouter{message: messageArchive, ack: ackArchive}, shelf.NewExecSlots(1)
			if err := f.c.Delete(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			assertRetryNoWrites(t, f)
			if tc.configure != nil {
				tc.configure(f, messageArchive, ackArchive)
			}
			w := f.do(http.MethodPost, path, recipient, tc.body)
			if w.Code != tc.want || messageArchive.calls != tc.messageIO || ackArchive.calls != tc.ackIO {
				t.Fatalf("retry %s = %d messageCalls=%d ackCalls=%d body=%s", tc.name, w.Code, messageArchive.calls, ackArchive.calls, w.Body.String())
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func assertRetryNoWrites(t *testing.T, f *fixture) {
	t.Helper()
	attempts := 0
	f.srv.Inbox.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
		attempts++
		return errors.New("archived retry Create forbidden")
	}})
	t.Cleanup(func() {
		if attempts != 0 {
			t.Errorf("archived branch attempted %d writes", attempts)
		}
	})
}

func archivedRetryRig(t *testing.T) (*fixture, string, *coord.InboxMessage, *coord.InboxReadAck, *inboxArchiveAckGetFake, string) {
	t.Helper()
	f, token, m, a, archive, path := archivedAckRig(t)
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	f.srv.CoordinationArchive = inboxArchiveAckRouter{message: &inboxArchiveGetFake{result: archivedInboxGetResult(t, m)}, ack: archive}
	assertRetryNoWrites(t, f)
	return f, token, m, a, archive, path
}

func TestInboxArchiveAckRetryStrictEmpty(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"x":1}`, `{} {}`, `{`, strings.Repeat(" ", 129) + `{}`} {
		t.Run(body, func(t *testing.T) {
			f, token, _, _, archive, path := archivedRetryRig(t)
			message := f.srv.CoordinationArchive.(inboxArchiveAckRouter).message
			w := f.do(http.MethodPost, path, token, body)
			if w.Code != http.StatusBadRequest || archive.calls != 0 || message.calls != 0 {
				t.Fatalf("invalid intent=%d messageIO=%d ackIO=%d %s", w.Code, message.calls, archive.calls, w.Body.String())
			}
		})
	}
}

func TestInboxArchiveAckRetryLiveAndPartialRelease(t *testing.T) {
	f, _, token, m, messageArchive, messagePath := inboxArchiveGetRig(t)
	path := strings.TrimSuffix(messagePath, "?project=sample") + "/ack?project=sample"
	f.srv.CoordinationArchiveReads = true
	ackArchive := &inboxArchiveAckGetFake{}
	f.srv.CoordinationArchive = inboxArchiveAckRouter{message: messageArchive, ack: ackArchive}
	f.srv.ShelfSlots = shelf.NewExecSlots(1)
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		w := f.do(http.MethodPost, path, token, `{}`)
		if w.Code != want || messageArchive.calls != 0 || ackArchive.calls != 0 {
			t.Fatalf("live=%d %s", w.Code, w.Body.String())
		}
	}
	if err := f.c.Delete(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	assertRetryNoWrites(t, f)
	ackArchive.err = protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "missing")
	w := f.do(http.MethodPost, path, token, `{}`)
	if w.Code != http.StatusNotFound || ackArchive.calls != 1 {
		t.Fatalf("partial release manufactured confirmation=%d %s", w.Code, w.Body.String())
	}
	assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
}

func TestInboxArchiveAckRetryValidEnvelopeSemanticRefusals(t *testing.T) {
	for _, mode := range []string{"recipient-namespace", "recipient-name", "recipient-uid", "message-name", "message-uid", "project", "namespace", "invalid-actor", "zero-created", "deleted", "nonutc-receipt"} {
		t.Run(mode, func(t *testing.T) {
			f, token, _, a, archive, path := archivedRetryRig(t)
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
			w := f.do(http.MethodPost, path, token, `{}`)
			if w.Code != http.StatusServiceUnavailable || archive.calls != 1 || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s=%d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}

func TestInboxArchiveAckRetryCallerFenceAndStoredActor(t *testing.T) {
	for _, mode := range []string{"coordinator", "unscoped-coordinator", "absent-sender", "deleting-recipient", "absent-recipient", "replaced-before", "replaced-after", "pod-after", "foreign-live"} {
		t.Run(mode, func(t *testing.T) {
			f, token, m, a, archive, path := archivedRetryRig(t)
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
				want, calls = http.StatusNotFound, 0
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
				m.ResourceVersion = ""
				if err := f.c.Create(context.Background(), m); err != nil {
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
			w := f.do(http.MethodPost, path, token, `{}`)
			if w.Code != want || archive.calls != calls {
				t.Fatalf("%s=%d calls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			if want == http.StatusOK {
				got := decode[apiv1.InboxAckResult](t, w).Ack
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

func TestInboxArchiveAckRetryEnvelopeAndAvailabilityRefusals(t *testing.T) {
	for _, mode := range []string{"not-found", "typed-error", "namespace", "name", "uid", "message", "project", "digest", "noncanonical", "zero-receipt", "nil-reader", "nil-slots", "busy", "cancel-before", "cancel-after"} {
		t.Run(mode, func(t *testing.T) {
			f, recipient, _, a, archive, ackPath := archivedRetryRig(t)
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
				req := httptest.NewRequest(http.MethodPost, ackPath, strings.NewReader(`{}`))
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
				req := httptest.NewRequest(http.MethodPost, ackPath, strings.NewReader(`{}`))
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
			w := f.do(http.MethodPost, ackPath, recipient, `{}`)
			if w.Code != want || archive.calls != calls {
				t.Fatalf("%s = %d calls=%d body=%s", mode, w.Code, archive.calls, w.Body.String())
			}
			if f.srv.ShelfSlots != nil && mode != "busy" {
				assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
			}
		})
	}
}

func TestInboxArchiveAckRetryExecutorAndEarlyFence(t *testing.T) {
	for _, mode := range []string{"foreign-recipient", "replaced-during-message", "missing-pod", "deleting-pod-before", "deleting-pod-after"} {
		t.Run(mode, func(t *testing.T) {
			f, token, _, _, archive, path := archivedRetryRig(t)
			message := f.srv.CoordinationArchive.(inboxArchiveAckRouter).message
			ackCalls := 0
			changePod := func() {
				pod := new(corev1.Pod)
				id := f.auth[token]
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: id.PodName}, pod); err != nil {
					t.Fatal(err)
				}
				if mode != "missing-pod" {
					pod.Finalizers = []string{"retain-fixture"}
					if err := f.c.Update(context.Background(), pod); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.c.Delete(context.Background(), pod); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "foreign-recipient":
				token = claimSession(t, f, "other-recipient", clientSA)
			case "replaced-during-message":
				message.after = func() {
					sess := f.session("archive-recipient")
					sess.UID = "replacement-session"
					if err := f.c.Update(context.Background(), sess); err != nil {
						t.Fatal(err)
					}
				}
			case "missing-pod", "deleting-pod-before":
				changePod()
			case "deleting-pod-after":
				archive.after = changePod
				ackCalls = 1
			}
			w := f.do(http.MethodPost, path, token, `{}`)
			if w.Code != http.StatusNotFound || archive.calls != ackCalls || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
				t.Fatalf("%s=%d ackCalls=%d %s", mode, w.Code, archive.calls, w.Body.String())
			}
			assertInboxArchiveGetSlot(t, f.srv.ShelfSlots)
		})
	}
}
