package agentd

import (
	"context"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// launchRestart reads the conversation the next boot resumes (launch.json,
// D-58). It does not stop the agent.
func launchRestart(s Settings, session string) protocol.RestartReport {
	rep := protocol.RestartReport{Session: session}
	var first Launch
	if readJSONFile(s.statePath(launchFile), &first) == nil && first.ConversationID != "" &&
		(first.Provider != protocol.AgentCodex || first.NativeThreadConfirmed && first.Session == session && nativeThreadID.MatchString(first.ConversationID)) {
		rep.ConversationID, rep.Resumable = first.ConversationID, true
	}
	return rep
}

// ReadRestart is `agentd ctl prepare-restart --dry-run` (DESIGN-001 5.2). It
// reads the launch record and the live tmux attach count. It does not stop
// the agent.
func ReadRestart(ctx context.Context, r Runner, s Settings, session string) protocol.RestartReport {
	rep := launchRestart(s, session)
	if r == nil {
		return rep
	}
	n := tmuxClients(ctx, r, s)
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
