package coordretention

import (
	"context"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const parentPolicyUID = "88888888-8888-4888-8888-888888888888"

func parentCaller() *coord.InboxCaller {
	return &coord.InboxCaller{ServiceAccount: coord.MeatballServiceAccount, PolicyUID: parentPolicyUID}
}

func policyActor() coord.ClaimActor {
	actor := actor()
	actor.Kind = "policy"
	actor.ServiceAccount = coord.MeatballServiceAccount
	return actor
}

func idUID(n int) types.UID {
	return types.UID(fmt.Sprintf("44444444-4444-4444-8444-%012x", n))
}

func liveSession(id coord.SessionIdentity) *devv1.AgentSession {
	return &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: id.Namespace, Name: id.Name, UID: types.UID(id.UID)}}
}

func noteMessage(t *testing.T, spec coord.InboxMessageSpec, uid types.UID, parent bool) *coord.InboxMessage {
	t.Helper()
	if !inbox.ValidMessage(spec) {
		t.Fatalf("fixture refused: kind %q key %q", spec.Kind, spec.Key)
	}
	var labels map[string]string
	if parent {
		labels = map[string]string{inbox.ParentNoteLabel: "true"}
	}
	return &coord.InboxMessage{
		TypeMeta: metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: retentionNS, Name: inbox.MessageNameFor(spec), UID: uid, ResourceVersion: "30", Labels: labels,
		},
		Spec: spec,
	}
}

func childNote(key, kind string) coord.InboxMessageSpec {
	spec := messageSpec()
	spec.Key, spec.Kind = key, kind
	return spec
}

func toCaller(key, kind string) coord.InboxMessageSpec {
	spec := childNote(key, kind)
	spec.Recipient = coord.SessionIdentity{}
	spec.CallerRecipient = parentCaller()
	return spec
}

func fromCaller(key, kind string) coord.InboxMessageSpec {
	spec := messageSpec()
	spec.Key, spec.Kind = key, kind
	spec.Sender = coord.SessionIdentity{}
	spec.CallerSender = parentCaller()
	spec.Actor = policyActor()
	return spec
}

func answerTo(t *testing.T, note *coord.InboxMessage, key string, uid types.UID, fromPolicy bool) *coord.InboxMessage {
	t.Helper()
	spec := messageSpec()
	spec.Key = key
	spec.Kind = inbox.KindReply
	spec.InReplyTo = &coord.InboxMessageRef{Name: note.Name, UID: string(note.UID)}
	if fromPolicy {
		spec.Sender = coord.SessionIdentity{}
		spec.CallerSender = parentCaller()
		spec.Actor = policyActor()
		spec.Recipient = note.Spec.Sender
	} else {
		spec.Sender = note.Spec.Recipient
		spec.Recipient = note.Spec.Sender
	}
	message := noteMessage(t, spec, uid, true)
	message.Labels[inbox.KindLabel] = inbox.KindReply
	message.Labels[taskclaim.ProjectLabel] = spec.Project
	return message
}

func sessionAck(t *testing.T, message *coord.InboxMessage, uid types.UID) *coord.InboxReadAck {
	t.Helper()
	spec := coord.InboxReadAckSpec{
		Project: message.Spec.Project, MessageName: message.Name, MessageUID: string(message.UID),
		Recipient: message.Spec.Recipient, Actor: actor(),
	}
	if !inbox.ValidAck(spec) {
		t.Fatalf("ack fixture %#v", spec)
	}
	return &coord.InboxReadAck{
		TypeMeta: metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxReadAck"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: retentionNS, Name: inbox.AckName(spec.MessageUID, spec.Recipient.UID),
			UID: uid, ResourceVersion: "40",
		},
		Spec: spec,
	}
}

func decideNotes(t *testing.T, messages []*coord.InboxMessage, sessions ...*devv1.AgentSession) map[string]Result {
	t.Helper()
	return decideLoaded(t, messages, nil, sessions)
}

func decideLoaded(t *testing.T, messages []*coord.InboxMessage, extras []client.Object, sessions []*devv1.AgentSession) map[string]Result {
	t.Helper()
	objects := make([]client.Object, 0, len(messages)+len(sessions)+len(extras))
	items := make([]unstructured.Unstructured, 0, len(messages))
	for _, message := range messages {
		objects = append(objects, message.DeepCopy())
		items = append(items, toUnstructured(t, message))
	}
	for _, extra := range extras {
		objects = append(objects, extra.DeepCopyObject().(client.Object))
	}
	for _, session := range sessions {
		objects = append(objects, session.DeepCopy())
	}
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": items}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(objects...).Build()
	out := map[string]Result{}
	for _, result := range Evaluate(context.Background(), config(reader), summaries) {
		out[result.Summary.Name] = result
	}
	return out
}

func requireDecision(t *testing.T, got Result, want Decision) {
	t.Helper()
	if got.Decision != want || got.RequiredMessage != nil {
		t.Fatalf("decision = %s, want %s (required %#v)", got.Decision, want, got.RequiredMessage)
	}
}

func toUnstructured(t *testing.T, message *coord.InboxMessage) unstructured.Unstructured {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(message)
	if err != nil {
		t.Fatal(err)
	}
	return unstructured.Unstructured{Object: raw}
}

func TestParentNoteReleaseFacts(t *testing.T) {
	child := liveSession(sender)
	parent := liveSession(recipient)

	t.Run("caller recipient releases after the child is gone", func(t *testing.T) {
		note := noteMessage(t, toCaller("caller-answered", inbox.KindQuestion), idUID(1), false)
		reply := answerTo(t, note, "caller-answer", idUID(2), true)
		got := decideNotes(t, []*coord.InboxMessage{note, reply})[note.Name]
		requireDecision(t, got, Released)
		if len(got.Observations) != 2 || got.Observations[0].Observed != "not-found" || got.Observations[1].Observed != "not-found" || got.Observations[1].UID != parentPolicyUID {
			t.Fatalf("caller absence observation: %#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
		replyResult := decideNotes(t, []*coord.InboxMessage{note, reply})[reply.Name]
		requireDecision(t, replyResult, Released)
	})

	t.Run("unanswered note to a live parent session is kept", func(t *testing.T) {
		note := noteMessage(t, childNote("session-unanswered", inbox.KindBlocker), idUID(3), true)
		got := decideNotes(t, []*coord.InboxMessage{note}, parent)[note.Name]
		requireDecision(t, got, Kept)
		if len(got.Observations) != 2 || got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
			t.Fatalf("live parent observation: %#v", got)
		}
		validateEvent(t, "inboxmessages", "present", got.Observations)
	})

	t.Run("caller recipient whose child still exists is kept", func(t *testing.T) {
		note := noteMessage(t, toCaller("caller-live-child", inbox.KindResult), idUID(4), false)
		got := decideNotes(t, []*coord.InboxMessage{note}, child)[note.Name]
		requireDecision(t, got, Kept)
		if len(got.Observations) != 1 || got.Observations[0].Observed != "present" || got.Observations[0].UID != sender.UID {
			t.Fatalf("live child observation: %#v", got)
		}
	})

	t.Run("answered note to a live parent session is kept", func(t *testing.T) {
		note := noteMessage(t, childNote("session-answered", inbox.KindQuestion), idUID(5), true)
		reply := answerTo(t, note, "session-answer", idUID(6), false)
		got := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[note.Name]
		requireDecision(t, got, Kept)
		if len(got.Observations) != 2 || got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
			t.Fatalf("live parent kept an answered note: %#v", got)
		}
		replyResult := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[reply.Name]
		requireDecision(t, replyResult, Kept)
	})

	t.Run("answered note is released once both sessions are gone", func(t *testing.T) {
		note := noteMessage(t, childNote("session-parent-gone", inbox.KindQuestion), idUID(7), true)
		reply := answerTo(t, note, "session-parent-gone-answer", idUID(8), false)
		got := decideNotes(t, []*coord.InboxMessage{note, reply})[note.Name]
		requireDecision(t, got, Released)
		if len(got.Observations) != 2 || got.Observations[0].Observed != "not-found" || got.Observations[1].Observed != "not-found" {
			t.Fatalf("gone parent: %#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
	})
}

func TestCallerShapes(t *testing.T) {
	child := liveSession(sender)
	recipientSession := liveSession(recipient)
	shapes := []struct {
		name  string
		build func(t *testing.T, n int) (*coord.InboxMessage, *devv1.AgentSession)
	}{
		{name: "caller recipient note", build: func(t *testing.T, n int) (*coord.InboxMessage, *devv1.AgentSession) {
			t.Helper()
			return noteMessage(t, toCaller(fmt.Sprintf("shape-note-%d", n), inbox.KindQuestion), idUID(n), false), child
		}},
		{name: "caller reply", build: func(t *testing.T, n int) (*coord.InboxMessage, *devv1.AgentSession) {
			t.Helper()
			note := noteMessage(t, toCaller(fmt.Sprintf("shape-reply-note-%d", n), inbox.KindQuestion), idUID(n), false)
			return answerTo(t, note, fmt.Sprintf("shape-reply-%d", n), idUID(n+1), true), child
		}},
		{name: "caller plain message", build: func(t *testing.T, n int) (*coord.InboxMessage, *devv1.AgentSession) {
			t.Helper()
			return noteMessage(t, fromCaller(fmt.Sprintf("shape-plain-%d", n), ""), idUID(n), false), recipientSession
		}},
	}
	for i, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			base := 100 + i*20
			t.Run("live child", func(t *testing.T) {
				message, session := shape.build(t, base)
				got := decideNotes(t, []*coord.InboxMessage{message}, session)[message.Name]
				requireDecision(t, got, Kept)
			})
			t.Run("live child with a read ack", func(t *testing.T) {
				message, session := shape.build(t, base+2)
				got := decideLoaded(t, []*coord.InboxMessage{message}, []client.Object{callerReadAck(t, message, idUID(800+base))}, []*devv1.AgentSession{session})[message.Name]
				requireDecision(t, got, Kept)
			})
			t.Run("child gone", func(t *testing.T) {
				message, _ := shape.build(t, base+4)
				got := decideNotes(t, []*coord.InboxMessage{message})[message.Name]
				requireDecision(t, got, Released)
				if len(got.Observations) != 2 || got.Observations[0].Observed == "acknowledged" || got.Observations[1].Observed == "acknowledged" {
					t.Fatalf("ack shortcut: %#v", got.Observations)
				}
				validateEvent(t, "inboxmessages", "absent", got.Observations)
			})
			t.Run("child gone with a read ack", func(t *testing.T) {
				message, _ := shape.build(t, base+6)
				got := decideLoaded(t, []*coord.InboxMessage{message}, []client.Object{callerReadAck(t, message, idUID(900+base))}, nil)[message.Name]
				requireDecision(t, got, Released)
				for _, obs := range got.Observations {
					if obs.Observed == "acknowledged" {
						t.Fatalf("read ack released a caller record: %#v", got.Observations)
					}
				}
			})
			t.Run("child read fails", func(t *testing.T) {
				message, session := shape.build(t, base+8)
				got := decideChildReadFails(t, message, session.Name)
				requireDecision(t, got, Unknown)
				if got.Observations != nil {
					t.Fatalf("failed child read recorded observations: %#v", got.Observations)
				}
			})
		})
	}

	t.Run("shapes that are not named stay unknown", func(t *testing.T) {
		plain := noteMessage(t, toCaller("session-to-caller-plain", ""), idUID(180), false)
		if decideNotes(t, []*coord.InboxMessage{plain}, child)[plain.Name].Decision != Unknown {
			t.Fatal("session-to-caller plain with a live child")
		}
		if decideNotes(t, []*coord.InboxMessage{plain})[plain.Name].Decision != Unknown {
			t.Fatal("session-to-caller plain with the child gone")
		}
		spec := fromCaller("caller-to-caller", inbox.KindQuestion)
		spec.Recipient = coord.SessionIdentity{}
		spec.CallerRecipient = parentCaller()
		both := noteMessage(t, spec, idUID(181), false)
		if got := decideNotes(t, []*coord.InboxMessage{both})[both.Name]; got.Decision != Unknown || got.Observations != nil {
			t.Fatalf("caller-to-caller %#v", got)
		}
		for _, kind := range []string{inbox.KindQuestion, inbox.KindResult, inbox.KindBlocker} {
			message := noteMessage(t, fromCaller("caller-kind-"+kind, kind), idUID(182), false)
			if decideNotes(t, []*coord.InboxMessage{message})[message.Name].Decision != Unknown {
				t.Fatalf("caller %s was decided", kind)
			}
		}
		wide := toCaller("caller-wide-uid", inbox.KindQuestion)
		wide.CallerRecipient.PolicyUID = "meatball-policy-uid"
		message := noteMessage(t, wide, idUID(183), false)
		if got := decideNotes(t, []*coord.InboxMessage{message})[message.Name]; got.Decision != Unknown || got.Observations != nil {
			t.Fatalf("non-kubernetes policy UID %#v", got)
		}
	})
}

// callerReadAck is a valid session ack for a caller-authored record, whose
// recipient is a session. A note to the caller account has no session
// recipient, so the object stored under its ack name cannot be a valid ack.
func callerReadAck(t *testing.T, message *coord.InboxMessage, uid types.UID) client.Object {
	t.Helper()
	if inbox.ValidSession(message.Spec.Recipient) {
		return sessionAck(t, message, uid)
	}
	return &coord.InboxReadAck{
		TypeMeta: metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxReadAck"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: retentionNS, Name: inbox.AckName(string(message.UID), parentPolicyUID),
			UID: uid, ResourceVersion: "40",
		},
		Spec: coord.InboxReadAckSpec{
			Project: message.Spec.Project, MessageName: message.Name, MessageUID: string(message.UID),
			Recipient: coord.SessionIdentity{UID: parentPolicyUID}, Actor: actor(),
		},
	}
}

func decideChildReadFails(t *testing.T, message *coord.InboxMessage, childName string) Result {
	t.Helper()
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, message)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summary %#v %v", summaries, err)
	}
	reader := &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		if _, ok := obj.(*devv1.AgentSession); ok && key.Name == childName {
			return apierrors.NewServiceUnavailable("child read failed")
		}
		return apierrors.NewNotFound(schema.GroupResource{Resource: "synthetic"}, key.Name)
	}}
	return Evaluate(context.Background(), config(reader), summaries)[0]
}

// A valid read ack is present and the child read fails. Both stay Unknown.
// Removing the caller-sender branch lets the ack release them.
func TestCallerSenderReadAckDoesNotShortcutAFailedChildRead(t *testing.T) {
	replyNote := noteMessage(t, toCaller("sender-ack-reply-note", inbox.KindQuestion), idUID(500), false)
	cases := []struct {
		name    string
		message *coord.InboxMessage
		child   string
	}{
		{name: "caller reply", message: answerTo(t, replyNote, "sender-ack-reply", idUID(501), true), child: sender.Name},
		{name: "caller plain message", message: noteMessage(t, fromCaller("sender-ack-plain", ""), idUID(502), false), child: recipient.Name},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ack := callerReadAck(t, tc.message, idUID(503)).(*coord.InboxReadAck)
			if !inbox.ValidAck(ack.Spec) || ack.Spec.MessageUID != string(tc.message.UID) || ack.Name != inbox.AckName(string(tc.message.UID), tc.message.Spec.Recipient.UID) {
				t.Fatalf("ack fixture %#v", ack)
			}
			got := decideChildReadFailsWithAck(t, tc.message, tc.child, ack)
			requireDecision(t, got, Unknown)
			if got.Observations != nil {
				t.Fatalf("failed child read recorded observations: %#v", got.Observations)
			}
		})
	}
}

func decideChildReadFailsWithAck(t *testing.T, message *coord.InboxMessage, childName string, ack *coord.InboxReadAck) Result {
	t.Helper()
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, message)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summary %#v %v", summaries, err)
	}
	reader := &hookReader{get: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
		if _, ok := obj.(*devv1.AgentSession); ok && key.Name == childName {
			return apierrors.NewServiceUnavailable("child read failed")
		}
		if got, ok := obj.(*coord.InboxReadAck); ok && key.Name == ack.Name {
			*got = *ack.DeepCopy()
			return nil
		}
		return apierrors.NewNotFound(schema.GroupResource{Resource: "synthetic"}, key.Name)
	}}
	return Evaluate(context.Background(), config(reader), summaries)[0]
}

func TestParentNoteReadAckKeepsALiveParent(t *testing.T) {
	note := noteMessage(t, childNote("ack-live-parent", inbox.KindQuestion), idUID(200), true)
	ack := sessionAck(t, note, idUID(201))
	parent := liveSession(recipient)
	store := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), ack.DeepCopy(), parent).Build()
	got := evaluateStored(t, store, note)[note.Name]
	requireDecision(t, got, Kept)
	if len(got.Observations) != 2 || got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
		t.Fatalf("live parent with an ack: %#v", got)
	}
	archive := newMemArchive()
	runnable := &Runnable{
		Reader: store, Client: store, Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		metrics: newRetentionMetrics(), Archive: archive, Slots: shelf.NewExecSlots(1),
		Now: func() time.Time { return time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC) },
	}
	if _, st := runnable.releaseRechecked(context.Background(), got.Summary); !st.refused || st.stop != "" || st.done {
		t.Fatalf("recheck %#v", st)
	}
	if len(archive.marks) == 0 || archive.marks[0].Event.State != "hold" {
		t.Fatalf("live parent was not kept: %#v", archive.marks)
	}
	status := runnable.release(context.Background(), candidate{item: LookupResult{Result: got}})
	if status.deleted || !status.refused {
		t.Fatalf("release %#v", status)
	}
	if err := store.Get(context.Background(), client.ObjectKey{Namespace: note.Namespace, Name: note.Name}, &coord.InboxMessage{}); err != nil {
		t.Fatalf("note after recheck: %v", err)
	}

	t.Run("parent gone still follows the ack", func(t *testing.T) {
		gone := noteMessage(t, childNote("ack-parent-gone", inbox.KindResult), idUID(202), true)
		goneAck := sessionAck(t, gone, idUID(203))
		goneStore := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(gone.DeepCopy(), goneAck.DeepCopy()).Build()
		decided := evaluateStored(t, goneStore, gone)[gone.Name]
		requireDecision(t, decided, Released)
		if decided.Observations[1].Observed != "acknowledged" {
			t.Fatalf("ack after the parent is gone: %#v", decided.Observations)
		}
		recheck := &Runnable{
			Reader: goneStore, Client: goneStore, Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
			metrics: newRetentionMetrics(), Archive: newMemArchive(), Slots: shelf.NewExecSlots(1),
			Now: func() time.Time { return time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC) },
		}
		rec, st := recheck.releaseRechecked(context.Background(), decided.Summary)
		if st.refused || st.stop != "" || st.done || rec.summary.UID != string(gone.UID) {
			t.Fatalf("recheck %#v uid %s", st, rec.summary.UID)
		}
	})
}

// A live parent keeps the note before the ack is read. On main each of these
// acks is Unknown. Removing the parent-present return makes these Unknown too.
func TestParentNoteLiveParentKeepsABadReadAck(t *testing.T) {
	for i, tc := range []struct {
		name string
		fail bool
		bad  func(t *testing.T, note *coord.InboxMessage) *coord.InboxReadAck
	}{
		{name: "ack-read-failed", fail: true},
		{name: "ack-malformed", bad: func(t *testing.T, note *coord.InboxMessage) *coord.InboxReadAck {
			t.Helper()
			ack := sessionAck(t, note, idUID(410))
			ack.Spec.Actor.Kind = "policy"
			if inbox.ValidAck(ack.Spec) {
				t.Fatal("malformed ack fixture")
			}
			return ack
		}},
		{name: "ack-unbound", bad: func(t *testing.T, note *coord.InboxMessage) *coord.InboxReadAck {
			t.Helper()
			ack := sessionAck(t, note, idUID(411))
			ack.Spec.MessageUID = "99999999-9999-4999-8999-999999999991"
			if ack.Spec.MessageUID == string(note.UID) || !inbox.ValidAck(ack.Spec) || ack.Name != inbox.AckName(string(note.UID), note.Spec.Recipient.UID) {
				t.Fatal("unbound ack fixture")
			}
			return ack
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := noteMessage(t, childNote("bad-ack-"+tc.name, inbox.KindQuestion), idUID(400+i), true)
			parent := liveSession(recipient)
			var ack *coord.InboxReadAck
			if tc.bad != nil {
				ack = tc.bad(t, note)
			}
			absent := noteReader(t, note, ack, tc.fail, nil)
			if got := evaluateStored(t, absent, note)[note.Name]; got.Decision != Unknown {
				t.Fatalf("bad ack without the parent = %s", got.Decision)
			}
			live := noteReader(t, note, ack, tc.fail, parent)
			got := evaluateStored(t, live, note)[note.Name]
			requireDecision(t, got, Kept)
			if len(got.Observations) != 2 || got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
				t.Fatalf("live parent with a bad ack: %#v", got)
			}
			recheckRefusesDelete(t, live, note, got.Summary)
		})
	}
}

// A parent read that is not present or absent stays Unknown before the ack.
// On main the valid ack below is release-eligible. Removing the Unknown return
// makes this Released, and the recheck can delete it.
func TestParentNoteUnknownParentWithAValidAckIsUnknown(t *testing.T) {
	note := noteMessage(t, childNote("unknown-parent-ack", inbox.KindBlocker), idUID(420), true)
	ack := sessionAck(t, note, idUID(421))
	base := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), ack.DeepCopy()).Build()
	if got := evaluateStored(t, base, note)[note.Name]; got.Decision != Released || len(got.Observations) < 2 || got.Observations[1].Observed != "acknowledged" {
		t.Fatalf("valid ack with the parent gone: %#v", got)
	}
	reader := &hookReader{Reader: base, get: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
		if _, ok := obj.(*devv1.AgentSession); ok && key.Name == recipient.Name {
			return apierrors.NewServiceUnavailable("parent read failed")
		}
		return base.Get(ctx, key, obj)
	}}
	got := evaluateStored(t, reader, note)[note.Name]
	requireDecision(t, got, Unknown)
	if got.Observations != nil {
		t.Fatalf("unknown parent recorded observations: %#v", got.Observations)
	}
	recheckRefusesDelete(t, reader, note, got.Summary)
}

func noteReader(t *testing.T, note *coord.InboxMessage, ack *coord.InboxReadAck, failAck bool, parent *devv1.AgentSession) client.Reader {
	t.Helper()
	objects := []client.Object{note.DeepCopy()}
	if ack != nil {
		objects = append(objects, ack.DeepCopy())
	}
	if parent != nil {
		objects = append(objects, parent.DeepCopy())
	}
	base := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(objects...).Build()
	if !failAck {
		return base
	}
	return &hookReader{Reader: base, get: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
		if _, ok := obj.(*coord.InboxReadAck); ok {
			return apierrors.NewServiceUnavailable("ack read failed")
		}
		return base.Get(ctx, key, obj)
	}}
}

func recheckRefusesDelete(t *testing.T, reader client.Reader, note *coord.InboxMessage, summary Summary) {
	t.Helper()
	store, ok := reader.(client.Client)
	if !ok {
		store = reader.(*hookReader).Reader.(client.Client)
	}
	archive := newMemArchive()
	runnable := &Runnable{
		Reader: reader, Client: store, Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		metrics: newRetentionMetrics(), Archive: archive, Slots: shelf.NewExecSlots(1),
		Now: func() time.Time { return time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC) },
	}
	if _, st := runnable.releaseRechecked(context.Background(), summary); !st.refused || st.stop != "" || st.done {
		t.Fatalf("recheck %#v", st)
	}
	status := runnable.release(context.Background(), candidate{item: LookupResult{Result: Result{Summary: summary}}})
	if status.deleted || !status.refused {
		t.Fatalf("release %#v", status)
	}
	if err := store.Get(context.Background(), client.ObjectKey{Namespace: note.Namespace, Name: note.Name}, &coord.InboxMessage{}); err != nil {
		t.Fatalf("note after recheck: %v", err)
	}
}

func TestParentNoteManifestRecipientUID(t *testing.T) {
	sessionNote := noteMessage(t, childNote("manifest-session", inbox.KindQuestion), idUID(210), true)
	callerNote := noteMessage(t, toCaller("manifest-caller", inbox.KindBlocker), idUID(211), false)
	for _, message := range []*coord.InboxMessage{sessionNote, callerNote} {
		t.Run(message.Spec.Key, func(t *testing.T) {
			item := toUnstructured(t, message)
			item.SetGroupVersionKind(kindResource(InboxMessage).gvk)
			summary := summarize(kindResource(InboxMessage), retentionNS, &item)
			if !summary.Valid {
				t.Fatalf("summary %#v", summary)
			}
			body, hash, err := canonicalBytes(&item)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := manifestFor(liveRecord{summary: summary, raw: body, hash: hash})
			if err != nil {
				t.Fatal(err)
			}
			if manifest.RecipientUID != inbox.RecipientFormUID(message.Spec) {
				t.Fatalf("recipient UID = %s, want %s", manifest.RecipientUID, inbox.RecipientFormUID(message.Spec))
			}
		})
	}
	if sessionNote.Spec.Recipient.UID == parentPolicyUID {
		t.Fatal("fixture recipient forms are not distinct")
	}
}

func TestRetentionDecisionsMatchMain(t *testing.T) {
	peer := summaryOf(t, InboxMessage)
	child := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: peer.Sender.Namespace, Name: peer.Sender.Name, UID: types.UID(peer.Sender.UID)}}
	parent := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: peer.Recipient.Namespace, Name: peer.Recipient.Name, UID: types.UID(peer.Recipient.UID)}}
	decideSummary := func(t *testing.T, s Summary, archive map[MessageRef]ArchiveEvidence, objects ...client.Object) Decision {
		t.Helper()
		c := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(objects...).Build()
		got := Evaluate(context.Background(), EvalConfig{Reader: c, OperatorNamespace: retentionNS, ReconciledNamespaces: map[string]bool{"dev-agents": true}, Archive: archive}, []Summary{s})[0]
		if got.RequiredMessage != nil && got.Decision != Unknown {
			t.Fatalf("pending with decision %s", got.Decision)
		}
		return got.Decision
	}
	ack := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: retentionNS, Name: inbox.AckName(peer.UID, peer.Recipient.UID), UID: "99999999-9999-4999-8999-999999999999", ResourceVersion: "1"}, Spec: coord.InboxReadAckSpec{Project: peer.Project, MessageName: peer.Name, MessageUID: peer.UID, Recipient: coord.SessionIdentity{Namespace: peer.Recipient.Namespace, Name: peer.Recipient.Name, UID: peer.Recipient.UID}, Actor: actor()}}
	if !inbox.ValidAck(ack.Spec) {
		t.Fatal("peer ack")
	}
	if decideSummary(t, peer, nil, child) != Kept || decideSummary(t, peer, nil, child, ack) != Kept {
		t.Fatal("peer sender present, with and without ack")
	}
	if decideSummary(t, peer, nil, parent) != Kept {
		t.Fatal("peer recipient present")
	}
	if decideSummary(t, peer, nil) != Released {
		t.Fatal("peer both absent")
	}
	if decideSummary(t, peer, nil, ack, parent) != Released || decideSummary(t, peer, nil, ack) != Released {
		t.Fatal("peer sender gone with ack, recipient present or gone")
	}

	for _, tc := range []struct {
		name string
		kind string
	}{
		{"old question", inbox.KindQuestion},
		{"old result", inbox.KindResult},
		{"old blocker", inbox.KindBlocker},
	} {
		message := noteMessage(t, childNote("main-"+tc.kind, tc.kind), idUID(220), false)
		ackFor := sessionAck(t, message, idUID(221))
		if decideNotes(t, []*coord.InboxMessage{message}, child)[message.Name].Decision != Kept {
			t.Fatalf("%s sender present", tc.name)
		}
		if decideLoaded(t, []*coord.InboxMessage{message}, []client.Object{ackFor}, []*devv1.AgentSession{child})[message.Name].Decision != Kept {
			t.Fatalf("%s sender present with ack", tc.name)
		}
		if decideNotes(t, []*coord.InboxMessage{message}, parent)[message.Name].Decision != Kept {
			t.Fatalf("%s recipient present", tc.name)
		}
		if decideNotes(t, []*coord.InboxMessage{message})[message.Name].Decision != Released {
			t.Fatalf("%s both absent", tc.name)
		}
		if decideLoaded(t, []*coord.InboxMessage{message}, []client.Object{ackFor}, []*devv1.AgentSession{parent})[message.Name].Decision != Released {
			t.Fatalf("%s ack with recipient present", tc.name)
		}
		if decideLoaded(t, []*coord.InboxMessage{message}, []client.Object{ackFor}, nil)[message.Name].Decision != Released {
			t.Fatalf("%s ack with recipient gone", tc.name)
		}
	}

	question := noteMessage(t, childNote("main-question", inbox.KindQuestion), idUID(230), false)
	oldReply := answerTo(t, question, "main-old-reply", idUID(231), false)
	oldReply.Labels = nil
	replyAck := sessionAck(t, oldReply, idUID(232))
	if decideNotes(t, []*coord.InboxMessage{oldReply}, parent)[oldReply.Name].Decision != Kept {
		t.Fatal("old reply sender present")
	}
	if decideLoaded(t, []*coord.InboxMessage{oldReply}, []client.Object{replyAck}, []*devv1.AgentSession{parent, child})[oldReply.Name].Decision != Kept {
		t.Fatal("old reply sender present with ack")
	}
	if decideNotes(t, []*coord.InboxMessage{oldReply}, child)[oldReply.Name].Decision != Kept {
		t.Fatal("old reply recipient present")
	}
	if decideNotes(t, []*coord.InboxMessage{oldReply})[oldReply.Name].Decision != Released {
		t.Fatal("old reply both absent")
	}
	if decideLoaded(t, []*coord.InboxMessage{oldReply}, []client.Object{replyAck}, []*devv1.AgentSession{child})[oldReply.Name].Decision != Released {
		t.Fatal("old reply ack with recipient present")
	}
	if decideLoaded(t, []*coord.InboxMessage{oldReply}, []client.Object{replyAck}, nil)[oldReply.Name].Decision != Released {
		t.Fatal("old reply ack with recipient gone")
	}
	stored := answerTo(t, question, "main-stored-reply", idUID(233), false)
	stored.Labels = nil
	if got := decideNotes(t, []*coord.InboxMessage{question, stored}, parent)[question.Name]; got.Decision != Kept {
		t.Fatalf("old question with a stored reply: %#v", got)
	}

	parentNote := func(key, kind string, n int) *coord.InboxMessage {
		return noteMessage(t, childNote(key, kind), idUID(n), true)
	}
	for _, answered := range []bool{false, true} {
		label := "unanswered"
		if answered {
			label = "answered"
		}
		withReply := func(t *testing.T, note *coord.InboxMessage, n int) []*coord.InboxMessage {
			t.Helper()
			if !answered {
				return []*coord.InboxMessage{note}
			}
			return []*coord.InboxMessage{note, answerTo(t, note, note.Spec.Key+"-answer", idUID(n), false)}
		}
		note := parentNote("main-parent-both-"+label, inbox.KindQuestion, 240)
		if decideNotes(t, withReply(t, note, 241), child, parent)[note.Name].Decision != Kept {
			t.Fatalf("parent note child present parent present %s", label)
		}
		note = parentNote("main-parent-child-"+label, inbox.KindResult, 242)
		if decideNotes(t, withReply(t, note, 243), child)[note.Name].Decision != Kept {
			t.Fatalf("parent note child present parent gone %s", label)
		}
		note = parentNote("main-parent-live-"+label, inbox.KindBlocker, 244)
		if decideNotes(t, withReply(t, note, 245), parent)[note.Name].Decision != Kept {
			t.Fatalf("parent note child gone parent present %s", label)
		}
		note = parentNote("main-parent-gone-"+label, inbox.KindQuestion, 246)
		if decideNotes(t, withReply(t, note, 247))[note.Name].Decision != Released {
			t.Fatalf("parent note both gone %s", label)
		}
	}
	acked := parentNote("main-parent-ack-gone", inbox.KindQuestion, 248)
	if decideLoaded(t, []*coord.InboxMessage{acked}, []client.Object{sessionAck(t, acked, idUID(249))}, nil)[acked.Name].Decision != Released {
		t.Fatal("parent note both gone with ack")
	}
	// A reply that fails validation does not hold the note. Both sessions are
	// gone and there is no read ack, which is Released on main.
	badReplyNote := parentNote("main-parent-bad-reply", inbox.KindBlocker, 250)
	badReply := answerTo(t, badReplyNote, "main-parent-bad-answer", idUID(251), false)
	badReply.Name = "not-the-derived-name"
	if decideNotes(t, []*coord.InboxMessage{badReplyNote, badReply})[badReplyNote.Name].Decision != Released {
		t.Fatal("parent note both gone with a reply that does not validate")
	}

	parentReply := answerTo(t, parentNote("main-reply-note", inbox.KindQuestion, 252), "main-parent-reply", idUID(253), false)
	parentReplyAck := sessionAck(t, parentReply, idUID(254))
	if decideNotes(t, []*coord.InboxMessage{parentReply}, parent, child)[parentReply.Name].Decision != Kept {
		t.Fatal("parent reply sender present child present")
	}
	if decideNotes(t, []*coord.InboxMessage{parentReply}, parent)[parentReply.Name].Decision != Kept {
		t.Fatal("parent reply sender present child gone")
	}
	if decideLoaded(t, []*coord.InboxMessage{parentReply}, []client.Object{parentReplyAck}, []*devv1.AgentSession{parent})[parentReply.Name].Decision != Kept {
		t.Fatal("parent reply sender present with ack")
	}
	if decideNotes(t, []*coord.InboxMessage{parentReply}, child)[parentReply.Name].Decision != Kept {
		t.Fatal("parent reply sender gone child present")
	}
	if decideNotes(t, []*coord.InboxMessage{parentReply})[parentReply.Name].Decision != Released {
		t.Fatal("parent reply both gone")
	}
	if decideLoaded(t, []*coord.InboxMessage{parentReply}, []client.Object{parentReplyAck}, []*devv1.AgentSession{child})[parentReply.Name].Decision != Released {
		t.Fatal("parent reply ack with child present")
	}
	if decideLoaded(t, []*coord.InboxMessage{parentReply}, []client.Object{parentReplyAck}, nil)[parentReply.Name].Decision != Released {
		t.Fatal("parent reply ack with child gone")
	}

	readAck := summaryOf(t, InboxReadAck)
	liveMessage := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: readAck.Message.Namespace, Name: readAck.Message.Name, UID: types.UID(readAck.Message.UID), ResourceVersion: "30"}}
	if decideSummary(t, readAck, map[MessageRef]ArchiveEvidence{readAck.Message: {Known: true, Holds: true}}, liveMessage) != Kept {
		t.Fatal("read ack message live")
	}
	if decideSummary(t, readAck, map[MessageRef]ArchiveEvidence{readAck.Message: {Known: true, Holds: true}}) != Released {
		t.Fatal("read ack archive holds")
	}
	if decideSummary(t, readAck, map[MessageRef]ArchiveEvidence{readAck.Message: {Known: true, Holds: false}}) != Kept {
		t.Fatal("read ack archive misses")
	}
	if decideSummary(t, readAck, nil) != Unknown {
		t.Fatal("read ack evidence unknown")
	}
	unreadable := &hookReader{get: func(context.Context, client.ObjectKey, client.Object) error {
		return apierrors.NewServiceUnavailable("unavailable")
	}}
	if got := Evaluate(context.Background(), config(unreadable), []Summary{readAck}); got[0].Decision != Unknown {
		t.Fatal("read ack message read failed")
	}

	claim := summaryOf(t, TaskClaim)
	owner := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: claim.Owner.Namespace, Name: claim.Owner.Name, UID: types.UID(claim.Owner.UID)}}
	if decideSummary(t, claim, nil, owner) != Kept || decideSummary(t, claim, nil) != Released {
		t.Fatal("task claim")
	}
	published := summaryOf(t, PublishedContext)
	source := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: published.Source.Namespace, Name: published.Source.Name, UID: types.UID(published.Source.UID)}}
	if decideSummary(t, published, nil, source) != Kept || decideSummary(t, published, nil) != Released {
		t.Fatal("published context")
	}
	if got := Evaluate(context.Background(), config(unreadable), []Summary{claim, published}); got[0].Decision != Unknown || got[1].Decision != Unknown {
		t.Fatalf("unknown session reads %#v %#v", got[0].Decision, got[1].Decision)
	}
	if decideSummary(t, Summary{Kind: TaskClaim, Namespace: retentionNS, Valid: true}, nil) != Unknown {
		t.Fatal("empty task claim identity")
	}
	if got := Evaluate(context.Background(), EvalConfig{OperatorNamespace: retentionNS, ReconciledNamespaces: map[string]bool{"dev-agents": true}}, []Summary{claim}); got[0].Decision != Unknown {
		t.Fatal("nil reader")
	}
	unsupportedSpec := childNote("main-unsupported", "nope")
	unsupported := &coord.InboxMessage{
		TypeMeta: metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: retentionNS, Name: inbox.MessageNameFor(unsupportedSpec), UID: idUID(260), ResourceVersion: "30",
		},
		Spec: unsupportedSpec,
	}
	if decideNotes(t, []*coord.InboxMessage{unsupported})[unsupported.Name].Decision != Unknown {
		t.Fatal("unsupported kind")
	}
}

func TestCallerRecipientNoteUsesPerRecordPath(t *testing.T) {
	exercisePerRecordPath(t, "caller-note", func(t *testing.T, n int) *coord.InboxMessage {
		t.Helper()
		return noteMessage(t, toCaller(fmt.Sprintf("per-note-%d", n), inbox.KindResult), idUID(n), false)
	})
}

func TestCallerReplyUsesPerRecordPath(t *testing.T) {
	exercisePerRecordPath(t, "caller-reply", func(t *testing.T, n int) *coord.InboxMessage {
		t.Helper()
		note := noteMessage(t, toCaller(fmt.Sprintf("per-reply-note-%d", n), inbox.KindQuestion), idUID(n), false)
		return answerTo(t, note, fmt.Sprintf("per-reply-%d", n), idUID(n+1), true)
	})
}

func exercisePerRecordPath(t *testing.T, name string, build func(*testing.T, int) *coord.InboxMessage) {
	t.Helper()
	t.Run("mark then immature grace then delete", func(t *testing.T) {
		message := build(t, 300)
		rig := newCallerRig(t, message)
		if got := rig.pass(); got != resultComplete || len(rig.archive.puts) != 1 || rig.archive.puts[0].Event == nil || rig.archive.puts[0].Event.State != "absent" || len(rig.deletes.names) != 0 {
			t.Fatalf("mark %s puts %d deletes %v", got, len(rig.archive.puts), rig.deletes.names)
		}
		key := lookupKey(identityOf(rig.summary(t)))
		if rig.archive.grace[key] == nil || !rig.archive.grace[key].Equal(rig.now) || rig.archive.latest[key].State != "absent" {
			t.Fatalf("grace %#v latest %#v", rig.archive.grace[key], rig.archive.latest[key])
		}
		opened := *rig.archive.grace[key]
		rig.now = opened.Add(time.Hour)
		if got := rig.pass(); got != resultComplete || len(rig.deletes.names) != 0 || len(rig.archive.puts) != 1 || rig.archive.grace[key] == nil || !rig.archive.grace[key].Equal(opened) {
			t.Fatalf("immature %s deletes %v puts %d", got, rig.deletes.names, len(rig.archive.puts))
		}
		if err := rig.store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{}); err != nil {
			t.Fatal(err)
		}
		rig.now = opened.Add(DefaultGrace)
		rig.steps = nil
		if got := rig.pass(); got != resultComplete || len(rig.deletes.names) != 1 || rig.deletes.names[0] != message.Name {
			t.Fatalf("delete %s deletes %v steps %v", got, rig.deletes.names, rig.steps)
		}
		if !verifiedBeforeDelete(rig.steps) {
			t.Fatalf("verify order %v", rig.steps)
		}
		if err := rig.store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{}); !apierrors.IsNotFound(err) {
			t.Fatalf("record after delete: %v", err)
		}
	})
	t.Run("hold stops delete", func(t *testing.T) {
		message := build(t, 320)
		rig := newCallerRig(t, message)
		if got := rig.pass(); got != resultComplete || len(rig.archive.puts) != 1 || len(rig.deletes.names) != 0 {
			t.Fatalf("mark %s", got)
		}
		key := lookupKey(identityOf(rig.summary(t)))
		opened := *rig.archive.grace[key]
		holdAt := opened.Add(time.Hour)
		rig.archive.latest[key] = protocol.CoordArchiveEvent{State: "hold", At: holdAt}
		second := rig.archive.puts[0]
		ev := *second.Event
		ev.At = opened
		second.Event = &ev
		if _, err := rig.archive.Put(context.Background(), second, true); err != nil {
			t.Fatal(err)
		}
		if rig.archive.grace[key] == nil || !rig.archive.grace[key].Equal(opened) || rig.archive.latest[key].State != "hold" || !rig.archive.latest[key].At.Equal(holdAt) {
			t.Fatalf("second marked put grace %v latest %#v", rig.archive.grace[key], rig.archive.latest[key])
		}
		rig.now = opened.Add(DefaultGrace)
		if got := rig.pass(); got != resultComplete || len(rig.deletes.names) != 0 {
			t.Fatalf("hold %s deletes %v", got, rig.deletes.names)
		}
		if err := rig.store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{}); err != nil {
			t.Fatalf("%s missing while held: %v", name, err)
		}
	})
}

// The pass marks the record while the child is gone. At the recheck the
// original UID is present again, or the child read fails. Neither deletes.
// The present recheck resets the mark. Removing that recheck would delete both.
func TestCallerRecheckKeepsAReturnedOrUnreadableChild(t *testing.T) {
	shapes := []struct {
		name  string
		build func(t *testing.T, n int) *coord.InboxMessage
		child coord.SessionIdentity
	}{
		{name: "caller-recipient note", child: sender, build: func(t *testing.T, n int) *coord.InboxMessage {
			t.Helper()
			return noteMessage(t, toCaller(fmt.Sprintf("recheck-note-%d", n), inbox.KindResult), idUID(n), false)
		}},
		{name: "caller reply", child: sender, build: func(t *testing.T, n int) *coord.InboxMessage {
			t.Helper()
			note := noteMessage(t, toCaller(fmt.Sprintf("recheck-reply-note-%d", n), inbox.KindQuestion), idUID(n), false)
			return answerTo(t, note, fmt.Sprintf("recheck-reply-%d", n), idUID(n+1), true)
		}},
	}
	for i, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Run("original uid present at the recheck", func(t *testing.T) {
				message := shape.build(t, 600+i*10)
				rig, sessionGets := rigUntilRecheck(t, message, shape.child, false)
				if got := rig.pass(); got != resultComplete || len(rig.archive.puts) != 1 || rig.archive.puts[0].Event == nil || rig.archive.puts[0].Event.State != "absent" || len(rig.deletes.names) != 0 || *sessionGets != 1 {
					t.Fatalf("mark %s puts %d deletes %v session reads %d", got, len(rig.archive.puts), rig.deletes.names, *sessionGets)
				}
				key := lookupKey(identityOf(rig.summary(t)))
				if rig.archive.grace[key] == nil || rig.archive.latest[key].State != "absent" {
					t.Fatalf("grace %#v latest %#v", rig.archive.grace[key], rig.archive.latest[key])
				}
				rig.now = rig.archive.grace[key].Add(DefaultGrace)
				if got := rig.pass(); got != resultComplete || len(rig.deletes.names) != 0 || *sessionGets != 3 {
					t.Fatalf("present recheck %s deletes %v session reads %d", got, rig.deletes.names, *sessionGets)
				}
				if rig.archive.grace[key] != nil || rig.archive.latest[key].State != "present" || len(rig.archive.marks) != 2 || rig.archive.marks[0].Event.State != "hold" || rig.archive.marks[1].Event.State != "present" {
					t.Fatalf("mark was not reset: grace %v latest %#v marks %#v", rig.archive.grace[key], rig.archive.latest[key], rig.archive.marks)
				}
				if err := rig.store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{}); err != nil {
					t.Fatalf("record after the present recheck: %v", err)
				}
			})
			t.Run("child read fails at the recheck", func(t *testing.T) {
				message := shape.build(t, 700+i*10)
				rig, sessionGets := rigUntilRecheck(t, message, shape.child, true)
				if got := rig.pass(); got != resultComplete || len(rig.deletes.names) != 0 || len(rig.archive.puts) != 1 || *sessionGets != 1 {
					t.Fatalf("mark %s deletes %v session reads %d", got, rig.deletes.names, *sessionGets)
				}
				key := lookupKey(identityOf(rig.summary(t)))
				opened := *rig.archive.grace[key]
				rig.now = opened.Add(DefaultGrace)
				if got := rig.pass(); got != resultComplete || len(rig.deletes.names) != 0 || *sessionGets != 3 {
					t.Fatalf("failed recheck %s deletes %v session reads %d", got, rig.deletes.names, *sessionGets)
				}
				if rig.archive.grace[key] == nil || !rig.archive.grace[key].Equal(opened) || rig.archive.latest[key].State != "absent" || len(rig.archive.marks) != 0 {
					t.Fatalf("failed recheck changed the mark: grace %v latest %#v marks %d", rig.archive.grace[key], rig.archive.latest[key], len(rig.archive.marks))
				}
				if err := rig.store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{}); err != nil {
					t.Fatalf("record after the failed recheck: %v", err)
				}
			})
		})
	}
}

// rigUntilRecheck marks from a child that is gone. The third session read is
// the recheck: the original UID, or a failed read.
func rigUntilRecheck(t *testing.T, message *coord.InboxMessage, child coord.SessionIdentity, fail bool) (*callerRig, *int) {
	t.Helper()
	rig := newCallerRig(t, message)
	sessionGets := 0
	live := liveSession(child)
	split := &splitReader{Client: rig.store, lists: &fixtureReader{objects: map[string][]unstructured.Unstructured{
		"InboxMessageList": {toUnstructured(t, rig.message)},
	}}}
	rig.run.Reader = &hookReader{
		list: split.List,
		get: func(ctx context.Context, key client.ObjectKey, obj client.Object) error {
			if _, ok := obj.(*devv1.AgentSession); ok && key.Namespace == child.Namespace && key.Name == child.Name {
				sessionGets++
				if sessionGets < 3 {
					return apierrors.NewNotFound(schema.GroupResource{Resource: "agentsessions"}, key.Name)
				}
				if fail {
					return apierrors.NewServiceUnavailable("child read failed")
				}
				*obj.(*devv1.AgentSession) = *live
				return nil
			}
			return rig.store.Get(ctx, key, obj)
		},
	}
	return rig, &sessionGets
}

func verifiedBeforeDelete(steps []string) bool {
	sawGet := false
	for _, step := range steps {
		if step == "get" {
			sawGet = true
		}
		if len(step) >= 7 && step[:7] == "delete:" {
			return sawGet
		}
	}
	return false
}

type callerRig struct {
	t       *testing.T
	store   client.Client
	archive *memArchive
	deletes *deleteNames
	run     *Runnable
	now     time.Time
	steps   []string
	message *coord.InboxMessage
}

func newCallerRig(t *testing.T, message *coord.InboxMessage) *callerRig {
	t.Helper()
	store := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(message.DeepCopy()).Build()
	stored := &coord.InboxMessage{}
	if err := store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, stored); err != nil {
		t.Fatal(err)
	}
	arch := newMemArchive()
	deletes := &deleteNames{Client: store}
	rig := &callerRig{t: t, store: store, archive: arch, deletes: deletes, now: time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC), message: stored}
	deletes.steps = &rig.steps
	arch.note = func(step string) { rig.steps = append(rig.steps, step) }
	reader := &splitReader{Client: store, lists: &fixtureReader{objects: map[string][]unstructured.Unstructured{
		"InboxMessageList": {toUnstructured(t, stored)},
	}}}
	rig.run = &Runnable{
		Enabled: true, Grace: DefaultGrace, Client: deletes, Reader: reader,
		Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		Kinds: map[Kind]bool{InboxMessage: true},
		Pods:  &staticPods{name: "dev-env-shelf"}, Archive: arch, Slots: shelf.NewExecSlots(2),
		metrics: newRetentionMetrics(), Now: func() time.Time { return rig.now },
	}
	return rig
}

func (r *callerRig) pass() string {
	r.t.Helper()
	return r.run.Pass(context.Background())
}

func (r *callerRig) summary(t *testing.T) Summary {
	t.Helper()
	item := toUnstructured(t, r.message)
	item.SetGroupVersionKind(kindResource(InboxMessage).gvk)
	return summarize(kindResource(InboxMessage), retentionNS, &item)
}

func evaluateStored(t *testing.T, reader client.Reader, messages ...*coord.InboxMessage) map[string]Result {
	t.Helper()
	items := make([]unstructured.Unstructured, 0, len(messages))
	for _, message := range messages {
		items = append(items, toUnstructured(t, message))
	}
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": items}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Result{}
	for _, result := range Evaluate(context.Background(), config(reader), summaries) {
		out[result.Summary.Name] = result
	}
	return out
}

type deleteNames struct {
	client.Client
	names []string
	steps *[]string
}

func (d *deleteNames) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	d.names = append(d.names, obj.GetName())
	if d.steps != nil {
		*d.steps = append(*d.steps, "delete:"+obj.GetName())
	}
	return d.Client.Delete(ctx, obj, opts...)
}

type splitReader struct {
	client.Client
	lists *fixtureReader
}

func (s *splitReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*unstructured.UnstructuredList); ok {
		return s.lists.List(ctx, list, opts...)
	}
	return s.Client.List(ctx, list, opts...)
}
