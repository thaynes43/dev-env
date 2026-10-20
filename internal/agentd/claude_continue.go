package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The 401-window continue (plan 03, Story110 T09 increment 2). When the
// keeper's access token is revoked or lapses before agentd merges the next
// generation, the TUI's last turn ends in an authentication error and the CLI
// sits idle. Once a NEW generation has been merged, agentd sends that session
// exactly one `continue` through Deliver, so the work resumes on the new token.
//
// Signal: the last conversational entry of the session's own transcript
// (projects/*/<conversation id>.jsonl under the Claude config dir). agentd
// holds no turn record of its own for a TUI, so the transcript is the only
// source. The CLI's exact error text is Unknown until the real checkpoint:
// the match below is for the shape a synthetic fixture uses (an assistant
// entry flagged isApiErrorMessage, or text starting "API Error", naming a
// revoked or expired OAuth token) and is the one thing to confirm there.
//
// Never twice: claude-continue.json on the volume records the generation last
// evaluated. It is written before the send, so a crash or restart after the
// send does not repeat it; only "agent not running yet" (nothing sent) leaves
// no new receipt, and a failure before the paste, are retried. A last turn that
// is a user turn is not decided either: the request may still be on the old token.

const (
	continueReceiptFile = "claude-continue.json"
	// transcriptTailBytes bounds how much of the transcript's end is read.
	transcriptTailBytes = 256 << 10
	// continueFrom is the sender Deliver names; it is not a session, so no reply hint.
	continueFrom = "agentd/claude-login"
	continueText = "The keeper renewed this session's Claude login after your last turn failed with a revoked or expired token error. Nothing you did caused it. Please continue the work you were doing."
)

var conversationIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// authErrorMarkers are lower-case fragments of the revoked/expired-token error.
var authErrorMarkers = []string{"oauth token has been revoked", "oauth token has expired", "authentication_error", "please run /login"}

// authErrorCode is the transcript entry's top-level error code for a 401.
const authErrorCode = "authentication_failed"

type continueReceipt struct {
	Generation  uint64    `json:"generation"`
	Outcome     string    `json:"outcome"` // sending, sent or skipped
	EvaluatedAt time.Time `json:"evaluatedAt"`
}

// lastTurn is the transcript's last conversational entry.
type lastTurn struct {
	Revoked bool
	// User: the last entry is a user turn, a request that may still be in flight.
	User bool
	At   time.Time
}

type transcriptEntry struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsAPIError  bool   `json:"isApiErrorMessage"`
	// APIErrorStatus and Error are the CLI's top-level fields on an API error
	// entry. Raw, so a field of an unexpected type never fails the whole line.
	APIErrorStatus json.RawMessage `json:"apiErrorStatus"`
	Error          json.RawMessage `json:"error"`
	Timestamp      string          `json:"timestamp"`
	Message        json.RawMessage `json:"message"`
}

// entryText joins the text blocks of a message.
func entryText(msg json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// classifyTranscript scans the tail from the end for the last user or
// assistant entry outside a sidechain.
func classifyTranscript(tail []byte) lastTurn {
	lines := bytes.Split(tail, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal(line, &e) != nil || e.IsSidechain || (e.Type != "user" && e.Type != "assistant") {
			continue
		}
		t := lastTurn{User: e.Type == "user"}
		t.At, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
		if e.Type == "assistant" {
			text := strings.ToLower(entryText(e.Message))
			if e.IsAPIError || strings.HasPrefix(strings.TrimSpace(text), "api error") {
				if string(e.APIErrorStatus) == "401" || string(e.Error) == `"`+authErrorCode+`"` {
					t.Revoked = true
				}
				for _, m := range authErrorMarkers {
					if strings.Contains(text, m) {
						t.Revoked = true
					}
				}
			}
		}
		return t
	}
	return lastTurn{}
}

// readTranscriptTail returns the last transcriptTailBytes of the session's
// transcript, a cut first line dropped; false when there is no transcript.
func readTranscriptTail(s Settings, conversationID string) ([]byte, bool) {
	if !conversationIDPattern.MatchString(conversationID) {
		return nil, false
	}
	matches, _ := filepath.Glob(filepath.Join(s.ClaudeConfigDir, "projects", "*", conversationID+".jsonl"))
	for _, path := range matches {
		if raw, ok := readTail(path); ok {
			return raw, true
		}
	}
	return nil, false
}

func readTail(path string) ([]byte, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	cut := fi.Size() > transcriptTailBytes
	if cut {
		if _, err := f.Seek(fi.Size()-transcriptTailBytes, io.SeekStart); err != nil {
			return nil, false
		}
	}
	raw, err := io.ReadAll(io.LimitReader(f, transcriptTailBytes))
	if err != nil {
		return nil, false
	}
	if cut {
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			return nil, false
		}
		raw = raw[i+1:]
	}
	return raw, true
}

// continueAfterRenewal runs on the poll after the projection sync. It acts
// once per applied generation, and only when the last sync applied cleanly (a
// held-back or refused merge never gets here), the session is a Claude
// remote/local TUI and the transcript's last turn ended in the auth error.
func (d *Daemon) continueAfterRenewal(ctx context.Context) {
	if !d.projectsClaude() || d.claudeSyncErr != nil || d.Session.Mode == protocol.ModeTask {
		return
	}
	var applied claudeReceipt
	if readJSONFile(d.S.statePath("claude-access-generation.json"), &applied) != nil || applied.Generation == 0 {
		return
	}
	receiptPath := d.S.statePath(continueReceiptFile)
	var prev continueReceipt
	havePrev := false
	if err := readJSONFile(receiptPath, &prev); err == nil {
		if prev.Generation >= applied.Generation {
			return
		}
		havePrev = true
	} else if !isNotExist(err) {
		// An unreadable receipt could hide a send already made: do nothing.
		return
	}
	// Nothing is decided until the agent runs: Deliver would refuse anyway, and
	// no receipt for this generation means the next poll looks again.
	l, ok := currentLaunch(d.S)
	var p agentPid
	if !ok || !l.TUI || readJSONFile(d.S.statePath(pidFile), &p) != nil || !pidAlive(p) {
		return
	}
	now := d.now()
	var turn lastTurn
	if tail, ok := readTranscriptTail(d.S, l.ConversationID); ok {
		turn = classifyTranscript(tail)
	}
	// A user turn last may be a request still in flight on the old token, whose
	// 401 has not been written yet: decide nothing, look again next poll.
	if turn.User {
		return
	}
	// An error older than the previous evaluation belongs to a generation that
	// was already handled. A missing timestamp does not block the one send.
	stale := havePrev && !turn.At.IsZero() && !turn.At.After(prev.EvaluatedAt)
	if !turn.Revoked || stale {
		_ = writeJSONFile(receiptPath, continueReceipt{applied.Generation, "skipped", now})
		return
	}
	if writeJSONFile(receiptPath, continueReceipt{applied.Generation, "sending", now}) != nil {
		return
	}
	err := Deliver(ctx, d.R, d.S, d.Session, continueFrom, continueText)
	switch {
	case err == nil:
		_ = writeJSONFile(receiptPath, continueReceipt{applied.Generation, "sent", now})
		d.Log.Info("Claude session continued after the login was renewed")
	case errors.Is(err, ErrNotAddressable), errors.Is(err, errNotSent):
		// Nothing reached the pane: put the earlier receipt back and look again next poll.
		if havePrev {
			_ = writeJSONFile(receiptPath, prev)
		} else {
			_ = os.Remove(receiptPath)
		}
	default:
		// The receipt stays "sending": a send that may have happened is not repeated.
		d.Log.Warn("Claude continue after renewal was not confirmed and will not be repeated")
	}
}
