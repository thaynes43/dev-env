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
		return capture, ErrCapture
	}
	if cmd.Start() != nil {
		_ = out.Close()
		return capture, ErrCapture
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
		clean := stopOwnedGroup(cmd)
		close(finished)
		<-watcher
		if !clean {
			capture, retErr = providerallowance.ClaudeUsageCapture{}, ErrCleanup
		} else if retErr == nil && !cmd.ProcessState.Success() {
			capture, retErr = providerallowance.ClaudeUsageCapture{}, ErrCapture
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
		if count > 64 || total > 512<<10 || done || !providerallowance.ValidJSONDocument(raw, 64<<10) || !providerallowance.CanonicalFieldNames(raw, "type", "subtype", "is_error", "num_turns", "usage", "total_cost_usd", "usage_report") {
			return capture, ErrCapture
		}
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return capture, ErrCapture
		}
		switch event.Type {
		case "system":
			if event.Subtype != "init" || seen {
				return capture, ErrCapture
			}
		case "assistant":
			if seen {
				return capture, ErrCapture
			}
			var err error
			capture, err = providerallowance.SanitizeClaudeUsageReport(raw, now())
			if err != nil {
				return capture, ErrCapture
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
			if !seen || json.Unmarshal(raw, &result) != nil || result.Subtype != "success" || result.Error == nil || *result.Error || result.Turns == nil || *result.Turns != 0 || result.Cost == nil || !zeroJSON([]byte(*result.Cost)) || len(result.Usage) == 0 {
				return capture, ErrCapture
			}
			// Every token counter, including cache counters, must explicitly be
			// numeric zero. Other result usage metadata is not quota evidence.
			for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
				if !zeroJSON(result.Usage[key]) {
					return capture, ErrCapture
				}
			}
			for key, value := range result.Usage {
				if bytes.Contains([]byte(key), []byte("tokens")) && !zeroJSON(value) {
					return capture, ErrCapture
				}
			}
			done = true
		default:
			return capture, ErrCapture
		}
	}
	if scanner.Err() != nil || !done {
		return capture, ErrCapture
	}
	return capture, nil
}

func zeroJSON(raw []byte) bool {
	var number json.Number
	if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &number) != nil {
		return false
	}
	value, ok := new(big.Rat).SetString(string(number))
	return ok && value.Sign() == 0
}
