package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func inboxAckStatusMessage() string {
	return "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
}

func inboxAckStatusReceipt(message string) apiv1.InboxReadAck {
	created := time.Date(2026, 11, 1, 7, 31, 0, 0, time.UTC)
	archived := created.Add(time.Minute)
	return apiv1.InboxReadAck{
		Name: "read-ack-name", UID: "ack-uid", Project: "p", MessageName: message, MessageUID: "message-uid",
		Recipient:  apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "recipient", UID: "recipient-uid"},
		Actor:      apiv1.TaskClaimActor{Kind: "Session", ServiceAccount: "dev-agents", PodName: "pod", PodUID: "pod-uid"},
		CreatedAt:  created,
		Archived:   true,
		ArchivedAt: &archived,
	}
}

func TestInboxAckStatusOneGetPrintsDirectReceipt(t *testing.T) {
	h := newHarness(t)
	message := inboxAckStatusMessage()
	receipt := inboxAckStatusReceipt(message)
	h.env.Stdin = strings.NewReader("do-not-send")
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || r.URL.Path != apiv1.InboxPath+"/"+message+"/ack" || len(body) != 0 || len(r.URL.Query()) != 1 || r.URL.Query().Get("project") != "p" || r.URL.Query().Get(apiv1.InboxViewQuery) != "" {
			t.Errorf("ack-status %s %s query %q body %q", r.Method, r.URL.Path, r.URL.RawQuery, body)
		}
		writeTestJSON(w, http.StatusOK, receipt)
	}
	h.mustRun(ExitOK, "inbox", "ack-status", message, "--project", "p")
	direct := h.stdout.String()
	if h.stderr.Len() != 0 || strings.Contains(direct, `"outcome"`) {
		t.Fatalf("default output was not the direct receipt\n%s\n%s", direct, h.stderr.String())
	}
	h.mustRun(ExitOK, "inbox", "ack-status", message, "--project", "p", "-o", "json")
	if h.stdout.String() != direct || h.stderr.Len() != 0 {
		t.Fatalf("default and -o json diverged or wrapped the receipt\n%s\n%s", direct, h.stderr.String())
	}
	var got apiv1.InboxReadAck
	if err := json.Unmarshal([]byte(direct), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != receipt.Name || got.UID != receipt.UID || got.Project != receipt.Project || got.MessageName != receipt.MessageName || got.MessageUID != receipt.MessageUID || got.Recipient != receipt.Recipient || got.Actor != receipt.Actor || !got.CreatedAt.Equal(receipt.CreatedAt) || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(*receipt.ArchivedAt) {
		t.Fatalf("receipt %+v", got)
	}
	reqs := h.api.requests()
	if len(reqs) != 2 || len(h.sleeps) != 0 {
		t.Fatalf("requests %+v sleeps %v", reqs, h.sleeps)
	}
	for _, req := range reqs {
		if req.method != http.MethodGet || req.path != apiv1.InboxPath+"/"+message+"/ack" || len(req.body) != 0 || len(req.query) != 1 || len(req.query["project"]) != 1 || req.query["project"][0] != "p" {
			t.Fatalf("request %+v", req)
		}
	}
}

func TestInboxAckStatusRejectsUnconfirmedReceipt(t *testing.T) {
	h := newHarness(t)
	message := inboxAckStatusMessage()
	other := "message." + strings.Repeat("c", 32) + "." + strings.Repeat("d", 32)
	base := inboxAckStatusReceipt(message)
	var respond func(http.ResponseWriter)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || len(body) != 0 || r.URL.Query().Get(apiv1.InboxViewQuery) != "" {
			t.Errorf("ack-status wrote %s query %q body %q", r.Method, r.URL.RawQuery, body)
		}
		respond(w)
	}
	for _, tc := range []struct {
		name string
		send func(http.ResponseWriter)
	}{
		{"wrong project", func(w http.ResponseWriter) {
			bad := base
			bad.Project = "other"
			writeTestJSON(w, http.StatusOK, bad)
		}},
		{"wrong message", func(w http.ResponseWriter) {
			bad := base
			bad.MessageName = other
			writeTestJSON(w, http.StatusOK, bad)
		}},
		{"empty name", func(w http.ResponseWriter) {
			bad := base
			bad.Name = ""
			writeTestJSON(w, http.StatusOK, bad)
		}},
		{"empty uid", func(w http.ResponseWriter) {
			bad := base
			bad.UID = ""
			writeTestJSON(w, http.StatusOK, bad)
		}},
		{"empty message uid", func(w http.ResponseWriter) {
			bad := base
			bad.MessageUID = ""
			writeTestJSON(w, http.StatusOK, bad)
		}},
		{"non-200", func(w http.ResponseWriter) { writeTestJSON(w, http.StatusCreated, base) }},
		{"post wrapper", func(w http.ResponseWriter) {
			writeTestJSON(w, http.StatusOK, apiv1.InboxAckResult{Outcome: "Confirmed", Ack: base})
		}},
		{"empty body", func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			respond = tc.send
			before := len(h.api.requests())
			h.mustRun(ExitFailed, "inbox", "ack-status", message, "--project", "p")
			if h.stdout.Len() != 0 || len(h.api.requests()) != before+1 || len(h.sleeps) != 0 || !strings.Contains(h.stderr.String(), "inbox ack-status returned an unconfirmed acknowledgement") {
				t.Fatalf("stdout %q stderr %q requests %d sleeps %v", h.stdout.String(), h.stderr.String(), len(h.api.requests()), h.sleeps)
			}
		})
	}
}

func TestInboxAckStatusHiddenAndUnavailableDoNotRetry(t *testing.T) {
	message := inboxAckStatusMessage()
	for _, tc := range []struct {
		name, want string
		status     int
		exit       int
	}{
		{"missing or hidden", "inbox record or authorized scope was not found (404)", http.StatusNotFound, ExitNotFound},
		{"unavailable", "HTTP status 503", http.StatusServiceUnavailable, ExitUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				if r.Method != http.MethodGet || len(body) != 0 {
					t.Errorf("write attempt %s %q", r.Method, body)
				}
				http.Error(w, "unacknowledged PRIVATE "+testToken, tc.status)
			}
			h.mustRun(tc.exit, "inbox", "ack-status", message, "--project", "p")
			if h.stdout.Len() != 0 || len(h.api.requests()) != 1 || len(h.sleeps) != 0 || !strings.Contains(h.stderr.String(), tc.want) || strings.Contains(h.stderr.String(), "unacknowledged") || strings.Contains(h.stderr.String(), "PRIVATE") || strings.Contains(h.stderr.String(), testToken) {
				t.Fatalf("stdout %q stderr %q requests %d sleeps %v", h.stdout.String(), h.stderr.String(), len(h.api.requests()), h.sleeps)
			}
		})
	}
}

func TestInboxAckStatusRejectsMalformedBeforeAPI(t *testing.T) {
	h := newHarness(t)
	message := inboxAckStatusMessage()
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Errorf("connected %s %s", r.Method, r.URL.Path)
	}
	h.mustRun(ExitUsage, "inbox", "ackstatus")
	if !strings.Contains(h.stderr.String(), "inbox requires send, ask, reply, list, read, ack or ack-status; see agent-run help inbox") {
		t.Fatal(h.stderr.String())
	}
	for _, args := range [][]string{
		{"inbox", "ack-status", "--project", "p"},
		{"inbox", "ack-status", "not-a-message", "--project", "p"},
		{"inbox", "ack-status", message},
		{"inbox", "ack-status", message, "--project", "Bad_Project"},
		{"inbox", "ack-status", message, "--project", "p", "extra"},
		{"inbox", "ack-status", message, "--project", "p", "--key", "k"},
		{"inbox", "ack-status", message, "--project", "p", "-o", "audit"},
		{"inbox", "ack", "status", "--project", "p"},
	} {
		h.mustRun(ExitUsage, args...)
		if args[len(args)-2] == "-o" && !strings.Contains(h.stderr.String(), "-o audit") {
			t.Fatalf("%q stderr %q", args, h.stderr.String())
		}
	}
	if len(h.api.requests()) != 0 {
		t.Fatalf("invalid ack-status reached the API: %+v", h.api.requests())
	}
}

func TestInboxAckStatusRejectsRedirect(t *testing.T) {
	h := newHarness(t)
	message := inboxAckStatusMessage()
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}
	h.mustRun(ExitFailed, "inbox", "ack-status", message, "--project", "p")
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != apiv1.InboxPath+"/"+message+"/ack" || len(reqs[0].body) != 0 || h.stdout.Len() != 0 || len(h.sleeps) != 0 || strings.Contains(h.stderr.String(), testToken) {
		t.Fatalf("redirect followed or wrote: %+v stdout %q stderr %q", reqs, h.stdout.String(), h.stderr.String())
	}
}

func TestInboxAckDefaultRemainsPostJSON(t *testing.T) {
	h := newHarness(t)
	message := inboxAckStatusMessage()
	ack := apiv1.InboxReadAck{Name: "ack-name", UID: "ack-uid", Project: "p", MessageName: message, MessageUID: "message-uid"}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.InboxPath+"/"+message+"/ack" || string(body) != "{}" || r.URL.Query().Get("project") != "p" || r.URL.Query().Get(apiv1.InboxViewQuery) != "" {
			t.Errorf("ack %s %s query %q body %q", r.Method, r.URL.Path, r.URL.RawQuery, body)
		}
		writeTestJSON(w, http.StatusCreated, apiv1.InboxAckResult{Outcome: "Created", Ack: ack})
	}
	h.mustRun(ExitOK, "inbox", "ack", message, "--project", "p")
	direct := h.stdout.String()
	var got apiv1.InboxAckResult
	if err := json.Unmarshal([]byte(direct), &got); err != nil || got.Outcome != "Created" || got.Ack.Name != ack.Name || got.Ack.UID != ack.UID || got.Ack.Project != ack.Project || got.Ack.MessageName != ack.MessageName || got.Ack.MessageUID != ack.MessageUID || !strings.Contains(direct, `"outcome"`) || !strings.Contains(direct, `"ack"`) {
		t.Fatalf("default ack json: %v %s", err, direct)
	}
	h.mustRun(ExitOK, "inbox", "ack", message, "--project", "p", "-o", "json")
	if h.stdout.String() != direct || len(h.api.requests()) != 2 {
		t.Fatalf("ack json changed or retried\n%s\nrequests %d", h.stdout.String(), len(h.api.requests()))
	}
}
