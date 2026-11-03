package agentd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Natural exit must keep a missing thread, a missing terminal and a second
// terminal distinct. Causes are fixed tokens and carry no stream text.
// second-terminal replays a valid stream control sequence before the refusal.
// A first completion missing usage and an unknown event after success stay
// parser_refused. A second turn.completed is second_terminal only once latched.
func TestRunAgentCodexProtocolCauseAfterReaderJoin(t *testing.T) {
	thread := `{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`
	turn := `{"type":"turn.started"}`
	complete := `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2,"cached_input_tokens":0}}`
	second := `{"type":"error","detail":"SENTINEL-SECRET"}`
	bare := `{"type":"turn.completed"}`
	again := `{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":4,"cached_input_tokens":0}}`
	unknown := `{"type":"not.terminal"}`
	cases := []struct {
		kind, body, cause string
	}{
		{kind: "missing-thread", body: "", cause: "missing_thread"},
		{kind: "missing-terminal", body: "printf '%s\\n' '" + thread + "' '" + turn + "'\n", cause: "missing_terminal"},
		{kind: "second-terminal", body: "printf '%s\\n' '" + thread + "' '" + turn + "' '" + complete + "' '" + second + "'\n", cause: "second_terminal"},
		{kind: "missing-usage", body: "printf '%s\\n' '" + thread + "' '" + turn + "' '" + bare + "'\n", cause: "parser_refused"},
		{kind: "second-completed", body: "printf '%s\\n' '" + thread + "' '" + turn + "' '" + complete + "' '" + again + "'\n", cause: "second_terminal"},
		{kind: "unknown-after-success", body: "printf '%s\\n' '" + thread + "' '" + turn + "' '" + complete + "' '" + unknown + "'\n", cause: "parser_refused"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			s := testSettings(t, t.TempDir())
			if err := os.MkdirAll(s.WorkDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			cli := fakeCLI(t, dir, `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli `+managedCodexVersion+`'; exit 0; fi
read -r prompt
[ "$prompt" = "synthetic task" ] || exit 9
`+tc.body)
			l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid", Argv: []string{cli, "exec", "--json"}, Dir: dir, Prompt: "synthetic task\n", LogPath: s.LogPath("task"), EventsPath: s.statePath(eventsFile), BootID: "first"}
			path := writeLaunch(t, s, l)
			pane := &syncBuffer{}
			code := RunAgent(path, pane, make(chan os.Signal), 20*time.Millisecond)
			if code == 0 {
				t.Fatal("protocol failure exited zero", tc.kind)
			}
			out := pane.String()
			if !strings.Contains(out, "[agentd] native Codex protocol cause: "+tc.cause) {
				t.Fatal("pane lost the protocol cause", tc.kind, code)
			}
			for _, other := range []string{"missing_thread", "missing_terminal", "second_terminal"} {
				if other != tc.cause && strings.Contains(out, "[agentd] native Codex protocol cause: "+other) {
					t.Fatal("protocol cause collided", tc.kind, other)
				}
			}
			if strings.Contains(out, "SENTINEL-SECRET") {
				t.Fatal("pane exposed raw native content", tc.kind)
			}
			if (tc.kind != "missing-thread") != strings.Contains(out, "native Codex thread confirmed") {
				t.Fatal("valid stream control was not preserved", tc.kind)
			}
			var saved taskResult
			if readJSONFile(s.statePath(resultFile), &saved) != nil || saved.Subtype != "native_protocol_refused" || !saved.IsError {
				t.Fatal("refusal kept a successful terminal classification", tc.kind, saved.Subtype)
			}
		})
	}
}
