package agentrun

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestParentWaitAnyPrintsNoTextAndPassesCursor(t *testing.T) {
	h := newHarness(t)
	name := parentTestMetadata().Name
	created := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodGet || r.URL.Path != apiv1.ParentNotesWaitAnyPath || len(body) != 0 || r.URL.Query().Get("seconds") != "1" || r.URL.Query().Get("after") != "42" {
			t.Errorf("wrong wait-any request %s %s", r.Method, r.URL.RequestURI())
		}
		writeTestJSON(w, http.StatusOK, map[string]any{
			"state": "note", "name": name, "child": "child", "kind": "question",
			"createdAt": created, "cursor": "43", "text": "PRIVATE_NOTE_TEXT", "key": "PRIVATE_KEY",
		})
	}
	h.mustRun(ExitOK, "parent", "wait-any", "--after", "42", "--for", "1s")
	want := name + " child question\n43\n"
	if h.stdout.String() != want || h.stderr.Len() != 0 || strings.Contains(h.stdout.String(), "PRIVATE_NOTE_TEXT") || strings.Contains(h.stdout.String(), "PRIVATE_KEY") {
		t.Fatalf("text output %q stderr %q", h.stdout.String(), h.stderr.String())
	}
	h.mustRun(ExitOK, "parent", "wait-any", "--after", "42", "--for", "1s", "-o", "json")
	if strings.Contains(h.stdout.String(), "PRIVATE_NOTE_TEXT") || strings.Contains(h.stdout.String(), "PRIVATE_KEY") || strings.Contains(h.stderr.String(), "PRIVATE_") {
		t.Fatal("JSON included note text or key")
	}
	var raw map[string]any
	if json.Unmarshal(h.stdout.Bytes(), &raw) != nil || raw["text"] != nil || raw["key"] != nil || raw["name"] != name || raw["cursor"] != "43" || raw["state"] != "note" {
		t.Fatalf("JSON %s", h.stdout.String())
	}
	if len(h.api.requests()) != 2 || len(h.sleeps) != 0 {
		t.Fatal("wait-any retried or slept")
	}
}

func TestParentWaitAnyWaitingResyncAndDefault(t *testing.T) {
	h := newHarness(t)
	mode := "waiting"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path != apiv1.ParentNotesWaitAnyPath || r.URL.Query().Get("seconds") != "900" || r.URL.Query().Get("after") != "" {
			t.Errorf("default wait-any %s", r.URL.RequestURI())
		}
		cursor := "77"
		if mode == "resync" {
			cursor = "900"
		}
		writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: mode, Cursor: cursor})
	}
	h.mustRun(ExitOK, "parent", "wait-any")
	if h.stdout.String() != "waiting\n77\n" || h.stderr.Len() != 0 {
		t.Fatalf("waiting %q %q", h.stdout.String(), h.stderr.String())
	}
	mode = "resync"
	h.mustRun(ExitOK, "parent", "wait-any", "--for", "15m")
	if h.stdout.String() != "resync\n900\nrun agent-run parent list\n" || h.stderr.Len() != 0 || len(h.api.requests()) != 2 || len(h.sleeps) != 0 {
		t.Fatalf("resync %q requests %d", h.stdout.String(), len(h.api.requests()))
	}
}

func TestParentWaitAnyRepeatsPast900(t *testing.T) {
	h := newHarness(t)
	var calls []string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		calls = append(calls, q.Get("seconds")+"|"+q.Get("after"))
		cursor := "77"
		if len(calls) == 2 {
			cursor = "88"
		}
		writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: apiv1.ParentNoteWaiting, Cursor: cursor})
	}
	h.mustRun(ExitOK, "parent", "wait-any", "--for", "901s")
	if len(calls) != 2 || calls[0] != "900|" || calls[1] != "1|77" || h.stdout.String() != "waiting\n88\n" || len(h.sleeps) != 0 {
		t.Fatalf("repeat calls %v out %q sleeps %v", calls, h.stdout.String(), h.sleeps)
	}

	h = newHarness(t)
	calls = nil
	name := parentTestMetadata().Name
	created := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls = append(calls, r.URL.Query().Get("seconds"))
		writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{
			State: apiv1.ParentNoteArrived, Name: name, Child: "child", Kind: "question", CreatedAt: created, Cursor: "43",
		})
	}
	h.mustRun(ExitOK, "parent", "wait-any", "--for", "901s")
	if len(calls) != 1 || calls[0] != "900" || h.stdout.String() != name+" child question\n43\n" {
		t.Fatalf("a note did not stop the repeat: %v %q", calls, h.stdout.String())
	}

	// A later call fails. The last cursor the server confirmed is printed, and
	// the exit code is the failure's own. Text and JSON both carry it.
	h = newHarness(t)
	n := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		n++
		if n == 1 {
			writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: apiv1.ParentNoteWaiting, Cursor: "77"})
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "inbox record not found")
	}
	h.mustRun(ExitNotFound, "parent", "wait-any", "--for", "901s")
	if h.stdout.String() != "77\nagent-run parent wait-any --after 77\n" || !strings.Contains(h.stderr.String(), "404") || strings.Contains(h.stdout.String(), "waiting") {
		t.Fatalf("later failure text %q stderr %q", h.stdout.String(), h.stderr.String())
	}
	n = 0
	h.mustRun(ExitNotFound, "parent", "wait-any", "--for", "901s", "-o", "json")
	var raw map[string]any
	if json.Unmarshal(h.stdout.Bytes(), &raw) != nil || raw["cursor"] != "77" || raw["command"] != "agent-run parent wait-any --after 77" || raw["state"] != nil {
		t.Fatalf("later failure JSON %s", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "404") {
		t.Fatalf("later failure JSON stderr %q", h.stderr.String())
	}

	// No call confirmed a cursor, including a call that was given --after.
	// The command says to read the list and does not print that cursor.
	for _, args := range [][]string{
		{"parent", "wait-any", "--for", "30s"},
		{"parent", "wait-any", "--after", "42", "--for", "1s"},
		{"parent", "wait-any", "--for", "30s", "-o", "json"},
	} {
		h = newHarness(t)
		h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "PRIVATE_LOST")
		}
		h.mustRun(ExitUnconfirmed, args...)
		if h.stdout.Len() != 0 || strings.Contains(h.stdout.String(), "42") || strings.Contains(h.stderr.String(), "42") || strings.Contains(h.stderr.String(), "PRIVATE_LOST") || !strings.Contains(h.stderr.String(), "read agent-run parent list before starting again") {
			t.Fatalf("%v stdout %q stderr %q", args, h.stdout.String(), h.stderr.String())
		}
	}
}

func TestParentWaitAnyClientTimeout(t *testing.T) {
	h := newHarness(t)
	old := http.DefaultTransport
	tr := old.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	defer func() { http.DefaultTransport = old }()
	var deadlines []time.Duration
	tr.Proxy = func(r *http.Request) (*url.URL, error) {
		end, ok := r.Context().Deadline()
		if !ok {
			t.Error("wait-any request has no client deadline")
			return nil, nil
		}
		deadlines = append(deadlines, time.Until(end))
		return nil, nil
	}
	http.DefaultTransport = tr
	n := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		n++
		cursor := "77"
		if r.URL.Query().Get("after") == "77" {
			cursor = "88"
		}
		writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: apiv1.ParentNoteWaiting, Cursor: cursor})
	}
	check := func(args []string, want ...time.Duration) {
		t.Helper()
		deadlines = nil
		h.mustRun(ExitOK, append([]string{"parent", "wait-any"}, args...)...)
		if len(deadlines) != len(want) {
			t.Fatalf("%v deadlines %v", args, deadlines)
		}
		for i, timeout := range want {
			if deadlines[i] > timeout || deadlines[i] < timeout-time.Second {
				t.Fatalf("%v deadline %v, want %v", args, deadlines[i], timeout)
			}
		}
	}
	check([]string{"--for", "1s"}, 121*time.Second)
	check([]string{"--for", "15m"}, 17*time.Minute)
	check([]string{"--for", "901s"}, 17*time.Minute, 121*time.Second)
	if n != 4 {
		t.Fatalf("timeout probe calls %d", n)
	}
}

func TestParentWaitAnyInvalidArguments(t *testing.T) {
	long := strings.Repeat("9", 257)
	for _, args := range [][]string{
		{"wait-any", "message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)},
		{"wait-any", "--for", "1500ms"}, {"wait-any", "--for", "-1s"},
		{"wait-any", "--after", "0"}, {"wait-any", "--after", "nope"}, {"wait-any", "--after", "01"}, {"wait-any", "--after", long},
		{"wait-any", "--project", "dev-env"},
	} {
		h := newHarness(t)
		h.mustRun(ExitUsage, append([]string{"parent"}, args...)...)
		if len(h.api.requests()) != 0 || h.stdout.Len() != 0 {
			t.Fatalf("%v reached the API or printed %q", args, h.stdout.String())
		}
	}
}

func TestParentWaitAnyForZero(t *testing.T) {
	for _, flag := range []string{"0", "0s"} {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method != http.MethodGet || r.URL.Path != apiv1.ParentNotesWaitAnyPath || len(body) != 0 || r.URL.Query().Get("seconds") != "0" || r.URL.Query().Get("after") != "" {
				t.Errorf("zero wait %s %s", r.Method, r.URL.RequestURI())
			}
			writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: apiv1.ParentNoteWaiting, Cursor: "15"})
		}
		h.mustRun(ExitOK, "parent", "wait-any", "--for", flag)
		if h.stdout.String() != "waiting\n15\n" || h.stderr.Len() != 0 || len(h.api.requests()) != 1 {
			t.Fatalf("--for %s %q requests %d", flag, h.stdout.String(), len(h.api.requests()))
		}
	}
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("seconds") != "0" || r.URL.Query().Get("after") != "42" {
			t.Errorf("zero after %s", r.URL.RequestURI())
		}
		writeTestJSON(w, http.StatusOK, apiv1.ParentNoteWaitAnyResult{State: apiv1.ParentNoteWaiting, Cursor: "42"})
	}
	h.mustRun(ExitOK, "parent", "wait-any", "--for", "0", "--after", "42", "-o", "json")
	var raw map[string]any
	if json.Unmarshal(h.stdout.Bytes(), &raw) != nil || raw["state"] != "waiting" || raw["cursor"] != "42" || len(h.api.requests()) != 1 {
		t.Fatalf("--for 0 JSON %s", h.stdout.String())
	}
}

func TestParentHelpMentionsWaitAny(t *testing.T) {
	help := commandHelp["parent"]
	if !strings.Contains(help, "agent-run parent wait-any [--after <cursor>] [--for 15m] [-o json]") || !strings.Contains(usageText, "parent tell|list|read|answer|wait|wait-any") {
		t.Fatal("help omits parent wait-any")
	}
	h := newHarness(t)
	h.mustRun(ExitOK, "help", "parent")
	if !strings.Contains(h.stdout.String(), "agent-run parent wait-any") || !strings.Contains(h.stdout.String(), "--for 0") || !strings.Contains(h.stdout.String(), "read agent-run parent list before starting again") || strings.Contains(h.stdout.String(), "PRIVATE") {
		t.Fatal("agent-run help parent omits wait-any")
	}
}
