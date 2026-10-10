# Requirement: stop stalled work and ask for help

Owner ruling, 2026-10-09 America/New_York, Q-23:

> 60 minutes without progress or 3 failed attempts at the same blocker

The first limit reached stops further attempts. Changing agents, pods or
conversations does not reset the task's counters. This requirement follows a
storage investigation that consumed roughly seven hours without resolving its
blocker or presenting an available alternative. It is required for the first
owner test. [Implementation issue #154](https://github.com/thaynes43/dev-env/issues/154).

## Contract

1. Give each logical task a success condition, an explicit overall work budget
   and a next checkpoint. Persist elapsed effort, last evidenced progress,
   blocker identity, failed attempts and outstanding escalation. Child work
   consumes the parent's budget; a replacement worker inherits the history.
2. Evidence must advance the success condition. Heartbeats, more tool calls,
   another script or a restated hypothesis alone do not establish progress.
   Attempts using different techniques still count when they fail at the same
   unresolved blocker. Checkpoints prevent useful intermediate evidence from
   becoming permission for an unlimited investigation.
3. At the accepted limit, block new model work, retries and child dispatch.
   Save evidence and WIP, then establish that the executor actually stopped
   under the existing ownership/rescue rules. Bounded cleanup may finish; it
   must not restart the investigation. Missing Pods, free locks and stale
   heartbeats are not termination proof. Preserve an uncertain owner.
4. The direct parent escalates with ONE actionable native owner question:
   blocker, effort spent, attempts, evidence, recommended alternative and the
   bounded cost of the next step. Confirm delivery on the owner's phone and
   persist the answer. Prose, an issue or a design entry alone is not delivery.
5. Retain one outstanding escalation per task. No model polling while waiting,
   worker relaunch, automatic model/provider switch or silent budget extension.
   No response or failed notification leaves work stopped and the delivery
   problem visible. An extension needs a recorded owner decision and bounded
   next step; workers cannot erase the prior history.
6. Enforce limits through the existing deterministic API/controllers/supervisor,
   not another reasoning agent repeatedly spending usage to monitor usage.
   Use provider usage counters when available; label estimates and missing
   values. Missing accounting cannot disable time and attempt limits.
7. Operational help requests do not confer privileged access grants. Stopping
   one task must preserve unrelated work, permanent projects, shared files,
   private enrollment/history and busy remote daemons.

## Acceptance

Use fake clocks and bounded fixtures to prove:

- The stall limit trips while heartbeats and repeated tool calls continue.
- Three failed attempts at the same blocker produce one escalation, including
  attempts across children and replacement agents.
- Resume, pod replacement, worker reassignment and parallel children preserve
  counters, ownership and the pending question.
- Stop/rescue retains WIP; uncertain stop proof prevents another writer.
- One question reaches the owner's phone. Its answer authorizes the recorded
  next step while retaining the task history.
- Missing answers, notification failure, controller restart and missing usage
  data do not reset limits or trigger repeated model calls.

Existing optional timeout/max-turn fields and saved child-decision source do
not implement this contract. Runtime enforcement and phone acceptance remain
open. No CPU burners, stress tools, busy loops or wide repeated tests on shared
nodes. Use fake timers and CPU limits on any cluster fixture.
