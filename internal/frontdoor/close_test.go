package frontdoor

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Run the real opwork CLI against an offline, persistent tracker transport.
// No network, real tracker, session or credential is used by the child.
const closeTrackerScript = `
import copy, importlib.util, json, os, pathlib, sys
state_path = pathlib.Path(__file__).with_name("tracker.json")
state = json.loads(state_path.read_text())
spec = importlib.util.spec_from_file_location("opwork", OPWORK_SOURCE)
opwork = importlib.util.module_from_spec(spec)
spec.loader.exec_module(opwork)
state["commands"].append({"args": sys.argv[1:], "session": os.environ.get("OPWORK_SESSION"), "origin": os.environ.get("OPWORK_WRITE_ORIGIN")})
statuses = {"In progress": 2, "In review": 3, "Accepted": 4, "Needs approval": 5, "Needs answer": 6, "Paused": 7}

def request(method, path, payload=None):
    route = path.split("?")[0]
    wp = state["item"]
    if route == "/api/v3/projects/haynes-engineering":
        return 200, {"id": 1, "identifier": "haynes-engineering"}
    if route == "/api/v3/statuses":
        return 200, {"_embedded": {"elements": [{"name": n, "id": i} for n, i in statuses.items()]}}
    if route == "/api/v3/work_packages/528/form":
        allowed = ["In review"] if state.get("blockAcceptance") else ["Accepted", "In review"]
        return 200, {"_embedded": {"schema": {"status": {"_embedded": {"allowedValues": [{"name": n} for n in allowed]}}}}}
    if route == "/api/v3/work_packages/528/activities":
        if method == "POST":
            if state.get("claimNoteFailure") and "Claimed." in payload["comment"]["raw"]:
                return 422, {"message": "fake note rejected"}
            state["writes"].append({"method": method, "payload": payload})
            state["notes"].append({"id": len(state["notes"])+1, "comment": payload["comment"]})
            return 201, {"id": len(state["notes"])}
        return 200, {"_embedded": {"elements": copy.deepcopy(state["notes"])}}
    if route == "/api/v3/work_packages/528":
        if state.get("otherRefusal"):
            return 403, {"message": "owned by someone else; text alone is not a guard signal"}
        if state.get("readBackFailure") and "show" in sys.argv:
            return 503, {"message": "fake read unavailable"}
        if method == "PATCH":
            if state.get("race"):
                state["race"] = False
                wp["lockVersion"] += 1
                wp["_links"]["status"] = {"title": "Needs approval", "href": "/api/v3/statuses/5"}
            if payload["lockVersion"] != wp["lockVersion"]:
                return 409, {"message": "changed"}
            state["writes"].append({"method": method, "payload": payload})
            for k, v in payload.items():
                if k not in ("lockVersion", "_links"):
                    wp[k] = v
            if "status" in payload.get("_links", {}):
                href = payload["_links"]["status"]["href"]
                title = next(n for n, i in statuses.items() if href == "/api/v3/statuses/"+str(i))
                wp["_links"]["status"] = {"title": title, "href": href}
            wp["lockVersion"] += 1
        return 200, copy.deepcopy(wp)
    raise AssertionError("unexpected fake route")

try:
    rc = opwork.main(sys.argv[1:], opwork.Tracker(request, RuntimeError, "haynes-engineering"), RuntimeError)
finally:
    state_path.write_text(json.dumps(state))
sys.exit(rc)
`

type closeTracker struct {
	Item             map[string]any   `json:"item"`
	Commands         []closeCommand   `json:"commands"`
	Writes           []map[string]any `json:"writes"`
	Notes            []map[string]any `json:"notes"`
	Race             bool             `json:"race,omitempty"`
	OtherRefusal     bool             `json:"otherRefusal,omitempty"`
	BlockAcceptance  bool             `json:"blockAcceptance,omitempty"`
	ClaimNoteFailure bool             `json:"claimNoteFailure,omitempty"`
	ReadBackFailure  bool             `json:"readBackFailure,omitempty"`
}

type closeCommand struct {
	Args    []string `json:"args"`
	Session string   `json:"session"`
	Origin  string   `json:"origin"`
}

const closeOwner = "agent | worktree finished-coordinator | branch agent/finished-coordinator"

func newCloseTracker() closeTracker {
	return closeTracker{
		Item: map[string]any{
			"id": 528, "lockVersion": 7, "subject": "Coordinator work", "customField2": closeOwner,
			"_links": map[string]any{
				"status":  map[string]any{"title": "In review", "href": "/api/v3/statuses/3"},
				"type":    map[string]any{"title": "Story"},
				"project": map[string]any{"href": "/api/v3/projects/1"},
			},
		},
		Commands: []closeCommand{}, Writes: []map[string]any{}, Notes: []map[string]any{},
	}
}

func finishedCoordinator() apiv1.Session {
	return apiv1.Session{
		Name: "finished-coordinator", IdempotencyKey: "coordinator-528", Mode: "task", Phase: "Running",
		AgentStatus: &apiv1.AgentStatus{State: "exited", Task: &apiv1.TaskResult{
			FinishedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC), Subtype: "success",
		}},
	}
}

func startCloseDoor(t *testing.T, state closeTracker, list []apiv1.Session, sess apiv1.Session, failAt string) (string, *fakeAPI, string) {
	t.Helper()
	source, err := filepath.Abs("../../images/agent/tools/openproject/opwork.py")
	if err != nil {
		t.Fatal(err)
	}
	base, api, _, raw := startDoor(t, "#!/usr/bin/env python3\nOPWORK_SOURCE = "+strconv.Quote(source)+"\n"+closeTrackerScript)
	statePath := filepath.Join(filepath.Dir(raw.(*handler).work.path), "tracker.json")
	writeFile(t, statePath, string(mustJSON(t, state)))
	api.set(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path == failAt {
			writeJSON(w, 503, apiv1.ErrorResponse{Error: apiv1.Error{Code: "unavailable", Message: "fake operator unavailable"}})
			return
		}
		switch r.URL.Path {
		case "/v1/sessions":
			if r.Method != http.MethodGet || r.URL.Query().Get("mine") != "true" {
				t.Error("coordinator lookup must be a caller-scoped read")
			}
			writeJSON(w, 200, apiv1.SessionList{Sessions: list})
		case "/v1/sessions/finished-coordinator", "/v1/sessions/different-coordinator":
			writeJSON(w, 200, sess)
		default:
			t.Error("unexpected operator request")
			writeJSON(w, 404, apiv1.ErrorResponse{})
		}
	})
	return base, api, statePath
}

func callClose(t *testing.T, base string) (string, bool) {
	t.Helper()
	status, raw := postMCP(t, nil, base, testBearer, "tools/call", map[string]any{
		"name": "backlog_close", "arguments": map[string]any{"id": 528, "evidence": "caller evidence"},
	})
	return toolResult(t, status, raw)
}

func readCloseTracker(t *testing.T, path string) closeTracker {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state closeTracker
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestBacklogCloseFinishedCoordinator(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "succeeded"
		if failed {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			sess := finishedCoordinator()
			if failed {
				sess.AgentStatus.Task.ExitCode = 1
			}
			base, api, path := startCloseDoor(t, newCloseTracker(), []apiv1.Session{sess}, sess, "")
			text, isError := callClose(t, base)
			if isError || !strings.Contains(text, "completed (read back): #528 Story [Accepted]") {
				t.Fatalf("close error=%v text=%s", isError, text)
			}
			state := readCloseTracker(t, path)
			want := []string{"complete", "claim", "complete"}
			if len(state.Commands) != len(want) || len(state.Writes) != 4 {
				t.Fatalf("commands=%v writes=%v", state.Commands, state.Writes)
			}
			for i, command := range state.Commands {
				if command.Args[1] != want[i] || command.Session != "meatball" || command.Origin != "meatball" {
					t.Fatalf("command %d: %+v", i, command)
				}
				joined := strings.Join(command.Args, " ")
				if (i == 1) != strings.Contains(joined, "--force") {
					t.Fatalf("force on wrong command: %v", command.Args)
				}
			}
			if !strings.Contains(strings.Join(state.Commands[1].Args, " "), "--expect-lock=7") {
				t.Fatal("claim did not use refused snapshot's lock")
			}
			if state.Item["customField2"] != "meatball" || len(state.Notes) != 2 {
				t.Fatalf("owner=%v notes=%v", state.Item["customField2"], state.Notes)
			}
			for i, note := range state.Notes {
				body := note["comment"].(map[string]any)["raw"].(string)
				if !strings.Contains(body, "**[meatball]**") || !strings.Contains(body, "Origin: meatball") {
					t.Fatal("write attribution missing")
				}
				if i == 0 && !strings.Contains(body, "own finished coordinator finished-coordinator") || i == 1 && !strings.Contains(body, "caller evidence") {
					t.Fatalf("wrong note %d: %s", i, body)
				}
			}
			if len(api.requests()) != 2 {
				t.Fatalf("lookup requests %d", len(api.requests()))
			}
		})
	}
}

func TestBacklogCloseRefusesWithoutWrites(t *testing.T) {
	cases := []struct {
		name   string
		change func(*closeTracker, *apiv1.Session, *[]apiv1.Session)
		failAt string
		reason string
	}{
		{"running_owner", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.AgentStatus.State = "running" }, "", "not proven finished"},
		{"starting_owner", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.AgentStatus.State = "starting" }, "", "not proven finished"},
		{"unknown_task", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.AgentStatus.Task.Subtype = "" }, "", "Succeeded or Failed"},
		{"timed_out_task", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.AgentStatus.Task.TimedOut = true }, "", "Succeeded or Failed"},
		{"missing_task", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.AgentStatus.Task = nil }, "", "not proven finished"},
		{"no_finish_time", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) {
			s.AgentStatus.Task.FinishedAt = time.Time{}
		}, "", "not proven finished"},
		{"interactive_owner", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.Mode = "interactive" }, "", "not proven finished"},
		{"other_callers_session", func(_ *closeTracker, _ *apiv1.Session, l *[]apiv1.Session) { *l = nil }, "", "could not prove"},
		{"owner_name_differs", func(_ *closeTracker, s *apiv1.Session, l *[]apiv1.Session) {
			s.Name = "different-coordinator"
			(*l)[0].Name = s.Name
		}, "", "does not match"},
		{"operator_has_no_session", func(_ *closeTracker, _ *apiv1.Session, l *[]apiv1.Session) { *l = []apiv1.Session{} }, "", "could not prove"},
		{"operator_call_fails", nil, "/v1/sessions", "could not prove"},
		{"operator_get_fails", nil, "/v1/sessions/finished-coordinator", "could not prove"},
		{"ambiguous_lookup", func(_ *closeTracker, s *apiv1.Session, l *[]apiv1.Session) { *l = append(*l, *s) }, "", "could not prove"},
		{"different_item_on_get", func(_ *closeTracker, s *apiv1.Session, _ *[]apiv1.Session) { s.IdempotencyKey = "coordinator-999" }, "", "does not match"},
		{"owner_not_session_shaped", func(w *closeTracker, _ *apiv1.Session, _ *[]apiv1.Session) { w.Item["customField2"] = "another-caller" }, "", "does not name a session"},
		{"owner_branch_differs", func(w *closeTracker, _ *apiv1.Session, _ *[]apiv1.Session) {
			w.Item["customField2"] = "agent | worktree finished-coordinator | branch agent/other"
		}, "", "does not name a session"},
		{"needs_answer_hold", holdCloseItem("Needs answer"), "", "held"},
		{"needs_approval_hold", holdCloseItem("Needs approval"), "", "held"},
		{"paused_review_hold", holdCloseItem("Paused"), "", "held"},
		{"changed_before_takeover", func(w *closeTracker, _ *apiv1.Session, _ *[]apiv1.Session) { w.Race = true }, "", "item changed"},
		{"other_opwork_refusal", func(w *closeTracker, _ *apiv1.Session, _ *[]apiv1.Session) { w.OtherRefusal = true }, "", "structured ownership refusal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, sess := newCloseTracker(), finishedCoordinator()
			list := []apiv1.Session{sess}
			if tc.change != nil {
				tc.change(&state, &sess, &list)
			}
			base, _, path := startCloseDoor(t, state, list, sess, tc.failAt)
			text, isError := callClose(t, base)
			if !isError || !strings.Contains(text, "Takeover not attempted: ") || !strings.Contains(text, tc.reason) {
				t.Fatalf("error=%v text=%s", isError, text)
			}
			if !state.OtherRefusal {
				status := state.Item["_links"].(map[string]any)["status"].(map[string]any)["title"].(string)
				original := "REFUSED: #528 is owned by <" + state.Item["customField2"].(string) + ">, not this session. Not overwritten; coordinate or use --force (takeover)."
				if status != "In review" {
					original = "REFUSED: #528 is [" + status + "], an explicit hold. Wait for the answer/approval; --force only with explicit authorization."
				}
				if !strings.HasPrefix(text, original+"\nopwork exited 3\nTakeover not attempted: ") {
					t.Fatalf("original refusal changed: %s", text)
				}
			}
			got := readCloseTracker(t, path)
			if len(got.Writes) != 0 || len(got.Notes) != 0 || got.Item["customField2"] != state.Item["customField2"] {
				t.Fatalf("refusal wrote tracker state: %+v", got)
			}
			wantCommands := 1
			if state.Race {
				wantCommands = 2
			}
			if len(got.Commands) != wantCommands {
				t.Fatalf("unexpected commands: %v", got.Commands)
			}
		})
	}
}

func holdCloseItem(status string) func(*closeTracker, *apiv1.Session, *[]apiv1.Session) {
	return func(w *closeTracker, _ *apiv1.Session, _ *[]apiv1.Session) {
		w.Item["_links"].(map[string]any)["status"].(map[string]any)["title"] = status
	}
}

func TestBacklogCloseAfterTakeoverReportsState(t *testing.T) {
	for _, name := range []string{"not_accepted", "claim_note_failed", "state_unavailable"} {
		t.Run(name, func(t *testing.T) {
			state, sess := newCloseTracker(), finishedCoordinator()
			state.BlockAcceptance = true
			state.ClaimNoteFailure = name == "claim_note_failed"
			state.ReadBackFailure = name == "state_unavailable"
			base, _, path := startCloseDoor(t, state, []apiv1.Session{sess}, sess, "")
			text, isError := callClose(t, base)
			if !isError || strings.Contains(text, "Takeover not attempted") {
				t.Fatalf("error=%v text=%s", isError, text)
			}
			want := "Current tracker state (read back):\n#528 Story [In review]"
			if state.ClaimNoteFailure {
				want = "Current tracker state (read back):\n#528 Story [In progress]"
			}
			if state.ReadBackFailure {
				want = "current tracker state is unconfirmed"
			}
			if !strings.Contains(text, want) {
				t.Fatalf("missing current state: %s", text)
			}
			got := readCloseTracker(t, path)
			if got.Item["customField2"] != "meatball" {
				t.Fatal("claim was undone")
			}
			wantCommands := 4
			if state.ClaimNoteFailure {
				wantCommands = 3
			}
			if len(got.Commands) != wantCommands || got.Commands[len(got.Commands)-1].Args[1] != "show" {
				t.Fatalf("unexpected follow-up: %+v", got.Commands)
			}
		})
	}
}

func TestBacklogCloseUnprovenGuardSignal(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
		exit   string
		prefix string
	}{
		{"missing_lock", func(g map[string]any) { delete(g, "lockVersion") }, "3", ""},
		{"negative_lock", func(g map[string]any) { g["lockVersion"] = -1 }, "3", ""},
		{"missing_hold", func(g map[string]any) { delete(g, "hold") }, "3", ""},
		{"held_snapshot", func(g map[string]any) { g["hold"] = true }, "3", ""},
		{"missing_status", func(g map[string]any) { delete(g, "status") }, "3", ""},
		{"wrong_item", func(g map[string]any) { g["id"] = 999 }, "3", ""},
		{"unsupported_schema", func(g map[string]any) { g["schema"] = "future" }, "3", ""},
		{"wrong_reason", func(g map[string]any) { g["reason"] = "conflict" }, "3", ""},
		{"wrong_exit_code", nil, "1", ""},
		{"extra_stdout", nil, "3", "echo 'unexpected output'\n"},
		{"truncated_signal", func(g map[string]any) { g["owner"] = strings.Repeat("x", opworkOutputCap) }, "3", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signal := map[string]any{"schema": "opwork.guard.v1", "reason": "ownership", "message": "original refusal", "id": 528,
				"lockVersion": 7, "owner": closeOwner, "status": "In review", "hold": false}
			if tc.change != nil {
				tc.change(signal)
			}
			script := "#!/bin/sh\n" + tc.prefix + "cat <<'SIGNAL' >&2\n" + string(mustJSON(t, signal)) + "\nSIGNAL\nexit " + tc.exit + "\n"
			base, api, _, _ := startDoor(t, script)
			text, isError := callClose(t, base)
			if !isError || !strings.Contains(text, "Takeover not attempted: ") || len(api.requests()) != 0 {
				t.Fatalf("unproven signal used: error=%v operator requests=%d", isError, len(api.requests()))
			}
		})
	}
}

func TestBacklogGroomHasNoTakeover(t *testing.T) {
	base, api, _, _ := startDoor(t, "#!/bin/sh\necho 'original ownership refusal' >&2\nexit 3\n")
	status, result := postMCP(t, nil, base, testBearer, "tools/call", map[string]any{
		"name": "backlog_groom", "arguments": map[string]any{"id": 528, "fields": map[string]any{"description": "scope"}},
	})
	text, isError := toolResult(t, status, result)
	if !isError || text != "original ownership refusal\nopwork exited 3" || len(api.requests()) != 0 {
		t.Fatalf("groom took over: error=%v text=%s", isError, text)
	}
}
