package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// A scheduled session's start deadline bounds its first launch (DESIGN-054,
// "Start deadline, enforced in the pod"). It holds without the executor: a pod
// that was unschedulable, or pulled its image, until past the deadline starts
// no work. A session that launched in time is never refused on a later boot
// (a drain, a preemption, a resume), because the deadline applies only while
// the session has no first-launch record.

// startDeadlineRefusal says why a first launch must not happen, or "" when it
// may. raw is the pod's start deadline, empty when the session has none;
// launched is whether the session has a first-launch record. A value that does
// not parse refuses: the controller passes only values it validated, so
// anything else is not a deadline agentd can honour.
func startDeadlineRefusal(raw string, launched bool, now time.Time) string {
	if raw == "" || launched {
		return ""
	}
	deadline, err := protocol.ParseStartDeadline(raw)
	if err != nil {
		return protocol.StartDeadlineInvalid
	}
	if !now.Before(deadline) {
		return protocol.StartDeadlinePassed
	}
	return ""
}

// firstLaunchRefusal is the start-deadline check of a boot, or of a remote
// agent the boot held, before the session's first launch. A Codex remote
// session is exempt, as the controller exempts it by refusing the annotation:
// its agent is the phone daemon and agentd keeps no launch record for it, so
// the check could not tell a late first boot from a later one, and a value
// that reached the pod some other way would refuse every boot.
func (d *Daemon) firstLaunchRefusal() string {
	return firstLaunchRefusal(d.S, d.Session, d.now())
}

// firstLaunchRefusal is the same check for a process other than the daemon:
// agentd ctl login url opens no login window for a session that cannot
// launch, so a ceremony requested after a refused held launch leaves nothing
// behind (remote.go, LoginURL).
func firstLaunchRefusal(s Settings, sess protocol.Session, now time.Time) string {
	if codexRemoteSession(sess) {
		return ""
	}
	return startDeadlineRefusal(s.StartDeadline, firstLaunchRecorded(s), now)
}

// firstLaunchRecorded reports whether the session has a first-launch record
// (launch.json, which StartAgent writes before the agent starts). Only its
// proven absence counts as never launched. A record agentd cannot stat counts
// as launched, so the deadline never refuses a session that may have started;
// startAgent then fails an unreadable record closed, without replaying the
// prompt.
func firstLaunchRecorded(s Settings) bool {
	_, err := os.Lstat(s.statePath(launchFile))
	return !errors.Is(err, os.ErrNotExist)
}

// refuseFirstLaunch reports a refused first launch and supervises without an
// agent. Run calls it before any other boot side effect: no config is
// rendered, no repo cloned, no credential projected and no agent started. It
// does not exit, so the pod does not crash-loop. The boot record says boot
// failed (complete, not booting) and the agent failed with why, so the
// heartbeat reports an ended agent and the session-end rule sees it once the
// pod-age proof holds. The daemon stays observational, as after a refused
// workspace admission. The record names no daemon process, so no side binary
// starts a continuation under it.
func (d *Daemon) refuseFirstLaunch(ctx context.Context, why string) error {
	d.writerRefused = true
	d.Log.Warn("first launch refused", "session", d.Session.Name, "reason", why)
	if err := os.MkdirAll(d.S.StateDir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	rec := bootRecord{
		BootID: newBootID(), BootedAt: d.now().UTC(),
		Boot: protocol.BootFailed, AgentError: why,
		Steps: []Step{newStep("start-deadline", nil, errors.New(why))},
	}
	if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
		return fmt.Errorf("refused boot record: %w", err)
	}
	d.beat(ctx)
	return d.supervise(ctx)
}
