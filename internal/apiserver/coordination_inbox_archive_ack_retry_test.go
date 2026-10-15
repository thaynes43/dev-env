package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

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
	f.srv.Inbox.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
		return errors.New("archived retry attempted inbox Create")
	}})
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
			f.srv.Inbox.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
				return errors.New("archived refusal attempted inbox Create")
			}})
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
