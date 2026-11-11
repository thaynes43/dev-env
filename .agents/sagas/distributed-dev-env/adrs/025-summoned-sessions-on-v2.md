# ADR-025: Summoned sessions run on v2 under one policy per caller, report their own outcome, and page through the keeper

- **Status:** Proposed 2026-11-11. The coordinator of story OP751
  (`dev-env-1111-143337`) sets Accepted when it rules on the re-reviewed PR.
  Three gates are Tom's and Pending (decision 15).
- **Date:** 2026-11-11
- **Decider:** to be the coordinator of OP751. Written by worker session
  `dev-env-1111-144138` for story OP734; fix round 1 by worker session
  `dev-env-1111-155209`; fix round 2 by worker session
  `dev-env-1111-170449`. The coordinator ruled the fifteen questions behind
  it, every finding of two reviews of the first draft, and every finding of
  the re-review of fix round 1 with fix round 2's questions, on 2026-11-11
  (DESIGN-064 sections 10.1 to 10.3); those are not rulings by Tom. This ADR
  records no decision by Tom. Where a decision rests on an earlier ruling of
  his, it names the record.
- **Supersedes:** these parts of [DESIGN-001](../designs/001-dev-env-v2.md),
  which was accepted with [ADR-001](001-distributed-dev-env.md). Everything
  else in DESIGN-001 3.7 (D-36), 6.10 and 7.3 stands, including 3.7's rule
  that every caller-supplied field reaches the session as data in a fenced
  block, which decision 13 now meets.
  1. 3.7, the caller table's curation row: `remote / curation`.
  2. 3.7, D-36's YAML example.
  3. 3.7, "A verified join handle": the sentence that the caller waits with
     `agent-run wait <name> --registered`.
  4. 3.7, "Plan credentials only": the sentence that the create fails with
     `503 plan credential unavailable`. Its sentence "No v2 pod holds a
     metered API key, so no session can fall back to metered" is qualified,
     not edited: the launch has no automatic metered fallback, but the
     operator sets the Cursor key on cursor task pods, and an ops session can
     reach caller pods that hold metered keys (DESIGN-064 5.8).
  5. 3.7, "Watchdogs": the words "and resumed from its volume where it can
     be". No summoned session is resumed, automatically or by anyone.
  6. 3.7, "Pages, from the operator on Pushover: ... an upgrade or
     escalation `failed` ...", and 6.10's operator bullet "`api.pushover.net`
     (summoned-session pages and the digest)".
  7. 3.7, "Storm limits" and "Escalations are not single-flight", where they
     imply a create past `concurrent` is refused, and where an escalation
     starts "within its caller's `concurrent` and `createsPerHour` limits".
     Past a pool a create is queued; past its queue bound (`queued` or
     `queuedUrgent`) it is refused.
  8. 3.7, "Retention": "a `done` session stays joinable for 1 day, a
     `failed` or `escalated` one for 7 days".
  9. 6.10, the D-24 table: "Pushover" in the Ops row, and "every session pod"
     in the Platform row.
  10. 7.3, "On a quota wall a summoned session moves to its policy's fallback
      plan model".
  11. 3.7, "Authorization": summoning callers cannot reap, and summoned
      sessions cannot create child sessions. Its sentence that every
      caller-supplied field reaches the session as data in a fenced block is
      kept, not superseded; decision 13 meets it.
  12. 3.7, "Lanes": the sentence "One remediation actuator touches the cluster
      at a time" is qualified, not edited: at most one admitted session holds
      each single-flight lane, but an old process that cannot be confirmed
      stopped can still act after its lane is released (decision 7).

  These twelve items match DESIGN-064 section 11's list, item by item.
- **Design:** [DESIGN-064](../designs/064-summoned-sessions-ops-profile.md).
- **Tracking:** OpenProject [#734](https://work.haynesnetwork.com/work_packages/734)
  (this design), [#751](https://work.haynesnetwork.com/work_packages/751)
  (the coordinator's story), under epic #112.

## Context

Plan 10 moves the sessions that automated callers start through the v1
executor `dev-env-ops` into the v2 operator. DESIGN-001 3.7 (D-36) set the
shape on 2026-10-06. Since then v2 grew parts of it for other reasons: the
first CallerPolicy (Meatball's), the outcome fields, the task clean-up, the
keeper's Claude projection, `agent-run rc` and the private-task executor
latch. Some of those differ from 3.7, and 3.7 has a stale example. DESIGN-064
compares each piece with the code at dev-env `b65a41c9` and haynes-ops
`3c78d4f2`. This ADR records the decisions that change DESIGN-001 or that
DESIGN-001 left open.

## Decision

1. **One CallerPolicy per caller, by a name derived from its ServiceAccount.**
   A new `spec.summon` block sits beside the seven fields Meatball's policy
   has. A policy holds one or the other. The policy for
   `upgrade-agent/alert-responder` is named `upgrade-agent.alert-responder`,
   and the API reads only that name, uncached. Each entry fixes the
   repository, the profile (`ops` only), the model and the brief's template;
   a summoning create may carry only `name`, `mode`, `lane`,
   `idempotencyKey` and a bounded `data` map, plus `agent`, `model` and
   `profile` only to repeat the entry. Meatball's policy, read path and
   rights do not change. DESIGN-064 6.4 has the schema; it replaces D-36's
   example.
2. **Curation is a `task`** (DESIGN-064 Q-03). It is an unattended daily
   batch. v1's own choice (`session-launch.sh`, `want_rc=0`) points that way,
   and so does the rule that only sessions that want Tom appear on his list,
   which DESIGN-001 6.7 attributes to him with a date (2026-08-23); no record
   linked here quotes him, so that attribution is second-hand.
3. **A summoned session reports its own outcome,** through `POST
   /v1/sessions/{name}/outcome` and `agent-run report
   working|done|failed|escalate`. Only the session's own pod may call it.
   A terminal outcome is final, and a later `working` cannot reopen it. In
   lane `remediation` a reported `failed` is stored as `escalated`, as v1
   promotes it, because a headless session has no window to join.
4. **One writer per session class for `status.outcome`.** For a session with
   `spec.caller`, only the report route and the operator's guaranteed close
   write it; both decision writers (the heartbeat and the decision route)
   refuse it. For any other session, only the decision path does.
5. **The operator, not the caller, watches Remote Control registration.** It
   applies the check `agent-run rc start` already makes and records a page
   event when the registration settles. No `agent-run wait` verb is built.
6. **A summoned session with no usable model does not start,** and its
   outcome follows its policy's `onUnreported`. The create itself does not
   return 503. agentd tries the fallback model only before the agent starts.
   A quota error during a run ends that run; the session is not restarted on
   another model, because an actuator restarted mid-run could act twice.
7. **No resume of a summoned session, automatic or by a person, enforced by
   an executor latch.** The latch is written before the first pod create and never
   reversed: a summoned task reuses the private-task latch that exists today;
   a summoned remote session gets one of the same shape. The controller
   checks it before it creates a pod, and agentd checks before it continues a
   saved launch, so neither a new pod nor a container restart in the same pod
   starts the agent again. A start deadline fences a late first launch only
   with new code: run-agent checks it again immediately before it spawns the
   provider, because today's check bounds only the launch record. Held
   and lost sessions are told apart by the latch, not by `status.podName`. A
   lost session is reported and closed as `onUnreported`. One terminal
   transition feeds the lane, the caller's counts, the page or digest and the
   remediation's filing, once. A single-flight lane is freed at once when the
   session reported its own outcome; when the operator declared it, the lane
   is freed once the latch guarantees no further launch, and until
   run-agent's check is built, with a first launch possibly in flight, only
   once the recorded pod is confirmed gone. If the old process cannot be
   confirmed stopped, it can still act after that: a task until its own
   timeout, a remote session until its pod ends. v1 has the same gap: its
   watchdog frees the lane and leaves a hung window running. Nobody resumes
   a summoned session, a person included: the resume route refuses it. Once
   a summoned session stops running, nothing reads its conversation (for
   tasks that is ADR-020's rule; for remote sessions today's lifecycle),
   where v1 keeps every transcript; Tom accepts that difference under
   decision 15's second gate.
8. **The keeper sends summoned-session pages and the digest** (DESIGN-064
   Q-16, option A). The operator records page events on the session's status;
   the keeper, which already holds the Pushover token and pushes Ask
   notifications, makes one deduplicated attempt for each. That is not proof
   of delivery. The operator holds no Pushover token, and its egress to
   `api.pushover.net` is removed. Failures page as v1's do, in every lane.
   One exclusive table maps each close, by lane, outcome, source and reason,
   to exactly one terminal page or the digest; lifecycle pages (registration,
   held, reminder, a pending or abandoned filing, overflow) stay apart from
   it. An upgrade, curation or escalation session that ends `escalated` pages
   (event `escalated`) and files no new session; a remediation never pages
   itself, its filing does. The keeper also pages when the static token is
   rejected, because stopping dev-env-ops ends v1's only alarm for it
   (Q-18).
9. **A summoned session is queued, then admitted; limits count reservations,
   not live sessions.** Every summoned session is created Queued and holds
   nothing until the operator leader admits it. Admitted sessions count in
   one of two pools: `concurrent` for every entry but the escalation entry,
   `escalations` for escalations, so an open escalation never holds a
   remediation. Queued upgrades and curation runs count toward their own
   bound, `queued`; Queued remediations and escalations, the platform's
   filings included, toward `queuedUrgent`. Past either a create is refused
   with 429. An urgent refusal is never silent: it is counted in the
   operator's create ledger, the keeper pages `overflow` once per caller per
   hour with the count, the responder fails open and pages as it does
   today, and an escalation caller logs it as it does today. The charge
   comes first: the API commits a create's charge for a
   dispatch id before it creates the Session, the Session carries the id,
   and the leader admits no Session whose id the ledger does not hold. An
   escalation queued over 15 minutes pages; a remediation queued past its
   `maxHold` closes as `escalated` and files its escalation.
10. **Retention and timers.** Task sessions follow ADR-020 (Tom, 2026-11-10:
    every finished task is deleted 7 days after it ends). Remote summoned
    sessions follow today's timers, unchanged (DESIGN-064 Q-12). A summoned
    session is never drained for a new revision (already built), and one
    suspended before its outcome is terminal is closed as `onUnreported`.
11. **The ops tier has no Pushover, and the platform tier does not select ops
    pods** (DESIGN-064 Q-01, Q-02). The ops policy carries its own DNS, API
    server and operator API rules. Its DNS rule lists v1's names minus
    Pushover, never any name, and it keeps the ingress deny the platform tier
    gives today, with the same exclusions.
12. **Summoning callers create and read their own sessions only,** and a
    summoned session creates no child sessions (DESIGN-064 Q-19).
13. **The platform renders every brief.** A caller sends bounded data, never
    a prompt; a `prompt` from a summoning caller is refused. The template is
    fixed text in the caller's policy entry, in `dev-env-system`, where no
    caller and no session can write. The API renders the template and puts
    every data field in one fenced block after removing anything that could
    open or close the fence. The brief is stored in `spec.prompt`; a remote
    session may carry one only when it is summoned, so ordinary remote
    sessions keep refusing it. A task reads it on stdin, as today; a remote
    session's CLI gets it as its first argument at its first launch only, as
    v1 does, with a durable delivery state and no resend after an ambiguous
    start. Nothing is typed into a live session.
14. **The platform files a remediation's escalation under its own identity.**
    The filing is bound to the remediation's UID, not the caller's key, under
    the name prefix `esc-rem-`, which no caller may use. The remediation
    records the pending filing, then the child's UID, so a replay never files
    twice; the filing is charged once to the caller's hourly limit, before
    the child is created, and counts in its `escalations` pool; its settings
    come from the snapshot taken when the remediation was created. A blocked
    filing keeps the remediation pending and pages; nothing bypasses a limit.
    A filing still pending 24 hours after its remediation closed is given up
    as `Abandoned`, with a second page, well before the task clean-up deletes
    the remediation; task retention does not change. Only remediations are
    filed for: an escalation's own `escalate` pages and files nothing.
15. **Three gates are Tom's.** No build story that changes access (#735 to
    #745) starts before Tom rules on DESIGN-064 as an access change, as item
    609 requires; the coordinator's rulings do not stand in for that, and
    Q-14 is part of that question. #749 does not stop dev-env-ops before Tom's
    recorded yes, as epic #116 and item 610 require; scaling down being
    reversible does not replace it, and he is shown the difference in
    retained data with it. #750 deletes no stored record before a separate
    yes, asked once with the list: the volume `dev-env-ops-home` and the
    ConfigMap `upgrade-work-orders` with its order history. All three are
    Pending (DESIGN-064 8.1).

## What this rejects

| Rejected | Why |
|---|---|
| A new CRD kind for summoning callers | Item 738 and D-36 name CallerPolicy; one kind keeps one read path and one RBAC rule. |
| Finding a caller's policy by listing all policies | Two policies could name one account. A derived name makes one per account by construction, with an uncached Get. |
| The caller rendering the prompt | The callers run `claude -p` on text an attacker can influence; a steered caller could send instructions to a session with operator verbs and a GitHub write token. v1's executor frames the caller's fields as data; v2's platform does. |
| Typing the brief into a live session after registration | A resend after an ambiguous send could deliver it twice, and the route for it sends into a running session. v1 passes the prompt at launch; so does v2. |
| The caller waiting for the link and paging | The operator files remediation escalations itself, so there is no caller process to wait. Callers' pages would also need the token in `upgrade-agent`. |
| The operator holding a Pushover token | A new holder of a credential, on Tom's list. The keeper already holds it and already pushes for the Asks it reads, with a ledger. |
| Switching models when a run hits the quota wall | A restart can repeat an actuator's actions. One plan pool serves every model, so a switch rarely helps at the plan's wall. |
| Resuming a summoned session, automatically or by a person | The same double-action risk. A replacement-pod continuation of a closed actuator needs its own design; a bounded resume of a terminal escalation is a follow-up, not built (DESIGN-064 9.1). A person or the caller starts new work instead. |
| No bound on queued urgent sessions | Open escalations could pile up queued sessions over hours. A bound with a counted refusal and an `overflow` page keeps every refusal visible. |
| Refusing a create past `concurrent` | An escalation must never be silent (R-01 F-04). A refusal leaves the caller to page; a queue pages from one place. |
| Counting queued sessions and open escalations toward `concurrent` | Queued sessions would hold each other back with nothing running, and an escalation waiting days for Tom would hold back the next remediation. |
| Filing the remediation's escalation under the caller's idempotency key | A re-fired remediation key would find the escalation, a caller could occupy its name, and a finished child would free the key for a second filing. |
| An any-name DNS rule for ops pods | It is a way out of a pod that has no open web. |

## Consequences

- Stories #738 to #745 build to DESIGN-064 sections 5.6, 5.9 and 6.4 to
  6.10, and none starts before Tom's ruling (decision 15). Item 738's Tested
  line ("refused past concurrent") changes to "queued past a pool once #741
  is on".
- The curation move (#746) needs no Remote Control and no keeper login.
- The keeper gains read access to AgentSessions in `dev-agents`, one more
  ledger ConfigMap of its own, and get on the operator's create ledger for
  the overflow counts. It gains no write on sessions.
- #747 is not Accepted while DESIGN-064 Q-20 (how a remediation on v2 sees
  what v1 sessions declared, now that the exec guard refuses v1's reader) is
  open. That question authorizes no exception to the guard.
- #749 depends on #742's rejected-token page and on Tom's yes. The rest of
  V-12 (the static token's mint date and the pages at 30 and 7 days) is
  deferred to a story the coordinator files, due before about 2027-06.
- The callers' own `claude -p` runs and their metered fallback stay outside
  this work; their spend ConfigMaps are read and reported at acceptance
  (DESIGN-064 5.4). A steered ops session can still reach what DESIGN-064 5.8
  lists, inherited from v1 and narrower than v1; the coordinator files a
  separate story for a narrower exec right for ops sessions.
- Nothing here changes Meatball's front door, Tom's own sessions, the task
  clean-up or any running session.
