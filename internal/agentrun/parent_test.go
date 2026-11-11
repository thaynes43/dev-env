package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func parentTestMetadata() apiv1.InboxMetadata {
	return apiv1.InboxMetadata{
		Name: "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32), UID: "note-uid",
		Project: "dev-env", Key: "stable-key", Kind: "question",
		Sender:    apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "child", UID: "child-uid"},
		Recipient: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "parent", UID: "parent-uid"},
	}
}

func TestParentMetadataAcceptsOnePartyForm(t *testing.T) {
	session := parentTestMetadata()
	if !validParentNoteMetadata(session) {
		t.Fatal("session parties refused")
	}
	toCaller := session
	toCaller.Recipient = apiv1.TaskClaimOwner{}
	toCaller.CallerRecipient = &apiv1.InboxCaller{ServiceAccount: parentCallerAccount, PolicyUID: "policy-uid"}
	if !validParentNoteMetadata(toCaller) {
		t.Fatal("a note to the Meatball account was refused")
	}
	fromCaller := session
	fromCaller.Kind = ""
	fromCaller.Sender = apiv1.TaskClaimOwner{}
	fromCaller.CallerSender = &apiv1.InboxCaller{ServiceAccount: parentCallerAccount, PolicyUID: "policy-uid"}
	if !validParentPlainMetadata(fromCaller) {
		t.Fatal("a plain message from the Meatball account was refused")
	}
	reply := fromCaller
	reply.Kind = apiv1.InboxKindReply
	reply.InReplyTo = &apiv1.InboxMessageRef{Name: session.Name, UID: session.UID}
	if !validParentReplyMetadata(reply, session.Name) {
		t.Fatal("a reply from the Meatball account was refused")
	}
	both := session
	both.CallerSender = &apiv1.InboxCaller{ServiceAccount: parentCallerAccount, PolicyUID: "policy-uid"}
	if validParentMetadata(both) {
		t.Fatal("both sender forms accepted")
	}
	wrong := toCaller
	wrong.CallerRecipient = &apiv1.InboxCaller{ServiceAccount: "other/account", PolicyUID: "policy-uid"}
	if validParentMetadata(wrong) {
		t.Fatal("another ServiceAccount accepted")
	}
	h := newHarness(t)
	plain := fromCaller
	plain.Kind = apiv1.InboxKindPlain
	listedReply := reply
	listedReply.Name = "message." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
	listedReply.UID = "reply-uid"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || r.URL.Path != apiv1.ParentNotesPath || len(body) != 0 {
			t.Error("parent list sent extra intent")
		}
		writeTestJSON(w, 200, apiv1.InboxList{Items: []apiv1.InboxMetadata{plain, listedReply}, Box: "inbox"})
	}
	h.mustRun(ExitOK, "parent", "list")
	if !strings.Contains(h.stdout.String(), plain.Name) || !strings.Contains(h.stdout.String(), listedReply.Name) {
		t.Fatal("list hid a caller message")
	}
}

func TestParentTellIntentAndUncertainty(t *testing.T) {
	h := newHarness(t)
	var intents []apiv1.TellParentRequest
	status := http.StatusCreated
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.ParentNotesPath || r.URL.RawQuery != "" {
			t.Error("wrong tell route")
		}
		var fields map[string]json.RawMessage
		var req apiv1.TellParentRequest
		if json.Unmarshal(body, &fields) != nil || len(fields) != 3 || fields["key"] == nil || fields["kind"] == nil || fields["text"] == nil || json.Unmarshal(body, &req) != nil {
			t.Fatal("tell sent fields beyond key, kind and text")
		}
		intents = append(intents, req)
		if status == http.StatusServiceUnavailable {
			writeTestJSON(w, status, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeUnavailable, Message: "PRIVATE_NOTE_TEXT"}})
			return
		}
		m := parentTestMetadata()
		m.Key, m.Kind = req.Key, req.Kind
		outcome := "Created"
		if status == 200 {
			outcome = "Confirmed"
		}
		if status == 409 {
			outcome = "Conflict"
		}
		writeTestJSON(w, status, apiv1.InboxResult{Outcome: outcome, Message: m})
	}
	args := []string{"parent", "tell", "--key", "stable-key"}
	text := "  PRIVATE_NOTE_TEXT\né\x00"
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitOK, args...)
	if len(intents) != 1 || intents[0] != (apiv1.TellParentRequest{Key: "stable-key", Kind: "question", Text: text}) || strings.Contains(h.stdout.String(), "PRIVATE_NOTE_TEXT") {
		t.Fatal("tell did not use default kind or exact stdin; receipt exposed text")
	}
	status = http.StatusServiceUnavailable
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitUnconfirmed, args...)
	if len(intents) != 2 || intents[1] != intents[0] || len(h.sleeps) != 0 || strings.Contains(h.stderr.String(), "PRIVATE_NOTE_TEXT") {
		t.Fatal("uncertain tell replayed, changed intent or exposed text")
	}
	status = http.StatusOK
	h.env.Stdin = strings.NewReader(text)
	h.mustRun(ExitOK, args...)
	if len(intents) != 3 || intents[2] != intents[0] {
		t.Fatal("explicit retry changed intent")
	}
	status = http.StatusConflict
	h.env.Stdin = strings.NewReader("changed")
	h.mustRun(ExitFailed, args...)
	if len(intents) != 4 || intents[3].Key != "stable-key" || len(h.sleeps) != 0 {
		t.Fatal("conflict replayed or generated a new key")
	}
	status = http.StatusCreated
	for _, kind := range []string{"result", "blocker"} {
		h.env.Stdin = strings.NewReader(text)
		h.mustRun(ExitOK, "parent", "tell", "--key", "stable-key", "--kind", kind)
		if intents[len(intents)-1].Kind != kind {
			t.Fatal("kind changed")
		}
	}
}

func TestParentCommandsRejectInvalidArgumentsBeforeRequest(t *testing.T) {
	for _, args := range [][]string{
		{"tell"}, {"tell", "--key", "bad/key"}, {"tell", "--key", "k", "--kind", "reply"},
		{"tell", "--key", "k", "--kind", "approval"}, {"tell", "--key", "k", "--to", "some-parent"},
		{"tell", "--key", "k", "--recipient-name", "some-parent"}, {"tell", "--key", "k", "--project", "dev-env"},
		{"tell", "--key", "k", "some-parent"}, {"list", "--limit", "0"}, {"list", "--limit", "101"},
		{"list", "--continue", "bad\nvalue"}, {"list", "--project", "dev-env"}, {"read"}, {"read", "bad-name"},
		{"answer"}, {"wait"}, {"send"},
	} {
		h := newHarness(t)
		h.env.Stdin = strings.NewReader("x")
		h.mustRun(ExitUsage, append([]string{"parent"}, args...)...)
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid arguments reached API")
		}
	}
	for _, text := range []string{"", strings.Repeat("é", 8193), string([]byte{255})} {
		h := newHarness(t)
		h.env.Stdin = strings.NewReader(text)
		h.mustRun(ExitUsage, "parent", "tell", "--key", "k")
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid text reached API")
		}
	}
}

func TestParentListAndRead(t *testing.T) {
	h := newHarness(t)
	m := parentTestMetadata()
	text := "  exact parent text\né\x00"
	queries := []string{}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || len(body) != 0 || r.URL.Query().Has("project") {
			t.Error("parent read sent extra intent")
		}
		if r.URL.Path == apiv1.ParentNotesPath {
			queries = append(queries, r.URL.RawQuery)
			if r.URL.Query().Get("limit") != "2" {
				t.Error("limit not sent")
			}
			writeTestJSON(w, 200, apiv1.InboxList{Items: []apiv1.InboxMetadata{m}, Box: r.URL.Query().Get("box"), Continue: "1:"})
			return
		}
		if r.URL.Path != apiv1.ParentNotesPath+"/"+m.Name || r.URL.RawQuery != "" {
			t.Error("wrong read path")
		}
		writeTestJSON(w, 200, apiv1.InboxMessage{InboxMetadata: m, Text: text})
	}
	h.mustRun(ExitOK, "parent", "list", "--limit", "2")
	if strings.Contains(h.stdout.String(), "exact parent text") || !strings.Contains(h.stdout.String(), `"kind": "question"`) {
		t.Fatal("list text or kind")
	}
	h.mustRun(ExitOK, "parent", "list", "--sent", "--limit", "2", "--continue", "1:")
	if !reflect.DeepEqual(queries, []string{"box=inbox&limit=2", "box=sent&continue=1%3A&limit=2"}) {
		t.Fatal("wrong list queries", queries)
	}
	h.mustRun(ExitOK, "parent", "read", m.Name)
	if h.stdout.String() != text || h.stderr.Len() != 0 || len(h.api.requests()) != 3 {
		t.Fatal("read changed text or made extra requests")
	}
	h.mustRun(ExitOK, "parent", "read", m.Name, "-o", "json")
	var got apiv1.InboxMessage
	if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Text != text || got.Name != m.Name {
		t.Fatal("JSON read lost text or metadata")
	}
	if len(h.api.requests()) != 4 || len(h.sleeps) != 0 {
		t.Fatal("read/list repeated a request")
	}
}

func TestParentCommandsRejectUnconfirmedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		body any
		exit int
	}{
		{"receipt", []string{"tell", "--key", "k"}, apiv1.InboxResult{Outcome: "Confirmed", Message: parentTestMetadata()}, ExitUnconfirmed},
		{"wrong-box", []string{"list", "--sent"}, apiv1.InboxList{Items: []apiv1.InboxMetadata{}, Box: "inbox"}, ExitFailed},
		{"nil-page", []string{"list"}, apiv1.InboxList{Box: "inbox"}, ExitFailed},
		{"invalid-note", []string{"list"}, apiv1.InboxList{Items: []apiv1.InboxMetadata{{Name: "bad"}}, Box: "inbox"}, ExitFailed},
		{"oversized-page", []string{"list", "--limit", "1"}, apiv1.InboxList{Items: []apiv1.InboxMetadata{parentTestMetadata(), parentTestMetadata()}, Box: "inbox"}, ExitFailed},
		{"invalid-read", []string{"read", parentTestMetadata().Name}, apiv1.InboxMessage{InboxMetadata: parentTestMetadata()}, ExitFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.env.Stdin = strings.NewReader("x")
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, 200, tc.body) }
			h.mustRun(tc.exit, append([]string{"parent"}, tc.args...)...)
			if h.stdout.Len() != 0 || len(h.api.requests()) != 1 {
				t.Fatal("unconfirmed response was printed or retried")
			}
		})
	}
}

func TestParentCommandsRefusalsAndRedirectsStayPrivate(t *testing.T) {
	for _, tc := range []struct {
		status, exit int
		want         string
	}{
		{400, ExitFailed, "key, kind, text"}, {404, ExitNotFound, "not found"},
		{422, ExitFailed, "snapshot"}, {429, ExitFailed, "limits"},
		{403, ExitAuth, "403"}, {307, ExitFailed, "307"},
		// A 503 on the one write is unconfirmed, as for the inbox: the same
		// status also means the store's outcome is unknown.
		{503, ExitUnconfirmed, "outcome Unknown"},
	} {
		h := newHarness(t)
		h.env.Stdin = strings.NewReader("x")
		h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Location", "/redirected")
			http.Error(w, "PRIVATE_NOTE_TEXT "+testToken, tc.status)
		}
		h.mustRun(tc.exit, "parent", "tell", "--key", "k")
		if !strings.Contains(h.stderr.String(), tc.want) || strings.Contains(h.stderr.String(), "PRIVATE_NOTE_TEXT") || strings.Contains(h.stderr.String(), testToken) {
			t.Fatal("refusal lost its reason or exposed API body", tc.status)
		}
		if len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
			t.Fatal("refusal or redirect repeated request")
		}
	}
	// A read that meets 503 says the feature may be off (DESIGN-062 7.3).
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		http.Error(w, "PRIVATE_NOTE_TEXT "+testToken, http.StatusServiceUnavailable)
	}
	if code := h.run("parent", "list"); code == ExitOK {
		t.Fatal("parent list accepted a 503")
	}
	if !strings.Contains(h.stderr.String(), "not enabled") || strings.Contains(h.stderr.String(), "PRIVATE_NOTE_TEXT") || strings.Contains(h.stderr.String(), testToken) {
		t.Fatal("a 503 read lost its reason or exposed the API body: " + h.stderr.String())
	}
}
