package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestInboxSendIntentAndUncertainty(t *testing.T) {
	h := newHarness(t)
	args := []string{"inbox", "send", "--project", "sample", "--recipient-namespace", "dev-agents", "--recipient-name", "recipient", "--recipient-uid", "uid-1", "--key", "logical-1"}
	var intents []apiv1.PublishInboxRequest
	status := http.StatusCreated
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		var intent apiv1.PublishInboxRequest
		if json.Unmarshal(body, &intent) != nil {
			t.Fatal("bad request")
		}
		intents = append(intents, intent)
		if status == http.StatusServiceUnavailable {
			writeTestJSON(w, status, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeUnavailable, Message: "outcome Unknown; retry same intent and key"}})
			return
		}
		if status == http.StatusConflict {
			writeTestJSON(w, status, apiv1.InboxResult{Outcome: "Conflict"})
			return
		}
		outcome := "Created"
		if status == http.StatusOK {
			outcome = "Confirmed"
		}
		writeTestJSON(w, status, apiv1.InboxResult{Outcome: outcome, Message: apiv1.InboxMetadata{Name: "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32), UID: "m-uid", Project: intent.Project, Key: intent.Key, Recipient: intent.Recipient}})
	}
	h.env.Stdin = strings.NewReader("hello")
	h.mustRun(ExitOK, args...)
	if len(intents) != 1 || intents[0].Project != "sample" || intents[0].Recipient != (apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "recipient", UID: "uid-1"}) || intents[0].Key != "logical-1" || intents[0].Text != "hello" {
		t.Fatalf("intent: %+v", intents)
	}
	status = http.StatusServiceUnavailable
	h.env.Stdin = strings.NewReader("hello")
	h.mustRun(ExitUnconfirmed, args...)
	if len(intents) != 2 || intents[0] != intents[1] || len(h.sleeps) != 0 {
		t.Fatal("uncertain intent replayed or changed")
	}
	status = http.StatusOK
	h.env.Stdin = strings.NewReader("hello")
	h.mustRun(ExitOK, args...)
	if len(intents) != 3 || intents[2] != intents[0] {
		t.Fatal("explicit retry changed intent")
	}
	status = http.StatusConflict
	h.env.Stdin = strings.NewReader("changed")
	h.mustRun(ExitFailed, args...)
	if len(intents) != 4 || intents[3].Key != "logical-1" {
		t.Fatal("conflict was replayed or key changed")
	}
}

func TestInboxInvalidBeforeRequest(t *testing.T) {
	for _, tc := range []struct{ project, ns, name, uid, key, text string }{
		{"", "dev-agents", "recipient", "uid", "k", "x"},
		{"p", "", "recipient", "uid", "k", "x"},
		{"p", "dev-agents", "bad/name", "uid", "k", "x"},
		{"p", "dev-agents", "recipient", "", "k", "x"},
		{"p", "dev-agents", "recipient", "uid", "", "x"},
		{"p", "dev-agents", "recipient", "uid", "k", string([]byte{255})},
		{"p", "dev-agents", "recipient", "uid", "k", strings.Repeat("é", 8193)},
	} {
		h := newHarness(t)
		h.env.Stdin = strings.NewReader(tc.text)
		h.mustRun(ExitUsage, "inbox", "send", "--project", tc.project, "--recipient-namespace", tc.ns, "--recipient-name", tc.name, "--recipient-uid", tc.uid, "--key", tc.key)
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid intent reached API")
		}
	}
}

func TestInboxListReadAck(t *testing.T) {
	h := newHarness(t)
	message := "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Query().Get("project") != "p" {
			t.Error("missing project")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == apiv1.InboxPath:
			if r.URL.Query().Get("limit") != "2" {
				t.Error("wrong limit")
			}
			writeTestJSON(w, 200, apiv1.InboxList{Items: []apiv1.InboxMetadata{{Name: message, UID: "m-uid", Project: "p"}}, Continue: "next"})
		case r.Method == "GET" && r.URL.Path == apiv1.InboxPath+"/"+message:
			writeTestJSON(w, 200, apiv1.InboxMessage{InboxMetadata: apiv1.InboxMetadata{Name: message, UID: "m-uid", Project: "p"}, Text: "private text"})
		case r.Method == "POST" && r.URL.Path == apiv1.InboxPath+"/"+message+"/ack":
			if string(body) != "{}" {
				t.Error("ack body")
			}
			writeTestJSON(w, 201, apiv1.InboxAckResult{Outcome: "Created", Ack: apiv1.InboxReadAck{UID: "ack-uid", Project: "p", MessageName: message, MessageUID: "m-uid"}})
		default:
			t.Error("unexpected request")
		}
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p", "--limit", "2")
	if strings.Contains(h.stdout.String(), "private text") || !strings.Contains(h.stdout.String(), "next") {
		t.Fatal("bad list")
	}
	h.mustRun(ExitOK, "inbox", "read", message, "--project", "p")
	if h.stdout.String() != "private text" || len(h.api.requests()) != 2 {
		t.Fatal("read did not return text or made an extra request")
	}
	h.mustRun(ExitOK, "inbox", "ack", message, "--project", "p")
	if len(h.api.requests()) != 3 {
		t.Fatal("ack made extra request")
	}
}

func TestInboxRejectsRedirect(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}
	h.mustRun(ExitFailed, "inbox", "list", "--project", "p")
	if len(h.api.requests()) != 1 || strings.Contains(h.stderr.String(), testToken) {
		t.Fatal("redirect followed or token exposed")
	}
}

func TestInboxTLSAndTokenRotation(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
		writeTestJSON(w, 200, apiv1.InboxList{Items: []apiv1.InboxMetadata{}})
	}
	h.mustRun(ExitOK, "inbox", "list", "--project", "p")
	h.write("api-token", "rotated-token\n")
	h.mustRun(ExitOK, "inbox", "list", "--project", "p")
	requests := h.api.requests()
	if len(requests) != 2 || requests[0].auth != "Bearer "+testToken || requests[1].auth != "Bearer rotated-token" {
		t.Fatal("token rotation not observed")
	}
	h.vars[envCAFile] = h.write("wrong-ca", "not a certificate")
	if code := h.run("inbox", "list", "--project", "p"); code == ExitOK {
		t.Fatal("untrusted TLS accepted")
	}
	if len(h.api.requests()) != 2 || strings.Contains(h.stderr.String(), "rotated-token") {
		t.Fatal("TLS failure sent request or exposed token")
	}
}

func TestInboxDoesNotEchoAPIErrorBody(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) { http.Error(w, testToken, http.StatusForbidden) }
	h.mustRun(ExitAuth, "inbox", "list", "--project", "p")
	if strings.Contains(h.stderr.String(), testToken) {
		t.Fatal("API body exposed")
	}
}
