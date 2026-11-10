package inbox

import (
	"encoding/json"
	"strings"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func meatballCaller() *coord.InboxCaller {
	return &coord.InboxCaller{ServiceAccount: coord.MeatballServiceAccount, PolicyUID: "meatball-policy-uid"}
}

func policyActor(base coord.ClaimActor) coord.ClaimActor {
	base.Kind = "policy"
	base.ServiceAccount = coord.MeatballServiceAccount
	return base
}

func TestPolicyMessageNameMatchesRefAndDiffers(t *testing.T) {
	spec := validMessage()
	sessionName := MessageName(spec.Sender, spec.Key)
	policyName := PolicyMessageName(coord.MeatballServiceAccount, "meatball-policy-uid", spec.Key)
	if !ValidMessageName(sessionName) || !ValidMessageName(policyName) {
		t.Fatalf("names must match the ref pattern: %s %s", sessionName, policyName)
	}
	if sessionName == policyName {
		t.Fatal("the same key from the two sender forms produced one name")
	}
	if MessageNameFor(spec) != sessionName {
		t.Fatal("a session sender did not keep MessageName")
	}
	policy := spec
	policy.Sender = coord.SessionIdentity{}
	policy.CallerSender = meatballCaller()
	if MessageNameFor(policy) != policyName {
		t.Fatal("a caller sender did not use PolicyMessageName")
	}
	if RecipientFormUID(spec) != spec.Recipient.UID {
		t.Fatal("a session recipient did not keep its UID")
	}
	policy.Recipient = coord.SessionIdentity{}
	policy.CallerRecipient = meatballCaller()
	if RecipientFormUID(policy) != meatballCaller().PolicyUID {
		t.Fatal("a caller recipient did not contribute the CallerPolicy UID")
	}
}

func TestValidMessageSpecAcceptsParentFormsAndPeer(t *testing.T) {
	peer := validMessage()
	if !validMessageSpec(peer, false) {
		t.Fatal("peer plain message refused")
	}
	note := peer
	note.Kind = KindQuestion
	if !validMessageSpec(note, false) {
		t.Fatal("child note to a session refused")
	}
	note.Kind = KindResult
	if !validMessageSpec(note, false) {
		t.Fatal("result refused")
	}
	toCaller := peer
	toCaller.Kind = KindBlocker
	toCaller.Recipient = coord.SessionIdentity{}
	toCaller.CallerRecipient = meatballCaller()
	if !validMessageSpec(toCaller, false) {
		t.Fatal("child note to the Meatball account refused")
	}
	fromCaller := peer
	fromCaller.Sender = coord.SessionIdentity{}
	fromCaller.CallerSender = meatballCaller()
	fromCaller.Actor = policyActor(fromCaller.Actor)
	if !validMessageSpec(fromCaller, false) {
		t.Fatal("plain message from the Meatball account refused")
	}
	ref := coord.InboxMessageRef{Name: MessageName(peer.Sender, "q"), UID: "question-uid"}
	reply := fromCaller
	reply.Kind = KindReply
	reply.InReplyTo = &ref
	if !validMessageSpec(reply, false) {
		t.Fatal("reply from the Meatball account refused")
	}
	reply.InReplyTo = nil
	if validMessageSpec(reply, false) {
		t.Fatal("reply with no inReplyTo accepted")
	}
	bothSenders := peer
	bothSenders.CallerSender = meatballCaller()
	if validMessageSpec(bothSenders, false) {
		t.Fatal("both sender forms accepted")
	}
	bothRecipients := peer
	bothRecipients.CallerRecipient = meatballCaller()
	if validMessageSpec(bothRecipients, false) {
		t.Fatal("both recipient forms accepted")
	}
	plainBoth := fromCaller
	plainBoth.Kind = ""
	plainBoth.InReplyTo = nil
	plainBoth.Recipient = coord.SessionIdentity{}
	plainBoth.CallerRecipient = meatballCaller()
	if validMessageSpec(plainBoth, false) {
		t.Fatal("plain message with callerSender and callerRecipient accepted")
	}
	wrongAccount := fromCaller
	wrongAccount.Kind = ""
	wrongAccount.InReplyTo = nil
	wrongAccount.Actor.ServiceAccount = "dev-agents/dev-env-agent"
	if validMessageSpec(wrongAccount, false) {
		t.Fatal("policy actor accepted for an account other than the Meatball account")
	}
	withReply := peer
	withReply.Kind = KindBlocker
	withReply.InReplyTo = &ref
	if validMessageSpec(withReply, false) {
		t.Fatal("blocker with inReplyTo accepted")
	}
}

// omitempty does not omit a zero struct. The sender and recipient tags are
// omitzero so an absent session form stays absent.
func TestOmittedSessionFormIsAbsent(t *testing.T) {
	spec := validMessage()
	spec.Sender = coord.SessionIdentity{}
	spec.CallerSender = meatballCaller()
	spec.Recipient = coord.SessionIdentity{}
	spec.CallerRecipient = meatballCaller()
	raw, err := json.Marshal(coord.InboxMessage{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"sender":`) || strings.Contains(string(raw), `"recipient":`) {
		t.Fatalf("zero session form was encoded: %s", raw)
	}
}

func TestValidAckStaysSession(t *testing.T) {
	spec := validMessage()
	ack := coord.InboxReadAckSpec{Project: spec.Project, MessageName: MessageName(spec.Sender, spec.Key), MessageUID: "ack-uid", Recipient: spec.Recipient, Actor: spec.Actor}
	if !ValidAck(ack) {
		t.Fatal("session ack refused")
	}
	ack.Actor = policyActor(ack.Actor)
	if ValidAck(ack) {
		t.Fatal("policy actor acknowledged")
	}
	if !strings.HasPrefix(ack.MessageName, "message.") {
		t.Fatal(ack.MessageName)
	}
}
