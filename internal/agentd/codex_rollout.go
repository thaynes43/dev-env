package agentd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	// maxRolloutBytes bounds one rollout read. A rollout past it is left
	// Unknown: a partial list of served models would read as a complete one.
	maxRolloutBytes = 64 << 20
	// maxRolloutLine bounds one JSONL line the reader will inspect. Longer
	// lines (message bodies, tool output) are skipped unread.
	maxRolloutLine = 64 << 10
)

var rolloutTurnContext = []byte(`"type":"turn_context"`)

// codexHomeOf is the CODEX_HOME the launch gave the CLI: the launch's own
// environment first, then the pod's, then ~/.codex.
func codexHomeOf(l Launch) string {
	for i := len(l.Env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(l.Env[i], "CODEX_HOME="); ok && v != "" {
			return v
		}
	}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return ""
}

// findCodexRollout returns the newest regular rollout file for a native thread
// under <codexHome>/sessions/YYYY/MM/DD. It follows no symlink: a link at any
// level is skipped, so the lookup cannot leave the sessions tree.
func findCodexRollout(codexHome, threadID string) string {
	if codexHome == "" || !nativeThreadID.MatchString(threadID) {
		return ""
	}
	suffix := "-" + threadID + ".jsonl"
	dirs := []string{filepath.Join(codexHome, "sessions")}
	for depth := 0; depth < 3; depth++ {
		var next []string
		for _, d := range dirs {
			entries, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() && e.Type()&os.ModeSymlink == 0 {
					next = append(next, filepath.Join(d, e.Name()))
				}
			}
		}
		dirs = next
	}
	var best string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !e.Type().IsRegular() || !strings.HasPrefix(n, "rollout-") || !strings.HasSuffix(n, suffix) {
				continue
			}
			if p := filepath.Join(d, n); p > best {
				best = p
			}
		}
	}
	return best
}

// codexRolloutModels reads the model ids a rollout records in its turn_context
// records, sorted and distinct. It reads no message text: only the model field
// of a turn_context record is decoded, and every other line is skipped on a
// prefix check. ok is false when the file is unreadable, not a regular file,
// over the read bound, or records a model that is not a valid id.
func codexRolloutModels(path string) (models []string, ok bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() > maxRolloutBytes {
		return nil, false
	}
	r := bufio.NewReaderSize(io.LimitReader(f, maxRolloutBytes+1), maxRolloutLine)
	var read int64
	for {
		line, err := r.ReadSlice('\n')
		read += int64(len(line))
		if errors.Is(err, bufio.ErrBufferFull) {
			// Longer than the line bound: skip the rest of this line unread.
			line = nil
			for errors.Is(err, bufio.ErrBufferFull) {
				var more []byte
				more, err = r.ReadSlice('\n')
				read += int64(len(more))
			}
		}
		if read > maxRolloutBytes || (err != nil && err != io.EOF) {
			return nil, false
		}
		if len(line) > 0 && bytes.Contains(line[:min(len(line), 256)], rolloutTurnContext) {
			var rec struct {
				Type    string `json:"type"`
				Payload struct {
					Model json.RawMessage `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Type == "turn_context" && len(rec.Payload.Model) > 0 && string(rec.Payload.Model) != "null" {
				var m string
				if json.Unmarshal(rec.Payload.Model, &m) != nil || !protocol.ValidUsageModelID(m) {
					return nil, false
				}
				if !slices.Contains(models, m) {
					models = append(models, m)
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	slices.Sort(models)
	return models, len(models) > 0 && len(models) <= protocol.MaxServedModels
}

// applyCodexRolloutModels fills the served models of a Codex observation whose
// exec stream named none, from the thread's rollout. One model takes the turn's
// counters, as the stream path does. Several share no honest split, so each
// keeps Unknown counters. Nothing found leaves the observation Unknown.
func applyCodexRolloutModels(u *protocol.UsageObservation, l Launch) bool {
	if u.ActualModelState == protocol.UsageProviderReported || !l.NativeThreadConfirmed {
		return false
	}
	path := findCodexRollout(codexHomeOf(l), l.ConversationID)
	if path == "" {
		return false
	}
	models, ok := codexRolloutModels(path)
	if !ok {
		return false
	}
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	served := make([]protocol.ModelUsage, 0, len(models))
	for _, m := range models {
		mu := protocol.ModelUsage{Model: m, InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown,
			Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}
		if len(models) == 1 {
			mu.InputTokens, mu.OutputTokens = u.InputTokens, u.OutputTokens
			mu.CacheReadInputTokens, mu.CacheCreationInputTokens = u.CacheReadInputTokens, u.CacheCreationInputTokens
		}
		served = append(served, mu)
	}
	u.ServedModels, u.ActualModelState = served, protocol.UsageProviderReported
	return true
}
