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
		if !providerallowance.ValidJSONDocument(raw, 64<<10) || !providerallowance.CanonicalFieldNames(raw, "type", "subtype", "is_error", "num_turns", "usage", "total_cost_usd", "usage_report") {
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
			// Every token counter, including cache counters, must explicitly be
			// numeric zero. Other result usage metadata is not quota evidence.
			for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
				if !zeroJSON(result.Usage[key]) {
					return capture, refused("builtin_required_tokens_unqualified", ErrCapture)
				}
			}
			for key, value := range result.Usage {
				if bytes.Contains([]byte(key), []byte("tokens")) && !zeroJSON(value) {
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

// Nested breakdowns cannot contradict the required zero aggregate counters.
// This only adds refusals: the existing top-level token-field check above still
// rejects unqualified object-valued token fields, including all-zero objects.
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
