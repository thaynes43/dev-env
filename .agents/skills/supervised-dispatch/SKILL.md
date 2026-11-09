---
name: supervised-dispatch
description: Hand long-running work to a coordinator and supervise it live until a demonstrated outcome or a recorded stop (a cancellation or pause from Tom or his verified coordinator, an exhausted authorized budget, a genuine unresolvable blocker, or a required safety or permission decision). Use when the work needs more than one session turn, needs workers, or must keep going while you steer. Skip for a lookup, a single bounded edit you can finish yourself, or any task you cannot give an acceptance check.
---

# Supervised dispatch

Tom's direction (2026-11-04, as relayed by the coordinator): "You should just be
reviewing what the agents are doing and steering live, not fire and forget sets
of instructions that do not have goals the agents have to demonstrate to stop
working."

This skill is for the session that hands work to a coordinator and then supervises
it. The coordinator runs the build loop with [worker-qa-loop](../worker-qa-loop/SKILL.md).
Both check in on a timer with [timer-checkin](../timer-checkin/SKILL.md). This skill
grants no scope, access or approval. Read the linked rules; do not copy them here.
These skills are repository files: a session loads them only from where its provider
looks, so read them by path until they are installed there
([where the skills load](../../../docs/workflow-guide.md#where-the-agent-loop-skills-load)).

- Ground rules, delivery gates, model and subagent tiers: [CLAUDE.md](../../../CLAUDE.md) and `~/.claude/CLAUDE.md`
- Session routes, task results and outcome files: [workflow guide](../../../docs/workflow-guide.md), sections 3 and 9
- A child on the other provider, its refusals, and how to read how it ended: [launch a project-bound child on another provider](../../../docs/workflow-guide.md#launch-a-project-bound-child-on-another-provider)
- What a headless child's parent reads when the inbox cannot reach it: [ask your coordinator](../../../docs/ask-your-coordinator.md#return-route-for-a-parent-without-a-project)
- Keeping custody of a result when its worker exits: [coordinator result handoff](../../../docs/coordinator-result-handoff.md)
- Routine questions between sessions: [ask your coordinator](../../../docs/ask-your-coordinator.md)

## 1. Write the goal and its proof before you dispatch

Put the story in OpenProject first (`opwork search`, then `create` or `claim`).
Write down:

- **Goal.** One outcome in one sentence, for a named user.
- **Acceptance checks.** For each check: the command or observation, the result
  that passes, the evidence that proves it (an exact head SHA, a CI run ID, a test
  log path, an image digest), and the state it proves: source, tested, installed
  or usable. Keep those four states apart.
- **Boundaries.** Repositories, worktrees, files and runtime resources the
  coordinator may change. Everything else is out of scope.
- **Budget.** The authorized elapsed time, usage or cost cap, concurrency, and any
  budget checkpoint or stop threshold, as recorded on the story. A decision 55
  stall threshold is never one of these
  ([ADR-019](../../sagas/distributed-dev-env/adrs/019-stalled-work-steers-not-stops.md)).
  Record an unknown amount as unknown.
- **Holds and stops.** Every hold that applies, and the stop rules in section 5.

If you cannot name a check that fails today and passes when the work is done, the
goal is not ready. Fix the goal before you dispatch anything.

## 2. Keep one status record

The working record is a small JSON file that the coordinator owns, for example
`WORK-STATUS.json` in its handoff directory. Write it atomically: a temporary
file in the same directory, then a rename. Record at least: the goal, each
acceptance check with its state and evidence reference, the coordinator, each
worker, the stage, holds, blockers and the next step. Mirror the same facts on the
tracker with `opwork comment` or `opwork checkpoint`. The file is for steering;
the tracker is the durable queue. They must agree. While a child runs in another
pod, keep its custody record too: owner, deadline, checkpoints, handoff and
outcome evidence, in the template in
[timer-checkin, section 2](../timer-checkin/SKILL.md#2-persist-the-state-first).

Keep the record short. Keep secrets, prompts, transcripts and raw logs out of it,
and point to logs instead.

**Do not assume a shared filesystem.**
[ADR-003](../../sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)
keeps repositories and worktrees per pod, and shared Git or RWX storage is not
required. `~/.shared` is a convenience that a receiver in another pod or harness
may not reach. So:

- For each handoff artifact (status record, ACK, RESULT, state file), record its
  route: the path or URL, its owner, and who can read it.
- Before you rely on a route, verify that the receiver can read it: the
  receiver's ACK names each path or route it actually read. An ACK that does not
  name it, or names another path, means the route is not proved.
- Subagents in the same session and pod may use the real local paths.
- Across pods, hand off through the supported routes: `agent-run inbox`, the
  tracker, and PRs, and `agent-run msg` only where section 5 allows it.
- Never read another pod's private files.

## 3. Record identities

- **Coordinator.** Its session name (the pod name, also in `AGENTD_SESSION`), or
  for a native subagent its task title.
- **Separate pod.** `agent-run -p` prints the session name. Record it with its UID
  from `agent-run show NAME -o json`. A headless task runs its prompt once and does
  not take messages. It reports through its PR and the outcome file
  `~/work/<session>.outcome.json`, which `agent-run show` reads back.
- **Provenance.** Keep, from that same `show -o json` read: `uid`, `parent`,
  `agent`, `model` and `effort` (requested launch settings), `repo`, `base`,
  `revision` and `createdAt`; and later the child's branch and head
  (`agentStatus.branch`, `agentStatus.head`). Keep three model facts apart:
  requested (the Session's `model`), configured (a template, default or UI
  setting) and served (the `Actual model` row, Unknown until the provider
  reports it). Never copy a requested or configured model into the served
  model; a synthetic fixture cannot attest one either. A model that a CLI
  prints in its own log is client-reported: record it as that, not as served.
- **A child on the other provider.** Start it as a managed project task, with
  every setting explicit:
  `agent-run --project P --agent codex --model <full id> --effort <level> --prompt-file F --wait 3m`.
  `--wait` bounds only the wait for the pod to start; exit 0 says nothing about
  the task. The [workflow guide](../../../docs/workflow-guide.md#launch-a-project-bound-child-on-another-provider)
  has the exact flags and what each refusal means: a 422 without `--project`, a
  422 for a project the catalog lacks (fixed only through the reviewed catalog
  path), and a 403 from `--dry-run=server`, which serves the exact sealed pilot
  only and is not a general preflight. Do not retry a refusal or route around
  it. Never run the other provider inside your own pod, such as `codex exec` in
  a Claude pod, as a stand-in for a managed session.
- **An owner-interactive Codex thread on a paired v2 host.** Tom starts it from
  the phone, or an agent on that host uses the `codex --remote unix://…` TUI or
  `scripts/codex-clean-session.py`. `codex exec` and `codex queue` threads never
  show on the phone. Never pass `-s`, `-a`, `--approve-for-me` or
  `--full-auto`: omit them, or pass `--dangerously-bypass-approvals-and-sandbox`.
  A `workspace-write` request falls back to `read-only`, where every shell and
  MCP call fails. `agent-run rc fresh` needs v2.71 or later,
  `--enable-codex-rc-thread` (off live on 2026-11-09; OpenProject #272, needs
  Tom's approval) and a qualified target. The
  [workflow guide](../../../docs/workflow-guide.md#start-an-owner-interactive-codex-task-on-a-paired-v2-host)
  has the recipe.
- **Native subagent.** Its task title and the launch settings you asked for, such
  as `subagent_type: opus-worker`. Label the model as the launch setting, not as a
  verified fact, unless you observed it.
- **Parent.** Who dispatched whom.

Never record an identity, model or result you did not observe as verified. Write
"owner-reported" or "not re-verified here" instead. A child's report about its
own launch or work is owner-reported too.

## 4. Dispatch, then wait for real work

The work order states the goal, the acceptance checks, the claims, the boundaries,
the budget, the holds, the stop rules, the status record route, an ACK route and
the reporting destination ("The dispatch and return loop", below).
The receiver's first action is to write that ACK atomically: schema, time,
receiver, accountable session, worktree, branch, base commit, claims, the paths
or routes it read, `accepted` and the next milestone.

**An assignment or an ACK is not proof of execution.** An assignment says you
asked. An ACK says the receiver accepted the scope. Proof is a commit on the
claimed branch, a test log, a CI run on the exact head, or a PR state read from
GitHub. Record "assigned", "acknowledged" and "first concrete work seen" as
separate stages.

### The evidence ladder

Climb it one rung at a time. Each rung proves only itself, and none stands in
for a later one. Record each rung in the status record with its time and an
evidence reference.

| Rung | What proves it | How to read it |
|---|---|---|
| 1. Launch accepted | The create printed the Session name and exited 0; the Session has a `uid` | the create's output and exit status; `agent-run show NAME -o json` |
| 2. Running | `phase` Running and `agentStatus.state` busy | `agent-run show NAME` |
| 3. Semantic ACK | The receiver's ACK restates the scope and names its worktree, branch, base and the routes it read | the ACK route (section 2) |
| 4. First work | A commit on the claimed branch, or the first piece of the deliverable | `git ls-remote`, the child's branch and head in `show`, the artifact |
| 5. Reviewed artifact | The deliverable exists at a stated head and you read it. For a review, that is its `review.md` | read the file at that head |
| 6. PR | The PR is open on the claimed branch with the exact head | `gh pr view N --json headRefOid,state` |
| 7. Required CI | The repository's required checks passed on that exact head (in dev-env: `CI - Success`) | `gh pr view N --json statusCheckRollup`; the branch's rulesets (`gh api repos/OWNER/REPO/rules/branches/main`) and its classic protection (`gh api repos/OWNER/REPO/branches/main`: `protected` and `protection.required_status_checks`) |
| 8. Delivery or native return | The authorized merge at that head, or, for work that is not merged here, the result returned: a native subagent's final result, or a headless child's task exit, verdict and retained outcome | `gh pr view N --json mergedAt,mergeCommit`; the subagent's notification; `agent-run show NAME` |

A repository with **no required checks** cannot reach rung 7. Record "no
required checks configured" with the evidence (no ruleset rules, no classic
protection, and the check rollup), not a pass, and say in the report that rung 7 was not met. An
advisory review is never rung 7, and its green does not replace a check. Your
own verification of the deliverable (rung 5) still stands; state the gap
beside it. Read each repository's own rules: one repository's lack of required
checks says nothing about another's. dev-env requires `CI - Success` on the
exact head.

Record each rung when you observe it. A later rung seen first does not prove an
earlier one: a PR seen before you read its deliverable leaves rung 5 open, and
a rung you never read stays "not observed". One read can record several rungs
when it is each rung's own evidence: a PR read shows a commit on the claimed
branch (rung 4) and the PR (rung 6), but not the deliverable's contents
(rung 5). For a deliverable that is a
message rather than a PR, rungs 4 and 5 are that message as you read it, and
rungs 6 and 7 do not apply: record them as not applicable. A deliverable in
several parts, such as a reply and then a correction, reaches rung 5 only when
you have read every part. In a repository without required checks, rung 7 is
recorded as not reachable, and rung 8 still follows that repository's rules
and the merge authority in the work order.

Keep three more facts apart from the rungs:

- **A delivery receipt is not an ACK.** An `agent-run inbox send` result, a
  `msg` delivery or an API read acknowledgement proves the text arrived. Only
  the receiver's own semantic ACK (rung 3) proves it understood the work.
- **An independent review is a separate fact.** It is a review of the rung-5
  deliverable by someone other than its author, at the same head. The author's
  own report, its tests and its green CI are not one.
- **Operational acceptance comes after rung 8.** Installed, usable by its user
  and accepted is a state of the story's own acceptance checks. A merge or a
  returned result does not prove it.

Exit 0, `Running` or a final message is not a successful review. Only the
accepted deliverable is (rung 5 and above). The
[workflow guide](../../../docs/workflow-guide.md#read-how-the-child-ended)
separates a finished provider turn, a failed command inside it, a masked exit
and a missing deliverable.

Keep each command's own exit status in the evidence. A pipe into `tail`, or a
final `echo`, replaces it. Save the output to a file and read `$?` right after
the command, or `set -o pipefail`:

```bash
agent-run show "$CHILD" -o json > child.json; rc=$?
echo "agent-run show exit=$rc"
```

### The dispatch and return loop

For a child in its own pod, such as a review on the other provider. A worked
example, labelled historical, is in
[references/studio-review-dispatch-2026-11-04.md](references/studio-review-dispatch-2026-11-04.md).

1. **Launch once, with every setting explicit:** `--project`, `--agent`,
   `--model` (a full ID), `--effort`, `--prompt-file` and a bounded `--wait`
   (section 3). `--idempotency-key` makes a repeat of the same command find the
   first Session instead of starting a second one. It is valid in source, but
   the example did not use it.
2. **Record the identity at once:** the printed name, then one
   `agent-run show NAME -o json` read (section 3). That is rung 1.
3. **Name the reporting destination in the work order:** where the ACK and
   the result go, and who reads them, the fallback if that route fails, and
   whether the child's PR is to be merged and by whom. Name supported routes
   (section 2). A route outside that list is unproved until its receiver
   confirms reading it. A headless child takes no
   `agent-run msg`, so it reports through durable routes: its outcome file,
   its log, its PR with committed artifacts, and `agent-run inbox` only where
   both ends support it. A parent without a project (no accepted project
   snapshot), such as a Remote Control coordinator, gets nothing from the
   child's inbox: it reads the child instead
   ([return route](../../../docs/ask-your-coordinator.md#return-route-for-a-parent-without-a-project)).
   Do not plan on automatic inbox return; it has not been shown. An inbox
   reply that does arrive is read with `agent-run inbox read` by the Session
   it was addressed to. Its sender identity is in `agent-run inbox list -o
   audit`. Its arrival is delivery, and only that read is consumption.
4. **Wait for the semantic ACK:** the child restates the scope, its branch,
   its base and the routes it read (rung 3). A delivery receipt is not an ACK.
5. **Observe the same task.** A new pod can be Running while its agent is
   still starting: `agent-run log` can answer not found, and the agent state
   can still be pending. That is the same task starting, not a failure.
   Re-read that same Session within a bounded observer window. The work order
   sets the window; if it does not, ask its owner. Re-read with other access
   you already hold. A short-lived bootstrap token minted for the launch serves
   that one launch only: it is kept in memory only, never renewed, and never
   reused for observation. Never launch the task again because its log is not
   ready yet, and never mint a token to keep watching it. When the observer
   window ends, record the child's identity and last observed state in the
   custody record (timer-checkin, section 2), and keep
   observing it through later authorized reads of that same task. The window
   ends the observation, not the task or its assignment. If no authorized read
   path is left, for example because the launcher's only credential was that
   launch token, record the gap and put the task's identity on the tracker.
   Then hand custody over as in timer-checkin, section 5: name a session that
   can read the task, usually the parent, as the successor, and send it the
   handoff. Until it reports, custody is in transit. Never create a
   credential to fill the gap.
6. **Watch for the return with a bounded PR-or-exit watcher**
   ([timer-checkin, section 1](../timer-checkin/SKILL.md#1-what-exists)). It
   ends when a PR appears on the child's branch, when the child ends, when
   reads fail or are refused, or at its deadline. If the child's remaining
   work waits on a decision only Tom can make, such as a merge or a trust
   approval, end the watcher. First check that claim against the work
   order's merge authority or a recorded hold: the child's own claim is
   owner-reported, and agents merge their own PRs unless a hold applies. That work is a pending decision (section 5;
   timer-checkin, section 6): record a checkpoint with its resume trigger,
   Tom's verified answer, and keep no timer, watcher or poll for it.
7. **Take the return:** the task exit, verdict and retained outcome from
   `agent-run show`, the log while the pod still runs, and the PR with its
   committed artifacts. Copy them into the custody record at once
   ("Waiting for a child", below). Then climb the ladder from rung 5: read the
   deliverable yourself, get the independent review the story requires, and
   check the repository's own required checks.
8. **Report to the destination.** If the report fails (for example, the route
   answers not found), that is a reporting failure, not a task failure. Record
   it with its error, report through the work order's fallback and, in any
   case, on the tracker item, which every session can read. Do not keep
   retrying the dead route. A dead route to the parent can mean the parent
   has ended. If the parent is a Session you can read, check it with
   `agent-run show PARENT`. If it is gone, or you cannot tell (an unnamed
   parent, a thread, no read access), record that. Report to the parent's own
   coordinator, or, for a top-level coordinator, to Tom or his verified
   coordinator, who names a successor (timer-checkin, section 5). If you cannot
   tell which applies, report on the tracker and to the coordinator that
   assigned you the work, and let it decide. The task's
   result stands on its own evidence.

### Waiting for a child

Never wait with an open-ended shell loop. Use what exists: a check-in timer
from [timer-checkin](../timer-checkin/SKILL.md) with its state file, a `Monitor`
or background until-loop that has a deadline (timer-checkin, section 1), and
`agent-run show` and `agent-run log` reads. Every wait handles these:

- **API failure.** A failed read is "unknown", not stopped and not done. Record
  agent-run's exit code. Exit 3 (auth, or 403) and 4 (not found) from
  `agent-run show` do not get better on retry: end the wait and check the name
  and your identity. A `log` read that answers not found while a new pod starts
  is different: the Session exists, and the loop's step 5 applies. Other
  non-zero exits, such as 5 (API unavailable after agent-run's own retries), can
  be transient: count them, and after a few in a row end the wait with "child
  state unknown since T". Never start a replacement child because reads failed:
  the first one may still be working.
- **Deadline.** The wait has a deadline from the work order. At the deadline,
  read the child once more, record its last known state, and run a check-in.
  The deadline ends the wait, not the work: steer by section 5, and re-arm a new
  bounded wait while work continues.
- **Lost ACK.** No ACK by the first check-in: read the receiver's state
  (`agent-run show` and `agent-run log`, or the subagent's notification) before
  you send anything. Delivery is not acknowledgement. A headless child takes no
  messages. Do not resend the work order or start a duplicate. If it is busy,
  look for first work. If it has exited, read its task result and outcome; a
  further pass needs its own identity and authorization.
- **Context handoff.** Before your context fills or your session stops, bring
  the custody record up to date (timer-checkin, sections 2 and 5) so a
  successor can continue from it.
- **Final outcome.** When the child ends, read `agent-run show` (task exit,
  verdict, retained outcome), the log while its pod still runs, and the PR.
  Copy them into the custody record at once: once a reap releases the Session,
  `show` answers 404 and the outcome is gone with it. Then check the
  deliverable itself (rung 5).

## 5. Steer live

At every check-in (see [timer-checkin](../timer-checkin/SKILL.md)):

1. Read the status record and the newest ACK and RESULT.
2. Check each claim against its evidence yourself: `git log` on the claimed
   branch, `gh pr view N --json headRefOid,statusCheckRollup`, the test log the
   claim cites. A claim you cannot check stays "unverified", not "done".
3. Compare progress with the goal. Look for drift, scope creep, a stale head or a
   repeated failure.
4. Correct course, and record each correction and the evidence that prompted it.
   Continue a native subagent with `SendMessage`. For a session in another pod,
   prefer `agent-run inbox`. Use `agent-run msg SESSION -` only for a live TUI
   session when delivery is safe, and never while a native question prompt is
   pending in it. A headless task takes no messages. For an existing provider CLI
   session in tmux (Cursor, Codex, Claude), follow
   [tmux-cli-handoff](../tmux-cli-handoff/SKILL.md).

The stop rule is [saga decision 94](../../sagas/distributed-dev-env/README.md)
([ADR-019](../../sagas/distributed-dev-env/adrs/019-stalled-work-steers-not-stops.md)).
The work stops only on one of these, each backed by specific evidence:

- **A demonstrated outcome.** Every acceptance check passes, with evidence
  checked by someone other than the agent that did the work.
- **A cancellation or pause from Tom or his verified coordinator.** A cancel, a
  pause or stop, a "wait for Meatball" (mark the work "Awaiting Meatball
  context"), or an explicit hold ends or pauses the relevant work. Record who,
  when and through which channel. Save a checkpoint, and do not poll while
  waiting.
- **An exhausted authorized budget.** The budget recorded for the story is
  actually spent, or a budget checkpoint or stop threshold recorded on the story
  is reached, read from its record (ADR-019). A decision 55 stall threshold is
  never a budget checkpoint or stop threshold. A latch the platform has actually
  set, read with `agent-run budget show <task-uid>` (`latched: true` and its
  reason), is a platform denial you never bypass; until #400 that includes the
  old `Stalled` and `RepeatedFailure` latches. An expected or estimated latch is
  not a stop, and neither is a feared or estimated overrun.
- **A genuine unresolvable blocker.** An input only someone else can supply, with
  no authorized repair left. Record the missing artifact and why no repair
  remains.
- **A required safety or permission decision.** A denial, or an approval or
  safety decision that only Tom or the policy owner can make. A denial is an
  exact blocker: record the exact denial and end the work that depends on it. A
  decision that is asked but not yet answered stops only the action that
  depends on it, with a checkpoint and its resume trigger (the verified answer).
  No timer, check-in or poll exists for that action or its answer:
  budget-contract item 5, kept by [ADR-019](../../sagas/distributed-dev-env/adrs/019-stalled-work-steers-not-stops.md), forbids model polling
  while waiting. The action resumes when the answer arrives through a verified
  channel, as an event. Other independent authorized work keeps its own timer
  and outcome; that timer never inspects, re-asks or polls the pending action
  or its pane, and its check-ins record nothing about it beyond the existing
  hold line. If every remaining piece of work depends on the decision, delete
  the timer and record the stop and the resume trigger, after writing the lane
  table of [timer-checkin](../timer-checkin/SKILL.md), section 6
  ([tmux-cli-handoff](../tmux-cli-handoff/SKILL.md), step 4, is one such case).
  A decline then counts as a denial. Never bypass either.

Check a relayed or quoted "stop" or "cancel" against its actual source and
chronology first. The last two are the exact blockers; a pending decision stops
only its own action. Nothing else ends the work.

**Time, attempts and context size trigger steering, not stopping.** The
[workflow guide](../../../docs/workflow-guide.md), section 9, sets the stall
thresholds: 60 minutes without progress or three failed attempts at the same
blocker. Its counters carry across agents, pods, methods and resumes, so a
threshold reached again stays visible. Reaching one, or filling a worker's
context window, triggers this while authorized repair remains:

1. **Diagnose.** Read the evidence and name the blocker.
2. **Steer.** Send the owner a correction that changes the approach. Repeating
   the same failed attempt is not steering.
3. **Checkpoint.** Update the status record and the tracker.
4. **Keep continuity.** The same owner continues. When its context is full or its
   session is gone, start a fresh worker from the handoff: branch, head, open
   findings, next step. Keep the timer armed.

Ask Tom one actionable question, with a concrete bounded next option, only when
real input or a permission decision is missing. Then that missing input or
decision is the stop reason for the work that needs it, not the elapsed time or
the count.

"Waiting", "looks done", "the worker said so", a finished turn and an expired
timer are not stop reasons either. At any stop, save a checkpoint with the next
safe step, end or pause only the work that depends on it, and keep doing
independent authorized work. A hold gates only its own action. Do not route
around a denial. Running agent sessions are sacred. Ask Tom real decisions one at
a time with AskUserQuestion, at the moment they arise.

## 6. Close

Check the outcome yourself against every acceptance check, and record the
highest rung of the evidence ladder (section 4) that each one reached. Then complete the
tracker item with its evidence (`opwork complete --evidence`), write the final
status record with the stop reason, delete the timer, and report. A report says
what was proved, how, and what is still open with its reason.

Behavior scenarios for an evaluator: [scenarios/permission-barrier.md](scenarios/permission-barrier.md),
[scenarios/new-pod-log-not-found.md](scenarios/new-pod-log-not-found.md) and
[scenarios/historical-review-return.md](scenarios/historical-review-return.md).
