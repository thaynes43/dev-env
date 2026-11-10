package coordretention

import (
	"context"
	"testing"
	"time"

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
	objects := make([]client.Object, 0, len(messages)+len(sessions))
	items := make([]unstructured.Unstructured, 0, len(messages))
	for _, message := range messages {
		objects = append(objects, message.DeepCopy())
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(message)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, unstructured.Unstructured{Object: raw})
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
		if !got.ReplyAck || len(got.Observations) != 2 || got.Observations[1].Observed != "acknowledged" || got.Observations[1].AckUID != string(reply.UID) || got.Observations[1].UID != parentPolicyUID {
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

	t.Run("answered note to a live parent session is released", func(t *testing.T) {
		note := noteMessage(t, childNote("session-answered", inbox.KindQuestion), "44444444-4444-4444-8444-444444444456", true)
		reply := answerTo(t, note, "session-answer", "44444444-4444-4444-8444-444444444457", false)
		got := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[note.Name]
		requireDecision(t, got, Released)
		if !got.ReplyAck || got.Observations[1].Observed != "acknowledged" || got.Observations[1].AckUID != string(reply.UID) || got.Observations[1].UID != recipient.UID {
			t.Fatalf("session reply ack: %#v", got)
		}
		validateEvent(t, "inboxmessages", "absent", got.Observations)
		replyResult := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[reply.Name]
		requireDecision(t, replyResult, Kept)
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
	parent := liveSession(recipient)
	summaries, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, note)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summary %#v %v", summaries, err)
	}
	reader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy(), parent).Build()
	decision, _, observations, replyAck := decide(context.Background(), config(reader), summaries[0], map[string]memoSession{}, nil)
	if decision != Released || !replyAck || observations[1].AckUID != string(reply.UID) {
		t.Fatalf("recheck decision %s ack %v %#v", decision, replyAck, observations)
	}

	unanswered := noteMessage(t, childNote("recheck-open", inbox.KindBlocker), "44444444-4444-4444-8444-4444444444a3", true)
	openSummary, err := Summaries(context.Background(), ListConfig{
		Reader:    &fixtureReader{objects: map[string][]unstructured.Unstructured{"InboxMessageList": {toUnstructured(t, unanswered)}}},
		Namespace: retentionNS, Enabled: map[Kind]bool{InboxMessage: true},
	})
	if err != nil || len(openSummary) != 1 {
		t.Fatal(err)
	}
	openReader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(unanswered.DeepCopy(), parent).Build()
	decision, _, _, replyAck = decide(context.Background(), config(openReader), openSummary[0], map[string]memoSession{}, nil)
	if decision != Kept || replyAck {
		t.Fatalf("unanswered recheck %s ack %v", decision, replyAck)
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
	note := noteMessage(t, childNote("mark-answered", inbox.KindQuestion), "44444444-4444-4444-8444-444444444491", true)
	reply := answerTo(t, note, "mark-answer", "44444444-4444-4444-8444-444444444492", false)
	parent := liveSession(recipient)
	got := decideNotes(t, []*coord.InboxMessage{note, reply}, parent)[note.Name]
	requireDecision(t, got, Released)
	if !got.ReplyAck {
		t.Fatal("reply was not the ack")
	}
	reader := fake.NewClientBuilder().WithScheme(retentionScheme(t)).WithObjects(note.DeepCopy(), reply.DeepCopy(), parent).Build()
	archive := newMemArchive()
	runnable := &Runnable{
		Reader: reader, Archive: archive, Slots: shelf.NewExecSlots(1), Namespace: retentionNS,
		Now: func() time.Time { return time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC) },
	}
	status := runnable.markReleased(context.Background(), candidate{item: lookupItem(got)})
	if status.skipped || status.refused || !status.marked || len(archive.puts) != 1 || archive.puts[0].Event == nil || archive.puts[0].Event.State != "absent" {
		t.Fatalf("mark status %#v puts %d", status, len(archive.puts))
	}
	if archive.puts[0].Manifest.RecipientUID != recipient.UID {
		t.Fatalf("marked recipient UID %s", archive.puts[0].Manifest.RecipientUID)
	}
}

func lookupItem(result Result) LookupResult { return LookupResult{Result: result} }
