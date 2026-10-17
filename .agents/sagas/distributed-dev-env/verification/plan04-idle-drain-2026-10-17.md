# Plan 04 idle drain — 2026-10-17

Source only. This change is not deployed, and it was not run against the cluster.
No session pod was restarted. `cmd/dev-env-operator/main.go` was not edited.

DESIGN-001 5.2 (Q-03, Tom 2026-10-06) is implemented in the operator source.
Q-03 is unchanged. An idle, outdated session that is not summoned is marked
`Draining`. The operator execs `agentd ctl prepare-restart --dry-run`, which
reads the launch record and the live tmux attach count and does not stop the
agent. It writes that conversation id onto `status.restart` only from that
read. It reads again, and only when the count is zero and the proof is stored
does it exec `agentd ctl prepare-restart`, which stops the agent, and then
`deletePod`. A read that is not resumable, a missing attach count, a failed
exec, or a proof that was not written does not stop the agent and does not
delete the pod; drain then leaves the idle-suspend timer free. A live attach
count above zero does not stop or delete either. After the pod is gone, the
existing ensure path creates the replacement on the current template revision
and the same volume. agentd's existing resume path is unchanged. `drainPod`
calls `deletePod`; it does not lift a volume finalizer.
`TestOnlyTheGuardDeletes` names `drainPod` the same way it names the other
delete callers.

A session is idle for this drain when its own pod is Ready, the heartbeat says
`idle` or `waiting`, and no tmux client is attached. Busy, unreported, exited,
and attached sessions stay `Running`. Summoned sessions (`spec.caller` or
`spec.lane`) are never drained. Private-history sessions, canary originals,
and pilots stay on the existing retention guard: drain does not mark them
`Draining` and does not delete their pods or volumes. A suspend or a delete
that arrives while the phase is still `Draining` still needs its rescue.

`ExecRescuer` implements `ReadRestart` and `PrepareRestart`. The operator
already passes that value as `Rescuer`, so the running process picks both
commands up without a second wiring in `main.go`.

## Not implemented

DESIGN-001 5.2 step 4 is not sent. A session that stays busy for 72 hours
after going outdated does not get a stopping-point message, and Tom does not
get the later Pushover line. The broker `Notifier` only reports a grant that
is waiting for approval. It does not send either drain notice. No new
Pushover client was added, and drain does not wait on the missing notice.

The existing `prepare-restart` report carries the conversation id, whether
the next boot can resume it, and what the agent stop did. It does not also
return mode, model, effort, or the Remote Control name. Those remain on the
session and on the volume's launch record. Drain requires the conversation
id from that existing report.

`agent-run restart`, image pre-pull, and the rest of plan 04 are outside
this change. Idle suspend still parks a session that is not on the drain
path. While a session is eligible to drain, the suspend timer waits so the
roll is not turned into a park.

## Tests

Run on linux/arm64, one package at a time, `nice -n 19`, `GOMAXPROCS=2`,
`GOFLAGS=-p=1`, `-count=1`. The controller tests import `internal/agentd`,
which does not compile on darwin, so the run was in a Linux container with
`KUBEBUILDER_ASSETS` pointed at envtest 1.35.0. Nothing was applied to the
cluster.

- `go test -count=1 -p=1 -timeout 40m ./internal/controller/` — ok, 74.624s.
  That includes the drain cases: a current Running pod is not deleted; a
  busy outdated pod is not marked Draining and is not deleted; a summoned
  session is not drained; a Draining pod without prepare-restart proof is
  not deleted; an idle outdated session with proof may lose the pod while
  the volume finalizer stays and the replacement uses the same claim; private
  history, canary, and pilot sessions are not drained. `TestOnlyTheGuardDeletes`
  and `TestPodGuardTable` passed in the same package run.
- `go test -count=1 -p=1 -timeout 40m ./internal/apiserver/` — ok, 35.440s.
  The heartbeat now stores the attached-client count the idle check reads.
