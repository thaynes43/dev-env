package coordretention

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(message)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, unstructured.Unstructured{Object: raw})
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

func TestParentNoteReleaseFacts(t *testing.T) {
	child := liveSession(sender)
	parent := liveSession(recipient)

	t.Run("caller recipient with a reply releases after the child is gone", func(t *testing.T) {
		note := noteMessage(t, toCaller("caller-answered", inbox.KindQuestion), "44444444-4444-4444-8444-444444444451", false)
		reply := answerTo(t, note, "caller-answer", "44444444-4444-4444-8444-444444444452", true)
		got := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[note.Name]
		requireDecision(t, got, Released)
		if !got.ReplyAck || got.Reply == nil || got.Reply.UID != string(reply.UID) || len(got.Observations) != 2 || got.Observations[1].Observed != "acknowledged" || got.Observations[1].AckUID != string(reply.UID) || got.Observations[1].UID != parentPolicyUID {
			t.Fatalf("reply ack observation: %#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
	})

	t.Run("unanswered note to a live parent session is kept", func(t *testing.T) {
		note := noteMessage(t, childNote("session-unanswered", inbox.KindBlocker), "44444444-4444-4444-8444-444444444453", true)
		got := decideNotes(t, []*coord.InboxMessage{note}, parent)[note.Name]
		requireDecision(t, got, Kept)
		if got.ReplyAck || len(got.Observations) != 2 || got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
			t.Fatalf("live parent observation: %#v", got)
		}
		validateEvent(t, "inboxmessages", "present", got.Observations)
	})

	t.Run("caller recipient whose child still exists is kept", func(t *testing.T) {
		note := noteMessage(t, toCaller("caller-live-child", inbox.KindResult), "44444444-4444-4444-8444-444444444454", false)
		got := decideNotes(t, []*coord.InboxMessage{note}, child)[note.Name]
		requireDecision(t, got, Kept)
		if got.ReplyAck || len(got.Observations) != 1 || got.Observations[0].Observed != "present" || got.Observations[0].UID != sender.UID {
			t.Fatalf("live child observation: %#v", got)
		}
	})

	t.Run("caller recipient with no reply releases after the child is gone", func(t *testing.T) {
		note := noteMessage(t, toCaller("caller-unanswered", inbox.KindBlocker), "44444444-4444-4444-8444-444444444455", false)
		got := decideNotes(t, []*coord.InboxMessage{note})[note.Name]
		requireDecision(t, got, Released)
		if got.ReplyAck || len(got.Observations) != 2 || got.Observations[0].Observed != "not-found" || got.Observations[1].Observed != "not-found" || got.Observations[1].UID != parentPolicyUID || got.Observations[1].Name != "dev-env-meatball" {
			t.Fatalf("caller absence observation: %#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
	})

	t.Run("answered note to a live parent session is kept", func(t *testing.T) {
		note := noteMessage(t, childNote("session-answered", inbox.KindQuestion), "44444444-4444-4444-8444-444444444456", true)
		reply := answerTo(t, note, "session-answer", "44444444-4444-4444-8444-444444444457", false)
		got := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[note.Name]
		requireDecision(t, got, Kept)
		if got.ReplyAck || got.Reply != nil || len(got.Observations) != 2 || got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
			t.Fatalf("live parent kept an answered note: %#v", got)
		}
		validateEvent(t, "inboxmessages", "present", got.Observations)
		replyResult := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[reply.Name]
		requireDecision(t, replyResult, Kept)
	})
	t.Run("answered note is released once the parent session is gone", func(t *testing.T) {
		note := noteMessage(t, childNote("session-parent-gone", inbox.KindQuestion), "44444444-4444-4444-8444-444444444458", true)
		reply := answerTo(t, note, "session-parent-gone-answer", "44444444-4444-4444-8444-444444444459", false)
		got := decideNotes(t, []*coord.InboxMessage{note, reply})[note.Name]
		requireDecision(t, got, Released)
		if !got.ReplyAck || got.Reply == nil || got.Reply.UID != string(reply.UID) || got.Observations[1].Observed != "acknowledged" || got.Observations[1].AckUID != string(reply.UID) {
			t.Fatalf("gone parent reply ack: %#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
	})
}

func TestOldInboxDecisionsStay(t *testing.T) {
	child := liveSession(sender)
	parent := liveSession(recipient)
	cases := []struct {
		name    string
		spec    coord.InboxMessageSpec
		uid     types.UID
		present []*devv1.AgentSession
		want    Decision
	}{
		{"peer sender present", messageSpec(), "44444444-4444-4444-8444-444444444461", []*devv1.AgentSession{child, parent}, Kept},
		{"peer both absent", messageSpec(), "44444444-4444-4444-8444-444444444462", nil, Released},
		{"peer recipient present", messageSpec(), "44444444-4444-4444-8444-444444444463", []*devv1.AgentSession{parent}, Kept},
		{"old question sender present", childNote("old-question-present", inbox.KindQuestion), "44444444-4444-4444-8444-444444444464", []*devv1.AgentSession{child, parent}, Kept},
		{"old question both absent", childNote("old-question-absent", inbox.KindQuestion), "44444444-4444-4444-8444-444444444465", nil, Released},
		{"old question recipient present", childNote("old-question-recipient", inbox.KindQuestion), "44444444-4444-4444-8444-444444444466", []*devv1.AgentSession{parent}, Kept},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := noteMessage(t, tc.spec, tc.uid, false)
			got := decideNotes(t, []*coord.InboxMessage{message}, tc.present...)[message.Name]
			requireDecision(t, got, tc.want)
			if got.ReplyAck {
				t.Fatal("old record used a reply ack")
			}
		})
	}

	t.Run("old reply sender present", func(t *testing.T) {
		question := noteMessage(t, childNote("old-reply-question", inbox.KindQuestion), "44444444-4444-4444-8444-444444444467", false)
		reply := answerTo(t, question, "old-reply", "44444444-4444-4444-8444-444444444468", false)
		reply.Labels = nil
		got := decideNotes(t, []*coord.InboxMessage{reply}, child, parent)[reply.Name]
		requireDecision(t, got, Kept)
	})
	t.Run("old reply both absent", func(t *testing.T) {
		question := noteMessage(t, childNote("old-reply-absent-question", inbox.KindQuestion), "44444444-4444-4444-8444-444444444469", false)
		reply := answerTo(t, question, "old-reply-absent", "44444444-4444-4444-8444-44444444446a", false)
		reply.Labels = nil
		got := decideNotes(t, []*coord.InboxMessage{reply})[reply.Name]
		requireDecision(t, got, Released)
	})
	t.Run("old reply recipient present", func(t *testing.T) {
		question := noteMessage(t, childNote("old-reply-recipient-question", inbox.KindQuestion), "44444444-4444-4444-8444-44444444446b", false)
		reply := answerTo(t, question, "old-reply-recipient", "44444444-4444-4444-8444-44444444446c", false)
		reply.Labels = nil
		// The reply's recipient is the question's sender.
		got := decideNotes(t, []*coord.InboxMessage{reply}, child)[reply.Name]
		requireDecision(t, got, Kept)
	})
	t.Run("peer reply does not ack an old question", func(t *testing.T) {
		question := noteMessage(t, childNote("peer-question", inbox.KindQuestion), "44444444-4444-4444-8444-44444444446d", false)
		reply := answerTo(t, question, "peer-answer", "44444444-4444-4444-8444-44444444446e", false)
		reply.Labels = nil
		got := decideNotes(t, []*coord.InboxMessage{question, reply}, parent)[question.Name]
		requireDecision(t, got, Kept)
		if got.ReplyAck {
			t.Fatal("peer reply counted as an ack")
		}
	})
}

func TestCallerSenderFollowsSection10(t *testing.T) {
	child := liveSession(recipient)
	t.Run("live child is kept", func(t *testing.T) {
		message := noteMessage(t, fromCaller("from-caller-live", ""), "44444444-4444-4444-8444-444444444471", false)
		got := decideNotes(t, []*coord.InboxMessage{message}, child)[message.Name]
		requireDecision(t, got, Kept)
		if got.Observations[1].Observed != "present" || got.Observations[1].UID != recipient.UID {
			t.Fatalf("%#v", got.Observations)
		}
		validateEvent(t, "inboxmessages", "present", got.Observations)
	})
	t.Run("gone child is released", func(t *testing.T) {
		message := noteMessage(t, fromCaller("from-caller-gone", ""), "44444444-4444-4444-8444-444444444472", false)
		got := decideNotes(t, []*coord.InboxMessage{message})[message.Name]
		requireDecision(t, got, Released)
		if got.ReplyAck || got.Observations[0].UID != parentPolicyUID || got.Observations[0].Observed != "not-found" || got.Observations[1].Observed != "not-found" {
			t.Fatalf("%#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
	})
	t.Run("meatball reply is released once the child is gone", func(t *testing.T) {
		note := noteMessage(t, toCaller("meatball-reply-note", inbox.KindQuestion), "44444444-4444-4444-8444-444444444473", false)
		reply := answerTo(t, note, "meatball-reply", "44444444-4444-4444-8444-444444444474", true)
		got := decideNotes(t, []*coord.InboxMessage{reply})[reply.Name]
		requireDecision(t, got, Released)
		validateEvent(t, "inboxmessages", "absent", got.Observations)
	})
	t.Run("meatball reply is kept while the child exists", func(t *testing.T) {
		note := noteMessage(t, toCaller("meatball-reply-live-note", inbox.KindQuestion), "44444444-4444-4444-8444-444444444475", false)
		reply := answerTo(t, note, "meatball-reply-live", "44444444-4444-4444-8444-444444444476", true)
		got := decideNotes(t, []*coord.InboxMessage{reply}, liveSession(sender))[reply.Name]
		requireDecision(t, got, Kept)
	})
	t.Run("a policy UID that is not a Kubernetes UID stays unknown", func(t *testing.T) {
		spec := toCaller("caller-wide-uid", inbox.KindQuestion)
		spec.CallerRecipient.PolicyUID = "meatball-policy-uid"
		message := noteMessage(t, spec, "44444444-4444-4444-8444-444444444477", false)
		got := decideNotes(t, []*coord.InboxMessage{message})[message.Name]
		requireDecision(t, got, Unknown)
		if got.Observations != nil {
			t.Fatalf("unknown recorded observations: %#v", got.Observations)
		}
	})
}

func TestRecheckReadsTheStoredReply(t *testing.T) {
	note := noteMessage(t, childNote("recheck-answered", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444a1", true)
	reply := answerTo(t, note, "recheck-answer", "44444444-4444-4444-8444-4444444444a2", false)
	// The parent session is gone, so the stored reply is the ack. A live parent
	// keeps the note and does not need the reply.
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, note)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summary %#v %v", summaries, err)
	}
	reader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy()).Build()
	decision, _, observations, replyAck, proven := decide(context.Background(), config(reader), summaries[0], map[string]memoSession{}, nil)
	if decision != Released || !replyAck || proven == nil || proven.UID != string(reply.UID) || observations[1].AckUID != string(reply.UID) {
		t.Fatalf("recheck decision %s ack %v reply %v %#v", decision, replyAck, proven, observations)
	}

	unanswered := noteMessage(t, childNote("recheck-open", inbox.KindBlocker), "44444444-4444-4444-8444-4444444444a3", true)
	openSummary, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, unanswered)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(openSummary) != 1 {
		t.Fatal(err)
	}
	parent := liveSession(recipient)
	openReader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(unanswered.DeepCopy(), parent).Build()
	decision, _, _, replyAck, _ = decide(context.Background(), config(openReader), openSummary[0], map[string]memoSession{}, nil)
	if decision != Kept || replyAck {
		t.Fatalf("unanswered recheck %s ack %v", decision, replyAck)
	}
}

func TestRecheckUsesTheSameReplyRuleAsThePass(t *testing.T) {
	callerNote := noteMessage(t, toCaller("recheck-caller", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444a4", true)
	callerReply := answerTo(t, callerNote, "recheck-caller-answer", "44444444-4444-4444-8444-4444444444a5", true)
	delete(callerReply.Labels, inbox.ParentNoteLabel)
	got := decideNotes(t, []*coord.InboxMessage{callerNote, callerReply})[callerNote.Name]
	requireDecision(t, got, Released)
	if !got.ReplyAck || got.Observations[1].Observed != "acknowledged" || got.Observations[1].AckUID != string(callerReply.UID) {
		t.Fatalf("pass %#v", got)
	}
	rechecked := recheckOnly(t, callerNote, callerReply)
	requireDecision(t, rechecked, Released)
	if !rechecked.ReplyAck || rechecked.Observations[1].Observed != "acknowledged" || rechecked.Observations[1].AckUID != string(callerReply.UID) {
		t.Fatalf("recheck %#v", rechecked)
	}

	sessionNote := noteMessage(t, childNote("recheck-old", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444a6", true)
	sessionReply := answerTo(t, sessionNote, "recheck-old-answer", "44444444-4444-4444-8444-4444444444a7", false)
	delete(sessionReply.Labels, inbox.ParentNoteLabel)
	parent := liveSession(recipient)
	got = decideNotes(t, []*coord.InboxMessage{sessionNote, sessionReply}, parent)[sessionNote.Name]
	requireDecision(t, got, Kept)
	if got.ReplyAck {
		t.Fatal("an unlabelled session reply acked the pass")
	}
	rechecked = recheckOnly(t, sessionNote, sessionReply, parent)
	requireDecision(t, rechecked, Kept)
	if rechecked.ReplyAck {
		t.Fatal("an unlabelled session reply acked the recheck")
	}
}

func recheckOnly(t *testing.T, note *coord.InboxMessage, objects ...client.Object) Result {
	t.Helper()
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, note)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summary %#v %v", summaries, err)
	}
	stored := append([]client.Object{note.DeepCopy()}, objects...)
	reader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(stored...).Build()
	decision, _, observations, replyAck, reply := decide(context.Background(), config(reader), summaries[0], map[string]memoSession{}, nil)
	return Result{Summary: summaries[0], Decision: decision, Observations: observations, ReplyAck: replyAck, Reply: reply}
}

func toUnstructured(t *testing.T, message *coord.InboxMessage) unstructured.Unstructured {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(message)
	if err != nil {
		t.Fatal(err)
	}
	return unstructured.Unstructured{Object: raw}
}

func TestParentNoteManifestRecipientUID(t *testing.T) {
	sessionNote := noteMessage(t, childNote("manifest-session", inbox.KindQuestion), "44444444-4444-4444-8444-444444444481", true)
	callerNote := noteMessage(t, toCaller("manifest-caller", inbox.KindBlocker), "44444444-4444-4444-8444-444444444482", false)
	for _, message := range []*coord.InboxMessage{sessionNote, callerNote} {
		t.Run(message.Spec.Key, func(t *testing.T) {
			raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(message)
			if err != nil {
				t.Fatal(err)
			}
			item := &unstructured.Unstructured{Object: raw}
			item.SetGroupVersionKind(kindResource(InboxMessage).gvk)
			summary := summarize(kindResource(InboxMessage), retentionNS, item)
			if !summary.Valid {
				t.Fatalf("summary %#v", summary)
			}
			body, hash, err := canonicalBytes(item)
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

func TestReplyAckIsMarkedWithoutInboxReadAck(t *testing.T) {
	note := noteMessage(t, toCaller("mark-answered", inbox.KindQuestion), "44444444-4444-4444-8444-444444444491", false)
	reply := answerTo(t, note, "mark-answer", "44444444-4444-4444-8444-444444444492", true)
	got := decideNotes(t, []*coord.InboxMessage{note, reply})[note.Name]
	requireDecision(t, got, Released)
	if !got.ReplyAck || got.Reply == nil || got.Reply.UID != string(reply.UID) {
		t.Fatal("reply was not the ack")
	}
	reader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy()).Build()
	archive := newMemArchive()
	runnable := &Runnable{
		Reader: reader, Archive: archive, Slots: shelf.NewExecSlots(1), Namespace: retentionNS,
		metrics: newRetentionMetrics(),
		Now:     func() time.Time { return time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC) },
	}
	status := runnable.markReleased(context.Background(), candidate{item: lookupItem(got)})
	if status.skipped || status.refused || !status.marked || len(archive.puts) != 1 || archive.puts[0].Event == nil || archive.puts[0].Event.State != "absent" {
		t.Fatalf("mark status %#v puts %d", status, len(archive.puts))
	}
	if archive.puts[0].Manifest.RecipientUID != parentPolicyUID {
		t.Fatalf("marked recipient UID %s", archive.puts[0].Manifest.RecipientUID)
	}
}

func lookupItem(result Result) LookupResult { return LookupResult{Result: result} }

func sessionAck(t *testing.T, message *coord.InboxMessage) *coord.InboxReadAck {
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
			UID: types.UID("77777777-7777-4777-8777-777777777777"), ResourceVersion: "40",
		},
		Spec: spec,
	}
}

func TestCallerShapesThatAreNotNamedStayKeptOrUnknown(t *testing.T) {
	t.Run("caller reply with a live child and a read ack is kept", func(t *testing.T) {
		note := noteMessage(t, toCaller("ack-live-note", inbox.KindQuestion), "44444444-4444-4444-8444-444444444481", false)
		reply := answerTo(t, note, "ack-live-reply", "44444444-4444-4444-8444-444444444482", true)
		got := decideLoaded(t, []*coord.InboxMessage{reply}, []client.Object{sessionAck(t, reply)}, []*devv1.AgentSession{liveSession(sender)})[reply.Name]
		requireDecision(t, got, Kept)
		if got.ReplyAck {
			t.Fatal("read ack released a caller reply")
		}
	})
	t.Run("caller plain message with a live recipient and a read ack is kept", func(t *testing.T) {
		message := noteMessage(t, fromCaller("ack-live-plain", ""), "44444444-4444-4444-8444-444444444483", false)
		got := decideLoaded(t, []*coord.InboxMessage{message}, []client.Object{sessionAck(t, message)}, []*devv1.AgentSession{liveSession(recipient)})[message.Name]
		requireDecision(t, got, Kept)
	})
	t.Run("non-plain caller-to-caller record is unknown", func(t *testing.T) {
		spec := fromCaller("caller-to-caller", inbox.KindQuestion)
		spec.Recipient = coord.SessionIdentity{}
		spec.CallerRecipient = parentCaller()
		message := noteMessage(t, spec, "44444444-4444-4444-8444-444444444484", false)
		got := decideNotes(t, []*coord.InboxMessage{message})[message.Name]
		requireDecision(t, got, Unknown)
		if got.Observations != nil || got.ReplyAck || got.Reply != nil {
			t.Fatalf("caller-to-caller %#v", got)
		}
	})
}

func TestReplyBindsRejectsEachField(t *testing.T) {
	sessionNote := noteMessage(t, childNote("bind-session", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444e1", true)
	sessionReply := answerTo(t, sessionNote, "bind-session-answer", "44444444-4444-4444-8444-4444444444e2", false)
	callerNote := noteMessage(t, toCaller("bind-caller", inbox.KindBlocker), "44444444-4444-4444-8444-4444444444e3", false)
	callerReply := answerTo(t, callerNote, "bind-caller-answer", "44444444-4444-4444-8444-4444444444e4", true)
	fromSpec := fromCaller("bind-from-caller", inbox.KindResult)
	fromNote := noteMessage(t, fromSpec, "44444444-4444-4444-8444-4444444444e5", false)
	back := messageSpec()
	back.Key, back.Kind = "bind-back", inbox.KindReply
	back.InReplyTo = &coord.InboxMessageRef{Name: fromNote.Name, UID: string(fromNote.UID)}
	back.Sender = fromNote.Spec.Recipient
	back.Recipient = coord.SessionIdentity{}
	back.CallerRecipient = parentCaller()
	backReply := noteMessage(t, back, "44444444-4444-4444-8444-4444444444e6", true)

	pairs := []struct {
		name  string
		note  *coord.InboxMessage
		reply *coord.InboxMessage
	}{
		{"session", sessionNote, sessionReply},
		{"caller recipient", callerNote, callerReply},
		{"caller sender", fromNote, backReply},
	}
	for _, pair := range pairs {
		note := mustSummary(t, pair.note)
		if !replyBinds(note, *pair.reply) {
			t.Fatalf("%s reply does not bind", pair.name)
		}
	}
	otherUID := "99999999-9999-4999-8999-999999999999"
	otherName := inbox.MessageName(sender, "some-other-note")
	mutate := func(reply *coord.InboxMessage, apply func(*coord.InboxMessage)) *coord.InboxMessage {
		next := reply.DeepCopy()
		apply(next)
		return next
	}
	cases := []struct {
		name  string
		note  *coord.InboxMessage
		reply *coord.InboxMessage
		apply func(*coord.InboxMessage)
	}{
		{"project", sessionNote, sessionReply, func(m *coord.InboxMessage) { m.Spec.Project = "foreign" }},
		{"note name", sessionNote, sessionReply, func(m *coord.InboxMessage) { m.Spec.InReplyTo.Name = otherName }},
		{"note UID", sessionNote, sessionReply, func(m *coord.InboxMessage) { m.Spec.InReplyTo.UID = otherUID }},
		{"sender party", sessionNote, sessionReply, func(m *coord.InboxMessage) { m.Spec.Sender.UID = otherUID }},
		{"recipient party", sessionNote, sessionReply, func(m *coord.InboxMessage) { m.Spec.Recipient.UID = otherUID }},
		{"caller recipient child party", callerNote, callerReply, func(m *coord.InboxMessage) { m.Spec.Recipient.UID = otherUID }},
		{"caller recipient policy UID", callerNote, callerReply, func(m *coord.InboxMessage) {
			m.Spec.CallerSender.PolicyUID = otherUID
		}},
		{"caller sender party", fromNote, backReply, func(m *coord.InboxMessage) { m.Spec.Sender.UID = otherUID }},
		{"caller sender policy UID", fromNote, backReply, func(m *coord.InboxMessage) {
			m.Spec.CallerRecipient.PolicyUID = otherUID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note := mustSummary(t, tc.note)
			if replyBinds(note, *mutate(tc.reply, tc.apply)) {
				t.Fatal("mutated reply still binds")
			}
		})
	}
}

func mustSummary(t *testing.T, message *coord.InboxMessage) Summary {
	t.Helper()
	item := toUnstructured(t, message)
	s := summarize(kindResource(InboxMessage), retentionNS, &item)
	if !s.Valid {
		t.Fatalf("summary %#v", s)
	}
	return s
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
	if decideSummary(t, peer, nil, child) != Kept {
		t.Fatal("peer sender present")
	}
	if decideSummary(t, peer, nil, parent) != Kept {
		t.Fatal("peer recipient present")
	}
	if decideSummary(t, peer, nil) != Released {
		t.Fatal("peer both absent")
	}
	if decideSummary(t, peer, nil, ack, parent) != Released {
		t.Fatal("peer sender gone with ack")
	}

	question := noteMessage(t, childNote("main-question", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444f1", false)
	if decideNotes(t, []*coord.InboxMessage{question}, child)[question.Name].Decision != Kept {
		t.Fatal("old question sender present")
	}
	if decideNotes(t, []*coord.InboxMessage{question}, parent)[question.Name].Decision != Kept {
		t.Fatal("old question recipient present")
	}
	if decideNotes(t, []*coord.InboxMessage{question})[question.Name].Decision != Released {
		t.Fatal("old question both absent")
	}
	oldReply := answerTo(t, question, "main-old-reply", "44444444-4444-4444-8444-4444444444f2", false)
	oldReply.Labels = nil
	if decideNotes(t, []*coord.InboxMessage{oldReply}, parent)[oldReply.Name].Decision != Kept {
		t.Fatal("old reply sender present")
	}
	if decideNotes(t, []*coord.InboxMessage{oldReply}, child)[oldReply.Name].Decision != Kept {
		t.Fatal("old reply recipient present")
	}
	if decideNotes(t, []*coord.InboxMessage{oldReply})[oldReply.Name].Decision != Released {
		t.Fatal("old reply both absent")
	}
	stored := answerTo(t, question, "main-stored-reply", "44444444-4444-4444-8444-4444444444f3", false)
	stored.Labels = nil
	if got := decideNotes(t, []*coord.InboxMessage{question, stored}, parent)[question.Name]; got.Decision != Kept || got.ReplyAck {
		t.Fatalf("old question with a stored reply: %#v", got)
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
	unknown := &hookReader{get: func(context.Context, client.ObjectKey, client.Object) error {
		return apierrors.NewServiceUnavailable("unavailable")
	}}
	if got := Evaluate(context.Background(), config(unknown), []Summary{claim, published}); got[0].Decision != Unknown || got[1].Decision != Unknown {
		t.Fatalf("unknown session reads %#v %#v", got[0].Decision, got[1].Decision)
	}
}

func TestReleaseRecheckedStopsWhenTheReplyChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, c client.Client, note, reply *coord.InboxMessage)
	}{
		{name: "malformed reply", change: func(t *testing.T, c client.Client, _, reply *coord.InboxMessage) {
			t.Helper()
			if err := c.Delete(context.Background(), reply); err != nil {
				t.Fatal(err)
			}
			bad := reply.DeepCopy()
			bad.Name = "not-the-derived-name"
			bad.ResourceVersion = ""
			if err := c.Create(context.Background(), bad); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced reply", change: func(t *testing.T, c client.Client, note, reply *coord.InboxMessage) {
			t.Helper()
			if err := c.Delete(context.Background(), reply); err != nil {
				t.Fatal(err)
			}
			next := answerTo(t, note, "replaced-reply", "44444444-4444-4444-8444-4444444444d1", true)
			next.ResourceVersion = ""
			if err := c.Create(context.Background(), next); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "reply removed", change: func(t *testing.T, c client.Client, _, reply *coord.InboxMessage) {
			t.Helper()
			if err := c.Delete(context.Background(), reply); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := noteMessage(t, toCaller("recheck-stop", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444d2", false)
			reply := answerTo(t, note, "recheck-stop-answer", "44444444-4444-4444-8444-4444444444d3", true)
			store := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy()).Build()
			got := evaluateStored(t, store, note, reply)[note.Name]
			requireDecision(t, got, Released)
			if got.Reply == nil || got.Reply.UID != string(reply.UID) || got.Reply.Name != reply.Name {
				t.Fatalf("pass reply %#v", got.Reply)
			}
			runnable := &Runnable{
				Reader: store, Client: store, Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
				metrics: newRetentionMetrics(), Archive: newMemArchive(), Slots: shelf.NewExecSlots(1),
				Now: func() time.Time { return time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC) },
			}
			if _, st := runnable.releaseRechecked(context.Background(), got.Summary, got.Reply); st.refused || st.stop != "" || st.done {
				t.Fatalf("unchanged recheck %#v", st)
			}
			tc.change(t, store, note, reply)
			if _, st := runnable.releaseRechecked(context.Background(), got.Summary, got.Reply); !st.refused || st.stop != "" {
				t.Fatalf("changed recheck %#v", st)
			}
			status := runnable.release(context.Background(), candidate{item: lookupItem(got)})
			if status.deleted || !status.refused {
				t.Fatalf("release %#v", status)
			}
			if err := store.Get(context.Background(), client.ObjectKey{Namespace: note.Namespace, Name: note.Name}, &coord.InboxMessage{}); err != nil {
				t.Fatalf("note after refused release: %v", err)
			}
		})
	}
}

func TestReleaseRecheckedStopsWhenAReplyArrivesAfterThePass(t *testing.T) {
	note := noteMessage(t, toCaller("recheck-late", inbox.KindQuestion), "44444444-4444-4444-8444-4444444444d4", false)
	store := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy()).Build()
	got := evaluateStored(t, store, note)[note.Name]
	requireDecision(t, got, Released)
	if got.Reply != nil || got.ReplyAck {
		t.Fatalf("unanswered pass %#v", got.Reply)
	}
	runnable := &Runnable{
		Reader: store, Client: store, Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		metrics: newRetentionMetrics(), Archive: newMemArchive(), Slots: shelf.NewExecSlots(1),
		Now: func() time.Time { return time.Date(2026, 11, 11, 4, 0, 0, 0, time.UTC) },
	}
	if _, st := runnable.releaseRechecked(context.Background(), got.Summary, got.Reply); st.refused || st.stop != "" || st.done {
		t.Fatalf("unanswered recheck %#v", st)
	}
	reply := answerTo(t, note, "recheck-late-answer", "44444444-4444-4444-8444-4444444444d5", true)
	reply.ResourceVersion = ""
	if err := store.Create(context.Background(), reply); err != nil {
		t.Fatal(err)
	}
	if _, st := runnable.releaseRechecked(context.Background(), got.Summary, nil); !st.refused || st.stop != "" {
		t.Fatalf("late reply recheck %#v", st)
	}
	status := runnable.release(context.Background(), candidate{item: lookupItem(got)})
	if status.deleted || !status.refused {
		t.Fatalf("release %#v", status)
	}
	if err := store.Get(context.Background(), client.ObjectKey{Namespace: note.Namespace, Name: note.Name}, &coord.InboxMessage{}); err != nil {
		t.Fatalf("note after refused release: %v", err)
	}
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

func TestCallerRecipientPairGoesThroughGraceArchiveAndDelete(t *testing.T) {
	note := noteMessage(t, toCaller("grace-note", inbox.KindResult), "44444444-4444-4444-8444-4444444444c1", false)
	reply := answerTo(t, note, "grace-reply", "44444444-4444-4444-8444-4444444444c2", true)
	store := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy()).Build()
	arch := newMemArchive()
	now := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
	past := now.Add(-DefaultGrace - time.Minute).UTC()
	key := lookupKey(protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: note.Spec.Project, Name: note.Name, UID: string(note.UID)})
	arch.grace[key] = timePtr(past)
	arch.latest[key] = protocol.CoordArchiveEvent{State: "absent", At: past}
	arch.revisions[key] = []protocol.CoordArchiveRevision{{ResourceVersion: "seed", SHA256: "abababababababababababababababababababababababababababababababab"}}
	var steps []string
	arch.note = func(step string) { steps = append(steps, step) }
	deletes := &deleteNames{Client: store, steps: &steps}
	reader := &splitReader{Client: store, lists: &fixtureReader{objects: map[string][]unstructured.Unstructured{
		"InboxMessageList": {toUnstructured(t, note), toUnstructured(t, reply)},
	}}}
	runnable := &Runnable{
		Enabled: true, Grace: DefaultGrace, Client: deletes, Reader: reader,
		Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		Kinds: map[Kind]bool{InboxMessage: true},
		Pods:  &staticPods{name: "dev-env-shelf"}, Archive: arch, Slots: shelf.NewExecSlots(2),
		metrics: newRetentionMetrics(), Now: func() time.Time { return now },
	}
	if got := runnable.Pass(context.Background()); got != resultComplete || len(deletes.names) != 2 {
		t.Fatalf("pass %s deletes %v steps %v puts %d", got, deletes.names, steps, len(arch.puts))
	}
	if deletes.names[0] != note.Name || deletes.names[1] != reply.Name {
		t.Fatalf("delete order %v", deletes.names)
	}
	gets := 0
	for _, step := range steps {
		if len(step) >= 7 && step[:7] == "delete:" {
			if gets < 2 {
				t.Fatalf("delete before both archives were verified: %v", steps)
			}
			continue
		}
		if step == "get" {
			gets++
		}
	}
	if len(arch.puts) < 2 || arch.puts[0].Manifest.UID != string(note.UID) || arch.puts[1].Manifest.UID != string(reply.UID) {
		t.Fatalf("archive puts %#v", putUIDs(arch.puts))
	}
	for _, message := range []*coord.InboxMessage{note, reply} {
		err := store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("%s after delete: %v", message.Name, err)
		}
	}

	t.Run("note archive write fails", func(t *testing.T) {
		assertPairStays(t, "note-put", func(arch *memArchive, note, _ *coord.InboxMessage) { arch.failPutUID = string(note.UID) })
	})
	t.Run("reply archive write fails", func(t *testing.T) {
		assertPairStays(t, "reply-put", func(arch *memArchive, _, reply *coord.InboxMessage) { arch.failPutUID = string(reply.UID) })
	})
	t.Run("note archive verify fails", func(t *testing.T) {
		assertPairStays(t, "note-verify", func(arch *memArchive, _, _ *coord.InboxMessage) { arch.hideGet = true })
	})
	t.Run("reply archive verify fails", func(t *testing.T) {
		assertPairStays(t, "reply-verify", func(arch *memArchive, _, _ *coord.InboxMessage) {
			gets := 0
			arch.onGet = func() {
				gets++
				if gets >= 2 {
					arch.hideGet = true
				}
			}
		})
	})
}

func assertPairStays(t *testing.T, name string, prepare func(*memArchive, *coord.InboxMessage, *coord.InboxMessage)) {
	t.Helper()
	note := noteMessage(t, toCaller("stay-note-"+name, inbox.KindQuestion), "44444444-4444-4444-8444-4444444444c3", false)
	reply := answerTo(t, note, "stay-reply-"+name, "44444444-4444-4444-8444-4444444444c4", true)
	store := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy()).Build()
	arch := newMemArchive()
	prepare(arch, note, reply)
	now := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	past := now.Add(-DefaultGrace - time.Minute).UTC()
	key := lookupKey(protocol.CoordArchiveIdentity{Resource: "inboxmessages", Project: note.Spec.Project, Name: note.Name, UID: string(note.UID)})
	arch.grace[key] = timePtr(past)
	arch.latest[key] = protocol.CoordArchiveEvent{State: "absent", At: past}
	arch.revisions[key] = []protocol.CoordArchiveRevision{{ResourceVersion: "seed", SHA256: "abababababababababababababababababababababababababababababababab"}}
	deletes := &deleteNames{Client: store}
	reader := &splitReader{Client: store, lists: &fixtureReader{objects: map[string][]unstructured.Unstructured{
		"InboxMessageList": {toUnstructured(t, note), toUnstructured(t, reply)},
	}}}
	runnable := &Runnable{
		Enabled: true, Grace: DefaultGrace, Client: deletes, Reader: reader,
		Namespace: retentionNS, Reconciled: map[string]bool{"dev-agents": true},
		Kinds: map[Kind]bool{InboxMessage: true},
		Pods:  &staticPods{name: "dev-env-shelf"}, Archive: arch, Slots: shelf.NewExecSlots(2),
		metrics: newRetentionMetrics(), Now: func() time.Time { return now },
	}
	got := runnable.Pass(context.Background())
	if got == resultComplete && len(deletes.names) > 0 {
		t.Fatalf("pass %s deleted %v", got, deletes.names)
	}
	if len(deletes.names) != 0 {
		t.Fatalf("deleted %v on %s", deletes.names, got)
	}
	for _, message := range []*coord.InboxMessage{note, reply} {
		if err := store.Get(context.Background(), client.ObjectKey{Namespace: message.Namespace, Name: message.Name}, &coord.InboxMessage{}); err != nil {
			t.Fatalf("%s missing after refused archive: %v", message.Name, err)
		}
	}
}

func putUIDs(puts []protocol.CoordArchivePutRequest) []string {
	out := make([]string, len(puts))
	for i := range puts {
		out[i] = puts[i].Manifest.UID
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
