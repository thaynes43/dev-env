package v1alpha1_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func meatballCaller() *coord.InboxCaller {
	return &coord.InboxCaller{ServiceAccount: coord.MeatballServiceAccount, PolicyUID: "meatball-policy-uid"}
}

func policyActor(base coord.ClaimActor) coord.ClaimActor {
	base.Kind = "policy"
	base.ServiceAccount = coord.MeatballServiceAccount
	return base
}

func parentMessage(key, text string) *coord.InboxMessage {
	m := schemaMessage(key, text)
	m.Name = inbox.MessageNameFor(m.Spec)
	return m
}

func mustCreate(t *testing.T, ctx context.Context, obj client.Object) {
	t.Helper()
	if err := kube.Create(ctx, obj); err != nil {
		t.Fatal(err)
	}
}

func mustRefuse(t *testing.T, ctx context.Context, obj client.Object, snippet string) {
	t.Helper()
	err := kube.Create(ctx, obj)
	if err == nil || !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), snippet) {
		t.Fatalf("got %v, want invalid containing %q", err, snippet)
	}
}

func TestInboxParentSchemaAcceptsAndRefuses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("accept a child note to a session", func(t *testing.T) {
		m := parentMessage("parent-to-session", "which base")
		m.Spec.Kind = inbox.KindQuestion
		mustCreate(t, ctx, m)
	})
	t.Run("accept a result to a session", func(t *testing.T) {
		m := parentMessage("parent-result", "done")
		m.Spec.Kind = inbox.KindResult
		mustCreate(t, ctx, m)
	})
	t.Run("accept a child note to the Meatball account", func(t *testing.T) {
		m := parentMessage("parent-to-meatball", "blocked on a review")
		m.Spec.Kind = inbox.KindBlocker
		m.Spec.Recipient = coord.SessionIdentity{}
		m.Spec.CallerRecipient = meatballCaller()
		m.Name = inbox.MessageNameFor(m.Spec)
		mustCreate(t, ctx, m)
	})
	var ref coord.InboxMessageRef
	t.Run("accept a question an answer can name", func(t *testing.T) {
		m := parentMessage("parent-question", "which base")
		m.Spec.Kind = inbox.KindQuestion
		mustCreate(t, ctx, m)
		ref = coord.InboxMessageRef{Name: m.Name, UID: string(m.UID)}
	})
	t.Run("accept a reply from the Meatball account", func(t *testing.T) {
		m := parentMessage("parent-reply", "main")
		m.Spec.Sender = coord.SessionIdentity{}
		m.Spec.CallerSender = meatballCaller()
		m.Spec.Actor = policyActor(m.Spec.Actor)
		m.Spec.Kind = inbox.KindReply
		m.Spec.InReplyTo = &ref
		m.Name = inbox.MessageNameFor(m.Spec)
		mustCreate(t, ctx, m)
	})
	t.Run("accept a plain message from the Meatball account", func(t *testing.T) {
		m := parentMessage("parent-plain", "hello child")
		m.Spec.Sender = coord.SessionIdentity{}
		m.Spec.CallerSender = meatballCaller()
		m.Spec.Actor = policyActor(m.Spec.Actor)
		m.Name = inbox.MessageNameFor(m.Spec)
		mustCreate(t, ctx, m)
	})
	t.Run("accept a peer plain message", func(t *testing.T) {
		mustCreate(t, ctx, parentMessage("parent-peer", "hello peer"))
	})

	t.Run("refuse both sender forms", func(t *testing.T) {
		m := parentMessage("parent-both-senders", "no")
		m.Spec.CallerSender = meatballCaller()
		mustRefuse(t, ctx, m, "exactly one of sender and callerSender")
	})
	t.Run("refuse both recipient forms", func(t *testing.T) {
		m := parentMessage("parent-both-recipients", "no")
		m.Spec.CallerRecipient = meatballCaller()
		mustRefuse(t, ctx, m, "exactly one of recipient and callerRecipient")
	})
	t.Run("refuse a plain message with callerSender and callerRecipient", func(t *testing.T) {
		m := parentMessage("parent-plain-both-callers", "no")
		m.Spec.Sender = coord.SessionIdentity{}
		m.Spec.Recipient = coord.SessionIdentity{}
		m.Spec.CallerSender = meatballCaller()
		m.Spec.CallerRecipient = meatballCaller()
		m.Spec.Actor = policyActor(m.Spec.Actor)
		m.Name = inbox.MessageNameFor(m.Spec)
		mustRefuse(t, ctx, m, "a plain message from the Meatball account has a session recipient")
	})
	t.Run("refuse a reply with no inReplyTo", func(t *testing.T) {
		m := parentMessage("parent-reply-missing", "no")
		m.Spec.Kind = inbox.KindReply
		mustRefuse(t, ctx, m, "every reply, and only a reply, names the question it answers")
	})
	t.Run("refuse a policy actor on a session sender", func(t *testing.T) {
		m := parentMessage("parent-policy-on-session", "no")
		m.Spec.Actor = policyActor(m.Spec.Actor)
		mustRefuse(t, ctx, m, "actor kind is session for a session sender and policy for a caller sender")
	})
	t.Run("refuse a session actor on a caller sender", func(t *testing.T) {
		m := parentMessage("parent-session-on-caller", "no")
		m.Spec.Kind = inbox.KindQuestion
		m.Spec.Sender = coord.SessionIdentity{}
		m.Spec.CallerSender = meatballCaller()
		mustRefuse(t, ctx, m, "actor kind is session for a session sender and policy for a caller sender")
	})
	t.Run("refuse an unknown kind", func(t *testing.T) {
		m := parentMessage("parent-bad-kind", "no")
		m.Spec.Kind = "approval"
		mustRefuse(t, ctx, m, "kind")
	})

	t.Run("accept a session actor on a task claim", func(t *testing.T) {
		mustCreate(t, ctx, schemaClaim("task:parent-session"))
	})
	t.Run("accept a coordinator actor on a task claim", func(t *testing.T) {
		c := schemaClaim("task:parent-coordinator")
		c.Spec.Actor.Kind = "coordinator"
		mustCreate(t, ctx, c)
	})
	t.Run("refuse policy on a TaskClaim", func(t *testing.T) {
		c := schemaClaim("task:parent-policy")
		c.Spec.Actor = policyActor(c.Spec.Actor)
		mustRefuse(t, ctx, c, "a task claim actor is not a caller policy")
	})
	t.Run("accept a session actor on a published context", func(t *testing.T) {
		mustCreate(t, ctx, schemaContext("parent-session"))
	})
	t.Run("refuse policy on a PublishedContext", func(t *testing.T) {
		c := schemaContext("parent-policy")
		c.Spec.Actor = policyActor(c.Spec.Actor)
		mustRefuse(t, ctx, c, "a published context actor is not a caller policy")
	})
}

func TestInboxStoredObjectStillValidatesAndUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := parentMessage("legacy-peer", "stored by the previous schema")
	mustCreate(t, ctx, m)
	got := &coord.InboxMessage{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: m.Name}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Sender != m.Spec.Sender || got.Spec.Recipient != m.Spec.Recipient || got.Spec.CallerSender != nil || got.Spec.CallerRecipient != nil || got.Spec.Kind != "" {
		t.Fatalf("stored shape changed: %#v", got.Spec)
	}
	got.Labels = map[string]string{"dev-env.haynesops.com/legacy": "kept"}
	got.Annotations = map[string]string{"dev-env.haynesops.com/parent-notice": "2026-11-10T00:00:00Z"}
	if err := kube.Update(ctx, got); err != nil {
		t.Fatal("stored object refused update", err)
	}
	bad := got.DeepCopy()
	bad.Spec.Text = "changed"
	if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
		t.Fatal("spec was not immutable", err)
	}
}
