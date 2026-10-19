package agentd

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// launchRestart reads the conversation the next boot resumes (launch.json,
// D-58). It does not stop the agent. launch.json is written by the first
// launch and kept across resumes (a resume writes resume.json beside it), so
// a remote pod that was itself started by resume still has it. A Claude
// launch picks its conversation id before the CLI has written anything, so
// the id proves a conversation only once its transcript file exists and is non-empty: `claude
// --resume` of an id the CLI never wrote fails, and drain keeps that pod.
func launchRestart(s Settings, session string) protocol.RestartReport {
	rep := protocol.RestartReport{Session: session}
	var first Launch
	if readJSONFile(s.statePath(launchFile), &first) != nil || first.ConversationID == "" {
		return rep
	}
	if first.Provider == protocol.AgentCodex {
		if !first.NativeThreadConfirmed || first.Session != session || !nativeThreadID.MatchString(first.ConversationID) {
			return rep
		}
	} else if !transcriptWritten(s, first.ConversationID) {
		return rep
	}
	rep.ConversationID, rep.Resumable = first.ConversationID, true
	return rep
}

// transcriptWritten reports that the CLI has written the conversation's
// transcript: a regular, non-symlink, non-empty file. It reads no content, so
// a huge last line (a large tool result) cannot hide a used session.
func transcriptWritten(s Settings, conversationID string) bool {
	if !conversationIDPattern.MatchString(conversationID) {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(s.ClaudeConfigDir, "projects", "*", conversationID+".jsonl"))
	for _, path := range matches {
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return true
		}
	}
	return false
}

// ReadRestart is `agentd ctl prepare-restart --dry-run` (DESIGN-001 5.2). It
// reads the launch record and the live tmux attach count. It does not stop
// the agent.
func ReadRestart(ctx context.Context, r Runner, s Settings, session string) protocol.RestartReport {
	rep := launchRestart(s, session)
	if r == nil {
		return rep
	}
	n, err := tmuxClientCount(ctx, r, s)
	if err != nil {
		// Nil means the count was not taken. Zero would let drain stop a
		// client it could not see.
		return rep
	}
	rep.Attached = &n
	return rep
}

// PrepareRestart is `agentd ctl prepare-restart` (DESIGN-001 5.2 step 2,
// D-58). It reports what the next boot resumes, then stops the agent CLI as
// the pod's SIGTERM would (SIGTERM, the grace, then SIGKILL to what is left),
// so the CLI flushes its transcript before the pod goes. Drain calls this
// only after ReadRestart's conversation is stored and the live attach count
// is zero. The stop still runs when this report is not resumable, so the
// caller must not use it as the read.
func PrepareRestart(s Settings, session string, grace time.Duration) protocol.RestartReport {
	rep := launchRestart(s, session)
	rep.Agent = stopAgent(s, grace)
	return rep
}
