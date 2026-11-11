package frontdoor

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestParentTools(t *testing.T) {
	base, api, logs, _ := startDoor(t, "#!/bin/sh\nexit 0\n")
	status, raw := postMCP(t, http.DefaultClient, base, testBearer, "tools/list", map[string]any{})
	var listed struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &listed) != nil || len(listed.Result.Tools) != 15 {
		t.Fatalf("tools/list status %d count %d", status, len(listed.Result.Tools))
	}
	want := map[string][4]bool{
		"parent_waiting": {true, false, true, false},
		"parent_read":    {true, false, true, false},
		"parent_answer":  {false, false, true, false},
		"parent_send":    {false, false, true, false},
	}
	keys := []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"}
	total := 0
	seen := map[string]bool{}
	for _, tool := range listed.Result.Tools {
		name, _ := tool["name"].(string)
		n := len(strings.Fields(tool["description"].(string)))
		if n == 0 || n > 30 {
			t.Fatalf("%s description has %d words", name, n)
		}
		total += descriptionWords(tool)
		ann, ok := want[name]
		if !ok {
			continue
		}
		seen[name] = true
		got, _ := tool["annotations"].(map[string]any)
		for i, key := range keys {
			if got[key] != ann[i] {
				t.Fatalf("%s %s = %#v, want %v", name, key, got[key], ann[i])
			}
		}
	}
	if len(seen) != len(want) || total != descriptionWordBound || descriptionWordBound != 214 {
		t.Fatalf("parent tools %d; descriptions %d; bound %d", len(seen), total, descriptionWordBound)
	}

	secret := "  SECRET_NOTE_TEXT\n"
	key := "secret-parent-key"
	note := "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
	other := "message." + strings.Repeat("a", 32) + "." + strings.Repeat("c", 32)
	third := "message." + strings.Repeat("a", 32) + "." + strings.Repeat("d", 32)
	created := time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)
	meta := func(name, kind, sender, itemKey string) apiv1.InboxMetadata {
		return apiv1.InboxMetadata{
			Name: name, UID: "note-uid", Project: "dev-env", Key: itemKey, Kind: kind,
			Sender:    apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: sender, UID: "uid-1"},
			Actor:     apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "coord-1", PodUID: "pod-1"},
			CreatedAt: created,
		}
	}
	api.set(func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ParentNotesPath && r.URL.Query().Get("box") == apiv1.ParentNotesInbox:
			writeJSON(w, http.StatusOK, apiv1.InboxList{Box: apiv1.ParentNotesInbox, Items: []apiv1.InboxMetadata{
				meta(note, "question", "coord-1", key),
				meta(other, "blocker", "coord-1", key+"-2"),
				meta(third, "result", "other-session", key+"-other"),
			}})
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ParentNotesPath+"/"+note:
			writeJSON(w, http.StatusOK, apiv1.InboxMessage{InboxMetadata: meta(note, "question", "coord-1", key), Text: secret})
		case r.Method == http.MethodPost && r.URL.Path == apiv1.ParentNotesPath+"/"+note+"/answer":
			var req apiv1.AnswerParentRequest
			if json.Unmarshal(body, &req) != nil || req.Text != secret || req.Key != "answer-key" {
				t.Errorf("answer body changed the text")
			}
			view := meta(note, "reply", "coord-1", req.Key)
			view.CallerSender = &apiv1.InboxCaller{ServiceAccount: "dev-env-system/dev-env-meatball", PolicyUID: "policy-uid"}
			writeJSON(w, http.StatusCreated, apiv1.InboxResult{Outcome: "Created", Message: view})
		case r.Method == http.MethodPost && r.URL.Path == apiv1.ParentNotesPath:
			var req apiv1.TellParentRequest
			if json.Unmarshal(body, &req) != nil || req.Text != secret || req.Session != "coord-1" || req.Kind != "" {
				t.Errorf("send body changed the text")
			}
			view := meta("message."+strings.Repeat("e", 32)+"."+strings.Repeat("f", 32), "", "", req.Key)
			view.Recipient = apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "coord-1", UID: "uid-1"}
			view.Sender = apiv1.TaskClaimOwner{}
			view.CallerSender = &apiv1.InboxCaller{ServiceAccount: "dev-env-system/dev-env-meatball", PolicyUID: "policy-uid"}
			writeJSON(w, http.StatusCreated, apiv1.InboxResult{Outcome: "Created", Message: view})
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath("coord-1"):
			writeJSON(w, http.StatusOK, apiv1.Session{Name: "coord-1", Phase: "Running"})
		default:
			writeJSON(w, http.StatusNotFound, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeNotFound, Message: secret + " foreign-session"}})
		}
	})

	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		status, raw := postMCP(t, http.DefaultClient, base, testBearer, "tools/call", map[string]any{"name": name, "arguments": args})
		return toolResult(t, status, raw)
	}
	waiting, isError := call("parent_waiting", map[string]any{"limit": 20})
	if isError || !strings.Contains(waiting, note) || !strings.Contains(waiting, "kind: blocker") || !strings.Contains(waiting, "age: 2026-11-10T22:00:00Z") || strings.Contains(waiting, key) || strings.Contains(waiting, secret) {
		t.Fatalf("waiting %q error %v", waiting, isError)
	}
	read, isError := call("parent_read", map[string]any{"name": note})
	if isError || !strings.Contains(read, secret) || strings.Contains(read, key) {
		t.Fatalf("read %q error %v", read, isError)
	}
	answer, isError := call("parent_answer", map[string]any{"name": note, "key": "answer-key", "text": secret})
	if isError || !strings.Contains(answer, secret) || !strings.HasSuffix(answer, secret) {
		t.Fatalf("answer %q error %v", answer, isError)
	}
	sent, isError := call("parent_send", map[string]any{"session": "coord-1", "key": "send-key", "text": secret})
	if isError || !strings.HasSuffix(sent, secret) || strings.Contains(sent, key) {
		t.Fatalf("send %q error %v", sent, isError)
	}
	statusText, isError := call("session_status", map[string]any{"name": "coord-1"})
	if isError || !strings.Contains(statusText, "coord-1") || !strings.Contains(statusText, "parent notes waiting: 2\n") || strings.Contains(statusText, key) || strings.Contains(statusText, secret) {
		t.Fatalf("status %q error %v", statusText, isError)
	}

	api.reset()
	api.set(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, http.StatusNotFound, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeNotFound, Message: secret + " foreign-session"}})
	})
	hidden, isError := call("parent_send", map[string]any{"session": "foreign-session", "key": "send-key", "text": secret})
	if !isError || !strings.Contains(hidden, "refused:") || !strings.Contains(hidden, "404") || strings.Contains(hidden, secret) || strings.Contains(hidden, "foreign-session") {
		t.Fatalf("foreign send %q error %v", hidden, isError)
	}
	hiddenRead, isError := call("parent_read", map[string]any{"name": note})
	if !isError || strings.Contains(hiddenRead, secret) {
		t.Fatalf("hidden read %q error %v", hiddenRead, isError)
	}
	badName, isError := call("parent_read", map[string]any{"name": secret})
	if !isError || strings.Contains(badName, secret) || len(api.requests()) != 2 {
		t.Fatalf("bad name reached the operator or echoed text: %q requests %d", badName, len(api.requests()))
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), key) || strings.Contains(logs.String(), "blocker") {
		t.Fatal("front door logged note text, key or kind")
	}
}
