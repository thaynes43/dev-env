package apiv1

import (
	"strings"
	"testing"
	"time"
)

func TestPortalQuestionAuditItemCorrelationAndAck(t *testing.T) {
	item := questionAuditFixture()
	if !PortalQuestionAuditItemOK(item) {
		t.Fatal("fixture rejected")
	}
	substituted := item
	substituted.Thread = copyQuestionThread(item.Thread)
	substituted.Thread.Question.Name = strings.Replace(item.Name, "a", "b", 1)
	if PortalQuestionAuditItemOK(substituted) {
		t.Fatal("substituted question reference was trusted")
	}
	crossed := questionAuditReply()
	if !PortalQuestionAuditItemOK(crossed) {
		t.Fatal("reply fixture rejected")
	}
	other := *crossed.InReplyTo
	other.Name = strings.Replace(other.Name, "a", "b", 1)
	crossed.InReplyTo = &other
	if PortalQuestionAuditItemOK(crossed) {
		t.Fatal("reply whose inReplyTo disagrees with thread.question was trusted")
	}
	crossed = questionAuditReply()
	crossed.Thread = copyQuestionThread(crossed.Thread)
	crossed.Thread.Reply.UID = "other-uid"
	if PortalQuestionAuditItemOK(crossed) {
		t.Fatal("reply that does not name itself was trusted")
	}
	acked := questionAuditReply()
	acked.Thread = copyQuestionThread(acked.Thread)
	acked.Thread.State = InboxStateAcknowledged
	acked.Thread.WaitingOn = nil
	at := acked.CreatedAt.Add(time.Second)
	acked.Acknowledgement = PortalQuestionAuditAck{Status: PortalQuestionAckRecorded, Actor: &TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env", PodName: "child", PodUID: "pod-1"}, CreatedAt: &at}
	if !PortalQuestionAuditItemOK(acked) {
		t.Fatal("ack by the recorded recipient was rejected")
	}
	claimed := acked
	actor := *acked.Acknowledgement.Actor
	actor.PodName = "claimed-origin"
	claimed.Acknowledgement.Actor = &actor
	if PortalQuestionAuditItemOK(claimed) {
		t.Fatal("claimed origin was trusted as the acknowledging session")
	}
	claimed = acked
	claimed.Acknowledgement = PortalQuestionAuditAck{Status: PortalQuestionAckUnavailable}
	if PortalQuestionAuditItemOK(claimed) {
		t.Fatal("Acknowledged without ack evidence was trusted")
	}
	early := acked
	before := acked.CreatedAt.Add(-time.Second)
	early.Acknowledgement.CreatedAt = &before
	if PortalQuestionAuditItemOK(early) {
		t.Fatal("ack time before the reply was trusted")
	}
	plain := item
	plain.Kind = InboxKindPlain
	plain.Thread = nil
	if PortalQuestionAuditItemOK(plain) {
		t.Fatal("plain message was trusted")
	}
}

func questionAuditFixture() PortalQuestionAuditItem {
	at := time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC)
	observed := at.Add(time.Minute)
	name := "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
	return PortalQuestionAuditItem{
		Name: name, UID: "uid-1", Kind: InboxKindQuestion,
		Sender:     TaskClaimOwner{Namespace: "dev-agents", Name: "child", UID: "uid-child"},
		Recipient:  TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "uid-lead"},
		Actor:      TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env", PodName: "child", PodUID: "pod-1"},
		Provenance: TaskClaimProvenance{CatalogRevision: strings.Repeat("e", 64), RulesRevision: strings.Repeat("f", 64), GitHub: "thaynes43/actual-source", DefaultBranch: "stable", InitialPinState: "Unknown"},
		CreatedAt:  at, Origin: PortalQuestionOriginUnknown, Approval: PortalQuestionApprovalNotRecorded,
		ProviderConsumption: PortalQuestionConsumptionNotRecorded, ServedModel: PortalQuestionServedModelUnknown,
		Thread: &InboxThread{State: InboxStateUnanswered, Question: InboxMessageRef{Name: name, UID: "uid-1"}, StaleAfterSeconds: PortalQuestionAuditStaleAfterSeconds, ObservedAt: observed,
			WaitingOn: &TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "uid-lead"}},
		Acknowledgement: PortalQuestionAuditAck{Status: PortalQuestionAckUnavailable},
	}
}

func questionAuditReply() PortalQuestionAuditItem {
	item := questionAuditFixture()
	question := item.Name
	item.Name = "message." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
	item.UID = "uid-2"
	item.Kind = InboxKindReply
	item.InReplyTo = &InboxMessageRef{Name: question, UID: "uid-1"}
	item.Sender, item.Recipient = item.Recipient, item.Sender
	item.Actor.PodName = "lead"
	item.Thread = &InboxThread{State: InboxStateAnswered, Question: InboxMessageRef{Name: question, UID: "uid-1"}, Reply: &InboxMessageRef{Name: item.Name, UID: item.UID}, StaleAfterSeconds: PortalQuestionAuditStaleAfterSeconds, ObservedAt: item.Thread.ObservedAt}
	return item
}

func copyQuestionThread(in *InboxThread) *InboxThread {
	out := *in
	if in.Reply != nil {
		reply := *in.Reply
		out.Reply = &reply
	}
	if in.WaitingOn != nil {
		owner := *in.WaitingOn
		out.WaitingOn = &owner
	}
	return &out
}
