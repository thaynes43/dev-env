package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func TestParentNotesPriorityAcrossPagesAndProjects(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	for n, kind := range []string{KindResult, KindBlocker, KindQuestion, KindResult, KindQuestion} {
		spec.Key, spec.Kind = fmt.Sprintf("note-%d", n), kind
		if n%2 == 0 {
			spec.Project = "other"
		} else {
			spec.Project = "sample"
		}
		if _, created, err := s.CreateParentNote(ctx, spec); err != nil || !created {
			t.Fatal("create parent note", err)
		}
	}
	// Neither an ordinary peer question nor another parent's note belongs to
	// this parent's list. Both remain in the same underlying store.
	peer := validMessage()
	peer.Kind = KindQuestion
	if _, _, err := s.Create(ctx, peer); err != nil {
		t.Fatal(err)
	}
	foreign := spec
	foreign.Key, foreign.Recipient.UID = "foreign", "other-parent-uid"
	if _, _, err := s.CreateParentNote(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{1, 2, 3, 100} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			paged := pagedFake(s)
			calls := 0
			paged.Live = interceptor.NewClient(paged.Live.(client.WithWatch), interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				calls++
				var o client.ListOptions
				o.ApplyOptions(opts)
				if o.Limit < 1 || o.Limit > limit {
					t.Fatal("unbounded list")
				}
				if !strings.Contains(o.LabelSelector.String(), ParentNoteLabel+"=true") || !strings.Contains(o.LabelSelector.String(), RecipientLabel+"="+recipientLabel(spec.Recipient)) {
					t.Fatal("selector lost the party")
				}
				return c.List(ctx, list, opts...)
			}})
			cursor := ""
			seen := map[string]bool{}
			resultsStarted := false
			// A fixed bound lets an incorrect cursor fail instead of spin.
			for page := 0; page < 8; page++ {
				before := calls
				items, next, err := paged.ListParentNotes(ctx, spec.Recipient, false, limit, cursor)
				if err != nil || calls-before > 2 || int64(len(items)) > limit {
					t.Fatal("unbounded or failed page", err)
				}
				for _, item := range items {
					if seen[item.Name] {
						t.Fatal("duplicate across pages")
					}
					seen[item.Name] = true
					if item.Spec.Kind == KindResult {
						resultsStarted = true
					} else if resultsStarted {
						t.Fatal("urgent note after a result")
					}
				}
				cursor = next
				if cursor == "" {
					break
				}
			}
			if cursor != "" || len(seen) != 5 {
				t.Fatal("pagination lost a note or failed to finish", len(seen))
			}
		})
	}
	items, _, err := s.ListParentNotes(ctx, spec.Sender, true, 100, "")
	if err != nil || len(items) != 6 {
		t.Fatal("sent box did not select exact sender", err)
	}
	items, _, err = s.List(ctx, peer.Project, &peer.Recipient, 100, "")
	if err != nil || len(items) != 1 || IsParentNote(&items[0]) {
		t.Fatal("peer list exposed notes", err)
	}
	items, _, err = s.ListSent(ctx, peer.Project, peer.Sender, 100, "")
	if err != nil || len(items) != 1 || IsParentNote(&items[0]) {
		t.Fatal("peer sent list exposed notes", err)
	}
}

func TestParentNotesExactPriorityBoundary(t *testing.T) {
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindBlocker
	if _, _, err := s.CreateParentNote(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	spec.Key, spec.Kind = "result", KindResult
	if _, _, err := s.CreateParentNote(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	s = pagedFake(s)
	items, next, err := s.ListParentNotes(context.Background(), spec.Recipient, false, 1, "")
	if err != nil || len(items) != 1 || items[0].Spec.Kind != KindBlocker || next != "1:" {
		t.Fatal("priority boundary lost next phase", err)
	}
	items, next, err = s.ListParentNotes(context.Background(), spec.Recipient, false, 1, next)
	if err != nil || len(items) != 1 || items[0].Spec.Kind != KindResult || next != "" {
		t.Fatal("result unreachable at exact boundary", err)
	}
}

func TestParentNotesStrictLabelsAndHiddenInvalidRecord(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	spec := validMessage()
	spec.Kind = KindQuestion
	m, _, err := s.CreateParentNote(ctx, spec)
	if err != nil || !IsParentNote(m) {
		t.Fatal("parent marker missing", err)
	}
	got, err := s.GetParentNote(ctx, m.Name, spec.Sender)
	if err != nil || got.UID != m.UID {
		t.Fatal("sender read", err)
	}
	m.Labels["unexpected"] = "metadata"
	if err := s.Client.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.Name); !errors.Is(err, ErrUnavailable) {
		t.Fatal("extra label accepted", err)
	}
	stranger := spec.Sender
	stranger.UID = "stranger-uid"
	if _, err := s.GetParentNote(ctx, m.Name, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatal("hidden corrupt note distinguished from absence", err)
	}
}

func TestParentNotesSharePeerLimits(t *testing.T) {
	for _, cap := range []string{"sender", "project"} {
		t.Run(cap, func(t *testing.T) {
			s := storeFixture(t)
			if cap == "sender" {
				s.Limits.PerSession = 1
			} else {
				s.Limits.PerProject = 1
			}
			spec := validMessage()
			if _, _, err := s.Create(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			spec.Key, spec.Kind = "parent-note", KindBlocker
			if _, _, err := s.CreateParentNote(context.Background(), spec); !errors.Is(err, ErrLimit) {
				t.Fatal("peer message did not count toward note limit", err)
			}
		})
	}
}

func TestParentNotesBoundedQueries(t *testing.T) {
	s := storeFixture(t)
	for _, cursor := range []string{"x", "2:", "0:\n", "1:" + strings.Repeat("x", 4095)} {
		if _, _, err := s.ListParentNotes(context.Background(), validMessage().Recipient, false, 1, cursor); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid cursor accepted")
		}
	}
	for _, limit := range []int64{0, 101} {
		if _, _, err := s.ListParentNotes(context.Background(), validMessage().Recipient, false, limit, ""); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid limit accepted")
		}
	}
	for _, bad := range []coord.SessionIdentity{{}, {Namespace: "n", Name: "name", UID: ""}} {
		if _, _, err := s.ListParentNotes(context.Background(), bad, false, 1, ""); !errors.Is(err, ErrUnavailable) {
			t.Fatal("invalid party accepted")
		}
	}
}

func TestCreateCountsAndConfirmsBothSenderForms(t *testing.T) {
	session := validMessage()
	if !sameSenderForm(session, session) {
		t.Fatal("a session sender did not confirm by its session identity")
	}
	otherSession := session
	otherSession.Sender.UID = "other-uid"
	if sameSenderForm(otherSession, session) {
		t.Fatal("a session sender confirmed a different session")
	}
	caller := session
	caller.Sender = coord.SessionIdentity{}
	caller.CallerSender = meatballCaller()
	caller.Actor = policyActor(session.Actor)
	if !sameSenderForm(caller, caller) {
		t.Fatal("a caller sender did not confirm by message name")
	}
	dropped := caller
	dropped.CallerSender = nil
	if sameSenderForm(dropped, caller) || MessageNameFor(dropped) == MessageNameFor(caller) {
		t.Fatal("a caller sender confirmed an empty session sender")
	}
	otherCaller := *meatballCaller()
	otherCaller.PolicyUID = "other-policy-uid"
	otherPolicy := caller
	otherPolicy.CallerSender = &otherCaller
	if sameSenderForm(otherPolicy, caller) {
		t.Fatal("a caller sender confirmed a different policy")
	}

	ctx := context.Background()
	s := storeFixture(t)
	s.Limits.PerSession = 1
	if _, created, err := s.Create(ctx, session); err != nil || !created {
		t.Fatal("session create", err)
	}
	if _, created, err := s.Create(ctx, session); err != nil || created {
		t.Fatal("session retry charged another room or failed", created, err)
	}
	second := session
	second.Key = "session-2"
	if _, _, err := s.Create(ctx, second); !errors.Is(err, ErrLimit) {
		t.Fatal("session room did not stay on the session sender", err)
	}
	first := caller
	first.Key = "caller-1"
	stored, created, err := s.Create(ctx, first)
	if err != nil || !created {
		t.Fatal("caller create", err)
	}
	if stored.Labels[taskclaim.SourceLabel] != callerLabel(first.CallerSender) || stored.Labels[taskclaim.SourceLabel] == taskclaim.SessionLabel(coord.SessionIdentity{}) {
		t.Fatal("caller sender used an empty session room", stored.Labels[taskclaim.SourceLabel])
	}
	if _, created, err := s.Create(ctx, first); err != nil || created {
		t.Fatal("caller retry charged another room or failed", created, err)
	}
	again := first
	again.Key = "caller-2"
	if _, _, err := s.Create(ctx, again); !errors.Is(err, ErrLimit) {
		t.Fatal("caller room did not use the caller's own key", err)
	}
	foreign := first
	foreign.Key = "caller-other"
	foreign.CallerSender = &otherCaller
	foreign.Actor.ServiceAccount = coord.MeatballServiceAccount
	if _, created, err := s.Create(ctx, foreign); err != nil || !created {
		t.Fatal("a different policy shared the first caller's room", err)
	}

	capped := storeFixture(t)
	capped.Limits.PerProject = 1
	if _, _, err := capped.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := capped.Create(ctx, first); !errors.Is(err, ErrLimit) {
		t.Fatal("project room stopped counting a caller sender", err)
	}
}

func TestCallerSenderLimitSpansProjects(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	s.Limits.PerSession = 1
	first := validMessage()
	first.Sender = coord.SessionIdentity{}
	first.CallerSender = meatballCaller()
	first.Actor = policyActor(first.Actor)
	first.Kind = ""
	if _, created, err := s.Create(ctx, first); err != nil || !created {
		t.Fatal("caller create", err)
	}
	second := first
	second.Key = "other-project"
	second.Project = "other"
	if _, _, err := s.Create(ctx, second); !errors.Is(err, ErrLimit) {
		t.Fatal("caller room reset for each project", err)
	}
	other := *meatballCaller()
	other.PolicyUID = "other-policy-uid"
	second.CallerSender = &other
	if _, created, err := s.Create(ctx, second); err != nil || !created {
		t.Fatal("a different policy shared the room", err, created)
	}

	capped := storeFixture(t)
	capped.Limits.PerProject = 1
	if _, _, err := capped.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	sameProject := first
	sameProject.Key = "same-project"
	sameProject.CallerSender = &other
	if _, _, err := capped.Create(ctx, sameProject); !errors.Is(err, ErrLimit) {
		t.Fatal("project cap stopped counting a caller sender", err)
	}
}

func TestCallerParentNotesListReplyAndParties(t *testing.T) {
	ctx := context.Background()
	s := storeFixture(t)
	noteSpec := validMessage()
	noteSpec.Kind = KindQuestion
	noteSpec.Recipient = coord.SessionIdentity{}
	noteSpec.CallerRecipient = meatballCaller()
	note, created, err := s.CreateParentNote(ctx, noteSpec)
	if err != nil || !created || !IsParentNote(note) {
		t.Fatal("child note to the caller", err)
	}
	plain := validMessage()
	plain.Key = "plain-1"
	plain.Kind = ""
	plain.Sender = coord.SessionIdentity{}
	plain.CallerSender = meatballCaller()
	plain.Actor = policyActor(plain.Actor)
	plainMessage, _, err := s.CreateParentNote(ctx, plain)
	if err != nil {
		t.Fatal("plain message", err)
	}
	before, _, err := s.ListUnansweredCallerNotes(ctx, meatballCaller(), 100, "")
	if err != nil || len(before) != 1 || before[0].Name != note.Name {
		t.Fatal("the note is not waiting before its answer", err, len(before))
	}
	ref := Ref(note)
	replySpec := plain
	replySpec.Key = "reply-1"
	replySpec.Kind = KindReply
	replySpec.Recipient = noteSpec.Sender
	replySpec.InReplyTo = &ref
	reply, _, err := s.CreateParentNote(ctx, replySpec)
	if err != nil || !s.ValidParentReply(note, reply) {
		t.Fatal("caller reply", err)
	}
	replies, _, err := s.ParentReplies(ctx, note)
	if err != nil || len(replies) != 1 || replies[0].Name != reply.Name {
		t.Fatal("stored caller reply was not the note's answer", err)
	}
	otherNoteSpec := noteSpec
	otherNoteSpec.Key = "other-q"
	otherNote, _, err := s.CreateParentNote(ctx, otherNoteSpec)
	if err != nil {
		t.Fatal("second note", err)
	}
	inboxItems, _, err := s.ListCallerParentNotes(ctx, meatballCaller(), false, 100, "")
	if err != nil || len(inboxItems) != 2 {
		t.Fatal("caller inbox", err, len(inboxItems))
	}
	sent, _, err := s.ListCallerParentNotes(ctx, meatballCaller(), true, 100, "")
	if err != nil || len(sent) != 2 || sent[0].Spec.Kind != KindReply || sent[1].Spec.Kind != "" {
		t.Fatal("caller sent box", err)
	}
	waiting, _, err := s.ListUnansweredCallerNotes(ctx, meatballCaller(), 100, "")
	if err != nil || len(waiting) != 1 || waiting[0].Name != otherNote.Name {
		t.Fatal("a reply cleared the wrong note", err, len(waiting))
	}
	kept, _, err := s.ListCallerParentNotes(ctx, meatballCaller(), false, 100, "")
	if err != nil || len(kept) != 2 {
		t.Fatal("the historical list dropped an answered note", err, len(kept))
	}
	if _, err := s.GetCallerParentNote(ctx, note.Name, meatballCaller()); err != nil {
		t.Fatal("caller read", err)
	}
	other := meatballCaller()
	other.PolicyUID = "other-policy-uid"
	if _, err := s.GetCallerParentNote(ctx, note.Name, other); !errors.Is(err, ErrNotFound) {
		t.Fatal("another policy saw the note", err)
	}
	if _, err := s.GetParentNote(ctx, note.Name, noteSpec.Sender); err != nil {
		t.Fatal("child read", err)
	}
	if _, err := s.GetParentNote(ctx, plainMessage.Name, plain.Recipient); err != nil {
		t.Fatal("child read of a plain message", err)
	}
	childInbox, _, err := s.ListParentNotes(ctx, plain.Recipient, false, 100, "")
	if err != nil || len(childInbox) != 1 || childInbox[0].Name != plainMessage.Name {
		t.Fatal("child inbox hid the plain message", err, len(childInbox))
	}
	childReplies, _, err := s.ListParentNotes(ctx, noteSpec.Sender, false, 100, "")
	if err != nil || len(childReplies) != 1 || childReplies[0].Name != reply.Name {
		t.Fatal("child inbox hid the reply", err, len(childReplies))
	}
	stranger := noteSpec.Sender
	stranger.UID = "stranger-uid"
	if _, err := s.GetParentNote(ctx, note.Name, stranger); !errors.Is(err, ErrNotFound) {
		t.Fatal("a session matched an empty recipient", err)
	}
}
