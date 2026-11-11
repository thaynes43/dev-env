# ADR-025: Summoned sessions run on v2 under one policy per caller, report their own outcome, and page through the keeper

- **Status:** Proposed 2026-11-11. The coordinator of story OP751
  (`dev-env-1111-143337`) sets Accepted when it rules on the reviewed PR.
- **Date:** 2026-11-11
- **Decider:** to be the coordinator of OP751. Written by worker session
  `dev-env-1111-144138` for story OP734. This ADR records no decision by Tom.
  Where a decision rests on an earlier ruling of his, it names the record.
- **Supersedes:** these parts of [DESIGN-001](../designs/001-dev-env-v2.md),
  which was accepted with [ADR-001](001-distributed-dev-env.md). Everything
  else in DESIGN-001 3.7 (D-36), 6.10 and 7.3 stands.
  1. 3.7, the caller table's curation row: `remote / curation`.
  2. 3.7, D-36's YAML example.
  3. 3.7, "A verified join handle": the sentence that the caller waits with
     `agent-run wait <name> --registered`.
  4. 3.7, "Plan credentials only": the sentence that the create fails with
     `503 plan credential unavailable`.
  5. 3.7, "Watchdogs": the words "and resumed from its volume where it can
     be".
  6. 3.7, "Pages, from the operator on Pushover", and 6.10's operator bullet
     "`api.pushover.net` (summoned-session pages and the digest)".
  7. 3.7, "Storm limits" and "Escalations are not single-flight", where they
     imply a create past `concurrent` is refused.
  8. 3.7, "Retention": "a `done` session stays joinable for 1 day, a
     `failed` or `escalated` one for 7 days".
  9. 6.10, the D-24 table: "Pushover" in the Ops row, and "every session pod"
     in the Platform row.
  10. 7.3, "On a quota wall a summoned session moves to its policy's fallback
      plan model".
- **Design:** [DESIGN-064](../designs/064-summoned-sessions-ops-profile.md).
- **Tracking:** OpenProject [#734](https://work.haynesnetwork.com/work_packages/734)
  (this design), [#751](https://work.haynesnetwork.com/work_packages/751)
  (the coordinator's story), under epic #112.

## Context

Plan 10 moves the sessions that automated callers start through the v1
executor `dev-env-ops` into the v2 operator. DESIGN-001 3.7 (D-36) set the
shape on 2026-10-06. Since then v2 grew parts of it for other reasons: the
first CallerPolicy (Meatball's), the outcome fields, the task clean-up, the
keeper's Claude projection and `agent-run rc`. Some of those differ from 3.7,
and 3.7 has a stale example. DESIGN-064 compares each piece with the code at
dev-env `6ded38a4` and haynes-ops `3c78d4f2`. This ADR records the decisions
that change DESIGN-001.

## Decision

1. **One CallerPolicy per caller, by a name derived from its ServiceAccount.**
   A new `spec.summon` block sits beside the seven fields Meatball's policy
   has. A policy holds one or the other. The policy for
   `upgrade-agent/alert-responder` is named `upgrade-agent.alert-responder`,
   and the API reads only that name, uncached. Meatball's policy, read path
   and rights do not change. DESIGN-064 6.4 has the schema; it replaces
   D-36's example.
2. **Curation is a `task`** (DESIGN-064 Q-03). It is an unattended daily
   batch. Tom's recorded rule that only sessions that want him appear on his
   list (DESIGN-001 6.7) and v1's own choice (`session-launch.sh`,
   `want_rc=0`) both point that way.
3. **A summoned session reports its own outcome,** through `POST
   /v1/sessions/{name}/outcome` and `agent-run report
   working|done|failed|escalate`. Only the session's own pod may call it.
   A terminal outcome is final.
4. **One writer per session class for `status.outcome`.** For a session with
   `spec.caller`, only the report route and the operator's guaranteed close
   write it. For any other session, only the decision path does.
5. **The operator, not the caller, watches Remote Control registration.** It
   applies the check `agent-run rc start` already makes and records a page
   event when the registration settles. No `agent-run wait` verb is built.
6. **A summoned session with no usable model does not start,** and its
   outcome follows its policy's `onUnreported`. The create itself does not
   return 503. agentd tries the fallback model only before the agent starts.
   A quota error during a run ends that run; the session is not restarted on
   another model, because an actuator restarted mid-run could act twice.
7. **No automatic resume of a lost summoned session.** It is reported and
   closed as `onUnreported`.
8. **The keeper sends summoned-session pages and the digest** (DESIGN-064
   Q-16, option A). The operator records page events on the session's status;
   the keeper, which already holds the Pushover token and pushes Ask
   notifications, sends each one once. The operator holds no Pushover token.
   A `failed` outcome pages in every lane, as v1 does.
9. **Past `concurrent` a create is held; past `createsPerHour` it is
   refused.** A held session exists with no pod and a condition that says
   why. An escalation held over 15 minutes pages.
10. **Retention.** Task sessions follow ADR-020 (Tom, 2026-11-10: every
    finished task is deleted 7 days after it ends). Remote summoned sessions
    follow v2's timers once their outcome is terminal (DESIGN-064 Q-12).
    Before that, neither is drained nor idle-suspended; the watchdog governs
    them.
11. **The ops tier has no Pushover, and the platform tier does not select ops
    pods** (DESIGN-064 Q-01, Q-02). The ops policy carries its own DNS, API
    server and operator API rules.
12. **Summoning callers create and read their own sessions only,** and a
    summoned session creates no child sessions (DESIGN-064 Q-19).

## What this rejects

| Rejected | Why |
|---|---|
| A new CRD kind for summoning callers | Item 738 and D-36 name CallerPolicy; one kind keeps one read path and one RBAC rule. |
| Finding a caller's policy by listing all policies | Two policies could name one account. A derived name makes one per account by construction, with an uncached Get. |
| The caller waiting for the link and paging | The operator files remediation escalations itself, so there is no caller process to wait. Callers' pages would also need the token in `upgrade-agent`. |
| The operator holding a Pushover token | A new holder of a credential, on Tom's list. The keeper already holds it and already pushes from watched objects. |
| Switching models when a run hits the quota wall | A restart can repeat an actuator's actions. One plan pool serves every model, so a switch rarely helps at the plan's wall. |
| Resuming a lost summoned session | The same double-action risk. A person or the caller decides. |
| Refusing a create past `concurrent` | An escalation must never be silent (R-01 F-04). A refusal leaves the caller to page; a hold pages from one place. |

## Consequences

- Stories #738 to #745 build to DESIGN-064 sections 6.4 to 6.10. Item 738's
  Tested line ("refused past concurrent") changes to "held once #741 is on".
- The curation move (#746) needs no Remote Control and no keeper login.
- The keeper gains read access to AgentSessions in `dev-agents` and one more
  ledger ConfigMap. It gains no write on sessions.
- V-12 (the static token's mint date and pages) is not built by any story
  here; DESIGN-064 Q-18 defers it.
- Nothing here changes Meatball's front door, Tom's own sessions, the task
  clean-up or any running session.
