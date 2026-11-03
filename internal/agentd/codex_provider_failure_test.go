package agentd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const syntheticCapacityMessage = "Selected model is at capacity. Please try a different model."

func TestCodexDiagnosticErrorsAwaitTerminal(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "recovered"}[success], func(t *testing.T) {
			l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid"}
			path := filepath.Join(t.TempDir(), "launch.json")
			var stream codexStream
			var result streamResult
			var pane bytes.Buffer
			for _, line := range []string{
				`{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`,
				`{"type":"turn.started"}`,
				`{"type":"error","message":"` + syntheticCapacityMessage + `"}`,
				`{"type":"error","message":"SENTINEL-PRIVATE-DIAGNOSTIC"}`,
			} {
				if err := stream.line([]byte(line), path, &l, &pane, &result); err != nil {
					t.Fatal("supported diagnostic sequence refused", err)
				}
			}
			if stream.terminal || stream.failed || result.Seen {
				t.Fatal("diagnostic manufactured a terminal result")
			}
			terminal := `{"type":"turn.failed","error":{"message":"` + syntheticCapacityMessage + `"}}`
			want := "provider_capacity"
			if success {
				terminal = `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`
				want = "success"
			}
			if err := stream.line([]byte(terminal), path, &l, &pane, &result); err != nil {
				t.Fatal("actual terminal refused", err)
			}
			if !stream.terminal || !result.Seen || result.Subtype != want || result.IsError == success {
				t.Fatal("actual terminal outcome lost", result.Subtype)
			}
			if strings.Contains(pane.String(), "SENTINEL") || strings.Contains(pane.String(), syntheticCapacityMessage) {
				t.Fatal("provider diagnostic text escaped into pane")
			}
			if err := stream.line([]byte(`{"type":"turn.failed","error":{"message":"SENTINEL"}}`), path, &l, &pane, &result); !errors.Is(err, errSecondCodexTerminal) {
				t.Fatal("a true second final was accepted", err)
			}
		})
	}
}

func TestRunAgentCodexProviderFailureIsNotProtocolRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, notification, failure, subtype string
		exit, wantExit                       int
	}{
		{"capacity-zero-exit", syntheticCapacityMessage, syntheticCapacityMessage, "provider_capacity", 0, 1},
		{"capacity-nonzero-exit", syntheticCapacityMessage, syntheticCapacityMessage, "provider_capacity", 17, 17},
		{"generic", "SENTINEL-PRIVATE-DIAGNOSTIC", "SENTINEL-PRIVATE-FAILURE", "provider_error", 0, 1},
		{"terminal-overrides-earlier-capacity", syntheticCapacityMessage, "SENTINEL-PRIVATE-FAILURE", "provider_error", 0, 1},
		{"near-capacity-is-generic", syntheticCapacityMessage, syntheticCapacityMessage + " SENTINEL", "provider_error", 0, 1},
		{"diagnostic-without-final", syntheticCapacityMessage, "", "native_protocol_refused", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSettings(t, t.TempDir())
			if err := os.MkdirAll(s.WorkDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			final := ""
			if tc.failure != "" {
				final = ` '{"type":"turn.failed","error":{"message":"` + tc.failure + `"}}'`
			}
			cli := fakeCLI(t, dir, `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli `+managedCodexVersion+`'; exit 0; fi
read -r prompt
printf '%s\n' '{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}' '{"type":"turn.started"}' '{"type":"error","message":"`+tc.notification+`"}'`+final+`
exit `+strconv.Itoa(tc.exit)+"\n")
			l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid", Argv: []string{cli, "exec", "--json"}, Dir: dir, Prompt: "synthetic task\n", LogPath: s.LogPath("task"), EventsPath: s.statePath(eventsFile), BootID: "first"}
			path := writeLaunch(t, s, l)
			pane := &syncBuffer{}
			code := RunAgent(path, pane, make(chan os.Signal), 20*time.Millisecond)
			var saved taskResult
			if readJSONFile(s.statePath(resultFile), &saved) != nil || code != tc.wantExit || saved.ExitCode != tc.wantExit || saved.Subtype != tc.subtype || !saved.IsError {
				t.Fatal("provider failure lost truthful nonzero result", code, saved.ExitCode, saved.Subtype)
			}
			for _, forbidden := range []string{"SENTINEL", syntheticCapacityMessage} {
				if strings.Contains(pane.String(), forbidden) {
					t.Fatal("provider failure emitted private text", forbidden)
				}
			}
			if tc.failure == "" {
				if !strings.Contains(pane.String(), "protocol cause: missing_terminal") {
					t.Fatal("diagnostic EOF manufactured a final result")
				}
			} else {
				for _, forbidden := range []string{"protocol refused", "protocol cause:", "SIGTERM"} {
					if strings.Contains(pane.String(), forbidden) {
						t.Fatal("provider failure triggered protocol stop", forbidden)
					}
				}
			}
		})
	}
}

func TestCodexMalformedProviderMessagesRefuseWithoutLeaks(t *testing.T) {
	for _, malformed := range []string{
		`{"type":"error"}`, `{"type":"error","message":null}`, `{"type":"error","message":{"secret":"SENTINEL"}}`,
		`{"type":"turn.failed"}`, `{"type":"turn.failed","error":null}`, `{"type":"turn.failed","error":{"message":null}}`,
		`{"type":"turn.failed","error":{"message":["SENTINEL"]}}`,
	} {
		l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid"}
		path := filepath.Join(t.TempDir(), "launch.json")
		var stream codexStream
		var result streamResult
		if err := stream.line([]byte(`{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`), path, &l, io.Discard, &result); err != nil {
			t.Fatal(err)
		}
		var pane bytes.Buffer
		err := stream.line([]byte(malformed), path, &l, &pane, &result)
		if err == nil || stream.terminal || result.Seen || strings.Contains(err.Error()+pane.String(), "SENTINEL") {
			t.Fatal("malformed provider message accepted or leaked")
		}
	}
}
