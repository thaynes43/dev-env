//go:build linux

package usagecollector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// collectClaudeBuiltin is version-qualified by its caller. It runs exactly the
// normal local /usage command and accepts one synthetic usage envelope followed
// by a successful zero-turn, zero-token result. No raw stream is retained.
func collectClaudeBuiltin(ctx context.Context, executable string, now func() time.Time) (capture providerallowance.ClaudeUsageCapture, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--print", "--output-format", "stream-json", "--verbose", "--setting-sources", "", "--settings", "{}", "--tools", "", "--max-turns", "1", "--no-session-persistence", "/usage")
	cmd.Env = normalClaudeEnv()
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return capture, refusedWithContext(ctx, "command_start_failed")
	}
	if cmd.Start() != nil {
		_ = out.Close()
		return capture, refusedWithContext(ctx, "command_start_failed")
	}
	finished, watcher := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-ctx.Done():
			_ = out.Close()
		case <-finished:
		}
	}()
	defer func() {
		_ = out.Close()
		if retErr == nil && waitOwnedExit(ctx, cmd) != nil {
			capture, retErr = providerallowance.ClaudeUsageCapture{}, refused("command_wait_failed", ErrCapture)
		}
		clean := stopOwnedGroup(cmd)
		close(finished)
		<-watcher
		if !clean {
			capture, retErr = providerallowance.ClaudeUsageCapture{}, ErrCleanup
		} else if ctx.Err() != nil {
			capture, retErr = providerallowance.ClaudeUsageCapture{}, refusedWithContext(ctx, "capture_refused")
		} else if retErr == nil && !cmd.ProcessState.Success() {
			capture, retErr = providerallowance.ClaudeUsageCapture{}, refused("command_failed", ErrCapture)
		}
	}()
	return readClaudeBuiltin(out, now)
}

func readClaudeBuiltin(out io.Reader, now func() time.Time) (providerallowance.ClaudeUsageCapture, error) {
	var capture providerallowance.ClaudeUsageCapture
	scanner := bufio.NewScanner(io.LimitReader(out, (512<<10)+1))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	count, total, seen, done := 0, 0, false, false
	for scanner.Scan() {
		raw := scanner.Bytes()
		count++
		total += len(raw) + 1
		if count > 64 || total > 512<<10 {
			return capture, refused("command_output_oversized", ErrCapture)
		}
		if done {
			return capture, refused("builtin_extra_event", ErrCapture)
		}
		if !providerallowance.ValidJSONDocument(raw, 64<<10) || !providerallowance.CanonicalFieldNames(raw, "type", "subtype", "is_error", "num_turns", "usage", "total_cost_usd", "usage_report", "model_usage_since_last_result") {
			return capture, refused("builtin_event_invalid", ErrCapture)
		}
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return capture, refused("builtin_event_invalid", ErrCapture)
		}
		switch event.Type {
		case "system":
			if event.Subtype != "init" || seen {
				return capture, refused("builtin_system_unqualified", ErrCapture)
			}
		case "assistant":
			if seen {
				return capture, refused("builtin_report_repeated", ErrCapture)
			}
			var err error
			capture, err = providerallowance.SanitizeClaudeUsageReport(raw, now())
			if err != nil {
				return capture, refused("builtin_report_unqualified", ErrCapture)
			}
			seen = true
		case "result":
			var result struct {
				Subtype string                     `json:"subtype"`
				Error   *bool                      `json:"is_error"`
				Turns   *int                       `json:"num_turns"`
				Cost    *json.Number               `json:"total_cost_usd"`
				Usage   map[string]json.RawMessage `json:"usage"`
				// Added in 2.1.312: per-model usage since the last result. A
				// zero-turn /usage makes no model call, so it must be absent,
				// null or an empty object; anything else contradicts zero turns.
				ModelUsage json.RawMessage `json:"model_usage_since_last_result"`
			}
			if !seen {
				return capture, refused("builtin_report_missing", ErrCapture)
			}
			if json.Unmarshal(raw, &result) != nil || result.Error == nil {
				return capture, refused("builtin_result_unqualified", ErrCapture)
			}
			if *result.Error {
				return capture, refused("builtin_result_error", ErrCapture)
			}
			if result.Subtype != "success" || result.Cost == nil || len(result.Usage) == 0 {
				return capture, refused("builtin_result_unqualified", ErrCapture)
			}
			if result.Turns == nil || *result.Turns != 0 {
				return capture, refused("builtin_turns_unqualified", ErrCapture)
			}
			if !zeroJSON([]byte(*result.Cost)) {
				return capture, refused("builtin_cost_unqualified", ErrCapture)
			}
			if len(result.ModelUsage) != 0 && !emptyJSONObjectOrNull(result.ModelUsage) {
				return capture, refused("builtin_model_usage_unqualified", ErrCapture)
			}
			// Every token counter, including cache counters, must explicitly be
			// numeric zero. Other result usage metadata is not quota evidence.
			for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
				if !zeroJSON(result.Usage[key]) {
					return capture, refused("builtin_required_tokens_unqualified", ErrCapture)
				}
			}
			for key, value := range result.Usage {
				if strings.Contains(strings.ToLower(key), "tokens") && !zeroClaudeTokenField(key, value) {
					return capture, refused("builtin_extra_tokens_unqualified", ErrCapture)
				}
				if !zeroNestedTokenCounters(value) {
					return capture, refused("builtin_nested_tokens_unqualified", ErrCapture)
				}
			}
			done = true
		default:
			return capture, refused("builtin_event_unsupported", ErrCapture)
		}
	}
	if scanner.Err() != nil {
		return capture, refused("builtin_stream_failed", ErrCapture)
	}
	if !done {
		return capture, refused("builtin_incomplete", ErrCapture)
	}
	return capture, nil
}

// The pinned 2.1.296 and 2.1.312 zero-usage constructors include exactly this
// counter object (byte-identical in both public packages).
// Require its explicit numeric zero; missing/null/extra fields are not zero.
// Other token-bearing objects have no qualified shape and still refuse.
func zeroClaudeTokenField(key string, raw json.RawMessage) bool {
	if key != "output_tokens_details" {
		return zeroJSON(raw)
	}
	if !providerallowance.ExactObjectKeys(raw, []string{"thinking_tokens"}) {
		return false
	}
	var detail struct {
		Thinking json.RawMessage `json:"thinking_tokens"`
	}
	return json.Unmarshal(raw, &detail) == nil && zeroJSON(detail.Thinking)
}

// Nested breakdowns cannot contradict the required zero aggregate counters.
// This walk only adds refusals; qualified top-level objects are checked above.
func zeroNestedTokenCounters(raw json.RawMessage) bool {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return false
	}
	pending := []any{root}
	for len(pending) > 0 {
		value := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if strings.Contains(strings.ToLower(key), "tokens") {
					number, ok := child.(json.Number)
					if !ok || !zeroJSON([]byte(number)) {
						return false
					}
				}
				pending = append(pending, child)
			}
		case []any:
			pending = append(pending, value...)
		}
	}
	return true
}

func zeroJSON(raw []byte) bool {
	var number json.Number
	if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &number) != nil {
		return false
	}
	value, ok := new(big.Rat).SetString(string(number))
	return ok && value.Sign() == 0
}

// emptyJSONObjectOrNull accepts only JSON null or an object with no members.
func emptyJSONObjectOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(trimmed, &object) == nil && object != nil && len(object) == 0
}
