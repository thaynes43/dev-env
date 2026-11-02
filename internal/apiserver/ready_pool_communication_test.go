package apiserver

import (
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// TestReadyPoolLostReplyAckRetryPreservesCorrelation chains one failure
// sequence. Creating or rereading a reply is HTTP receipt of the stored inbox
// record: the thread stays answered and waiting on the asker until that
// asker's acknowledgement is stored. While that acknowledgement is absent, the
// same reply intent returns the original correlation and does not insert
// another message. A second question stays unanswered and waiting on its
// recipient. A replaced pod proof cannot acknowledge, read, or publish the
// stored intent.
func TestReadyPoolLostReplyAckRetryPreservesCorrelation(t *testing.T) {
	f, lead, child, bystander := askReplyFixture(t)
	base := countMessages(t, f)
	waitingReq := askParent("q-wait", "Still open?")
	waiting := publish(t, f, child, waitingReq, http.StatusCreated)
	q := publish(t, f, child, askParent("q-lost", "Which base?"), http.StatusCreated)
	if got, want := countMessages(t, f), base+2; got != want {
		t.Fatalf("stored questions %d want %d", got, want)
	}

	threadOf := func(tok, extra, name string) apiv1.InboxThread {
		t.Helper()
		item := itemNamed(t, threadList(t, f, tok, extra), name)
		if item.Thread == nil {
			t.Fatalf("no thread token %s extra %q name %s", tok, extra, name)
		}
		return *item.Thread
	}
	assertUnanswered := func() {
		t.Helper()
		for _, probe := range []struct{ tok, extra string }{{lead, ""}, {child, "&box=sent"}} {
			got := threadOf(probe.tok, probe.extra, waiting.Name)
			if got.State != apiv1.InboxStateUnanswered || got.Reply != nil || got.WaitingOn == nil || got.WaitingOn.Name != "lead" || got.Question.Name != waiting.Name || got.Question.UID != waiting.UID || got.StaleAfterSeconds != 1800 || !got.ObservedAt.Equal(f.now) {
				t.Fatalf("unanswered waiting changed for %s: %+v", probe.tok, got)
			}
		}
	}
	assertCorrelated := func(state, replyName, waitingOn string) {
		t.Helper()
		for _, probe := range []struct{ tok, extra string }{{lead, ""}, {child, "&box=sent"}} {
			got := threadOf(probe.tok, probe.extra, q.Name)
			if got.State != state || got.Question.Name != q.Name || got.Question.UID != q.UID || got.Reply == nil || got.Reply.Name != replyName {
				t.Fatalf("%s correlation for %s: %+v", state, probe.tok, got)
			}
			if waitingOn == "" {
				if got.WaitingOn != nil {
					t.Fatalf("%s still waiting for %s: %+v", state, probe.tok, got)
				}
				continue
			}
			if got.WaitingOn == nil || got.WaitingOn.Name != waitingOn {
				t.Fatalf("%s waiting for %s: %+v", state, probe.tok, got)
			}
		}
	}
	assertAckViews := func(replyName string) {
		t.Helper()
		assertCorrelated(apiv1.InboxStateAcknowledged, replyName, "")
		for _, probe := range []struct{ tok, extra string }{{child, ""}, {lead, "&box=sent"}} {
			got := threadOf(probe.tok, probe.extra, replyName)
			if got.State != apiv1.InboxStateAcknowledged || got.Reply == nil || got.Reply.Name != replyName || got.WaitingOn != nil || got.Question.Name != q.Name || got.Question.UID != q.UID {
				t.Fatalf("acknowledged view %s: %+v", probe.tok, got)
			}
		}
	}
	stored := base + 2
	hold := func() {
		t.Helper()
		if got := countMessages(t, f); got != stored {
			t.Fatalf("message count %d want %d", got, stored)
		}
	}
	reject := func(tok, what string, msg apiv1.InboxMetadata) {
		t.Helper()
		resp := f.do(http.MethodPost, ackPath(msg), tok, struct{}{})
		if resp.Code < 400 || resp.Code > 499 {
			t.Fatalf("%s ack status %d %s", what, resp.Code, resp.Body.String())
		}
		resp = f.do(http.MethodGet, inboxPath(msg), tok, nil)
		if resp.Code < 400 || resp.Code > 499 {
			t.Fatalf("%s read status %d %s", what, resp.Code, resp.Body.String())
		}
		hold()
	}
	setPodUID := func(tok, uid string) {
		t.Helper()
		entry := f.auth[tok]
		entry.PodUID = uid
		f.auth[tok] = entry
	}
	readReply := func(reply apiv1.InboxMetadata, wantState string) apiv1.InboxMessage {
		t.Helper()
		w := f.do(http.MethodGet, apiv1.InboxPath+"/"+reply.Name+"?project=sample&view=thread", child, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("asker read %d %s", w.Code, w.Body.String())
		}
		got := decode[apiv1.InboxMessage](t, w)
		if got.Text != "main" || got.Name != reply.Name || got.UID != reply.UID || got.InReplyTo == nil || got.InReplyTo.Name != q.Name || got.InReplyTo.UID != q.UID || got.Thread == nil || got.Thread.State != wantState {
			t.Fatalf("stored reply changed at %s: %+v", wantState, got)
		}
		return got
	}

	assertUnanswered()
	hold()

	replyReq := replyTo(q, "r-lost", "main")
	r := publish(t, f, lead, replyReq, http.StatusCreated)
	if r.Kind != apiv1.InboxKindReply || r.InReplyTo == nil || r.InReplyTo.Name != q.Name || r.InReplyTo.UID != q.UID {
		t.Fatalf("create correlation %+v", r)
	}
	stored = base + 3
	hold()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()

	// GET 200 records receipt of the stored text. It does not acknowledge it.
	readReply(r, apiv1.InboxStateAnswered)
	hold()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()

	askerUID := f.auth[child].PodUID
	resumeInboxPod(t, f, bystander)
	liveBystanderUID := f.auth[bystander].PodUID
	if liveBystanderUID == "" {
		t.Fatal("replaced bystander proof is empty")
	}
	setPodUID(bystander, askerUID)
	reject(bystander, "transplanted pod uid", r)
	if f.auth[child].PodUID != askerUID {
		t.Fatal("transplant rewrote the asker proof")
	}
	setPodUID(bystander, liveBystanderUID)
	reject(bystander, "replaced bystander pod", r)
	hold()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()
	readReply(r, apiv1.InboxStateAnswered)

	resumeInboxPod(t, f, child)
	liveChildUID := f.auth[child].PodUID
	if liveChildUID == "" || liveChildUID == askerUID {
		t.Fatal("asker pod proof did not rotate")
	}
	setPodUID(child, askerUID)
	reject(child, "retired asker proof", r)
	setPodUID(child, liveChildUID)
	hold()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()
	readReply(r, apiv1.InboxStateAnswered)

	// The acknowledgement is still lost. The same intent must not duplicate.
	again := publish(t, f, lead, replyReq, http.StatusOK)
	if again.Name != r.Name || again.UID != r.UID || again.Kind != apiv1.InboxKindReply || again.InReplyTo == nil || again.InReplyTo.Name != q.Name || again.InReplyTo.UID != q.UID {
		t.Fatalf("retry correlation %+v want %+v", again, r)
	}
	listed := itemNamed(t, threadList(t, f, child, ""), r.Name)
	if listed.Kind != apiv1.InboxKindReply || listed.UID != r.UID || listed.InReplyTo == nil || listed.InReplyTo.Name != q.Name || listed.InReplyTo.UID != q.UID || listed.Thread == nil || listed.Thread.State != apiv1.InboxStateAnswered || listed.Thread.Reply == nil || listed.Thread.Reply.Name != r.Name {
		t.Fatalf("listed retry %+v", listed)
	}
	hold()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()

	conflict := replyReq
	conflict.Text = "other-base"
	cw := f.do(http.MethodPost, apiv1.InboxPath, lead, conflict)
	if cw.Code == http.StatusCreated || cw.Code >= 500 {
		t.Fatalf("conflicting reply text status %d %s", cw.Code, cw.Body.String())
	}
	if cw.Code/100 == 2 {
		got := decode[apiv1.InboxResult](t, cw).Message
		if got.Name != r.Name || got.UID != r.UID || got.InReplyTo == nil || got.InReplyTo.Name != q.Name || got.InReplyTo.UID != q.UID {
			t.Fatalf("conflicting text changed correlation %+v", got)
		}
	}
	hold()
	readReply(r, apiv1.InboxStateAnswered)
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()

	replay := publish(t, f, child, waitingReq, http.StatusOK)
	if replay.Name != waiting.Name || replay.UID != waiting.UID || replay.InReplyTo != nil {
		t.Fatalf("waiting retry %+v", replay)
	}
	waitingItem := itemNamed(t, threadList(t, f, lead, ""), waiting.Name)
	if waitingItem.Kind != apiv1.InboxKindQuestion || waitingItem.UID != waiting.UID || waitingItem.InReplyTo != nil || waitingItem.Thread == nil || waitingItem.Thread.State != apiv1.InboxStateUnanswered || waitingItem.Thread.Reply != nil || waitingItem.Thread.WaitingOn == nil || waitingItem.Thread.WaitingOn.Name != "lead" {
		t.Fatalf("waiting item after retry %+v", waitingItem)
	}
	hold()
	assertUnanswered()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")

	priorLeadUID := f.auth[lead].PodUID
	resumeInboxPod(t, f, lead)
	liveLeadUID := f.auth[lead].PodUID
	if liveLeadUID == "" || liveLeadUID == priorLeadUID {
		t.Fatal("coordinator pod proof did not rotate")
	}
	setPodUID(lead, priorLeadUID)
	stale := f.do(http.MethodPost, apiv1.InboxPath, lead, replyReq)
	if stale.Code < 400 || stale.Code > 499 {
		t.Fatalf("replaced coordinator token status %d %s", stale.Code, stale.Body.String())
	}
	hold()
	setPodUID(lead, liveLeadUID)
	restored := publish(t, f, lead, replyReq, http.StatusOK)
	if restored.Name != r.Name || restored.UID != r.UID || restored.InReplyTo == nil || restored.InReplyTo.Name != q.Name || restored.InReplyTo.UID != q.UID {
		t.Fatalf("restored retry %+v", restored)
	}
	hold()
	assertCorrelated(apiv1.InboxStateAnswered, r.Name, "child")
	assertUnanswered()

	w := f.do(http.MethodPost, ackPath(r), child, struct{}{})
	if w.Code != http.StatusCreated {
		t.Fatalf("first ack %d %s", w.Code, w.Body.String())
	}
	first := decode[apiv1.InboxAckResult](t, w).Ack
	hold()
	assertAckViews(r.Name)
	assertUnanswered()

	// The acknowledgement response is lost, so the asker repeats it.
	w = f.do(http.MethodPost, ackPath(r), child, struct{}{})
	if w.Code != http.StatusOK {
		t.Fatalf("lost ack retry %d %s", w.Code, w.Body.String())
	}
	second := decode[apiv1.InboxAckResult](t, w).Ack
	if second.Actor != first.Actor || second.CreatedAt != first.CreatedAt {
		t.Fatalf("ack retry replaced original %+v want %+v", second, first)
	}
	hold()
	assertAckViews(r.Name)
	assertUnanswered()

	finalRetry := publish(t, f, lead, replyReq, http.StatusOK)
	if finalRetry.Name != r.Name || finalRetry.UID != r.UID || finalRetry.InReplyTo == nil || finalRetry.InReplyTo.Name != q.Name || finalRetry.InReplyTo.UID != q.UID {
		t.Fatalf("post-ack retry %+v", finalRetry)
	}
	hold()
	assertAckViews(r.Name)
	assertUnanswered()
	reject(bystander, "post-ack replaced bystander", r)
	assertAckViews(r.Name)
	assertUnanswered()
	readReply(r, apiv1.InboxStateAcknowledged)
	hold()
}
