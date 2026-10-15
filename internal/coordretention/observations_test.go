package coordretention

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

const replacementUID = "88888888-8888-4888-8888-888888888888"

func TestObservation(t *testing.T) {
	t.Run("owner and source", func(t *testing.T) {
		for _, kind := range []Kind{TaskClaim, PublishedContext} {
			s := summaryOf(t, kind)
			role, ref, resource := "owner", s.Owner, "taskclaims"
			if kind == PublishedContext {
				role, ref, resource = "source", s.Source, "publishedcontexts"
			}
			t.Run(string(kind)+"/not-found", func(t *testing.T) {
				gets := 0
				got := evalSessions(t, &gets, ref, "notfound", s)
				if gets != 1 || got.Decision != Released || len(got.Observations) != 1 {
					t.Fatalf("gets=%d result=%#v", gets, got)
				}
				assertObs(t, got.Observations[0], role, ref, "not-found", "")
				validateEvent(t, resource, "absent", got.Observations)
			})
			t.Run(string(kind)+"/present deleting", func(t *testing.T) {
				gets := 0
				got := evalSessions(t, &gets, ref, "deleting", s)
				if gets != 1 || got.Decision != Kept || len(got.Observations) != 1 {
					t.Fatalf("gets=%d result=%#v", gets, got)
				}
				assertObs(t, got.Observations[0], role, ref, "present", "")
				validateEvent(t, resource, "present", got.Observations)
			})
			t.Run(string(kind)+"/one get two UIDs", func(t *testing.T) {
				other := s
				otherRef := ref
				otherRef.UID = replacementUID
				if kind == TaskClaim {
					other.Owner = otherRef
				} else {
					other.Source = otherRef
				}
				gets := 0
				r := sessionReader(&gets, ref, "same")
				got := Evaluate(context.Background(), config(r), []Summary{s, other})
				if gets != 1 || len(got) != 2 {
					t.Fatalf("gets=%d len=%d", gets, len(got))
				}
				if got[0].Decision != Kept || got[1].Decision != Released {
					t.Fatalf("decisions=%s %s", got[0].Decision, got[1].Decision)
				}
				assertObs(t, got[0].Observations[0], role, ref, "present", "")
				assertObs(t, got[1].Observations[0], role, otherRef, "uid-replaced", "")
				validateEvent(t, resource, "present", got[0].Observations)
				validateEvent(t, resource, "absent", got[1].Observations)
			})
		}
	})

	t.Run("message", func(t *testing.T) {
		s := summaryOf(t, InboxMessage)
		t.Run("sender present", func(t *testing.T) {
			gets := 0
			got := evalMessage(t, &gets, s, "same", "", "")
			if gets != 1 || got.Decision != Kept || len(got.Observations) != 1 {
				t.Fatalf("gets=%d %#v", gets, got)
			}
			assertObs(t, got.Observations[0], "sender", s.Sender, "present", "")
			validateEvent(t, "inboxmessages", "present", got.Observations)
		})
		for _, mode := range []string{"notfound", "replaced"} {
			observed := "not-found"
			if mode == "replaced" {
				observed = "uid-replaced"
			}
			t.Run("recipient "+observed, func(t *testing.T) {
				gets := 0
				got := evalMessage(t, &gets, s, mode, "absent-ack", mode)
				if gets != 3 || got.Decision != Released || len(got.Observations) != 2 {
					t.Fatalf("gets=%d %#v", gets, got)
				}
				assertObs(t, got.Observations[0], "sender", s.Sender, observed, "")
				assertObs(t, got.Observations[1], "recipient", s.Recipient, observed, "")
				validateEvent(t, "inboxmessages", "absent", got.Observations)
			})
		}
		t.Run("recipient present", func(t *testing.T) {
			gets := 0
			got := evalMessage(t, &gets, s, "notfound", "absent-ack", "same")
			if gets != 3 || got.Decision != Kept || len(got.Observations) != 2 {
				t.Fatalf("gets=%d %#v", gets, got)
			}
			assertObs(t, got.Observations[0], "sender", s.Sender, "not-found", "")
			assertObs(t, got.Observations[1], "recipient", s.Recipient, "present", "")
			validateEvent(t, "inboxmessages", "present", got.Observations)
		})
		t.Run("bound ack", func(t *testing.T) {
			gets := 0
			got := evalMessage(t, &gets, s, "notfound", "ack", "")
			if gets != 2 || got.Decision != Released || len(got.Observations) != 2 || got.Observations[1].AckUID != "99999999-9999-4999-8999-999999999999" {
				t.Fatalf("gets=%d %#v", gets, got)
			}
			assertObs(t, got.Observations[0], "sender", s.Sender, "not-found", "")
			assertObs(t, got.Observations[1], "recipient", s.Recipient, "acknowledged", "99999999-9999-4999-8999-999999999999")
			validateEvent(t, "inboxmessages", "absent", got.Observations)
		})
	})

	t.Run("unknown has no proof", func(t *testing.T) {
		s := summaryOf(t, TaskClaim)
		cases := []string{"malformed", "forbidden", "foreign", "cancel"}
		for _, name := range cases {
			t.Run(name, func(t *testing.T) {
				gets := 0
				ctx := context.Background()
				summaries := []Summary{s}
				var r client.Reader = sessionReader(&gets, s.Owner, "same")
				switch name {
				case "malformed":
					summaries = []Summary{{Kind: TaskClaim, Namespace: retentionNS, Valid: true}}
					r = sessionReader(&gets, s.Owner, "same")
				case "forbidden":
					r = sessionReader(&gets, s.Owner, "forbidden")
				case "foreign":
					r = sessionReader(&gets, s.Owner, "same")
				case "cancel":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				cfg := config(r)
				if name == "foreign" {
					cfg.ReconciledNamespaces = map[string]bool{}
				}
				got := Evaluate(ctx, cfg, summaries)[0]
				if got.Decision != Unknown || got.Observations != nil || got.RequiredMessage != nil {
					t.Fatalf("%#v", got)
				}
				if name != "forbidden" && gets != 0 {
					t.Fatalf("gets=%d", gets)
				}
				if name == "forbidden" && gets != 1 {
					t.Fatalf("gets=%d", gets)
				}
			})
		}
		msg := summaryOf(t, InboxMessage)
		t.Run("malformed ack", func(t *testing.T) {
			gets := 0
			got := evalMessage(t, &gets, msg, "notfound", "bad-ack", "")
			if got.Decision != Unknown || got.Observations != nil {
				t.Fatalf("%#v", got)
			}
		})
	})

	t.Run("read ack", func(t *testing.T) {
		s := summaryOf(t, InboxReadAck)
		messageRef := SessionRef{Namespace: s.Message.Namespace, Name: s.Message.Name, UID: s.Message.UID}
		t.Run("message present", func(t *testing.T) {
			gets := 0
			got := evalAck(t, &gets, s, "same", nil)
			if gets != 1 || got.Decision != Kept || len(got.Observations) != 1 {
				t.Fatalf("gets=%d %#v", gets, got)
			}
			assertObs(t, got.Observations[0], "message", messageRef, "present", "")
			validateEvent(t, "inboxreadacks", "present", got.Observations)
		})
		t.Run("pending", func(t *testing.T) {
			gets := 0
			got := evalAck(t, &gets, s, "notfound", nil)
			if gets != 1 || got.Decision != Unknown || got.RequiredMessage == nil || *got.RequiredMessage != s.Message || got.Observations != nil {
				t.Fatalf("gets=%d %#v", gets, got)
			}
		})
		t.Run("known holds", func(t *testing.T) {
			gets := 0
			got := evalAck(t, &gets, s, "notfound", map[MessageRef]ArchiveEvidence{s.Message: {Known: true, Holds: true}})
			if gets != 1 || got.Decision != Released || got.RequiredMessage != nil || len(got.Observations) != 1 {
				t.Fatalf("gets=%d %#v", gets, got)
			}
			assertObs(t, got.Observations[0], "message", messageRef, "message-released", "")
			validateEvent(t, "inboxreadacks", "absent", got.Observations)
		})
		t.Run("known no holds", func(t *testing.T) {
			gets := 0
			got := evalAck(t, &gets, s, "notfound", map[MessageRef]ArchiveEvidence{s.Message: {Known: true, Holds: false}})
			if gets != 1 || got.Decision != Kept || got.Observations != nil || got.RequiredMessage != nil {
				t.Fatalf("gets=%d %#v", gets, got)
			}
		})
		t.Run("bulk holds and ordinary unknown", func(t *testing.T) {
			gets := 0
			claim := summaryOf(t, TaskClaim)
			evaluated := Evaluate(context.Background(), config(bulkReader(t, &gets, s, claim)), []Summary{s, claim})
			if gets != 2 || evaluated[0].Decision != Unknown || evaluated[0].Observations != nil || evaluated[1].Decision != Unknown {
				t.Fatalf("gets=%d %#v", gets, evaluated)
			}
			fake := &lookupFake{holds: map[string]bool{lookupKey(protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: s.Message.Project, Name: s.Message.Name, UID: s.Message.UID}): true}}
			before := gets
			out, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), fake, evaluated)
			if err != nil || gets != before || len(fake.calls) != 3 {
				t.Fatalf("gets=%d calls=%d err=%v", gets, len(fake.calls), err)
			}
			if out[0].Result.Decision != Released || out[0].Result.RequiredMessage != nil || len(out[0].Result.Observations) != 1 {
				t.Fatalf("%#v", out[0].Result)
			}
			assertObs(t, out[0].Result.Observations[0], "message", messageRef, "message-released", "")
			validateEvent(t, "inboxreadacks", "absent", out[0].Result.Observations)
			if out[1].Result.Decision != Unknown || out[1].Result.Observations != nil {
				t.Fatalf("ordinary %#v", out[1].Result)
			}
		})
		t.Run("bulk no holds", func(t *testing.T) {
			gets := 0
			evaluated := Evaluate(context.Background(), config(ackReader(&gets, s, "notfound")), []Summary{s})
			fake := &lookupFake{}
			out, err := Lookup(context.Background(), retentionNS, shelf.NewExecSlots(1), fake, evaluated)
			if err != nil || gets != 1 || len(fake.calls) != 2 || out[0].Result.Decision != Kept || out[0].Result.Observations != nil || out[0].Result.RequiredMessage != nil {
				t.Fatalf("gets=%d calls=%d err=%v %#v", gets, len(fake.calls), err, out)
			}
		})
	})
}

func assertObs(t *testing.T, got protocol.CoordArchiveObservation, role string, ref SessionRef, observed, ackUID string) {
	t.Helper()
	if got.Role != role || got.Namespace != ref.Namespace || got.Name != ref.Name || got.UID != ref.UID || got.Observed != observed || got.AckUID != ackUID {
		t.Fatalf("observation=%#v want role=%s ref=%#v observed=%s ack=%s", got, role, ref, observed, ackUID)
	}
}

func validateEvent(t *testing.T, resource, state string, obs []protocol.CoordArchiveObservation) {
	t.Helper()
	rule := map[string]string{"taskclaims": "owner-absent", "publishedcontexts": "source-absent", "inboxmessages": "sender-absent-recipient-done", "inboxreadacks": "message-released"}[resource]
	ev := protocol.CoordArchiveEvent{
		Format: protocol.CoordArchiveEventFormat, State: state,
		At: time.Date(2026, 10, 15, 23, 9, 48, 0, time.UTC), Rule: rule, By: "coordretention-observation-fixture", Sessions: obs,
	}
	if err := ev.Validate(resource); err != nil {
		t.Fatal(err)
	}
}

func evalSessions(t *testing.T, gets *int, ref SessionRef, mode string, s Summary) Result {
	t.Helper()
	return Evaluate(context.Background(), config(sessionReader(gets, ref, mode)), []Summary{s})[0]
}

func sessionReader(gets *int, ref SessionRef, mode string) *hookReader {
	return &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		*gets++
		if key != (client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}) {
			return errors.New("unexpected get")
		}
		switch mode {
		case "notfound":
			return absentObject(key)
		case "forbidden":
			return apierrors.NewForbidden(schema.GroupResource{Resource: "agentsessions"}, key.Name, errors.New("fixture"))
		}
		got := obj.(*devv1.AgentSession)
		uid := ref.UID
		if mode == "replaced" {
			uid = replacementUID
		}
		got.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(uid)}
		if mode == "deleting" {
			now := metav1.Now()
			got.DeletionTimestamp = &now
			got.Finalizers = []string{"test/preserve"}
		}
		return nil
	}}
}

func evalMessage(t *testing.T, gets *int, s Summary, senderMode, ackMode, recipientMode string) Result {
	t.Helper()
	return Evaluate(context.Background(), config(messageReader(gets, s, senderMode, ackMode, recipientMode)), []Summary{s})[0]
}

func messageReader(gets *int, s Summary, senderMode, ackMode, recipientMode string) *hookReader {
	return &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		*gets++
		switch target := obj.(type) {
		case *devv1.AgentSession:
			mode := senderMode
			ref := s.Sender
			if key.Name == s.Recipient.Name {
				mode, ref = recipientMode, s.Recipient
			}
			if key.Namespace != ref.Namespace || key.Name != ref.Name {
				return errors.New("unexpected session")
			}
			if mode == "notfound" {
				return absentObject(key)
			}
			uid := ref.UID
			if mode == "replaced" {
				uid = replacementUID
			}
			target.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(uid)}
			return nil
		case *coord.InboxReadAck:
			if ackMode == "absent-ack" {
				return absentObject(key)
			}
			if ackMode == "bad-ack" {
				target.ObjectMeta = metav1.ObjectMeta{Namespace: "foreign", Name: key.Name, UID: "99999999-9999-4999-8999-999999999999", ResourceVersion: "1"}
				return nil
			}
			target.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: "99999999-9999-4999-8999-999999999999", ResourceVersion: "1"}
			target.Spec = coord.InboxReadAckSpec{Project: s.Project, MessageName: s.Name, MessageUID: s.UID, Recipient: coord.SessionIdentity{Namespace: s.Recipient.Namespace, Name: s.Recipient.Name, UID: s.Recipient.UID}, Actor: actor()}
			return nil
		default:
			return &meta.NoKindMatchError{}
		}
	}}
}

func evalAck(t *testing.T, gets *int, s Summary, mode string, archive map[MessageRef]ArchiveEvidence) Result {
	t.Helper()
	cfg := config(ackReader(gets, s, mode))
	cfg.Archive = archive
	return Evaluate(context.Background(), cfg, []Summary{s})[0]
}

func ackReader(gets *int, s Summary, mode string) *hookReader {
	return &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		*gets++
		if key != (client.ObjectKey{Namespace: s.Message.Namespace, Name: s.Message.Name}) {
			return errors.New("unexpected message get")
		}
		if mode == "notfound" {
			return absentObject(key)
		}
		msg := obj.(*coord.InboxMessage)
		msg.ObjectMeta = metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, UID: types.UID(s.Message.UID), ResourceVersion: "30"}
		return nil
	}}
}

func bulkReader(t *testing.T, gets *int, ack, claim Summary) *hookReader {
	t.Helper()
	return &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		*gets++
		switch obj.(type) {
		case *coord.InboxMessage:
			if key.Name != ack.Message.Name {
				return errors.New("unexpected message")
			}
			return absentObject(key)
		case *devv1.AgentSession:
			if key.Name != claim.Owner.Name {
				return errors.New("unexpected session")
			}
			return apierrors.NewForbidden(schema.GroupResource{Resource: "agentsessions"}, key.Name, errors.New("fixture"))
		default:
			return errors.New("unexpected type")
		}
	}}
}
