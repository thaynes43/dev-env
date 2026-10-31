package v1alpha1_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func schemaMessage(key, text string) *coord.InboxMessage {
	c := schemaClaim("task:inbox")
	spec := coord.InboxMessageSpec{Project: "sample", Key: key, Sender: c.Spec.Owner, Recipient: coord.SessionIdentity{Namespace: "dev-agents", Name: "recipient", UID: "recipient-uid"}, Actor: c.Spec.Actor, Provenance: c.Spec.Provenance, Repository: "repo", Provider: "claude", RequestedModel: "claude-opus-5-5", Text: text}
	return &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: inbox.MessageName(spec.Sender, spec.Key)}, Spec: spec}
}
func TestInboxRealSchemaUTF8ByteBoundAndImmutability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for n, text := range []string{strings.Repeat("a", 16<<10), strings.Repeat("é", 8<<10), strings.Repeat("界", (16<<10)/3)} {
		m := schemaMessage(fmt.Sprintf("valid-%d", n), text)
		if err := kube.Create(ctx, m); err != nil {
			t.Fatal("valid full digest name/byte limit refused", n, err)
		}
		duplicate := schemaMessage(m.Spec.Key, "other immutable text")
		if err := kube.Create(ctx, duplicate); !apierrors.IsAlreadyExists(err) {
			t.Fatal("message identity was not atomic", err)
		}
		for _, mutate := range []func(*coord.InboxMessage){func(m *coord.InboxMessage) { m.Spec.Text = "changed" }, func(m *coord.InboxMessage) { m.Spec.Recipient.UID = "new-recipient" }, func(m *coord.InboxMessage) { m.Spec.Actor.PodUID = "new-pod" }, func(m *coord.InboxMessage) { m.Spec.Provenance.RulesRevision = strings.Repeat("c", 64) }} {
			bad := m.DeepCopy()
			mutate(bad)
			if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
				t.Fatal("message mutated", err)
			}
		}
		if n == 0 {
			a := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Name: inbox.AckName(string(m.UID), m.Spec.Recipient.UID), Namespace: "default"}, Spec: coord.InboxReadAckSpec{Project: m.Spec.Project, MessageName: m.Name, MessageUID: string(m.UID), Recipient: m.Spec.Recipient, Actor: m.Spec.Actor}}
			if err := kube.Create(ctx, a); err != nil {
				t.Fatal("ack schema", err)
			}
			for _, mutate := range []func(*coord.InboxReadAck){func(a *coord.InboxReadAck) { a.Spec.Recipient.UID = "new-recipient" }, func(a *coord.InboxReadAck) { a.Spec.MessageUID = "new-message" }, func(a *coord.InboxReadAck) { a.Spec.Actor.PodUID = "new-pod" }} {
				bad := a.DeepCopy()
				mutate(bad)
				if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
					t.Fatal("ack mutated", err)
				}
			}
		}
	}
	for n, text := range []string{"", strings.Repeat("a", (16<<10)+1), strings.Repeat("é", (8<<10)+1), strings.Repeat("界", (16<<10)/3+1)} {
		if err := kube.Create(ctx, schemaMessage(fmt.Sprintf("invalid-%d", n), text)); !apierrors.IsInvalid(err) {
			t.Fatal("schema accepted invalid UTF-8 byte bound", n, err)
		}
	}
	badActor := schemaMessage("coordinator-actor", "bounded")
	badActor.Spec.Actor.Kind = "coordinator"
	if err := kube.Create(ctx, badActor); !apierrors.IsInvalid(err) {
		t.Fatal("schema allowed synthetic coordinator sender", err)
	}
}

func TestInboxQuestionReplySchemaIsExactAndImmutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	question := schemaMessage("question-1", "Which base?")
	question.Spec.Kind = "question"
	if err := kube.Create(ctx, question); err != nil {
		t.Fatal("question refused", err)
	}
	ref := coord.InboxMessageRef{Name: question.Name, UID: string(question.UID)}
	reply := schemaMessage("reply-1", "main")
	reply.Spec.Kind, reply.Spec.InReplyTo = "reply", &ref
	if err := kube.Create(ctx, reply); err != nil {
		t.Fatal("reply refused", err)
	}
	for _, mutate := range []func(*coord.InboxMessage){
		func(m *coord.InboxMessage) { m.Spec.Kind = "question"; m.Spec.InReplyTo = nil },
		func(m *coord.InboxMessage) { m.Spec.InReplyTo.UID = "other-question" },
	} {
		bad := reply.DeepCopy()
		mutate(bad)
		if err := kube.Update(ctx, bad); !apierrors.IsInvalid(err) {
			t.Fatal("reply correlation mutated", err)
		}
	}
	for n, mutate := range []func(*coord.InboxMessageSpec){
		func(s *coord.InboxMessageSpec) { s.Kind = "reply" },
		func(s *coord.InboxMessageSpec) { s.Kind = "question"; s.InReplyTo = &ref },
		func(s *coord.InboxMessageSpec) { s.InReplyTo = &ref },
		func(s *coord.InboxMessageSpec) { s.Kind = "approval" },
		func(s *coord.InboxMessageSpec) {
			s.Kind, s.InReplyTo = "reply", &coord.InboxMessageRef{Name: "message.short", UID: ref.UID}
		},
		func(s *coord.InboxMessageSpec) { s.Kind, s.InReplyTo = "reply", &coord.InboxMessageRef{Name: ref.Name} },
	} {
		m := schemaMessage(fmt.Sprintf("bad-correlation-%d", n), "x")
		mutate(&m.Spec)
		if err := kube.Create(ctx, m); !apierrors.IsInvalid(err) {
			t.Fatal("schema accepted an inexact correlation", n, err)
		}
	}
}
