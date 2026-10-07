package agentd

import (
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// PrepareRestart is `agentd ctl prepare-restart` (DESIGN-001 5.2 step 2,
// D-58). The conversation to resume is already on the volume: the first
// launch records it (launch.json), and every later boot resumes it. So this
// reports what the next boot resumes, then stops the agent CLI as the pod's
// SIGTERM would (SIGTERM, the grace, then SIGKILL to what is left), so the CLI
// flushes its transcript before the pod goes. Plan 04's drain calls it.
func PrepareRestart(s Settings, session string, grace time.Duration) protocol.RestartReport {
	rep := protocol.RestartReport{Session: session}
	var first Launch
	if readJSONFile(s.statePath(launchFile), &first) == nil && first.ConversationID != "" {
		rep.ConversationID, rep.Resumable = first.ConversationID, true
	}
	rep.Agent = stopAgent(s, grace)
	return rep
}
