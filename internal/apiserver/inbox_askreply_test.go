package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

// askReplyFixture has a coordinator Session "lead", its child "child" (whose
// recorded parent is lead) and an unrelated "bystander".
func askReplyFixture(t *testing.T) (f *fixture, lead, child, bystander string) {
	t.Helper()
	f = coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	f.srv.InboxCoordinatorReadProjects = []string{"sample"}
	lead = claimSession(t, f, "lead", humanSA)
	child = claimSession(t, f, "child", "lead")
	bystander = claimSession(t, f, "bystander", humanSA)
	return f, lead, child, bystander
}

func owner(f *fixture, name string) apiv1.TaskClaimOwner {
	s := f.session(name)
	return apiv1.TaskClaimOwner{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}
}

func askParent(key, text string) apiv1.PublishInboxRequest {
	return apiv1.PublishInboxRequest{Project: "sample", Key: key, Text: text, Kind: apiv1.InboxKindQuestion, ToParent: true}
}

func replyTo(m apiv1.InboxMetadata, key, text string) apiv1.PublishInboxRequest {
	return apiv1.PublishInboxRequest{Project: "sample", Key: key, Text: text, Kind: apiv1.InboxKindReply, InReplyTo: &apiv1.InboxMessageRef{Name: m.Name}}
}

func publish(t *testing.T, f *fixture, tok string, req apiv1.PublishInboxRequest, want int) apiv1.InboxMetadata {
	t.Helper()
	w := f.do(http.MethodPost, apiv1.InboxPath, tok, req)
	if w.Code != want {
		t.Fatalf("publish %s/%s: %d want %d: %s", req.Kind, req.Key, w.Code, want, w.Body.String())
	}
	if want/100 != 2 {
		return apiv1.InboxMetadata{}
	}
	return decode[apiv1.InboxResult](t, w).Message
}

func threadList(t *testing.T, f *fixture, tok, extra string) apiv1.InboxList {
	t.Helper()
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&view=thread"+extra, tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("thread list %s: %d %s", extra, w.Code, w.Body.String())
	}
	list := decode[apiv1.InboxList](t, w)
	if list.View != apiv1.InboxViewThread {
		t.Fatal("thread view not echoed", w.Body.String())
	}
	return list
}

func itemNamed(t *testing.T, list apiv1.InboxList, name string) apiv1.InboxMetadata {
	t.Helper()
	for _, item := range list.Items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("%s not listed in %+v", name, list.Items)
	return apiv1.InboxMetadata{}
}

func TestInboxAskDefaultsToRecordedParent(t *testing.T) {
	f, lead, child, _ := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "Which base branch?\n"), http.StatusCreated)
	if q.Recipient != owner(f, "lead") || q.Sender != owner(f, "child") || q.Kind != apiv1.InboxKindQuestion || q.InReplyTo != nil {
		t.Fatalf("ask was not addressed to the recorded parent: %+v", q)
	}
	if q.Actor.PodName != "child" || q.Provenance.InitialPinState != "Unknown" {
		t.Fatal("ask lost authenticated attribution", q.Actor, q.Provenance)
	}
	// Text never names the recipient: identical text with a parent-shaped
	// recipient is still the server's resolution, and the request cannot carry both.
	both := askParent("q-both", "to bystander please")
	both.Recipient = owner(f, "bystander")
	publish(t, f, child, both, http.StatusBadRequest)

	// A Session created by a ServiceAccount has no parent Session.
	w := f.do(http.MethodPost, apiv1.InboxPath, lead, askParent("q-root", "no parent"))
	wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)

	// An explicit recipient by name resolves its current UID; a stale UID is refused.
	named := askParent("q-named", "for the bystander")
	named.ToParent, named.Recipient = false, apiv1.TaskClaimOwner{Name: "bystander"}
	if m := publish(t, f, child, named, http.StatusCreated); m.Recipient != owner(f, "bystander") {
		t.Fatal("named recipient", m.Recipient)
	}
	named.Key, named.Recipient.UID = "q-stale", "earlier-uid"
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, child, named), http.StatusNotFound, apiv1.CodeNotFound)

	// A parent created after the child is a replacement, not the parent.
	claimSession(t, f, "orphan", "late-parent")
	f.now = f.now.Add(time.Hour)
	claimSession(t, f, "late-parent", humanSA)
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, "tok-session-orphan", askParent("q-late", "x")), http.StatusNotFound, apiv1.CodeNotFound)
	// So is a parent whose UID differs from the child's recorded usage parent UID.
	s := f.session("child")
	s.Spec.UsageCaptureParentUID = "earlier-parent-uid"
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, child, askParent("q-usage", "x")), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestInboxReplyBindsAndRefusesWrongParent(t *testing.T) {
	f, lead, child, bystander := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "May I rebase?"), http.StatusCreated)

	// Missing, stale, foreign-project and not-addressed parents read as not found.
	missing := q
	missing.Name = inbox.MessageName(coord.SessionIdentity{Namespace: sessionNS, Name: "nobody", UID: "nobody-uid"}, "q-1")
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, lead, replyTo(missing, "r-missing", "x")), http.StatusNotFound, apiv1.CodeNotFound)
	stale := replyTo(q, "r-stale", "x")
	stale.InReplyTo.UID = "earlier-question-uid"
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, lead, stale), http.StatusNotFound, apiv1.CodeNotFound)
	stored, err := f.srv.Inbox.Get(context.Background(), q.Name)
	if err != nil {
		t.Fatal(err)
	}
	foreign := stored.Spec
	foreign.Project, foreign.Key = "other", "q-other"
	other, _, err := f.srv.Inbox.Create(context.Background(), foreign)
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, lead, replyTo(apiv1.InboxMetadata{Name: other.Name}, "r-foreign", "x")), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, bystander, replyTo(q, "r-bystander", "x")), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, child, replyTo(q, "r-self", "x")), http.StatusNotFound, apiv1.CodeNotFound)
	// A reply names its question only: the server picks the recipient.
	addressed := replyTo(q, "r-addressed", "x")
	addressed.Recipient = owner(f, "bystander")
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, lead, addressed), http.StatusBadRequest, apiv1.CodeBadRequest)

	r := publish(t, f, lead, replyTo(q, "r-1", "Yes, rebase on main.\n"), http.StatusCreated)
	if r.Kind != apiv1.InboxKindReply || r.InReplyTo == nil || *r.InReplyTo != (apiv1.InboxMessageRef{Name: q.Name, UID: q.UID}) || r.Recipient != q.Sender || r.Sender != q.Recipient || r.Actor.PodName != "lead" {
		t.Fatalf("reply did not bind the question: %+v", r)
	}
	// One reply per question; a reply or a plain message takes none.
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, lead, replyTo(q, "r-2", "second thoughts")), http.StatusConflict, apiv1.CodeConflict)
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, child, replyTo(r, "r-to-reply", "thanks")), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	plain := publish(t, f, child, apiv1.PublishInboxRequest{Project: "sample", Recipient: owner(f, "lead"), Key: "plain-1", Text: "fyi"}, http.StatusCreated)
	if plain.Kind != "" || plain.InReplyTo != nil {
		t.Fatal("plain receipt changed shape", plain)
	}
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, lead, replyTo(plain, "r-plain", "noted")), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
}

func TestInboxAskReplyRetryIsIdempotent(t *testing.T) {
	f, lead, child, _ := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "Which base?"), http.StatusCreated)
	if again := publish(t, f, child, askParent("q-1", "Which base?"), http.StatusOK); !reflect.DeepEqual(again, q) {
		t.Fatalf("ask retry changed the record:\n%+v\n%+v", again, q)
	}
	publish(t, f, child, askParent("q-1", "Which base, again?"), http.StatusConflict)
	explicit := askParent("q-1", "Which base?")
	explicit.ToParent, explicit.Recipient = false, apiv1.TaskClaimOwner{Name: "lead"}
	if again := publish(t, f, child, explicit, http.StatusOK); again.UID != q.UID {
		t.Fatal("same recipient by name is the same intent")
	}
	explicit.Recipient.Name = "bystander"
	publish(t, f, child, explicit, http.StatusConflict)

	r := publish(t, f, lead, replyTo(q, "r-1", "main"), http.StatusCreated)
	if again := publish(t, f, lead, replyTo(q, "r-1", "main"), http.StatusOK); !reflect.DeepEqual(again, r) {
		t.Fatal("reply retry changed the record")
	}
	publish(t, f, lead, replyTo(q, "r-1", "stable"), http.StatusConflict)

	// An accepted retry survives the end of the other party.
	removeInboxSession(t, f, "child")
	if again := publish(t, f, lead, replyTo(q, "r-1", "main"), http.StatusOK); again.UID != r.UID {
		t.Fatal("reply retry after the asker ended")
	}
	f2, _, child2, _ := askReplyFixture(t)
	q2 := publish(t, f2, child2, askParent("q-1", "Which base?"), http.StatusCreated)
	removeInboxSession(t, f2, "lead")
	if again := publish(t, f2, child2, askParent("q-1", "Which base?"), http.StatusOK); again.UID != q2.UID {
		t.Fatal("ask retry after the parent ended")
	}
	if n := countMessages(t, f); n != 2 {
		t.Fatal("retries created records", n)
	}
}

func countMessages(t *testing.T, f *fixture) int {
	t.Helper()
	var list coord.InboxMessageList
	if err := f.c.List(context.Background(), &list, client.InNamespace("dev-env-system")); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func TestInboxThreadStatesOnBothSides(t *testing.T) {
	f, lead, child, bystander := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "Which base?"), http.StatusCreated)
	state := func(tok, extra, name string) apiv1.InboxThread {
		t.Helper()
		item := itemNamed(t, threadList(t, f, tok, extra), name)
		if item.Thread == nil {
			t.Fatalf("no thread for %s: %+v", name, item)
		}
		return *item.Thread
	}
	check := func(got apiv1.InboxThread, want string, reply string, waiting string) {
		t.Helper()
		if got.State != want || got.Question != (apiv1.InboxMessageRef{Name: q.Name, UID: q.UID}) || got.StaleAfterSeconds != 1800 || !got.ObservedAt.Equal(f.now) {
			t.Fatalf("state %+v, want %s", got, want)
		}
		if (reply == "") != (got.Reply == nil) || (got.Reply != nil && got.Reply.Name != reply) {
			t.Fatalf("reply %+v, want %q", got.Reply, reply)
		}
		if (waiting == "") != (got.WaitingOn == nil) || (got.WaitingOn != nil && got.WaitingOn.Name != waiting) {
			t.Fatalf("waiting on %+v, want %q", got.WaitingOn, waiting)
		}
	}
	// Unanswered on both sides: the coordinator's inbox and the asker's sent box.
	check(state(lead, "", q.Name), apiv1.InboxStateUnanswered, "", "lead")
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateUnanswered, "", "lead")
	if item := itemNamed(t, threadList(t, f, lead, ""), q.Name); item.Kind != apiv1.InboxKindQuestion || item.InReplyTo != nil {
		t.Fatal("question kind", item)
	}
	// A Suspended recipient can resume: still Unanswered.
	s := f.session("lead")
	s.Status.Phase = v1alpha1.PhaseSuspended
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateUnanswered, "", "lead")
	f.now = f.now.Add(inbox.StaleAfter)
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateUnanswered, "", "lead")
	f.now = f.now.Add(time.Second)
	check(state(lead, "", q.Name), apiv1.InboxStateStale, "", "lead")
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateStale, "", "lead")

	r := publish(t, f, lead, replyTo(q, "r-1", "main"), http.StatusCreated)
	check(state(lead, "", q.Name), apiv1.InboxStateAnswered, r.Name, "child")
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateAnswered, r.Name, "child")
	reply := itemNamed(t, threadList(t, f, child, ""), r.Name)
	if reply.Kind != apiv1.InboxKindReply || reply.InReplyTo == nil || reply.InReplyTo.Name != q.Name {
		t.Fatal("reply correlation in the asker's inbox", reply)
	}
	check(*reply.Thread, apiv1.InboxStateAnswered, r.Name, "child")
	check(state(lead, "&box=sent", r.Name), apiv1.InboxStateAnswered, r.Name, "child")
	w := f.do(http.MethodGet, apiv1.InboxPath+"/"+r.Name+"?project=sample&view=thread", child, nil)
	read := decode[apiv1.InboxMessage](t, w)
	if w.Code != http.StatusOK || read.Text != "main" || read.Thread == nil || read.Thread.State != apiv1.InboxStateAnswered {
		t.Fatal("thread read", w.Code, w.Body.String())
	}
	// Reading never acknowledges; the asker's explicit ack does.
	if w := f.do(http.MethodPost, ackPath(r), child, struct{}{}); w.Code != http.StatusCreated {
		t.Fatal("ack", w.Code, w.Body.String())
	}
	check(state(lead, "", q.Name), apiv1.InboxStateAcknowledged, r.Name, "")
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateAcknowledged, r.Name, "")
	check(state(child, "", r.Name), apiv1.InboxStateAcknowledged, r.Name, "")

	// RecipientEnded: a task whose agent exited, then a recipient that is gone.
	q2 := publish(t, f, child, apiv1.PublishInboxRequest{Project: "sample", Key: "q-2", Text: "x", Kind: apiv1.InboxKindQuestion, Recipient: apiv1.TaskClaimOwner{Name: "bystander"}}, http.StatusCreated)
	b := f.session("bystander")
	b.Status.Agent.Status = protocol.AgentExited
	if err := f.c.Status().Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := itemNamed(t, threadList(t, f, child, "&box=sent"), q2.Name).Thread; got.State != apiv1.InboxStateRecipientEnded || got.WaitingOn != nil {
		t.Fatal("exited task recipient", got)
	}
	removeInboxSession(t, f, "bystander")
	if got := itemNamed(t, threadList(t, f, child, "&box=sent"), q2.Name).Thread; got.State != apiv1.InboxStateRecipientEnded {
		t.Fatal("gone recipient", got)
	}
	_ = bystander
	// An answered question keeps its reply after its recipient ends.
	removeInboxSession(t, f, "lead")
	check(state(child, "&box=sent", q.Name), apiv1.InboxStateAcknowledged, r.Name, "")

	// Plain messages carry no thread; an unconfirmed lookup is Unknown, never Unanswered.
	f.srv.Inbox.Live = failingReplyLookup{f.c}
	sentBox := threadList(t, f, child, "&box=sent")
	if got := itemNamed(t, sentBox, q.Name).Thread; got.State != apiv1.InboxStateUnknown || got.Reply != nil || got.WaitingOn != nil {
		t.Fatal("unconfirmed lookup was not Unknown", got)
	}
}

func TestInboxThreadUnknownWhenRecipientUnreadable(t *testing.T) {
	f, _, child, _ := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "x"), http.StatusCreated)
	if got := (&Server{Live: failingSessionGet{f.c}, Policy: f.srv.Policy, Inbox: f.srv.Inbox, Now: f.srv.Now}).inboxRecipientState(context.Background(), coord.SessionIdentity{Namespace: sessionNS, Name: q.Recipient.Name, UID: q.Recipient.UID}, map[coord.SessionIdentity]inboxEnded{}); got != inboxEndedUnknown {
		t.Fatal("unreadable recipient", got)
	}
}

// failingReplyLookup fails only the reply lookup by its label selector.
type failingReplyLookup struct{ client.Reader }

func (r failingReplyLookup) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	for _, o := range opts {
		if l, ok := o.(client.MatchingLabels); ok && l[inbox.InReplyToLabel] != "" {
			return context.DeadlineExceeded
		}
	}
	return r.Reader.List(ctx, list, opts...)
}

type failingSessionGet struct{ client.Reader }

func (r failingSessionGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.AgentSession); ok {
		return context.DeadlineExceeded
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// The pre-ask/reply wire shape, as an older strict client decodes it.
type oldInboxMetadata struct {
	Name           string                    `json:"name"`
	UID            string                    `json:"uid"`
	Project        string                    `json:"project"`
	Key            string                    `json:"key"`
	Sender         apiv1.TaskClaimOwner      `json:"sender"`
	Recipient      apiv1.TaskClaimOwner      `json:"recipient"`
	Actor          apiv1.TaskClaimActor      `json:"actor"`
	Provenance     apiv1.TaskClaimProvenance `json:"provenance"`
	Repository     string                    `json:"repository"`
	Provider       string                    `json:"provider"`
	RequestedModel string                    `json:"requestedModel"`
	CreatedAt      time.Time                 `json:"createdAt"`
	Archived       bool                      `json:"archived,omitempty"`
	ArchivedAt     *time.Time                `json:"archivedAt,omitempty"`
}
type oldInboxList struct {
	Items    []oldInboxMetadata `json:"items"`
	Continue string             `json:"continue,omitempty"`
}
type oldInboxMessage struct {
	oldInboxMetadata
	Text string `json:"text"`
}

func strictDecode(body []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func TestInboxOlderStrictClientStillDecodes(t *testing.T) {
	f, lead, child, _ := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "Which base?"), http.StatusCreated)
	r := publish(t, f, lead, replyTo(q, "r-1", "main"), http.StatusCreated)
	for _, tc := range []struct {
		tok, path string
		out       any
	}{
		{lead, apiv1.InboxPath + "?project=sample", &oldInboxList{}},
		{child, apiv1.InboxPath + "?project=sample&limit=5", &oldInboxList{}},
		{lead, inboxPath(q), &oldInboxMessage{}},
		{child, inboxPath(r), &oldInboxMessage{}},
	} {
		w := f.do(http.MethodGet, tc.path, tc.tok, nil)
		if w.Code != http.StatusOK {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
		if err := strictDecode(w.Body.Bytes(), tc.out); err != nil {
			t.Fatalf("older strict client cannot decode %s: %v: %s", tc.path, err, w.Body.String())
		}
	}
	// The new fields exist, and only the opt-in view carries them.
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&view=thread", lead, nil)
	if err := strictDecode(w.Body.Bytes(), &oldInboxList{}); err == nil || !strings.Contains(w.Body.String(), `"kind":"question"`) {
		t.Fatal("thread view did not add the correlation", w.Body.String())
	}
}

func TestInboxSentBoxAndQueryBounds(t *testing.T) {
	f, _, child, bystander := askReplyFixture(t)
	q := publish(t, f, child, askParent("q-1", "x"), http.StatusCreated)
	if list := threadList(t, f, bystander, "&box=sent"); len(list.Items) != 0 || list.Box != apiv1.InboxBoxSent {
		t.Fatal("another Session's sent box", list)
	}
	if list := threadList(t, f, child, "&box=sent"); len(list.Items) != 1 || list.Items[0].Name != q.Name || list.Box != apiv1.InboxBoxSent {
		t.Fatal("own sent box", list)
	}
	// The sent box lists without the thread view too, in the older item shape.
	w := f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&box=sent", child, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"kind":"question"`) || strictDecode(w.Body.Bytes(), &struct {
		oldInboxList
		Box string `json:"box"`
	}{}) != nil {
		t.Fatal("plain sent box", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ tok, query string }{
		{tokCoordinator, "&box=sent"},
		{child, "&view=all"},
		{child, "&view=thread&view=thread"},
		{child, "&box=inbox"},
		{child, "&archived=only&view=thread"},
	} {
		wantError(t, f.do(http.MethodGet, apiv1.InboxPath+"?project=sample"+tc.query, tc.tok, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	}
	wantError(t, f.do(http.MethodGet, inboxPath(q)+"&box=sent", child, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	// A configured coordinator host reads the project's threads but sends nothing.
	if item := itemNamed(t, threadList(t, f, tokCoordinator, ""), q.Name); item.Thread == nil || item.Thread.State != apiv1.InboxStateUnanswered {
		t.Fatal("coordinator host thread view", item)
	}
	wantError(t, f.do(http.MethodPost, apiv1.InboxPath, tokCoordinator, replyTo(q, "r-host", "x")), http.StatusNotFound, apiv1.CodeNotFound)
}
