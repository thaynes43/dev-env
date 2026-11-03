//go:build linux

package usagecollector

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireBuiltinReason(t *testing.T, provider string, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrCapture) || err.Error() != ErrCapture.Error() {
		t.Fatal("builtin refusal changed its public error contract", err)
	}
	var out bytes.Buffer
	emitAcquisitionDiagnostic(&out, provider, atAcquisitionStage("builtin", err))
	got := DecodeAcquisitionDiagnostics(out.Bytes())
	if len(got) != 1 || got[0].Reason != want || got[0].Stage != "builtin" || got[0].ClientVersion != "" {
		t.Fatalf("closed builtin reason: want %s, got %v", want, got)
	}
	if strings.Contains(out.String(), "PRIVATE-SENTINEL") {
		t.Fatal("builtin diagnostic copied vendor data")
	}
}

func TestClaudeBuiltinClosedReasonsPreserveRefusals(t *testing.T) {
	quota, result, _ := strings.Cut(strings.TrimSuffix(finiteClaudeStream, "\n"), "\n")
	for _, test := range []struct{ name, stream, reason string }{
		{"empty", "", "builtin_incomplete"},
		{"malformed", "PRIVATE-SENTINEL\n", "builtin_event_invalid"},
		{"unsupported", "{\"type\":\"PRIVATE-SENTINEL\"}\n", "builtin_event_unsupported"},
		{"system", "{\"type\":\"system\",\"subtype\":\"PRIVATE-SENTINEL\"}\n", "builtin_system_unqualified"},
		{"report", "{\"type\":\"assistant\",\"content\":\"PRIVATE-SENTINEL\"}\n", "builtin_report_unqualified"},
		{"repeated", quota + "\n" + quota + "\n", "builtin_report_repeated"},
		{"missing-report", result + "\n", "builtin_report_missing"},
		{"error", quota + "\n" + strings.Replace(result, `"is_error":false`, `"is_error":true`, 1) + "\n", "builtin_result_error"},
		{"turns", quota + "\n" + strings.Replace(result, `"num_turns":0`, `"num_turns":1`, 1) + "\n", "builtin_turns_unqualified"},
		{"cost", quota + "\n" + strings.Replace(result, `"total_cost_usd":0`, `"total_cost_usd":1`, 1) + "\n", "builtin_cost_unqualified"},
		{"tokens", quota + "\n" + strings.Replace(result, `"input_tokens":0`, `"input_tokens":1`, 1) + "\n", "builtin_required_tokens_unqualified"},
		{"extra", finiteClaudeStream + quota + "\n", "builtin_extra_event"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readClaudeBuiltin(strings.NewReader(test.stream), time.Now)
			requireBuiltinReason(t, "anthropic", err, test.reason)
		})
	}
}

func TestCursorBuiltinClosedReasonsPreserveInputFences(t *testing.T) {
	for _, test := range []struct {
		name, frame, reason string
		state               int
	}{
		{"terminal", "\x1b?PRIVATE-SENTINEL", "builtin_terminal_escape_unqualified", 0},
		{"prompt", "Sign in PRIVATE-SENTINEL\n", "builtin_prompt_refused", 0},
		{"workspace", "Workspace Trust Required\n/PRIVATE-SENTINEL\n[a] Trust this workspace\n[q] Quit\n", "builtin_workspace_unqualified", 0},
		{"input", cursorReady("/private/empty") + "PRIVATE-SENTINEL", "builtin_input_unqualified", 1},
		{"display", strings.Replace(cursorScreen, "Included 61% used", "Included PRIVATE-SENTINEL", 1), "builtin_display_unqualified", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := cursorProtocol{workspace: "/private/empty", state: test.state}
			input, report, err := feedCursorFrame(&p, test.frame)
			if input != "" || report != nil {
				t.Fatal("refusal authorized input or a capture")
			}
			requireBuiltinReason(t, "cursor", err, test.reason)
		})
	}
}

func TestCursorOwnedProcessKeepsProtocolDiagnostic(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "synthetic-cursor")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '\\033?PRIVATE-SENTINEL'\n/bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := CollectCursor(ctx, CursorOptions{Executable: executable, Workspace: workspace}, time.Now)
	requireBuiltinReason(t, "cursor", err, "builtin_terminal_escape_unqualified")
}

func TestClaudeBuiltinNestedTokenCountersCannotContradictZeroTotals(t *testing.T) {
	for _, detail := range []string{
		`{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":0}`,
		`{"ephemeral_5m_input_tokens":"0"}`,
		`{"nested":[{"input_tokens":1}]}`,
	} {
		stream := strings.Replace(finiteClaudeStream, `"usage":{`, `"usage":{"cache_creation":`+detail+`,`, 1)
		_, err := readClaudeBuiltin(strings.NewReader(stream), time.Now)
		if err == nil {
			t.Fatal("nested token counters escaped the zero-token guard")
		}
		requireBuiltinReason(t, "anthropic", err, "builtin_nested_tokens_unqualified")
	}
	zero := strings.Replace(finiteClaudeStream, `"usage":{`, `"usage":{"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},`, 1)
	if _, err := readClaudeBuiltin(strings.NewReader(zero), time.Now); err != nil {
		t.Fatal("a previously accepted all-zero breakdown was refused", err)
	}
	// An object-valued token field has no pinned-protocol qualification. Do not
	// turn the separate conditional audit finding into new accepted input.
	unknown := strings.Replace(finiteClaudeStream, `"usage":{`, `"usage":{"output_tokens_details":{"thinking_tokens":0},`, 1)
	if _, err := readClaudeBuiltin(strings.NewReader(unknown), time.Now); err == nil {
		t.Fatal("unqualified token-bearing object became accepted")
	}
}

func TestCursorBuiltinContextFailureDoesNotBecomeTerminalFault(t *testing.T) {
	testBuiltinContextFailure(t, "cursor", func(ctx context.Context, executable, workspace string) error {
		_, err := CollectCursor(ctx, CursorOptions{Executable: executable, Workspace: workspace}, time.Now)
		return err
	})
}

func TestClaudeBuiltinContextFailureDoesNotBecomeStreamFault(t *testing.T) {
	testBuiltinContextFailure(t, "anthropic", func(ctx context.Context, executable, _ string) error {
		_, err := collectClaudeBuiltin(ctx, executable, time.Now)
		return err
	})
}

func testBuiltinContextFailure(t *testing.T, provider string, read func(context.Context, string, string) error) {
	t.Helper()
	for _, reason := range []string{"deadline", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			dir := t.TempDir()
			workspace := filepath.Join(dir, "workspace")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(dir, "idle-cursor")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0700); err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if reason == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				timer := time.AfterFunc(50*time.Millisecond, cancel)
				defer timer.Stop()
			}
			defer cancel()
			requireBuiltinReason(t, provider, read(ctx, executable, workspace), reason)
		})
	}
}

func TestScheduledFailureBranchesHaveClosedSubreasons(t *testing.T) {
	for _, test := range []struct{ name, field, reason string }{
		{"required", `"input_tokens":1,`, "builtin_required_tokens_unqualified"},
		{"extra", `"PRIVATE-SENTINEL_tokens":{},`, "builtin_extra_tokens_unqualified"},
		{"nested", `"PRIVATE-SENTINEL":{"input_tokens":1},`, "builtin_nested_tokens_unqualified"},
	} {
		t.Run("claude-"+test.name, func(t *testing.T) {
			stream := strings.Replace(finiteClaudeStream, `"usage":{`, `"usage":{`+test.field, 1)
			if test.name == "required" {
				stream = strings.Replace(finiteClaudeStream, `"input_tokens":0,`, test.field, 1)
			}
			_, err := readClaudeBuiltin(strings.NewReader(stream), time.Now)
			requireBuiltinReason(t, "anthropic", err, test.reason)
		})
	}
	for _, test := range []struct{ name, frame, reason string }{
		{"escape", "\x1b?PRIVATE-SENTINEL", "builtin_terminal_escape_unqualified"},
		{"mode", "\x1b[?9999hPRIVATE-SENTINEL", "builtin_terminal_mode_unqualified"},
		{"params", "\x1b[99999APRIVATE-SENTINEL", "builtin_terminal_params_unqualified"},
		{"operation", "\x1b[1XPRIVATE-SENTINEL", "builtin_terminal_operation_unqualified"},
		{"rune", "☃PRIVATE-SENTINEL", "builtin_terminal_rune_unqualified"},
		{"control", "\x00PRIVATE-SENTINEL", "builtin_terminal_control_unqualified"},
		{"bounds", strings.Repeat("x", 181) + "PRIVATE-SENTINEL", "builtin_terminal_bounds_unqualified"},
	} {
		t.Run("cursor-"+test.name, func(t *testing.T) {
			p := cursorProtocol{}
			input, report, err := p.accept([]byte(test.frame))
			if input != "" || report != nil {
				t.Fatal("terminal failure authorized input/capture")
			}
			requireBuiltinReason(t, "cursor", err, test.reason)
		})
	}
}
