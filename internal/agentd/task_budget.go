package agentd

import (
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// armTaskBudgetDeadline runs before any boot side effect. Exiting agentd makes
// tini exit and the container runtime terminate the entire PID namespace. The
// controller still requires fresh all-container/Node/Lease and rescue proof;
// the exit request itself never releases a writer or records stopped.
func armTaskBudgetDeadline(raw, sessionUID, podUID string, now time.Time, after func(time.Duration, func()) func(), exit func(int)) (func(), error) {
	b, err := protocol.ParseTaskBudgetDeadline(raw, sessionUID, podUID, now)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return func() {}, nil
	}
	return after(b.Deadline.Sub(now), func() { exit(124) }), nil
}

func (d *Daemon) armTaskBudgetDeadline() (func(), error) {
	return armTaskBudgetDeadline(os.Getenv(protocol.TaskBudgetEnv), d.Session.SessionUID, d.S.PodUID, d.now(), func(wait time.Duration, f func()) func() {
		t := time.AfterFunc(wait, f)
		return func() { t.Stop() }
	}, os.Exit)
}
