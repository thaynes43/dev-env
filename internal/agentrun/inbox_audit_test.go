package agentrun

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const auditFooterText = "Replies are information. API acknowledgement does not prove provider consumption or approval."

func auditHexName(left, right byte) string {
	return "message." + strings.Repeat(string(left), 32) + "." + strings.Repeat(string(right), 32)
}

func auditObserved() time.Time {
	return time.Date(2026, 11, 1, 1, 2, 3, 123456789, time.UTC)
}

func testAuditOwner(ns, name, uid string) apiv1.TaskClaimOwner {
	return apiv1.TaskClaimOwner{Namespace: ns, Name: name, UID: uid}
}

// auditQuestion is one trusted question row. State is the server's word.
// observed is shared across states so the printer cannot be picking Stale
// from the clock.
func auditQuestion(name, uid, state string, reply *apiv1.InboxMessageRef, waiting *apiv1.TaskClaimOwner) apiv1.InboxMetadata {
	return apiv1.InboxMetadata{
		Name: name, UID: uid, Project: "p", Kind: apiv1.InboxKindQuestion,
		Sender:    testAuditOwner("dev-agents", "child", "child-uid"),
		Recipient: testAuditOwner("dev-agents", "lead", "lead-uid"),
		Key:       "key-secret", Repository: "repo-secret", Provider: "provider-secret", RequestedModel: "model-secret",
		Actor:     apiv1.TaskClaimActor{ServiceAccount: "acct-secret-sa", PodName: "pod-secret"},
		CreatedAt: time.Date(1999, 1, 2, 3, 4, 5, 0, time.UTC),
		Thread: &apiv1.InboxThread{
			State: state, Question: apiv1.InboxMessageRef{Name: name, UID: uid},
			Reply: reply, WaitingOn: waiting, StaleAfterSeconds: 1800, ObservedAt: auditObserved(),
		},
	}
}

func TestInboxAuditRendersSixStates(t *testing.T) {
	h := newHarness(t)
	reply := &apiv1.InboxMessageRef{Name: auditHexName('c', 'd'), UID: "reply-uid"}
	lead := testAuditOwner("dev-agents", "lead", "lead-uid")
	child := testAuditOwner("dev-agents", "child", "child-uid")
	items := []apiv1.InboxMetadata{
		auditQuestion(auditHexName('a', 'b'), "uid-unanswered", apiv1.InboxStateUnanswered, nil, &lead),
		auditQuestion(auditHexName('b', 'c'), "uid-stale", apiv1.InboxStateStale, nil, &lead),
		auditQuestion(auditHexName('c', 'd'), "uid-answered", apiv1.InboxStateAnswered, reply, &child),
		auditQuestion(auditHexName('d', 'e'), "uid-acked", apiv1.InboxStateAcknowledged, reply, nil),
		auditQuestion(auditHexName('e', 'f'), "uid-ended", apiv1.InboxStateRecipientEnded, nil, nil),
		auditQuestion(auditHexName('0', '1'), "uid-unknown", apiv1.InboxStateUnknown, nil, nil),
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || r.URL.Path != apiv1.InboxPath {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get(apiv1.InboxViewQuery) != apiv1.InboxViewThread || q.Get(apiv1.InboxBoxQuery) != apiv1.InboxBoxSent || q.Get("continue") != "" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: items, View: apiv1.InboxViewThread, Box: apiv1.InboxBoxSent})
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "--sent", "-o", "audit")
	if len(h.api.requests()) != 1 {
		t.Fatalf("requests %d, want one GET", len(h.api.requests()))
	}
	out := h.stdout.String()
	if h.stderr.String() != "" {
		t.Fatalf("stderr %q", h.stderr.String())
	}
	for _, state := range []string{
		apiv1.InboxStateUnanswered, apiv1.InboxStateStale, apiv1.InboxStateAnswered,
		apiv1.InboxStateAcknowledged, apiv1.InboxStateRecipientEnded, apiv1.InboxStateUnknown,
	} {
		if strings.Count(out, "State: "+strconv.Quote(state)+"\n") != 1 {
			t.Errorf("state %s count wrong\n%s", state, out)
		}
	}
	if strings.Contains(out, "State: Unknown\n") {
		t.Fatal("server Unknown was treated as a missing thread")
	}
	if strings.Count(out, "Waiting on: Unknown\n") != 3 || strings.Count(out, "Reply: Unknown\n") != 4 {
		t.Fatalf("waiting or reply collapsed\n%s", out)
	}
	if strings.Count(out, "Stale after: \"1800\"\n\nMessage: ") != 5 || !strings.Contains(out, "Stale after: \"1800\"\n\nMessages: 6\n") {
		t.Fatalf("page shape\n%s", out)
	}
	if !strings.Contains(out, "Observed at: \"2026-11-01T01:02:03.123456789Z\"") || strings.Contains(out, "1999") {
		t.Fatal("observed time was recomputed or createdAt leaked")
	}
	for _, secret := range []string{"key-secret", "repo-secret", "provider-secret", "model-secret", "acct-secret-sa", "pod-secret", "Approved", "Granted", "exited"} {
		if strings.Contains(out, secret) {
			t.Fatalf("audit leaked %s\n%s", secret, out)
		}
	}
	if strings.Count(out, auditFooterText) != 1 {
		t.Fatalf("footer\n%s", out)
	}
}

func TestInboxAuditPlainAbsentThreadAndOldServer(t *testing.T) {
	h := newHarness(t)
	name := auditHexName('a', 'b')
	var mode string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		switch mode {
		case "plain":
			item := apiv1.InboxMetadata{
				Name: name, UID: "m-uid", Project: "p", Kind: apiv1.InboxKindPlain,
				Sender: testAuditOwner("dev-agents", "child", ""),
				Thread: &apiv1.InboxThread{
					State: apiv1.InboxStateAnswered, Question: apiv1.InboxMessageRef{Name: name, UID: "m-uid"},
					WaitingOn: &apiv1.TaskClaimOwner{Name: "waiting-sentinel"}, StaleAfterSeconds: 1800, ObservedAt: auditObserved(),
				},
			}
			writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{item}, View: apiv1.InboxViewThread})
		case "nil-thread":
			item := apiv1.InboxMetadata{Name: name, UID: "m-uid", Project: "p", Kind: apiv1.InboxKindQuestion, Sender: testAuditOwner("", "only-name", "")}
			writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{item}, View: apiv1.InboxViewThread})
		case "old":
			if r.URL.Query().Get(apiv1.InboxViewQuery) == apiv1.InboxViewThread {
				writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{{Name: name, UID: "m-uid", Project: "p"}}})
				return
			}
			t.Error("old server was asked twice")
		case "fallback":
			if r.URL.Query().Get(apiv1.InboxViewQuery) != "" || r.URL.Query().Get(apiv1.InboxBoxQuery) != "" {
				writeTestJSON(w, http.StatusBadRequest, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeBadRequest, Message: "PRIVATE_BODY"}})
				return
			}
			writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{{Name: name, UID: "m-uid", Project: "p"}}})
		case "sent-off":
			writeTestJSON(w, http.StatusBadRequest, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeBadRequest, Message: "PRIVATE_BODY"}})
		case "nil-page":
			writeTestJSON(w, http.StatusOK, apiv1.InboxList{})
		case "over":
			writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{
				{Name: name, UID: "m-uid", Project: "p"},
				{Name: auditHexName('c', 'd'), UID: "m-uid-2", Project: "p"},
			}, View: apiv1.InboxViewThread})
		default:
			t.Fatal("mode")
		}
	}

	mode = "plain"
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
	if h.stderr.String() != "" || !strings.Contains(h.stdout.String(), "Kind: \"plain\"\n") || !strings.Contains(h.stdout.String(), "State: Unknown\n") || !strings.Contains(h.stdout.String(), "Sender: namespace=\"dev-agents\" name=\"child\"\n") || strings.Contains(h.stdout.String(), "Answered") || strings.Contains(h.stdout.String(), "waiting-sentinel") || strings.Contains(h.stdout.String(), "uid=") {
		t.Fatalf("plain\nstdout %s\nstderr %s", h.stdout.String(), h.stderr.String())
	}

	mode = "nil-thread"
	before := len(h.api.requests())
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
	if len(h.api.requests()) != before+1 {
		t.Fatal("nil thread fetched again")
	}
	if !strings.Contains(h.stdout.String(), "Kind: \"question\"\n") || !strings.Contains(h.stdout.String(), "State: Unknown\n") || !strings.Contains(h.stdout.String(), "Sender: name=\"only-name\"\n") {
		t.Fatalf("nil thread\n%s", h.stdout.String())
	}

	mode = "old"
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
	if !strings.Contains(h.stderr.String(), "no thread view") || !strings.Contains(h.stdout.String(), "Kind: Unknown\n") || strings.Contains(h.stderr.String(), "PRIVATE_BODY") {
		t.Fatalf("old server stdout %q stderr %q", h.stdout.String(), h.stderr.String())
	}

	mode = "fallback"
	before = len(h.api.requests())
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
	got := h.api.requests()[before:]
	if len(got) != 2 || got[0].method != http.MethodGet || got[1].method != http.MethodGet || len(got[0].query[apiv1.InboxViewQuery]) != 1 || got[0].query[apiv1.InboxViewQuery][0] != apiv1.InboxViewThread || len(got[1].query[apiv1.InboxViewQuery]) != 0 {
		t.Fatalf("fallback %+v", got)
	}
	if !strings.Contains(h.stderr.String(), "no thread view") || strings.Contains(h.stderr.String(), "PRIVATE_BODY") || !strings.Contains(h.stdout.String(), "Messages: 1\n") {
		t.Fatalf("fallback output %q %q", h.stdout.String(), h.stderr.String())
	}

	mode = "sent-off"
	before = len(h.api.requests())
	h.mustRun(ExitFailed, "inbox", "list", "--project", "p", "--sent", "-o", "audit")
	if len(h.api.requests()) != before+1 || h.stdout.Len() != 0 || !strings.Contains(h.stderr.String(), "does not list sent messages") || strings.Contains(h.stderr.String(), "PRIVATE_BODY") {
		t.Fatalf("sent refusal stdout %q stderr %q", h.stdout.String(), h.stderr.String())
	}

	mode = "nil-page"
	h.mustRun(ExitFailed, "inbox", "list", "--project", "p", "-o", "audit")
	if h.stdout.Len() != 0 {
		t.Fatal("nil item list was printed as an empty audit")
	}
	mode = "over"
	h.mustRun(ExitFailed, "inbox", "list", "--project", "p", "--limit", "1", "-o", "audit")
	if h.stdout.Len() != 0 || !strings.Contains(h.stderr.String(), "more than the requested limit") {
		t.Fatalf("oversize %q %q", h.stdout.String(), h.stderr.String())
	}
}

func TestInboxAuditRejectsBadCorrelationAndEscapesMetadata(t *testing.T) {
	h := newHarness(t)
	qName := auditHexName('a', 'b')
	rName := auditHexName('c', 'd')
	other := auditHexName('e', 'f')
	hostile := "lead\nMessage: \"injected\"\t\r\x1b[2J\u2028Waiting on: injected"
	cursor := "page-2\nMessages: 0\x1b[2J"
	var mode string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || r.URL.Path != apiv1.InboxPath {
			t.Errorf("request %s %s body %d", r.Method, r.URL.Path, len(body))
		}
		var item apiv1.InboxMetadata
		switch mode {
		case "name":
			item = auditQuestion(qName, "q-uid", apiv1.InboxStateAnswered, nil, nil)
			item.Thread.Question.Name = other
		case "uid":
			item = auditQuestion(qName, "q-uid", apiv1.InboxStateAnswered, nil, nil)
			item.Thread.Question.UID = "other-uid"
		case "spaced":
			item = auditQuestion(qName, "q-uid", "Unanswered ", nil, nil)
		case "reply-ok":
			item = apiv1.InboxMetadata{
				Name: rName, UID: "r-uid", Project: "p", Kind: apiv1.InboxKindReply,
				InReplyTo: &apiv1.InboxMessageRef{Name: qName, UID: "q-uid"},
				Sender:    testAuditOwner("dev-agents", "lead", "lead-uid"),
				Recipient: testAuditOwner("dev-agents", "child", "child-uid"),
				Thread: &apiv1.InboxThread{
					State: apiv1.InboxStateAnswered, Question: apiv1.InboxMessageRef{Name: qName},
					Reply: &apiv1.InboxMessageRef{Name: rName}, StaleAfterSeconds: 1800, ObservedAt: auditObserved(),
				},
			}
		case "reply-mismatch":
			item = apiv1.InboxMetadata{
				Name: rName, UID: "r-uid", Project: "p", Kind: apiv1.InboxKindReply,
				InReplyTo: &apiv1.InboxMessageRef{Name: qName, UID: "q-uid"},
				Thread: &apiv1.InboxThread{
					State: apiv1.InboxStateAcknowledged, Question: apiv1.InboxMessageRef{Name: qName, UID: "other-uid"},
					Reply: &apiv1.InboxMessageRef{Name: qName, UID: "r-uid"}, StaleAfterSeconds: 1800, ObservedAt: auditObserved(),
				},
			}
		case "escape":
			item = auditQuestion(qName, "q-uid", apiv1.InboxStateUnanswered, nil, &apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: hostile, UID: "lead-uid"})
			item.Thread.WaitingOn = &apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: hostile, UID: "lead-uid"}
			writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{item}, Continue: cursor, View: apiv1.InboxViewThread})
			return
		default:
			t.Fatal(mode)
		}
		writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{item}, View: apiv1.InboxViewThread})
	}

	run := func(next string) string {
		t.Helper()
		mode = next
		before := len(h.api.requests())
		h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
		if len(h.api.requests()) != before+1 || h.api.requests()[before].method != http.MethodGet {
			t.Fatalf("%s requests", next)
		}
		return h.stdout.String()
	}

	out := run("name")
	if strings.Contains(out, "Answered") || strings.Contains(out, other) || !strings.Contains(out, "State: Unknown\n") || !strings.Contains(out, "Question: Unknown\n") {
		t.Fatalf("name mismatch\n%s", out)
	}
	out = run("uid")
	if strings.Contains(out, "other-uid") || strings.Contains(out, "Answered") || strings.Count(out, "q-uid") != 1 {
		t.Fatalf("uid mismatch\n%s", out)
	}
	out = run("spaced")
	if strings.Contains(out, "Unanswered") {
		t.Fatalf("unlisted state echoed\n%s", out)
	}
	out = run("reply-ok")
	if !strings.Contains(out, "Kind: \"reply\"\n") || !strings.Contains(out, "State: \"Answered\"\n") || !strings.Contains(out, "Question: name="+strconv.Quote(qName)+"\n") || !strings.Contains(out, "Reply: name="+strconv.Quote(rName)+"\n") || strings.Contains(out, "q-uid") {
		t.Fatalf("reply correlation\n%s", out)
	}
	out = run("reply-mismatch")
	if strings.Contains(out, "Acknowledged") || strings.Contains(out, "other-uid") || strings.Contains(out, qName) || !strings.Contains(out, "State: Unknown\n") {
		t.Fatalf("reply mismatch\n%s", out)
	}
	out = run("escape")
	for _, line := range strings.Split(out, "\n") {
		for _, r := range line {
			if r < 0x20 || r == 0x7f || r == '\u2028' {
				t.Fatalf("raw control %q", line)
			}
		}
	}
	messages := 0
	var continueValue string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Message: ") {
			messages++
		}
		if strings.HasPrefix(line, "Continue: ") {
			continueValue = strings.TrimPrefix(line, "Continue: ")
		}
	}
	if messages != 1 || strings.Contains(out, "\nMessages: 0\n") {
		t.Fatalf("message rows %d\n%s", messages, out)
	}
	gotCursor, err := strconv.Unquote(continueValue)
	if err != nil || gotCursor != cursor {
		t.Fatalf("continue %q err %v", continueValue, err)
	}
	if !strings.Contains(out, "State: \"Unanswered\"\n") || !strings.Contains(out, strconv.Quote(hostile)) {
		t.Fatalf("escaped row\n%s", out)
	}
}

func TestInboxAuditPaginationIsOneGet(t *testing.T) {
	h := newHarness(t)
	name := auditHexName('a', 'b')
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || len(body) != 0 {
			t.Errorf("method %s body %d", r.Method, len(body))
		}
		q := r.URL.Query()
		if q.Get("continue") != "page-1" || q.Get("limit") != "3" || q.Get(apiv1.InboxViewQuery) != apiv1.InboxViewThread || q.Get(apiv1.InboxBoxQuery) != "" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		item := apiv1.InboxMetadata{Name: name, UID: "m-uid", Project: "p", Kind: apiv1.InboxKindPlain}
		writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{item}, Continue: "page-2", View: apiv1.InboxViewThread})
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "--limit", "3", "--continue", "page-1", "-o", "audit")
	if len(h.api.requests()) != 1 {
		t.Fatalf("automatic pagination or POST: %+v", h.api.requests())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "Continue: \"page-2\"\nMessages: 1\n") || !strings.Contains(out, "Kind: \"plain\"\n") {
		t.Fatalf("page\n%s", out)
	}
}

func TestInboxAuditJSONUnchanged(t *testing.T) {
	h := newHarness(t)
	item := auditQuestion(auditHexName('a', 'b'), "q-uid", apiv1.InboxStateStale, nil, &apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "lead", UID: "lead-uid"})
	list := apiv1.InboxList{Items: []apiv1.InboxMetadata{item}, Continue: "cursor-1", View: apiv1.InboxViewThread}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		writeTestJSON(w, http.StatusOK, list)
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p")
	first := h.stdout.String()
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "json")
	if h.stdout.String() != first {
		t.Fatalf("default and -o json diverged\n%s\n%s", first, h.stdout.String())
	}
	if strings.Contains(first, auditFooterText) || strings.Contains(first, "Messages:") {
		t.Fatal("json list gained audit text")
	}
	var got apiv1.InboxList
	if err := json.Unmarshal([]byte(first), &got); err != nil {
		t.Fatal(err)
	}
	if got.Continue != "cursor-1" || got.View != apiv1.InboxViewThread || len(got.Items) != 1 || got.Items[0].Name != item.Name || got.Items[0].UID != item.UID || got.Items[0].Kind != apiv1.InboxKindQuestion || got.Items[0].Thread == nil || got.Items[0].Thread.State != apiv1.InboxStateStale || got.Items[0].Thread.StaleAfterSeconds != 1800 || !got.Items[0].Thread.ObservedAt.Equal(item.Thread.ObservedAt) || got.Items[0].Key != "key-secret" {
		t.Fatalf("json %+v", got)
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
	if strings.Contains(h.stdout.String(), "key-secret") || !strings.Contains(h.stdout.String(), "State: \"Stale\"\n") {
		t.Fatalf("audit after json\n%s", h.stdout.String())
	}
}

func TestInboxAuditRejectsNonListBeforeConnect(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Errorf("connected %s %s", r.Method, r.URL.Path)
	}
	name := auditHexName('a', 'b')
	h.env.Stdin = strings.NewReader("hello")
	for _, args := range [][]string{
		{"inbox", "send", "--project", "p", "--recipient-namespace", "dev-agents", "--recipient-name", "recipient", "--recipient-uid", "uid", "--key", "k", "-o", "audit"},
		{"inbox", "ask", "--project", "p", "--key", "k", "-o", "audit"},
		{"inbox", "reply", name, "--project", "p", "--key", "k", "-o", "audit"},
		{"inbox", "read", name, "--project", "p", "-o", "audit"},
		{"inbox", "ack", name, "--project", "p", "-o", "audit"},
	} {
		h.mustRun(ExitUsage, args...)
		if !strings.Contains(h.stderr.String(), "-o audit") {
			t.Fatalf("%q stderr %q", args, h.stderr.String())
		}
	}
	h.mustRun(ExitUsage, "inbox", "list", "-o", "audit")
	if !strings.Contains(h.stderr.String(), "--project") || len(h.api.requests()) != 0 {
		t.Fatalf("list without a project connected or hid the usage error: %q requests %d", h.stderr.String(), len(h.api.requests()))
	}
}

func TestInboxAuditEmptyPage(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		writeTestJSON(w, http.StatusOK, apiv1.InboxList{Items: []apiv1.InboxMetadata{}, View: apiv1.InboxViewThread})
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "-o", "audit")
	want := "Messages: 0\n\n" + auditFooterText + "\n"
	if h.stdout.String() != want || h.stderr.String() != "" {
		t.Fatalf("empty page stdout %q stderr %q", h.stdout.String(), h.stderr.String())
	}
	if len(h.api.requests()) != 1 {
		t.Fatal("empty page paginated")
	}
}
