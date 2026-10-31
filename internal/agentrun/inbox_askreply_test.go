package agentrun

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var (
	askQuestion = "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
	askReply    = "message." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
)

func TestInboxAskDefaultsToParentAndChecksReceipt(t *testing.T) {
	h := newHarness(t)
	var bodies []map[string]json.RawMessage
	receipt := apiv1.InboxMetadata{Name: askQuestion, UID: "q-uid", Project: "sample", Key: "q-1", Kind: apiv1.InboxKindQuestion, Recipient: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "lead-uid"}}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		var fields map[string]json.RawMessage
		if r.Method != http.MethodPost || r.URL.Path != apiv1.InboxPath || json.Unmarshal(body, &fields) != nil {
			t.Fatal("unexpected request", r.Method, r.URL)
		}
		bodies = append(bodies, fields)
		writeTestJSON(w, http.StatusCreated, apiv1.InboxResult{Outcome: "Created", Message: receipt})
	}
	h.env.Stdin = strings.NewReader("Which base branch?")
	h.mustRun(ExitOK, "inbox", "ask", "--project", "sample", "--key", "q-1")
	if len(bodies) != 1 || string(bodies[0]["kind"]) != `"question"` || string(bodies[0]["toParent"]) != "true" || bodies[0]["recipient"] != nil || bodies[0]["inReplyTo"] != nil {
		t.Fatalf("ask without --to must ask the server for the recorded parent: %v", bodies)
	}
	if !strings.Contains(h.stdout.String(), `"recipient"`) || !strings.Contains(h.stdout.String(), "lead-uid") {
		t.Fatal("ask did not print the resolved recipient", h.stdout.String())
	}
	h.env.Stdin = strings.NewReader("For you")
	receipt.Recipient = apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "bystander", UID: "b-uid"}
	h.mustRun(ExitOK, "inbox", "ask", "--project", "sample", "--to", "bystander", "--key", "q-1")
	var to apiv1.TaskClaimOwner
	if json.Unmarshal(bodies[1]["recipient"], &to) != nil || to != (apiv1.TaskClaimOwner{Name: "bystander"}) || bodies[1]["toParent"] != nil {
		t.Fatalf("ask --to intent: %v", bodies[1])
	}
	// A receipt for another recipient, kind or key is not this ask.
	for _, bad := range []apiv1.InboxMetadata{
		{Name: askQuestion, UID: "q-uid", Project: "sample", Key: "q-1", Kind: apiv1.InboxKindQuestion, Recipient: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "lead-uid"}},
		{Name: askQuestion, UID: "q-uid", Project: "sample", Key: "q-1", Recipient: receipt.Recipient},
		{Name: askQuestion, UID: "q-uid", Project: "sample", Key: "other", Kind: apiv1.InboxKindQuestion, Recipient: receipt.Recipient},
	} {
		receipt = bad
		h.env.Stdin = strings.NewReader("For you")
		h.mustRun(ExitUnconfirmed, "inbox", "ask", "--project", "sample", "--to", "bystander", "--key", "q-1")
	}
	n := len(h.api.requests())
	for _, args := range [][]string{
		{"--key", "Bad Key"},
		{"--key", "q-1", "--to", "bad/name"},
		{"--key", "q-1", "--to-uid", "uid-without-to"},
		{"--key", "q-1", "extra-positional"},
	} {
		h.env.Stdin = strings.NewReader("x")
		h.mustRun(ExitUsage, append([]string{"inbox", "ask", "--project", "sample"}, args...)...)
	}
	if len(h.api.requests()) != n {
		t.Fatal("invalid ask reached the API")
	}
}

func TestInboxReplyNamesOnlyTheQuestion(t *testing.T) {
	h := newHarness(t)
	var bodies []map[string]json.RawMessage
	status := http.StatusCreated
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil {
			t.Fatal("reply body")
		}
		bodies = append(bodies, fields)
		switch status {
		case http.StatusCreated:
			writeTestJSON(w, status, apiv1.InboxResult{Outcome: "Created", Message: apiv1.InboxMetadata{Name: askReply, UID: "r-uid", Project: "sample", Key: "r-1", Kind: apiv1.InboxKindReply, InReplyTo: &apiv1.InboxMessageRef{Name: askQuestion, UID: "q-uid"}, Recipient: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "child", UID: "child-uid"}}})
		default:
			writeTestJSON(w, status, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeConflict, Message: "PRIVATE_BODY"}})
		}
	}
	h.env.Stdin = strings.NewReader("Yes, rebase on main.")
	h.mustRun(ExitOK, "inbox", "reply", askQuestion, "--project", "sample", "--key", "r-1")
	var ref apiv1.InboxMessageRef
	if json.Unmarshal(bodies[0]["inReplyTo"], &ref) != nil || ref != (apiv1.InboxMessageRef{Name: askQuestion}) || string(bodies[0]["kind"]) != `"reply"` || bodies[0]["recipient"] != nil || bodies[0]["toParent"] != nil {
		t.Fatalf("reply intent: %v", bodies[0])
	}
	for _, tc := range []struct {
		status, exit int
		want         string
	}{
		{http.StatusConflict, ExitFailed, "already has a reply"},
		{http.StatusUnprocessableEntity, ExitFailed, "only a question takes a reply"},
		{http.StatusNotFound, ExitNotFound, "addressed to this Session"},
	} {
		status = tc.status
		h.env.Stdin = strings.NewReader("again")
		h.mustRun(tc.exit, "inbox", "reply", askQuestion, "--project", "sample", "--key", "r-1")
		if !strings.Contains(h.stderr.String(), tc.want) || strings.Contains(h.stderr.String(), "PRIVATE_BODY") {
			t.Fatalf("%d refusal: %s", tc.status, h.stderr.String())
		}
	}
	n := len(h.api.requests())
	h.env.Stdin = strings.NewReader("x")
	h.mustRun(ExitUsage, "inbox", "reply", "not-a-message", "--project", "sample", "--key", "r-1")
	h.mustRun(ExitUsage, "inbox", "reply", askQuestion, "--project", "sample")
	if len(h.api.requests()) != n {
		t.Fatal("invalid reply reached the API")
	}
}

func TestInboxListAndReadShowThreadState(t *testing.T) {
	h := newHarness(t)
	echoBox := true
	thread := &apiv1.InboxThread{State: apiv1.InboxStateAnswered, Question: apiv1.InboxMessageRef{Name: askQuestion, UID: "q-uid"}, Reply: &apiv1.InboxMessageRef{Name: askReply, UID: "r-uid"}, WaitingOn: &apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "child", UID: "child-uid"}, StaleAfterSeconds: 1800}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Query().Get(apiv1.InboxViewQuery) != apiv1.InboxViewThread {
			t.Error("list and read must ask for the thread view")
		}
		switch r.URL.Path {
		case apiv1.InboxPath:
			list := apiv1.InboxList{Items: []apiv1.InboxMetadata{{Name: askQuestion, UID: "q-uid", Project: "p", Kind: apiv1.InboxKindQuestion, Thread: thread}}, View: apiv1.InboxViewThread}
			if r.URL.Query().Get(apiv1.InboxBoxQuery) == apiv1.InboxBoxSent && echoBox {
				list.Box = apiv1.InboxBoxSent
			}
			writeTestJSON(w, 200, list)
		case apiv1.InboxPath + "/" + askReply:
			writeTestJSON(w, 200, apiv1.InboxMessage{InboxMetadata: apiv1.InboxMetadata{Name: askReply, UID: "r-uid", Project: "p", Kind: apiv1.InboxKindReply, InReplyTo: &thread.Question, Thread: thread}, Text: "exact reply text"})
		default:
			t.Error("unexpected request", r.URL)
		}
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "--sent")
	if !strings.Contains(h.stdout.String(), `"state": "Answered"`) || !strings.Contains(h.stdout.String(), `"kind": "question"`) {
		t.Fatal("list did not show the thread state", h.stdout.String())
	}
	if got := h.api.requests(); url.Values(got[len(got)-1].query).Get(apiv1.InboxBoxQuery) != apiv1.InboxBoxSent {
		t.Fatal("--sent did not ask for the sent box")
	}
	// An older API ignores box=sent and would answer with the caller's inbox.
	echoBox = false
	h.mustRun(ExitFailed, "inbox", "list", "--project", "p", "--sent")
	if !strings.Contains(h.stderr.String(), "does not support --sent") {
		t.Fatal(h.stderr.String())
	}
	h.mustRun(ExitOK, "inbox", "read", askReply, "--project", "p")
	if h.stdout.String() != "exact reply text" {
		t.Fatalf("read stdout must stay the exact text: %q", h.stdout.String())
	}
	if want := "inbox: reply " + askQuestion + " state=Answered reply=" + askReply + " waiting-on=child"; !strings.Contains(h.stderr.String(), want) {
		t.Fatalf("read did not report the thread on stderr: %q", h.stderr.String())
	}
}

func TestInboxThreadOutputIsCheckedAndOlderAPIsAreNamed(t *testing.T) {
	h := newHarness(t)
	state := "Answered\x1b[2J"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodPost:
			// An older or question-disabled API refuses ask and reply as unknown intent.
			writeTestJSON(w, http.StatusBadRequest, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeBadRequest, Message: "PRIVATE_BODY"}})
		case r.URL.Path == apiv1.InboxPath:
			// An older API ignores view=thread.
			writeTestJSON(w, 200, apiv1.InboxList{Items: []apiv1.InboxMetadata{{Name: askQuestion, UID: "q-uid", Project: "p"}}})
		default:
			thread := &apiv1.InboxThread{State: state, Question: apiv1.InboxMessageRef{Name: askQuestion}}
			writeTestJSON(w, 200, apiv1.InboxMessage{InboxMetadata: apiv1.InboxMetadata{Name: askReply, UID: "r-uid", Project: "p", Kind: apiv1.InboxKindReply, Thread: thread}, Text: "text"})
		}
	}
	h.mustRun(ExitOK, "inbox", "read", askReply, "--project", "p")
	if strings.Contains(h.stderr.String(), "\x1b") || !strings.Contains(h.stderr.String(), "inbox: reply state could not be confirmed") || h.stdout.String() != "text" {
		t.Fatalf("unchecked thread output: %q", h.stderr.String())
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p")
	if !strings.Contains(h.stderr.String(), "no thread view") {
		t.Fatalf("missing thread view was not named: %q", h.stderr.String())
	}
	for _, args := range [][]string{{"ask", "--key", "q-1"}, {"reply", askQuestion, "--key", "r-1"}} {
		h.env.Stdin = strings.NewReader("x")
		h.mustRun(ExitFailed, append(append([]string{"inbox"}, args...), "--project", "p")...)
		if !strings.Contains(h.stderr.String(), "does not accept questions and replies") || strings.Contains(h.stderr.String(), "PRIVATE_BODY") {
			t.Fatalf("400 refusal: %q", h.stderr.String())
		}
	}
}
