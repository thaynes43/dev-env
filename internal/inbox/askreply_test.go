package inbox

import (
	"context"
	"errors"
	"maps"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func questionAndReply(t *testing.T, s KubeStore) (*coord.InboxMessage, *coord.InboxMessage) {
	t.Helper()
	ctx := context.Background()
	spec := validMessage()
	spec.Kind = KindQuestion
	q, created, err := s.Create(ctx, spec)
	if err != nil || !created {
		t.Fatal("question", err)
	}
	reply := validMessage()
	reply.Key, reply.Kind = "answer-1", KindReply
	reply.Sender, reply.Recipient = spec.Recipient, spec.Sender
	reply.Actor.PodName, reply.Actor.PodUID = "recipient", "recipient-pod-uid"
	ref := Ref(q)
	reply.InReplyTo = &ref
	r, created, err := s.Create(ctx, reply)
	if err != nil || !created {
		t.Fatal("reply", err)
	}
	return q, r
}

func TestInboxCorrelationIsExactAndLabelled(t *testing.T) {
	ref := coord.InboxMessageRef{Name: MessageName(validMessage().Sender, "q"), UID: "question-uid"}
	for _, tc := range []struct {
		kind  string
		ref   *coord.InboxMessageRef
		valid bool
	}{
		{"", nil, true}, {KindQuestion, nil, true}, {KindReply, &ref, true},
		{"", &ref, false}, {KindQuestion, &ref, false}, {KindReply, nil, false},
		{KindReply, &coord.InboxMessageRef{Name: "message.short", UID: "uid"}, false},
		{KindReply, &coord.InboxMessageRef{Name: ref.Name}, false},
		{"approval", nil, false},
	} {
		spec := validMessage()
		spec.Kind, spec.InReplyTo = tc.kind, tc.ref
		if ValidMessage(spec) != tc.valid {
			t.Fatalf("kind %q ref %v: valid=%v", tc.kind, tc.ref, !tc.valid)
		}
	}
	s := storeFixture(t)
	q, r := questionAndReply(t, s)
	if q.Labels[KindLabel] != KindQuestion || q.Labels[InReplyToLabel] != "" || r.Labels[KindLabel] != KindReply || r.Labels[InReplyToLabel] != inReplyToLabel(Ref(q)) {
		t.Fatal("question/reply labels", q.Labels, r.Labels)
	}
	// A question or reply without its full label set is not a confirmed record.
	for _, m := range []*coord.InboxMessage{q, r} {
		bad := m.DeepCopy()
		bad.Labels = recipientLabels(m.Spec.Project, m.Spec.Recipient)
		if s.validMessage(bad) {
			t.Fatal("legacy label set accepted for", m.Spec.Kind)
		}
	}
	plain := validMessage()
	plain.Key = "plain"
	m, _, err := s.Create(context.Background(), plain)
	if err != nil || m.Labels[KindLabel] != "" || !maps.Equal(m.Labels, messageLabels(plain)) {
		t.Fatal("plain labels changed", err, m.Labels)
	}
}

func TestInboxRepliesBindTheQuestion(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, r := questionAndReply(t, s)
	got, err := s.Replies(ctx, q)
	if err != nil || len(got) != 1 || got[0].UID != r.UID {
		t.Fatal("reply lookup", err, got)
	}
	if _, err := s.Replies(ctx, r); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a reply has replies", err)
	}
	sent, _, err := s.ListSent(ctx, "sample", q.Spec.Sender, 10, "")
	if err != nil || len(sent) != 1 || sent[0].UID != q.UID {
		t.Fatal("sent box", err, sent)
	}
	// A record carrying the question's reply label but another sender is not
	// guessed at: the lookup is Unknown.
	forged := r.DeepCopy()
	forged.ResourceVersion, forged.UID = "", ""
	forged.Spec.Key = "forged"
	forged.Spec.Sender = coord.SessionIdentity{Namespace: "dev-agents", Name: "bystander", UID: "bystander-uid"}
	forged.Name = MessageName(forged.Spec.Sender, forged.Spec.Key)
	forged.Labels = messageLabels(forged.Spec)
	forged.Labels[taskclaim.SourceLabel] = taskclaim.SessionLabel(forged.Spec.Sender)
	if err := s.Client.Create(ctx, forged); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replies(ctx, q); !errors.Is(err, ErrUnavailable) {
		t.Fatal("foreign reply accepted", err)
	}
	if err := s.Client.Delete(ctx, forged); err != nil {
		t.Fatal(err)
	}
	failing := s
	failing.Live = failingList{s.Live}
	if _, err := failing.Replies(ctx, q); !errors.Is(err, ErrUnavailable) {
		t.Fatal("list failure was not Unknown", err)
	}
}

type failingList struct{ client.Reader }

func (failingList) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("synthetic list failure")
}

func TestInboxReplyIndexSelectsOnePartyBounded(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	q, r := questionAndReply(t, s)
	for _, tc := range []struct{ from, to *coord.SessionIdentity }{{&q.Spec.Recipient, nil}, {nil, &q.Spec.Sender}, {nil, nil}} {
		got, err := s.ReplyIndex(ctx, "sample", tc.from, tc.to)
		if err != nil || len(got) != 1 || got[0].UID != r.UID || got[0].Name != r.Name {
			t.Fatal("reply index", err, got)
		}
		replies, err := s.RepliesIn(ctx, q, got)
		if err != nil || len(replies) != 1 || replies[0].UID != r.UID {
			t.Fatal("replies of", err, replies)
		}
	}
	if got, err := s.ReplyIndex(ctx, "sample", &q.Spec.Sender, nil); err != nil || len(got) != 0 {
		t.Fatal("the asker sent no reply", err, got)
	}
	failing := s
	failing.Live = failingList{s.Live}
	if _, err := failing.ReplyIndex(ctx, "sample", nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("list failure was not Unknown", err)
	}
	wrong := *r.DeepCopy()
	wrong.Spec.Sender = coord.SessionIdentity{Namespace: "dev-agents", Name: "bystander", UID: "bystander-uid"}
	if _, err := RepliesOf(q, []coord.InboxMessage{wrong}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a reply from the wrong party was accepted", err)
	}
}
