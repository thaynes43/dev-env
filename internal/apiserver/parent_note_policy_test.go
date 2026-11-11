package apiserver

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
)

func TestParentNotePolicyCaller(t *testing.T) {
	f, _, child, _ := parentNoteFixture(t, false)
	if err := f.c.Create(context.Background(), meatballPolicy()); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(meatballAccount)
	id.PodName, id.PodUID = "front-door", "front-door-pod"
	f.auth[tokMeatball] = id
	f.srv.ParentNotes = false
	f.h = f.srv.Handler()
	if n := countMessages(t, f); n != 0 {
		t.Fatal("fixture stored a note", n)
	}
	off := apiv1.TellParentRequest{Key: "off", Kind: inbox.KindQuestion, Text: "x"}
	for _, call := range []struct {
		token string
		body  any
	}{
		{child, off},
		{tokMeatball, apiv1.TellParentRequest{Session: "child", Key: "off", Text: "x"}},
	} {
		w := f.do(http.MethodPost, apiv1.ParentNotesPath, call.token, call.body)
		wantError(t, w, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
		if strings.Contains(w.Body.String(), "x") && strings.Contains(w.Body.String(), "off") {
			t.Fatal("flag-off refusal carried the request")
		}
	}
	if countMessages(t, f) != 0 {
		t.Fatal("flag off stored a note")
	}
	f.srv.ParentNotes = true
	f.h = f.srv.Handler()

	wantError(t, f.do(http.MethodPost, apiv1.ParentNotesPath, child, apiv1.TellParentRequest{Session: "child", Key: "no-child", Kind: inbox.KindQuestion, Text: "x"}), http.StatusBadRequest, apiv1.CodeBadRequest)
	if countMessages(t, f) != 0 {
		t.Fatal("a session used the caller send form")
	}

	account := f.sessionPod("account-child", "dev", 1)
	accountSession := f.session("account-child")
	owned := f.session("child")
	accountSession.Spec.Parent = "other/account"
	accountSession.Spec.Repo = owned.Spec.Repo
	accountSession.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: owned.Annotations[v1alpha1.AnnotationProjectSnapshot]}
	if err := f.c.Update(context.Background(), accountSession); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPost, apiv1.ParentNotesPath, account, apiv1.TellParentRequest{Key: "other-sa", Kind: inbox.KindQuestion, Text: "  PRIVATE_OTHER_SA\n"})
	wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if strings.Contains(w.Body.String(), "PRIVATE_OTHER_SA") || countMessages(t, f) != 0 {
		t.Fatal("another ServiceAccount parent was stored or disclosed", w.Body.String())
	}

	owned.Spec.Parent = meatballAccount
	if err := f.c.Update(context.Background(), owned); err != nil {
		t.Fatal(err)
	}
	otherToken := f.sessionPod("other-child", "dev", 1)
	other := f.session("other-child")
	other.Spec.Parent = "parent"
	other.Spec.Repo = owned.Spec.Repo
	other.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: owned.Annotations[v1alpha1.AnnotationProjectSnapshot]}
	if err := f.c.Update(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	f.srv.Log = logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))
	text := "  PRIVATE_POLICY_NOTE\n"
	key := "private-policy-key"
	note := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: key, Kind: inbox.KindQuestion, Text: text}, http.StatusCreated)
	if note.Sender.Name != "child" || note.Recipient.Name != "" || note.CallerRecipient == nil || note.CallerRecipient.ServiceAccount != meatballAccount || note.CallerRecipient.PolicyUID != "policy-uid" || note.Kind != inbox.KindQuestion || note.Actor.Kind != "session" {
		t.Fatalf("owned note lost its caller recipient: %+v", note)
	}
	again := tellParentNote(t, f, child, apiv1.TellParentRequest{Key: key, Kind: inbox.KindQuestion, Text: text}, http.StatusOK)
	if again.Name != note.Name || again.UID != note.UID {
		t.Fatal("same intent did not confirm the stored note")
	}
	changed := f.do(http.MethodPost, apiv1.ParentNotesPath, child, apiv1.TellParentRequest{Key: key, Kind: inbox.KindQuestion, Text: text + "changed"})
	if changed.Code != http.StatusConflict || strings.Contains(changed.Body.String(), text) {
		t.Fatal("different intent replaced a note or returned its text", changed.Code, changed.Body.String())
	}
	if countMessages(t, f) != 1 {
		t.Fatal("retries stored another note", countMessages(t, f))
	}

	foreign := tellParentNote(t, f, otherToken, apiv1.TellParentRequest{Key: "other-parent", Kind: inbox.KindBlocker, Text: "  PRIVATE_FOREIGN_NOTE\n"}, http.StatusCreated)
	hidden := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+foreign.Name, tokMeatball, nil)
	missing := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+missingParentNoteName, tokMeatball, nil)
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != missing.Body.String() || strings.Contains(hidden.Body.String(), "PRIVATE_FOREIGN_NOTE") || strings.Contains(hidden.Body.String(), foreign.Name) {
		t.Fatal("another parent's note was not hidden as a missing name", hidden.Code, hidden.Body.String())
	}
	inboxList := parentNoteList(t, f, tokMeatball, "?box=inbox")
	if len(inboxList.Items) != 1 || inboxList.Items[0].Name != note.Name {
		t.Fatal("caller inbox did not list only its own note", inboxList.Items)
	}
	waitingBefore := parentNoteList(t, f, tokMeatball, "?waiting=true")
	if len(waitingBefore.Items) != 1 || waitingBefore.Items[0].Name != note.Name {
		t.Fatal("the note was not waiting before its answer", waitingBefore.Items)
	}
	read := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+note.Name, tokMeatball, nil)
	if read.Code != http.StatusOK || decode[apiv1.InboxMessage](t, read).Text != text {
		t.Fatal("caller read did not return the stored text", read.Code, read.Body.String())
	}

	beforeAnswer := countMessages(t, f)
	for _, token := range []string{child, otherToken} {
		w := f.do(http.MethodPost, apiv1.ParentNotesPath+"/"+note.Name+"/answer", token, apiv1.AnswerParentRequest{Key: "session-answer", Text: "session answer"})
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if strings.Contains(w.Body.String(), text) {
			t.Fatal("a session answer disclosed the note")
		}
	}
	if countMessages(t, f) != beforeAnswer {
		t.Fatal("a session stored a caller answer")
	}

	answerText := "  PRIVATE_POLICY_ANSWER\n"
	answer := answerParentTest(t, f, tokMeatball, note.Name, "policy-answer", answerText, http.StatusCreated)
	if answer.CallerSender == nil || answer.CallerSender.ServiceAccount != meatballAccount || answer.CallerSender.PolicyUID != "policy-uid" || answer.Kind != inbox.KindReply || answer.Recipient.Name != "child" || answer.Actor.Kind != "policy" || answer.InReplyTo == nil || answer.InReplyTo.Name != note.Name {
		t.Fatalf("answer was not stored as the policy caller: %+v", answer)
	}
	confirmed := answerParentTest(t, f, tokMeatball, note.Name, "policy-answer", answerText, http.StatusOK)
	if confirmed.UID != answer.UID {
		t.Fatal("same answer did not confirm")
	}
	second := f.do(http.MethodPost, apiv1.ParentNotesPath+"/"+note.Name+"/answer", tokMeatball, apiv1.AnswerParentRequest{Key: "policy-answer-2", Text: "other"})
	wantError(t, second, http.StatusConflict, apiv1.CodeConflict)
	if strings.Contains(second.Body.String(), answerText) || strings.Contains(second.Body.String(), "other") {
		t.Fatal("second answer disclosed text")
	}
	waitingAfter := parentNoteList(t, f, tokMeatball, "?waiting=true")
	if len(waitingAfter.Items) != 0 {
		t.Fatal("an answered note stayed in the waiting list", waitingAfter.Items)
	}
	if kept := parentNoteList(t, f, tokMeatball, "?box=inbox"); len(kept.Items) != 1 || kept.Items[0].Name != note.Name {
		t.Fatal("the historical list dropped the answered note", kept.Items)
	}
	wantError(t, f.do(http.MethodGet, apiv1.ParentNotesPath+"?waiting=true", child, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	childRead := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+answer.Name, child, nil)
	if childRead.Code != http.StatusOK || decode[apiv1.InboxMessage](t, childRead).Text != answerText {
		t.Fatal("child could not read the stored answer", childRead.Code, childRead.Body.String())
	}
	wait := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+note.Name+"/wait?seconds=1", child, nil)
	if wait.Code != http.StatusOK {
		t.Fatal("child wait", wait.Code, wait.Body.String())
	}
	waited := decode[apiv1.ParentNoteWaitResult](t, wait)
	if waited.State != apiv1.ParentNoteAnswered || waited.Reply == nil || waited.Reply.Text != answerText {
		t.Fatal("child wait did not return the stored answer")
	}

	foreignSend := f.do(http.MethodPost, apiv1.ParentNotesPath, tokMeatball, apiv1.TellParentRequest{Session: "other-child", Key: "foreign-send", Text: "  PRIVATE_FOREIGN_SEND\n"})
	missingSend := f.do(http.MethodPost, apiv1.ParentNotesPath, tokMeatball, apiv1.TellParentRequest{Session: "missing-child", Key: "missing-send", Text: "  PRIVATE_MISSING_SEND\n"})
	if foreignSend.Code != http.StatusNotFound || foreignSend.Body.String() != missingSend.Body.String() || foreignSend.Body.String() != missing.Body.String() || strings.Contains(foreignSend.Body.String(), "PRIVATE_FOREIGN_SEND") || strings.Contains(missingSend.Body.String(), "missing-child") {
		t.Fatal("a session the caller did not start was not hidden as a missing name", foreignSend.Code, foreignSend.Body.String(), missingSend.Body.String())
	}
	sentText := "  PRIVATE_POLICY_SEND\n"
	sent := tellParentNote(t, f, tokMeatball, apiv1.TellParentRequest{Session: "child", Key: "policy-send", Text: sentText}, http.StatusCreated)
	if sent.CallerSender == nil || sent.CallerSender.PolicyUID != "policy-uid" || sent.Kind != "" || sent.Recipient.Name != "child" || sent.Actor.Kind != "policy" {
		t.Fatalf("plain message was not stored as the policy caller: %+v", sent)
	}
	childPlain := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+sent.Name, child, nil)
	if childPlain.Code != http.StatusOK || decode[apiv1.InboxMessage](t, childPlain).Text != sentText {
		t.Fatal("child read changed the stored message", childPlain.Code, childPlain.Body.String())
	}
	sentAgain := tellParentNote(t, f, tokMeatball, apiv1.TellParentRequest{Session: "child", Key: "policy-send", Text: sentText}, http.StatusOK)
	if sentAgain.Name != sent.Name {
		t.Fatal("same plain message did not confirm")
	}
	resent := f.do(http.MethodPost, apiv1.ParentNotesPath, tokMeatball, apiv1.TellParentRequest{Session: "child", Key: "policy-send", Text: sentText + "changed"})
	if resent.Code != http.StatusConflict || strings.Contains(resent.Body.String(), sentText) {
		t.Fatal("a different plain message replaced the stored one or returned its text", resent.Code, resent.Body.String())
	}
	sentList := parentNoteList(t, f, tokMeatball, "?box=sent")
	if len(sentList.Items) != 2 || sentList.Items[0].Kind != inbox.KindReply || sentList.Items[1].Kind != apiv1.InboxKindPlain {
		t.Fatal("caller sent box", sentList.Items)
	}
	childInbox := parentNoteList(t, f, child, "")
	if len(childInbox.Items) != 2 || childInbox.Items[0].Name != answer.Name || childInbox.Items[1].Name != sent.Name || childInbox.Items[1].Kind != apiv1.InboxKindPlain {
		t.Fatal("child inbox hid the answer or the plain message", childInbox.Items)
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, apiv1.ParentNotesPath + "/" + note.Name + "/wait?seconds=1"},
		{http.MethodPost, apiv1.InboxPath},
		{http.MethodGet, "/v1/sessions/child/log"},
		{http.MethodPost, "/v1/sessions/child/messages"},
		{http.MethodPost, "/v1/sessions/child/threads"},
		{http.MethodGet, "/v1/grants"},
	} {
		w := f.do(route.method, route.path, tokMeatball, map[string]any{})
		wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
		if strings.Contains(w.Body.String(), text) || strings.Contains(w.Body.String(), answerText) || strings.Contains(w.Body.String(), key) {
			t.Fatal("a refused route carried a note", route.method, route.path)
		}
	}
	privatePath := "PRIVATE_PATH_TEXT"
	pathRead := f.do(http.MethodGet, apiv1.ParentNotesPath+"/"+privatePath, tokMeatball, nil)
	if pathRead.Code != http.StatusNotFound || strings.Contains(pathRead.Body.String(), privatePath) {
		t.Fatal("a private path was echoed", pathRead.Code, pathRead.Body.String())
	}
	if strings.Contains(logs.String(), text) || strings.Contains(logs.String(), answerText) || strings.Contains(logs.String(), sentText) || strings.Contains(logs.String(), key) || strings.Contains(logs.String(), "PRIVATE_FOREIGN_NOTE") || strings.Contains(logs.String(), "policy-answer") || strings.Contains(logs.String(), privatePath) {
		t.Fatal("parent note text or key was logged")
	}
	if !strings.Contains(logs.String(), "/v1/parent-notes/{message}") {
		t.Fatal("parent note log used the raw path")
	}
}

func TestParentNoteFencesRefuseReplacement(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		f, child := meatballChildFixture(t)
		reads := 0
		f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if err == nil && key.Name == callerPolicyName && key.Namespace == callerPolicyNamespace {
				reads++
				if reads == 1 {
					if err := recreateObject(c, obj, "policy-uid-2"); err != nil {
						return err
					}
				}
			}
			return err
		}})
		before := countMessages(t, f)
		w := f.do(http.MethodPost, apiv1.ParentNotesPath, child, apiv1.TellParentRequest{Key: "replaced-policy", Kind: inbox.KindQuestion, Text: "note text"})
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if countMessages(t, f) != before || strings.Contains(w.Body.String(), "note text") {
			t.Fatal("a replaced policy stored a note", w.Code, w.Body.String())
		}
	})
	t.Run("child", func(t *testing.T) {
		f, _ := meatballChildFixture(t)
		reads := 0
		f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if err == nil && key.Name == "child" && key.Namespace == sessionNS {
				reads++
				if reads == 1 {
					if err := recreateObject(c, obj, "child-uid-2"); err != nil {
						return err
					}
				}
			}
			return err
		}})
		before := countMessages(t, f)
		w := f.do(http.MethodPost, apiv1.ParentNotesPath, tokMeatball, apiv1.TellParentRequest{Session: "child", Key: "replaced-child", Text: "note text"})
		wantError(t, w, http.StatusNotFound, apiv1.CodeNotFound)
		if countMessages(t, f) != before || strings.Contains(w.Body.String(), "note text") {
			t.Fatal("a replaced child stored a message", w.Code, w.Body.String())
		}
	})
}

func meatballChildFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f, _, child, _ := parentNoteFixture(t, false)
	if err := f.c.Create(context.Background(), meatballPolicy()); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(meatballAccount)
	id.PodName, id.PodUID = "front-door", "front-door-pod"
	f.auth[tokMeatball] = id
	owned := f.session("child")
	owned.Spec.Parent = meatballAccount
	if err := f.c.Update(context.Background(), owned); err != nil {
		t.Fatal(err)
	}
	f.h = f.srv.Handler()
	return f, child
}

func recreateObject(c client.WithWatch, obj client.Object, uid types.UID) error {
	ctx := context.Background()
	fresh := obj.DeepCopyObject().(client.Object)
	if err := c.Delete(ctx, fresh); err != nil {
		return err
	}
	fresh.SetResourceVersion("")
	fresh.SetUID(uid)
	return c.Create(ctx, fresh)
}
