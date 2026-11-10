package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestParentWaitWaiting(t *testing.T) {
	h := newHarness(t)
	m := parentTestMetadata()
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodGet || r.URL.Path != apiv1.ParentNotesPath+"/"+m.Name+"/wait" || r.URL.Query().Get("seconds") != "900" {
			t.Error("wrong wait request")
		}
		writeTestJSON(w, 200, map[string]string{"state": "waiting"})
	}
	h.mustRun(ExitOK, "parent", "wait", m.Name, "--for", "15m")
	if h.stdout.String() != "waiting\n" || h.stderr.Len() != 0 || len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
		t.Fatal("wait changed output or repeated the held read")
	}
}

func parentAnswerMetadata() apiv1.InboxMetadata {
	note := parentTestMetadata()
	reply := note
	reply.Name = "message." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
	reply.UID, reply.Kind = "answer-uid", "reply"
	reply.Sender, reply.Recipient = note.Recipient, note.Sender
	reply.InReplyTo = &apiv1.InboxMessageRef{Name: note.Name, UID: note.UID}
	return reply
}

func TestParentAnswerIntentRetryAndWaitText(t *testing.T) {
	h := newHarness(t)
	note, reply := parentTestMetadata(), parentAnswerMetadata()
	text := "  private answer\né\x00"
	status := http.StatusCreated
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.URL.Path {
		case apiv1.ParentNotesPath + "/" + note.Name + "/answer":
			var req apiv1.AnswerParentRequest
			var fields map[string]json.RawMessage
			if r.Method != "POST" || r.URL.RawQuery != "" || json.Unmarshal(body, &req) != nil || json.Unmarshal(body, &fields) != nil || len(fields) != 2 || req.Key != reply.Key || req.Text != text {
				t.Error("answer did not send only key and exact stdin")
			}
			outcome := "Created"
			if status == 200 {
				outcome = "Confirmed"
			}
			writeTestJSON(w, status, apiv1.InboxResult{Outcome: outcome, Message: reply})
		case apiv1.ParentNotesPath + "/" + note.Name + "/wait":
			if r.Method != "GET" || len(body) != 0 || r.URL.Query().Get("seconds") != "61" {
				t.Error("wrong wait intent")
			}
			writeTestJSON(w, 200, apiv1.ParentNoteWaitResult{State: "answered", Reply: &apiv1.InboxMessage{InboxMetadata: reply, Text: text}})
		default:
			t.Error("unexpected parent path")
		}
	}
	args := []string{"parent", "answer", note.Name, "--key", reply.Key}
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitOK, args...)
	if strings.Contains(h.stdout.String(), "private answer") {
		t.Fatal("answer receipt included text")
	}
	status = 200
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitOK, args...)
	h.mustRun(ExitOK, "parent", "wait", note.Name, "--for", "61s")
	if h.stdout.String() != text || h.stderr.Len() != 0 {
		t.Fatal("wait changed the exact answer")
	}
	h.mustRun(ExitOK, "parent", "wait", note.Name, "--for", "61s", "-o", "json")
	var got apiv1.ParentNoteWaitResult
	if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Reply == nil || got.Reply.Text != text || len(h.api.requests()) != 4 || len(h.sleeps) != 0 {
		t.Fatal("JSON wait lost the answer or the commands retried")
	}
}

func TestParentWaitClientTimeoutAndOtherCommands(t *testing.T) {
	h := newHarness(t)
	a := &app{env: h.env}
	defer a.close()
	c, err := a.connect(context.Background(), connOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, wait := range []time.Duration{time.Second, 61 * time.Second, 15 * time.Minute} {
		long := parentNoteClient(c, wait)
		if long == c || long.http == c.http || long.http.Timeout != wait+2*time.Minute || long.http.Transport != c.http.Transport || c.http.Timeout != 30*time.Second || parentNoteClient(c, 0).http.Timeout != 30*time.Second {
			t.Fatal("wait did not copy the shared client with only its own longer timeout")
		}
	}
	// Observe the actual request's deadline at the transport as well, so a
	// helper which is correct but unused cannot make this test pass.
	old := http.DefaultTransport
	tr := old.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	defer func() { http.DefaultTransport = old }()
	var deadline time.Duration
	tr.Proxy = func(r *http.Request) (*url.URL, error) {
		end, ok := r.Context().Deadline()
		if !ok {
			t.Error("parent request has no client deadline")
		}
		deadline = time.Until(end)
		return nil, nil
	}
	http.DefaultTransport = tr
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, "refused", http.StatusServiceUnavailable)
	}
	note := parentTestMetadata().Name
	for _, tc := range []struct {
		args    []string
		timeout time.Duration
	}{
		{[]string{"wait", note, "--for", "1s"}, 121 * time.Second},
		{[]string{"wait", note, "--for", "61s"}, 181 * time.Second},
		{[]string{"wait", note, "--for", "15m"}, 17 * time.Minute},
		{[]string{"tell", "--key", "k"}, 30 * time.Second},
		{[]string{"list"}, 30 * time.Second}, {[]string{"read", note}, 30 * time.Second},
		{[]string{"answer", note, "--key", "k"}, 30 * time.Second},
	} {
		h.env.Stdin = strings.NewReader("x")
		deadline = 0
		if h.run(append([]string{"parent"}, tc.args...)...) == ExitOK {
			t.Fatal("fixture refusal was accepted")
		}
		if deadline > tc.timeout || deadline < tc.timeout-time.Second {
			t.Fatalf("%s request deadline %v, want %v", tc.args[0], deadline, tc.timeout)
		}
	}
}

func TestParentAnswerWaitInvalidArguments(t *testing.T) {
	note := parentTestMetadata().Name
	for _, args := range [][]string{
		{"wait", note, "--for", "0s"}, {"wait", note, "--for", "901s"}, {"wait", note, "--for", "1500ms"},
		{"wait", note, "--for", "later"}, {"wait", note, "--for", "-1s"}, {"wait", note, "--project", "dev-env"},
		{"answer", note}, {"answer", note, "--key", "bad/key"}, {"answer", note, "--key", "k", "--in-reply-to", note},
		{"answer", note, "--key", "k", "--kind", "reply"}, {"answer", note, "--key", "k", "--to", "child"},
		{"answer", note, "--key", "k", "--project", "dev-env"},
	} {
		h := newHarness(t)
		h.env.Stdin = strings.NewReader("x")
		h.mustRun(ExitUsage, append([]string{"parent"}, args...)...)
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid arguments reached the API")
		}
	}
	for _, text := range []string{"", strings.Repeat("é", 8193), string([]byte{255})} {
		h := newHarness(t)
		h.env.Stdin = strings.NewReader(text)
		h.mustRun(ExitUsage, "parent", "answer", note, "--key", "k")
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid stdin reached the API")
		}
	}
}

func TestParentAnswerWaitRefusalsStayPrivate(t *testing.T) {
	for _, verb := range []string{"answer", "wait"} {
		for _, status := range []int{200, 307, 400, 401, 403, 404, 409, 422, 429, 500, 503} {
			h := newHarness(t)
			h.env.Stdin = strings.NewReader("x")
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.Header().Set("Location", "/redirected")
				http.Error(w, "PRIVATE_API_BODY "+testToken, status)
			}
			args := []string{"parent", verb, parentTestMetadata().Name}
			if verb == "answer" {
				args = append(args, "--key", "k")
			}
			if h.run(args...) == ExitOK || h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), "PRIVATE_API_BODY") || strings.Contains(h.stderr.String(), testToken) || len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
				t.Fatalf("%s status %d exposed or retried the API body", verb, status)
			}
		}
	}
}

func TestParentWaitRejectsUnboundAnswers(t *testing.T) {
	note, reply := parentTestMetadata(), parentAnswerMetadata()
	wrong := reply
	wrong.InReplyTo = &apiv1.InboxMessageRef{Name: reply.Name, UID: "wrong-note"}
	for _, result := range []apiv1.ParentNoteWaitResult{
		{State: "unknown"}, {State: "answered"},
		{State: "waiting", Reply: &apiv1.InboxMessage{InboxMetadata: reply, Text: "PRIVATE_ANSWER"}},
		{State: "answered", Reply: &apiv1.InboxMessage{InboxMetadata: wrong, Text: "PRIVATE_ANSWER"}},
		{State: "answered", Reply: &apiv1.InboxMessage{InboxMetadata: note, Text: "PRIVATE_QUESTION"}},
		{State: "answered", Reply: &apiv1.InboxMessage{InboxMetadata: reply}},
		{State: "answered", Reply: &apiv1.InboxMessage{InboxMetadata: reply, Text: strings.Repeat("x", (16<<10)+1)}},
	} {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, 200, result) }
		h.mustRun(ExitFailed, "parent", "wait", note.Name)
		if h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), "PRIVATE_") {
			t.Fatal("unconfirmed wait response was printed")
		}
	}
}

func TestParentTellConflictSentence(t *testing.T) {
	h := newHarness(t)
	h.env.Stdin = strings.NewReader("private intent")
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, "PRIVATE_API_BODY", http.StatusConflict)
	}
	h.mustRun(ExitFailed, "parent", "tell", "--key", "conflict")
	want := "agent-run: parent tell conflicts with the existing key (409); nothing was replaced and no new key was generated\n"
	if h.stderr.String() != want || h.stdout.Len() != 0 {
		t.Fatal("409 did not print only the fixed conflict sentence")
	}
}
