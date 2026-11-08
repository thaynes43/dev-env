package agentd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestCodexCommandOutcomeStaysInsideACompletedTurn(t *testing.T) {
	pane, result, stream := codexCommandFixture(t,
		codexCommandItem(`"status":"failed","exit_code":1,"aggregated_output":"tool output","command":"SECRET-COMMAND","id":"SECRET-ID","processId":"SECRET-PID","cwd":"SECRET-CWD"`),
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2,"cached_input_tokens":0}}`)
	if !result.Seen || result.IsError || result.Subtype != "success" || result.NumTurns != 1 || stream.failed {
		t.Fatalf("completed turn changed by a failed command: %+v failed=%v", result, stream.failed)
	}
	if !strings.Contains(pane, "[agentd] native Codex command outcome status=failed exit=1\n") || !strings.Contains(pane, "tool output\n") {
		t.Fatalf("failed-tool evidence missing: %q", pane)
	}
	for _, secret := range []string{"SECRET-COMMAND", "SECRET-ID", "SECRET-PID", "SECRET-CWD", "task succeeded"} {
		if strings.Contains(pane, secret) {
			t.Fatalf("pane exposed %s: %q", secret, pane)
		}
	}
}

func TestCodexCommandOutcomeRecoversThenCompletes(t *testing.T) {
	pane, result, stream := codexCommandFixture(t,
		codexCommandItem(`"status":"failed","exit_code":2,"aggregated_output":"exploratory"`),
		codexCommandItem(`"status":"completed","exit_code":0,"aggregated_output":"recovered"`),
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2,"cached_input_tokens":0}}`)
	if result.Subtype != "success" || result.IsError || stream.failed || stream.terminal != true {
		t.Fatal("recovered commands failed the completed turn", result.Subtype, result.IsError, stream.failed)
	}
	failed := strings.Index(pane, "status=failed exit=2\n")
	recovered := strings.Index(pane, "status=completed exit=0\n")
	if failed < 0 || recovered < failed || !strings.Contains(pane, "exploratory\n") || !strings.Contains(pane, "recovered\n") {
		t.Fatalf("recovery order lost: %q", pane)
	}
}

func TestCodexCommandOutcomeEmptyAndAdvisoryValues(t *testing.T) {
	cases := []struct {
		name string
		item string
		want string
	}{
		{name: "empty output", item: `"status":"completed","exit_code":0,"aggregated_output":""`, want: "status=completed exit=0\n\n"},
		{name: "missing", item: `"aggregated_output":"kept"`, want: "status=Unknown exit=Unknown\nkept\n"},
		{name: "null", item: `"status":null,"exit_code":null,"aggregated_output":"kept"`, want: "status=Unknown exit=Unknown\nkept\n"},
		{name: "malformed", item: `"status":"FAILED","exit_code":"1","aggregated_output":"kept"`, want: "status=Unknown exit=Unknown\nkept\n"},
		{name: "fraction and bool", item: `"status":true,"exit_code":1.5,"aggregated_output":"kept"`, want: "status=Unknown exit=Unknown\nkept\n"},
		{name: "int32 maximum", item: `"status":"completed","exit_code":2147483647`, want: "status=completed exit=2147483647\n"},
		{name: "int32 minimum", item: `"status":"failed","exit_code":-2147483648`, want: "status=failed exit=-2147483648\n"},
		{name: "upper overflow", item: `"status":"declined","exit_code":2147483648`, want: "status=declined exit=Unknown\n"},
		{name: "lower overflow", item: `"status":"failed","exit_code":-2147483649`, want: "status=failed exit=Unknown\n"},
		{name: "declined null", item: `"status":"declined","exit_code":null`, want: "status=declined exit=Unknown\n"},
		{name: "in progress", item: `"status":"in_progress","exit_code":null`, want: "status=in_progress exit=Unknown\n"},
		{name: "camel status", item: `"status":"inProgress","exit_code":4`, want: "status=Unknown exit=4\n"},
		{name: "independent malformed code", item: `"status":"completed","exit_code":"1"`, want: "status=completed exit=Unknown\n"},
		{name: "independent malformed status", item: `"status":{},"exit_code":1`, want: "status=Unknown exit=1\n"},
		{name: "canonical ignores camel alias", item: `"status":"completed","exit_code":1,"exitCode":0,"aggregated_output":"kept"`, want: "status=completed exit=1\nkept\n"},
		{name: "camel exit ignored", item: `"status":"completed","exitCode":7,"aggregated_output":"kept"`, want: "status=completed exit=Unknown\nkept\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pane, result, _ := codexCommandFixture(t, codexCommandItem(tc.item), `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"cached_input_tokens":0}}`)
			if result.Subtype != "success" || result.IsError {
				t.Fatal("advisory command value refused a completed turn", result.Subtype, result.IsError)
			}
			if !strings.Contains(pane, "[agentd] native Codex command outcome "+tc.want) {
				t.Fatalf("outcome %q missing from %q", tc.want, pane)
			}
			if strings.Contains(pane, "exit=0") && !strings.Contains(tc.want, "exit=0") {
				t.Fatalf("missing exit became zero: %q", pane)
			}
		})
	}
}

func TestCodexCommandOutcomeDoesNotReplaceTurnFailureOrProtocolRefusal(t *testing.T) {
	pane, result, stream := codexCommandFixture(t,
		codexCommandItem(`"status":"failed","exit_code":1,"aggregated_output":"before failure"`),
		`{"type":"turn.failed","error":{"message":"synthetic provider error"}}`)
	if !result.Seen || !result.IsError || result.Subtype != "provider_error" || !stream.failed {
		t.Fatal("turn.failed lost its provider result", result.Subtype, result.IsError, stream.failed)
	}
	if !strings.Contains(pane, "status=failed exit=1\n") || !strings.Contains(pane, "[agentd] native Codex turn failed: provider_error\n") {
		t.Fatalf("failure evidence missing: %q", pane)
	}
	if result.Subtype == "success" {
		t.Fatal("failed turn reported success")
	}

	path := filepath.Join(t.TempDir(), "launch.json")
	l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid"}
	var refused codexStream
	var ignored streamResult
	var out bytes.Buffer
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`,
		`{"type":"turn.started"}`,
		`{"type":"not-a-pinned-event"}`,
	} {
		err := refused.line([]byte(line), path, &l, &out, &ignored)
		if line == `{"type":"not-a-pinned-event"}` {
			if err == nil || ignored.Seen {
				t.Fatal("unknown event became a completed turn")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if err := refused.line([]byte(`{"type":"turn.completed"}`), path, &l, &out, &ignored); !errors.Is(err, errInvalidCodexCompletion) {
		t.Fatal("completion without usage accepted", err)
	}
	valid := []byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"cached_input_tokens":0}}`)
	if err := refused.line(valid, path, &l, &out, &ignored); err != nil {
		t.Fatal(err)
	}
	if err := refused.line(valid, path, &l, &out, &ignored); !errors.Is(err, errInvalidCodexCompletion) {
		t.Fatal("second terminal accepted", err)
	}
}

func TestCodexCommandOutcomeRequiresAnIntegerJSONToken(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", "Unknown"}, {"null", "Unknown"}, {" null ", "Unknown"},
		{"0", "0"}, {"1", "1"}, {"-1", "-1"},
		{"2147483647", "2147483647"}, {"-2147483648", "-2147483648"},
		{"2147483648", "Unknown"}, {"-2147483649", "Unknown"},
		{`"0"`, "Unknown"}, {`"1"`, "Unknown"},
		{"1.0", "Unknown"}, {"1e0", "Unknown"}, {"-1.5", "Unknown"},
		{"true", "Unknown"}, {"false", "Unknown"}, {"[]", "Unknown"}, {"{}", "Unknown"},
		{"01", "Unknown"}, {"+1", "Unknown"}, {"garbage", "Unknown"}, {"1 2", "Unknown"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := commandExit([]byte(tc.raw)); got != tc.want {
				t.Fatalf("exit token classified as %q; want %q", got, tc.want)
			}
		})
	}
}

func TestCodexCommandOutcomeCountsOnlyCompletedRecords(t *testing.T) {
	stage := func(event, id, fields string) string {
		return `{"type":"` + event + `","item":{"type":"command_execution","id":"` + id + `",` + fields + `}}`
	}
	started := stage("item.started", "command-a", `"status":"in_progress","exit_code":null,"aggregated_output":"STAGE-ONLY"`)
	updated := stage("item.updated", "command-a", `"status":"in_progress","exit_code":null,"aggregated_output":"STAGE-ONLY"`)
	failed := stage("item.completed", "command-a", `"status":"failed","exit_code":1,"aggregated_output":"final-a"`)
	completedB := stage("item.completed", "command-b", `"status":"completed","exit_code":0,"aggregated_output":"final-b"`)
	for _, tc := range []struct {
		name  string
		items []string
		count int
		order []string
	}{
		{"started only", []string{started}, 0, nil},
		{"updated only", []string{updated}, 0, nil},
		{"started updated completed", []string{started, updated, updated, failed}, 1, []string{"final-a"}},
		{"completion without start", []string{failed}, 1, []string{"final-a"}},
		{"interleaved ids", []string{started, stage("item.started", "command-b", `"status":"in_progress"`), updated, completedB, updated, failed}, 2, []string{"final-b", "final-a"}},
		// Evidence is per completed event, without retaining or deduplicating item IDs.
		{"repeated completed record", []string{failed, failed}, 2, []string{"final-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := append(append([]string(nil), tc.items...), `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"cached_input_tokens":0}}`)
			pane, result, stream := codexCommandFixture(t, lines...)
			if !result.Seen || result.IsError || result.Subtype != "success" || result.NumTurns != 1 || stream.failed || !stream.terminal {
				t.Fatal("command stages changed terminal-turn semantics", result.Subtype, result.IsError)
			}
			if n := strings.Count(pane, "[agentd] native Codex command outcome "); n != tc.count {
				t.Fatalf("got %d completed-record witnesses; want %d", n, tc.count)
			}
			for _, hidden := range []string{"command-a", "command-b", "STAGE-ONLY"} {
				if strings.Contains(pane, hidden) {
					t.Fatalf("stage or identifier exposed: %s", hidden)
				}
			}
			last := -1
			for _, output := range tc.order {
				at := strings.Index(pane, output)
				if at <= last {
					t.Fatal("completed output order lost")
				}
				last = at
			}
		})
	}
}

func TestCodexCommandOutcomeProcessExitZeroRetainsFailedToolEvidence(t *testing.T) {
	s := testSettings(t, t.TempDir())
	for _, path := range []string{s.WorkDir(), s.StateDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	cli := fakeCLI(t, dir, `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli `+managedCodexVersion+`'; exit 0; fi
read -r prompt
[ "$prompt" = "synthetic task" ] || exit 9
printf '%s\n' '{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}' '{"type":"turn.started"}' '{"type":"item.completed","item":{"type":"command_execution","status":"failed","exit_code":1,"aggregated_output":"synthetic tool output"}}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2,"cached_input_tokens":0}}'
exit 0
`)
	l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid", Argv: []string{cli, "exec", "--json"}, Dir: dir, Prompt: "synthetic task\n", LogPath: s.LogPath("task"), EventsPath: s.statePath(eventsFile), BootID: "first"}
	path := writeLaunch(t, s, l)
	pane := &syncBuffer{}
	if code := RunAgent(path, pane, make(chan os.Signal), 20*time.Millisecond); code != 0 {
		t.Fatalf("failed exploratory tool changed completed CLI exit: %d", code)
	}
	if !strings.Contains(pane.String(), "[agentd] native Codex command outcome status=failed exit=1\n") || !strings.Contains(pane.String(), "synthetic tool output\n") {
		t.Fatal("CLI exit zero lost the separate failed-command witness")
	}
}

func codexCommandItem(fields string) string {
	return `{"type":"item.completed","item":{"type":"command_execution",` + fields + `}}`
}

func codexCommandFixture(t *testing.T, lines ...string) (string, streamResult, codexStream) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "launch.json")
	l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid"}
	var stream codexStream
	var result streamResult
	var pane bytes.Buffer
	prefix := []string{
		`{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`,
		`{"type":"turn.started"}`,
	}
	for _, line := range append(prefix, lines...) {
		if err := stream.line([]byte(line), path, &l, &pane, &result); err != nil {
			t.Fatal(err)
		}
	}
	return pane.String(), result, stream
}
