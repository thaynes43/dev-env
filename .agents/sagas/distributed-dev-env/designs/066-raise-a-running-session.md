# DESIGN-066: Raise a running session from outside it

- **Status:** Proposed 2026-11-13, for the coordinator's review. Nothing is
  built or installed. No flag, CRD, route, command or haynes-ops change
  exists for it. [ADR-028](../adrs/028-raise-a-running-session.md) is
  Proposed with it.
- **Tracking:** [Story OP830](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/830).
  Tom's words are recorded on story 828 comment 7927. Why the case's session
  started where it did is story 831 (section 1.3).
- **Origin:** Tom, 2026-11-13, quoted in section 1.1. Nothing else here is
  Tom's. The rulings in section 4 are proposed by a worker for the
  coordinator session `dev-env-fable-coordinator-1109`, which decides them.
- **ADR:** [ADR-028](../adrs/028-raise-a-running-session.md), Proposed. It
  adds one named exemption to the depth rule of DESIGN-001 section 3.4 and a
  fourth Ask kind, so it is accepted before any slice merges.
- **Builds on:** [DESIGN-001](001-dev-env-v2.md) section 3.4, D-19 and D-46;
  [DESIGN-065](065-session-display-titles.md) section 9.4 and
  [ADR-027](../adrs/027-session-title-annotation.md) (a later change is an
  annotation, not a spec write);
  [ADR-021](../adrs/021-grants-through-the-asks-board.md) and
  [ADR-022](../adrs/022-grant-ask-route.md) (a typed Ask the platform applies);
  [DESIGN-034](034-agent-asks-and-approvals-board.md) and
  [ADR-010](../adrs/010-agent-asks-and-approvals-board.md) (the Asks board).
- **Evidence:** dev-env `2774e599` (origin/main), source and docs only.
  DESIGN-042 is not on main; it was read on the branch of open dev-env PR 782
  at `5449647f`. The facts of the case are the coordinator's readings of
  2026-11-13, quoted and not measured again. Nothing in the cluster, the
  tracker or any session was read or written for this document.

## 1. Problem and the case

### 1.1 The owner's words

Tom, 2026-11-13, typed answer to a phone prompt in session
`dev-env-fable-coordinator-1109`, recorded on story 828 comment 7927:

"Make it an agent in charge of the backlog for this project, it does
OpenProject work and knows the requirements I laid out. Create a new super
user coordinator who dispatches to it for backlog add/remove/update tasks and
gets work from it. Fix why meatball started it as the wrong elevation and
build a way to elevate agents permissions OUTSIDE of their session (this is
all dev-env v2 restrictions so I have no idea why its so hard)"

This design is for the last clause: "build a way to elevate agents
permissions OUTSIDE of their session". The first two sentences were done by
hand. The clause "Fix why meatball started it" is story 831.

### 1.2 The case

The coordinator read these on 2026-11-13. They are quoted, not measured
again, and no live session was touched for this document.

- Session `haynesnetwork-1113-150420` was created at 2026-11-13T15:04:20Z by
  a call authenticated as session `haynes-ops-1109-230612`. The operator log
  line reads "session created", caller `session/haynes-ops-1109-230612`,
  depth 2. The creator sits at depth 1, so the new session sits at depth 2
  (label `dev-env.haynesops.com/depth: "2"`).
- Its create asked for no profile, so its pod runs as ServiceAccount
  `dev-agents/dev-env-agent`, the ordinary identity.
- At 15:34:23Z its `POST /v1/sessions` was refused with 403: "... levels deep
  and may not start sessions: two levels is the limit (DESIGN-001 3.4)".
- Nothing could raise it. No route, flag or `agent-run` command rewrites a
  session's parent, depth or profile. The fix was a second session, which
  loses the conversation the owner had spent a long time on.

The code explains each step. The depth label is the creator's depth plus one
(`caller.go:325`, written at `validate.go:384-388`). The refusal is
`admitCreate` (`sessions.go:699-702`). The creator is a workbench session
(DESIGN-065 section 9.1, from a read-only `kubectl` of 2026-11-13), and a
workbench session's child that names no profile runs on the templates'
default profile (`validate.go:324-327` and `:339`, OP502). A pod runs as
`dev-env-agent` unless its profile names another account (`pod.go:385-394`),
so the default profile names none.

### 1.3 Not in this design: why it started there

Why that session started at depth 2 on the ordinary profile, and what should
choose a new session's elevation, is story 831. This design does not answer
it. This design is the remedy after the fact: a session that started too low
can be raised without losing its conversation.

## 2. What exists today

### 2.1 Depth and the child cap

- `maxSessionDepth = 2` (`internal/apiserver/caller.go:49-52`): "depth 0 is a
  session Tom or a client created, 1 its child, 2 a grandchild, which creates
  no children". `maxRunningChildrenPerAgent = 4` (`caller.go:54-56`).
- A session caller's `depth` is the depth of the session it would create: its
  own label plus one (`caller.go:325`). `sessionDepth` reads the label and
  takes a missing or bad label as 0 (`caller.go:332-340`).
- `admitCreate` (`internal/apiserver/sessions.go:651-713`) refuses a session
  caller whose `depth` is above 2 with 403 and the message of the case
  (`sessions.go:699-702`). It then counts the caller's unfinished children by
  `spec.parent` and agent, read live (`runningChildren`, `sessions.go:263-276`;
  `unfinished`, `:303-318`), and answers 429 at 4 (`:703-710`). The direct
  create and the scheduled create both run it under the create lock
  (`sessions.go:651-657`).
- At create the API writes the depth label from the caller
  (`internal/apiserver/validate.go:384-388`) and `spec.parent` from the
  caller, never from the body (`validate.go:258-270`;
  `api/v1alpha1/agentsession_types.go:314-319`). The create log line carries
  the caller and the depth (`sessions.go:252-253`).
- DESIGN-001 section 3.4 states the rule (design line 433-436, "403 beyond
  2") and keeps it as one of three guards "about runaway agents, not
  capacity" (design line 4983-4986).

### 2.2 Profile, identity and what a profile brings

- `spec.profile` is the creator's choice; empty means the templates' default
  (`agentsession_types.go:289-297`).
- A child's profile (`validate.go:315-348`): an ordinary session's child runs
  on its parent's profile, the profile label on the parent's pod
  (`caller.go:317-320`), and another profile is a 403. A workbench session's
  child runs on the profile it names, workbench included, or on the default
  (OP502). A Codex host a workbench session starts runs on the default. A
  child of a session on a named coordinator profile runs on the default.
- The controller builds a pod from `spec.profile` (`internal/controller/pod.go:320`):
  - its ServiceAccount (`pod.go:378-394`): `dev-env-agent`, or the workbench
    account when the profile names it (which also sets `DEV_ENV_WORKBENCH=1`,
    `:388-390`), or a named coordinator identity only with the API's
    admission label (`:391-393`);
  - env, envFrom and the remote block (`pod.go:434-443`) and mounts
    (`:510-522`);
  - the profile's labels (`pod.go:341-342`), which "go on the pod, for the
    egress tiers and the broker's standing policies to select on"
    (`internal/templates/templates.go:350-352`), and the profile label itself
    (`pod.go:725-743`; `api/v1alpha1/labels.go:22-24`: "The egress tiers
    select on it").
- A live pod is never replaced (`pod.go:546-547`). Its ServiceAccount is
  fixed for its life.
- The session volume carries the profile label from its create and "is never
  updated after create" (`internal/controller/volume.go:14-24`). No reader of
  a volume's profile label is in this repo.
- Readers take the profile from the pod's label: the caller
  (`caller.go:317-320`), a grant's requester profile, which standing
  GrantPolicies match on (`internal/apiserver/grants.go:161-170`,
  `internal/broker/match.go:151-173`), the thread fence
  (`internal/apiserver/threads.go:132-139`), the legacy delegate
  (`internal/broker/legacy_delegate.go:87-91`) and the Codex host routes
  (`internal/apiserver/codexhost.go:234`, `:264`, `:464`). The API view
  shows `spec.profile` (`internal/apiserver/view.go:30`).
- The Claude token depends on the mode, not the profile (`pod.go:676-682`).
  agentd reads no session profile (no reader under `internal/agentd`).

### 2.3 What cannot change after create

D-39: spec is immutable after create except `operatingMode` and `lifecycle`
(`agentsession_types.go:28` and the spec rule at `:169`). That covers
`spec.profile` (`:295`), `spec.parent` (`:317`) and the two UID-bound fields
`usageCaptureParentUID` (`:321-328`) and `coordinatorInstructionParentUID`
(`:330-339`). A spec write moves `metadata.generation`, which four places
compare: the drain restart proof, an in-flight decision write, a published
canary and a rescue record (ADR-027, Context). ADR-027 records a later title
change as the annotation `dev-env.haynesops.com/title` for that reason.

### 2.4 Who runs as the workbench account

- The workbench account is `dev-env-workbench` (`templates.go:112-118`). A
  pod runs as it when its profile names it (`pod.go:388`).
- Tom, a trusted client and any workbench session may create a session on
  the workbench profile (`validate.go:339`, OP502). An ordinary session may
  not (`validate.go:340-347`).
- The API treats a pod-bound token of the workbench account as that session,
  and decides by the pod, not the token (`caller.go:141-144`, `:255-282`).
- A named coordinator identity (`dev-env-coordinator-<name>`) has its own
  rule: Claude only, one holder at a time, never inherited, and three launch
  shapes (Tom or a client at depth 0, or a depth-0 workbench session
  launching at depth 1) (`internal/apiserver/named_coordinator.go:23-37`,
  `:75-89`; D-19 in DESIGN-001 design line 3583-3600). The admission label
  and the launcher annotation are trusted because "Only the API writes
  AgentSessions in the session namespace" (`named_coordinator.go:55-58`), and
  the controller runs a pod as the account only with that label
  (`pod.go:391-393`).
- DESIGN-042 is not on main. On the branch of open PR 782, its section 6.4
  proposes a new named delegate identity and adds no rule to the code above.
  The rule in force is the code.

### 2.5 Approvals that reach Tom

| Piece | In source on main | Installed |
| --- | --- | --- |
| Asks board, kinds `info` and `approval`, `--enable-asks` | Yes (`cmd/dev-env-operator/main.go:263`) | On as of the 2026-11-10 switch sheet (`docs/switches-2026-11-10.md:18`). Today: Unknown, not read again |
| Kind `grant`, its typed scope, the answer's `username` | Yes (`api/coordination/v1alpha1/ask_types.go:29-30`, `:62`, `:225-231`; `internal/apiserver/apiv1/asks.go:145`) | Ask CRD copy: Unknown |
| Broker opens a grant Ask and applies the answer, `--enable-grant-asks` | Yes, default off (`cmd/dev-env-operator/broker.go:62`; `internal/broker/grant_ask.go:218`, `:229`) | Never set as of 2026-11-10 (`docs/asks.md:25-26`). Today: Unknown |
| Portal grant card and `preferred_username` retention (ADR-021 build PR 6) | No: `web/portal` has no `preferred_username` | No |
| Keeper push for new Asks, `--enable-ask-notifications` | Yes, default off (`cmd/dev-env-keeper/main.go:269`) | Unknown |
| Standing and attested GrantPolicies; delegate decisions (`--grant-delegates`) | Yes (`internal/broker/match.go`, `internal/apiserver/grant_decision.go`) | On as of DESIGN-060 section 5 (2026-11-10). Today: Unknown |

An `approval` Ask is a record and grants no machine action (ADR-010). The
`grant` kind exists so that one stored answer drives one machine action, with
its scope bound to a UID and a digest (ADR-021 sections 1 and 2). The saga's
decisions 97 and 98 send risky access to Tom through that board. An
AccessGrant is always requested by the session for itself
(`grants.go:161-170`), lasts at most 8 hours, and is installed by the broker
into the session's pod (`internal/broker/broker.go:566-580`).

### 2.6 Restarting a remote session

- Any caller may suspend or resume any session (`internal/apiserver/lifecycle.go:18-25`).
  A summoned session is never resumed (`:59-65`). A resume of a named
  coordinator session rechecks its admission (`:95-119`). A suspend rescues
  the pod first and keeps the volume (D-51, `lifecycle.go:20-21`).
- The drain never moves a remote session (`internal/controller/drain.go:62-72`).
  `docs/remote-session-upgrades.md` line 3: an automatic change "does not
  drain an existing user remote-control session", and "Explicit
  owner-requested suspend, deletion and recovery use their existing paths".
- Measured on ordinary sessions: a resume brings back the same phone entry
  and the same conversation (`docs/v2-quickstart.md:102-106`; image moves,
  `.agents/HANDOFF.md:483-485`). Not measured across a change of profile.
- agentd reports the CLI's `shell` status as busy: only `idle` and `waiting`
  are mapped, everything else is "working" (`internal/agentd/activity.go:251-263`).
  Coordinators are in `shell` almost all the time (ADR-027, What this
  rejects). So a coordinator seldom reads idle in `agent-run show`.

### 2.7 What this document could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry (section 7.4).
- Whether the workbench ServiceAccount can patch AgentSessions or write Ask
  status today. DESIGN-001 says it holds v1's OPERATOR tier cluster-wide
  (design line 3728); the RBAC is in haynes-ops.
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- What the workbench profile's env, envFrom, mounts and remote block hold
  today (haynes-ops `templates.yaml`).
- Whether `--enable-asks`, `--enable-grant-asks`,
  `--enable-ask-notifications` and the Ask CRD copy are installed today.
- How an older operator's board treats an Ask of a kind it does not know.
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.

## 3. Outcome

An action taken from outside a session, and decided by Tom, raises what that
running session may do. The session keeps its name, UID, conversation,
volume, work in progress and its entry in the Claude app. Two things can be
raised:

1. its **profile**, for example from the default profile to `workbench`;
2. its **right to start sessions**, for a session at depth 2.

The session can never raise itself. `spec.parent`, `spec.profile`, the depth
label and the creation record are never rewritten. `maxSessionDepth` stays
2. Every raise is logged and can be taken back.

## 4. Rulings proposed

These are proposed by a worker for the coordinator, dated 2026-11-13. None is
Tom's. Section 12 holds the four questions that need him.

**Ruling 1. Who asks, who decides.** Tom decides, and nothing else does. His
decision reaches the platform in one of two ways:

1. his answer to a `raise` card on the Asks board, signed in on the portal,
   recorded as `authentik/<username>` exactly as ADR-021 records a grant
   answer (section 6.3);
2. a call by his own ServiceAccount (`kindHuman`, `caller.go:24-26`) to the
   raise route. That call is the decision.

These may ask, which only puts a card on his board:

- Tom;
- a trusted client (`kindClient`, `caller.go:27-29`);
- a session created on the workbench profile ("workbench by creation",
  Ruling 7) that is not the target and does not sit under the target. "Under"
  is read live: the target is not on the asker's chain of parents, matched by
  name and UID, as `sessionAncestry` walks it
  (`internal/apiserver/grant_decision.go:299-320`). Any depth.

These never ask: the target; any session under the target; a session raised
to workbench; an ordinary session; a named coordinator session; a coordinator
host; Meatball (`kindPolicy`); a summoning caller; the portal.

Why: the card is the control. Whoever may open one is one tap from a
superuser. So only the classes that may already start a workbench session may
ask, and a session never asks for itself, directly or through a session it
started. An ordinary session that needs a raise tells its coordinator, which
decides whether to ask.

Rejected:

- *Only Tom's ServiceAccount.* Correct, and almost never used: Q-17 in
  DESIGN-001 (design line 5797) was withdrawn because Tom works through
  agents and the phone, not a laptop token. It stays as the second route.
- *A workbench coordinator decides alone.* It may already start a workbench
  session, but a raise brings in a conversation that grew on an ordinary
  chain, and the right to start sessions overrides a global guard. Decisions
  97 and 98 send risky access to Tom.
- *The coordinator records Tom's yes from its own phone prompt*, as
  `agent-run grant attest` does for some grants. That is the owner's choice:
  Q-066-01.
- *Depth-0 askers only.* The case's creator is a depth-1 workbench
  coordinator. Forcing it to relay through the main coordinator adds a step
  and no safety, because Tom decides either way.

**Ruling 2. The record.** One annotation on the AgentSession,
`dev-env.haynesops.com/raise`, holding one JSON object (section 6.1). Only
the operator writes it: the raise route for Tom's own call and for a
take-back, and the applier for a board answer (section 6.4). It names the
session's UID, and every reader compares that with `metadata.uid` and ignores
a record that does not match. It is not a spec write, so
`metadata.generation` does not move (ADR-027). The card is the record of the
decision. The annotation is the current state.

It is trusted as the named coordinator admission label is trusted
(`named_coordinator.go:55-58`, `pod.go:391-393`): only the operator writes
it. Before any flag is set, an admission rule in haynes-ops refuses a change
to this annotation from any user but the operator's ServiceAccount (safety
rule 15). Whether the workbench account can patch AgentSessions today is
Unknown from this repo: DESIGN-001 says it holds v1's OPERATOR tier
cluster-wide since 2026-10-20 (design line 3728).

Rejected: a spec write (D-39, and generation in four places, ADR-027); a
status field (a stale CRD prunes it, haynes-ops #3502, an older operator's
status write drops it, and status is what the operator saw, not a decision);
a separate object (a new CRD, RBAC, CRD copy and watch, for the same trust
the fenced annotation gives); an AccessGrant (section 13).

**Ruling 3. The profile.** The effective profile is the record's profile
when the record is active, names this UID, and its profile is on the operator
flag `--session-raise-profiles`. Otherwise it is `spec.profile`. The operator
reads the effective profile in one place: when it builds a session pod
(`pod.go:320`). A running pod keeps its ServiceAccount, so a profile raise
takes effect at the session's next pod. Neither the raise route nor the
operator restarts the session. `agent-run raise <session> --restart` is the
explicit step: the existing suspend and resume, only when the session reads
quiet (section 7.3). Whether the asker takes that step right after Tom's yes
is Q-066-02.

`--session-raise-profiles` is empty by default. A profile that runs as a
named coordinator identity is never given, whatever the flag says: the route,
the applier and the controller each check the profile against the templates
they read, and a templates read that fails gives nothing. The one-holder rule
and the launch shapes of section 2.4 stay as they are. The expected value is
`workbench`. The workbench rule needs no change: the
controller's account choice and the API's classification both follow the pod
(section 2.4). One rule is new: Ruling 7.

**Ruling 4. The right to start sessions.** In `admitCreate`, a session caller
whose own depth label is exactly `2` passes the depth check when, read live
under the create lock, its record is active, names its UID and grants
`startSessions`, and the operator flag `--enable-session-raise-dispatch` is
on. Section 8 gives the rule and every reader of the depth label. The child
gets the true label `3`, `spec.parent` the raised session, the same child cap
and every other admission. A session at depth 3 is never exempt and starts
nothing. The exemption serves the direct create only, not scheduled work,
because a schedule outlives the moment Tom approved.

**Ruling 5. Taking it back.** Anyone who may ask, and the target itself, may
take a raise back, with no approval. The record stays, marked taken back,
with who and when. The right to start sessions ends at the next create. A
profile raise ends at the next pod, so a take-back suspends the session at
once through the existing route, rescue first, unless the caller passes
`--keep-running`. Children already started keep running and keep counting
against the cap. Section 9.

**Ruling 6. What readers show.** The view's `profile` stays the creation
profile. A raise is shown beside it, in one new key that appears only on a
session that has a raise record or an open raise card. Section 10.

**Ruling 7. A raise never spreads.** A session raised to workbench is a
workbench session for its own pod, its Kubernetes rights and the API routes
that check the workbench account. It is not one for three things: it may not
ask for a raise; it may not start a child on the workbench profile; it may
not launch a named coordinator identity. The API tells the two apart by
`spec.profile`: a session is workbench by creation only when its
`spec.profile` names a profile that runs as the workbench account. A
templates read that fails counts as "not by creation". Whether its workers
are guarded is Q-066-04; this ruling assumes yes.

## 5. Safety rules

1. **No self-raise.** The target never asks for, decides or answers its own
   raise. No session under the target asks. No session raised to workbench
   asks.
2. **Only Tom decides.** No GrantPolicy, attestation, delegate, coordinator,
   client, Meatball or session decides a raise. One answer is used once.
3. **Bound to the UID.** The card's scope and the record carry the target's
   UID and a digest of what is raised. The applier and every reader compare
   them with the live session. A later session with the same name gets
   nothing. A record copied onto another object is ignored.
4. **Provenance stays true.** `spec.parent`, `spec.profile`, the depth label,
   the creation log line and the volume's labels are never written. The
   view's `profile` stays the creation profile.
5. **The global limit stays.** `maxSessionDepth` stays 2. One named
   exemption, for a session at depth 2 only. Nothing at depth 3 starts
   anything. The child cap is unchanged.
6. **Never a named coordinator identity.** A raise never gives such a
   profile, and a raised session never launches one.
7. **A raise never spreads** (Ruling 7).
8. **No restart on its own.** The operator and the raise route never suspend,
   resume or replace a pod for a raise. `--restart` is an explicit step that
   refuses unless the session reads quiet. A take-back suspends at once,
   because taking power back must not wait for the session to agree.
9. **Eligible sessions only.** A Claude session in `remote` or `local` mode
   that is not summoned (`spec.caller`), not a project canary or private
   pilot, not budget-bound, not a coordinator host's child, not on a named
   coordinator identity, not being reaped and not archived. Everything else
   is refused.
10. **Audit.** Every ask, decision, apply, unapplied answer, restart and
    take-back writes one operator log line with the caller
    (`caller.String()`), the session's name and UID, what changed and the
    card's id. The card keeps the answer and the username. The record keeps
    the current state, with who and when.
11. **Rate limits.** One open raise card per session; three per asker; twelve
    in the namespace. Sixty seconds between record changes on one session
    (429). After Tom declines, the same raise for the same session is refused
    for an hour; a different raise is not.
12. **Fail closed on rollback.** With both flags off, or on an operator
    without this build, the record is ignored: new pods use `spec.profile`,
    and the depth limit holds. A pod already running as a raised account
    keeps it until it ends (`pod.go:546-547`). On such an operator, a raise
    is taken back by suspending the session and resuming it.
13. **Schema order.** The AgentSession CRD does not change. The Ask CRD gains
    the kind `raise` (the kinds are an enum, `ask_types.go:62`), so haynes-ops
    gets that copy before the operator pin that writes one. The operator is
    not rolled back below that build while a raise card is open: how an older
    board treats an unknown kind is Unknown.
14. **Older readers.** A new key appears in the session view only on a
    session with a raise record or an open raise card (DESIGN-065 section
    9.7). An `agent-run` older than the slice that adds it cannot read that
    one session with `show --json --once` or `usage export`.
15. **A fenced record.** Before any flag is set, haynes-ops refuses a change
    to the annotation `dev-env.haynesops.com/raise`, and to a `raise` card's
    answer, from any user but the operator's ServiceAccount. The coordinator
    lists the live writers of AgentSession metadata and Ask status in
    `dev-agents` first, as `docs/asks.md` line 292 requires for grant cards.
16. **Review.** Each slice that grants access or overrides a guard gets an
    adversarial review by another model family before it merges (section 11).

## 6. Record and API

### 6.1 The record

The annotation `dev-env.haynesops.com/raise` holds one JSON object of at most
1 KiB:

```json
{
  "v": 1,
  "sessionUID": "<metadata.uid of the raised session>",
  "state": "active",
  "profile": "workbench",
  "fromProfile": "full",
  "startSessions": true,
  "askedBy": "session/<asker>",
  "decidedBy": "authentik/<username>",
  "decidedAt": "2026-11-13T16:02:11Z",
  "ask": "ask-0123456789abcdef",
  "takenBackBy": "",
  "takenBackAt": ""
}
```

- `state` is `active` or `taken-back`.
- `profile` is empty or a DNS label. At least one of `profile` and
  `startSessions` is set.
- `fromProfile` is the profile label on the pod when the raise was asked.
- `decidedBy` is `authentik/<username>` (ADR-021's username pattern) or
  `human/<namespace>/<name>` for Tom's ServiceAccount. `ask` is empty only
  for the second.
- Times are RFC 3339 in UTC.

A reader that finds any other shape, version or UID ignores the record, and
`agent-run show` says why. A new raise for a session whose record is taken
back replaces the record. The earlier one stays in its card and the log.

### 6.2 The routes

`POST /v1/sessions/{name}/raise`

```json
{"expectUID": "<uid the caller read>", "profile": "workbench", "startSessions": true, "reason": "<why>"}
```

- `expectUID` is required. `reason` is 1 to 500 characters under the Ask text
  rules; it is what Tom reads.
- Tom's ServiceAccount: the checks of section 6.5 run, the record is written
  with the `resourceVersion` precondition and retry of `setOperatingMode`
  (`lifecycle.go:40-160`), and the answer is 200 with the session view.
- Any other permitted asker: the checks run, the card is opened, and the
  answer is 202 with `{"ask": "<card id>"}`.
- The same raise already active: 200, nothing written.

`DELETE /v1/sessions/{name}/raise`

```json
{"expectUID": "<uid the caller read>", "keepRunning": false}
```

It marks the record taken back, withdraws an open raise card for that
session, and acts as section 9 says. 200 with the view.

Two operator flags, both off by default: `--session-raise-profiles` (a comma
list of profiles a raise may give) and `--enable-session-raise-dispatch`.
With both off, `POST` answers 403, "This operator does not raise
sessions.", and does nothing else. `DELETE` still works: taking a raise back
is never switched off. An operator without the routes answers 404, and
`agent-run raise` says the operator does not have them.

### 6.3 The raise card

A fourth Ask kind, `raise`, beside `info`, `approval` and `grant`. It follows
ADR-021 section 1 for trust:

- The operator creates it, through the Ask store, and nothing else does.
  `POST /v1/asks` refuses `type=raise` and a `requestId` starting `raise-`;
  the session withdraw route refuses `type=raise`.
- Label `coordination.dev-env.haynesops.com/ask-origin=operator`.
  `requestId` is `raise-` plus 16 hex digits taken from the target's UID and
  a counter.
- Its requester is the target, so the existing per-requester bounds and the
  ended-session card of ADR-021 section 5 apply to the session the raise is
  about. The asker is in the typed scope. The target cannot create, withdraw
  or answer it.
- No expiry, as a grant card (ADR-021 section 3). Counted apart from the
  sixteen info and approval slots, under the caps of safety rule 11.

Typed scope:

| Field | Meaning |
| --- | --- |
| `raise.sessionUID` | The target's UID |
| `raise.profile` | The profile asked, or empty |
| `raise.fromProfile` | The pod's profile label when asked |
| `raise.startSessions` | Whether the right to start sessions is asked |
| `raise.askedBy` | The asker, as `caller.String()`, at most 253 characters |
| `raise.digest` | 43 base64url characters: SHA-256 of the canonical JSON of the five fields above |

The card shows: what (the session, `fromProfile` to `profile`, and "may start
sessions one level deeper; those start nothing"); who asked; why; "If you
say yes, the session keeps running as it is now. The new profile applies
when it next starts. Whoever asked may restart it when it is quiet; it keeps
its conversation."; "If you say no, nothing changes." Then Approve and
Decline. For a session that has ended,
Dismiss only.

Tom answers on the existing route `POST /v1/portal/asks/{id}/answer`, with
the username rule of ADR-021 section 2. That depends on the portal keeping
`preferred_username`, which is ADR-021 build PR 6 and is not in source on
main (section 2.5). This design reuses it and does not build a second one.

### 6.4 The applier

A leader-only loop in the operator, beside the Ask store. For an answered
raise card:

1. Declined: nothing is written. One log line.
2. Approved: read the session live. Write the record only when the UID is
   `raise.sessionUID`; the session is not being reaped or archived; it is
   still eligible (safety rule 9); its pod's profile label is still
   `fromProfile`, or it has no pod; the profile is still on the flag list;
   for `startSessions`, its depth label is still exactly `2`; and the digest
   matches. Write with the `resourceVersion` precondition. On a conflict,
   read again and decide again.
3. Anything else: nothing is written, and one log line gives the reason.
   `agent-run show` reports the card as answered and not applied.
4. One use: a record that names the card means it was applied, and the
   applier does nothing more for it.

### 6.5 Refusals

Every refusal is one plain sentence.

| Case | HTTP | Sentence |
| --- | --- | --- |
| The caller is the target | 403 | "A session may not raise itself." |
| The caller sits under the target | 403 | "`<caller>` works under `<target>`, so it may not ask to raise it." |
| The caller is a session raised to workbench | 403 | "`<caller>` was raised to workbench, not started on it, so it may not ask to raise others." |
| Any other caller not in Ruling 1 | 403 | "Only Tom, a trusted client or a workbench coordinator may ask to raise a session." |
| `expectUID` differs | 409 | "Session `<name>` was replaced: its UID is not the one you read. Read it again." |
| Not eligible (safety rule 9) | 409 | For example "Session `<name>` is a summoned session; summoned sessions are never raised." |
| Profile not on the flag list | 422 | "This operator does not raise sessions to profile `<p>`." |
| Profile runs as a named coordinator identity | 422 | "Profile `<p>` runs as a named coordinator identity; a raise never gives one." |
| `startSessions` at depth 0 or 1 | 422 | "Session `<name>` sits at depth `<d>` and may already start sessions." |
| `startSessions` at depth 3 or a bad label | 422 | "Only a session at depth 2 may be raised to start sessions." |
| A card is already open for the session | 409 | "A raise for session `<name>` is already waiting for Tom (`<card>`)." |
| Within sixty seconds of a change | 429 | "Session `<name>`'s raise changed less than a minute ago. Try again in `<n>` seconds." |
| Tom declined this raise within the hour | 429 | "Tom declined this raise at `<time>`. Ask again after `<time>`, or ask for something different." |
| Both flags off | 403 | "This operator does not raise sessions." |

## 7. The pod change

### 7.1 Where the effective profile is read

One place: `buildSessionPod` (`pod.go:320`), which builds the agent pod for a
first start, a resume and an image move. The hold pod that rescues a volume
never runs as a profile's account (`pod.go:385-387`) and is unchanged. The
volume is never rebuilt (`volume.go:14-17`) and keeps the creation profile's
label. No reader of that label is in this repo; one in haynes-ops is Unknown.

### 7.2 What changes in the new pod

The new pod is the effective profile's pod in every way the profile decides:

- its ServiceAccount, and `DEV_ENV_WORKBENCH=1` for workbench (`pod.go:385-394`);
- env, envFrom and the remote block (`pod.go:434-443`), and mounts
  (`:510-522`);
- the profile's labels, so its egress tier and the broker's standing
  policies (`pod.go:341-342`, `templates.go:350-352`);
- the profile label, so the profile its children get (`caller.go:317-320`),
  the requester profile of its next grants (`grants.go:161-170`) and the
  thread fence (`threads.go:132-139`).

What the workbench profile holds is in haynes-ops `templates.yaml`, not in
this repo. DESIGN-060 section 4 lists what a workbench session can do that an
ordinary one cannot, including pod exec, `keeper-login` and the hardware SSH
key in its envFrom (its table, rows at design line 112-117). The Claude token
does not change (`pod.go:676-682`).

Grants: the broker installs an active kube grant into the session's current
pod and installs it again into a new pod (`broker.go:566-580`). A grant whose
decision is bound to the requester's pod UID ends or is refused after any pod
change, as on any resume: delegate reach (`RequesterPodUID`,
`internal/broker/delegate_reach.go:175`) and maintenance (the coordinator's
`PodUID`, `grants.go:186`).

### 7.3 The restart step

`agent-run raise <session> --restart`, run by the asker or Tom after a
profile raise is active:

1. It reads the session view. It refuses unless the record is active, the
   pod's profile label differs from the effective profile, the phase is
   Running, the agent state is `idle` (not `busy`, not `waiting`), no client
   is attached, and the last heartbeat is under 90 seconds old.
2. It calls the existing suspend and waits for Suspended, which comes after
   the rescue (D-51).
3. It calls the existing resume and waits for Running and, for a remote
   session, `registered`.

It never forces and never loops. Its refusal says which reading failed, for
example "Session `<name>` is busy. Try again when it is quiet, or suspend and
resume it yourself." This is an owner-requested suspend in the sense of
`docs/remote-session-upgrades.md`: the card Tom approved says the asker may
restart the session when it is quiet.

What remains. A heartbeat is up to 60 seconds old, so a turn or a question
that starts after it is cut by the suspend, as `docs/remote-session-upgrades.md`
states for the drain. The conversation and the volume stay; an open question
is lost and has to be asked again. Background commands end at the restart. A
coordinator with a background command reads busy (section 2.6), so it may
never pass step 1; then the asker decides whether to suspend and resume by
hand, or waits for the next resume.

### 7.4 The phone entry

Measured on ordinary sessions: a resume keeps the phone entry and the
conversation (section 2.6). Not measured across a change of profile or
ServiceAccount. Slice 3's live proof reads it on an unused test session,
with DESIGN-065 section 5's readings before and after (name, UID,
conversation id, and the first 12 hex of the SHA-256 of the Remote Control
session id and of its link, never the values) plus the pod's ServiceAccount
and profile label.

## 8. The dispatch exemption

### 8.1 The rule

In `admitCreate`, where it now refuses (`sessions.go:699-702`):

- If `c.kind` is a session and `c.depth` is 3 (its own label exactly `"2"`,
  as `strconv.Itoa` writes it), read the session live under the create lock.
  Let it pass the depth check only when `--enable-session-raise-dispatch` is
  on, the live UID equals the caller's session UID, and the record is valid,
  active, names that UID and has `startSessions`.
- Any other depth above 2 is refused as now. The message gains one sentence
  when a raise could help: "A session at depth 2 can be raised to start
  sessions (agent-run help raise)."
- The child cap (`sessions.go:703-710`) and every other admission run
  unchanged. `newSession` writes the true label `"3"` (`validate.go:387`)
  and `spec.parent` the raised session (`:268`).
- The child's profile follows `validate.go:315-348` as today, with Ruling 7:
  a session raised to workbench is not a workbench launcher, so its child
  runs on the profile it names, except `workbench`, or on the default.
- Only the direct create (`createSessionMode`). Scheduled work
  (`CreateScheduledSession`), the standby and summoned creates keep today's
  rule.

### 8.2 Every reader of the depth label

`git grep -n LabelDepth -- internal cmd api` at `2774e599`, and the readers of
the caller's depth:

| Reader | What it does for a child at depth 3 | Fail closed |
| --- | --- | --- |
| `sessionDepth` and `resolveSession` (`caller.go:325`, `:332-340`) | `c.depth` is 4; `admitCreate` refuses (`sessions.go:700-702`). It starts nothing | Yes |
| `newSession` (`validate.go:384-388`) | Writes `"3"` | Not a reader |
| `NamedCoordinatorLaunchAllowed` and `NamedCoordinatorSessionEligible` (`named_coordinator.go:75-110`) | No shape admits depth 3, so it never runs as a named coordinator identity, and its raised parent never launches one | Yes |
| Broker `ancestry` (`delegate_reach.go:63-67`) | Depth above 2 is "ambiguous ancestry depth": no delegate decides its grants and it is never a delegate. Its grants go to a standing policy, Tom's grant card, or the 30-minute deny | Yes |
| Broker `topLevelSession` and `listedCreator` (`internal/broker/maintenance_attestation.go:121-129`, `:181-189`) | Not top-level, not a listed creator: no attested maintenance | Yes |
| Broker `CheckLegacyDelegate` (`legacy_delegate.go:91`) | Requires `"1"`: refused | Yes |
| `instructionPublisherImageEligible` (`internal/controller/instruction_image.go:43-53`) | Requires `"0"` or `"1"`: not eligible | Yes |
| API `sessionAncestry` (`grant_decision.go:299-320`, bound `maxDelegateAncestry` = 4 at `:61-64`) | Walks its three session parents, within the bound. Used by `delegateReach` (`:259-284`) and the thread fence (`threads.go:139`) on the real chain | Follows the chain; the broker refuses a depth-3 grant anyway |
| `buildSummonSession` (`internal/apiserver/summon.go:668`) | Writes `"0"` for a summoned session | Not a reader |
| Usage rollup (`internal/apiserver/usage.go:146-157`) | Breadth-first, capped at 64 sessions, no depth bound: its usage rolls up to its ancestors | Not a guard |
| The raised session itself | Every reader sees depth 2, as before | Unchanged |
| haynes-ops (Kyverno, NetworkPolicy, dashboards) | Unknown: not in this repo | Unknown |

The comment on `LabelDepth` (`labels.go:113-116`, "A session at depth 2
creates no children") and DESIGN-001 section 3.4 gain the exemption by
reference to ADR-028. DESIGN-001 is accepted and is not edited.

## 9. Taking it back

`agent-run raise <session> --take-back [--keep-running]`, or
`DELETE /v1/sessions/{name}/raise`.

- **Who.** Anyone who may ask under Ruling 1, and the target itself. No
  approval: taking power back never waits for one.
- **The record.** `state` becomes `taken-back`, with `takenBackBy` and
  `takenBackAt`. It is kept. An open raise card for the session is withdrawn.
- **The right to start sessions** ends at the next create: `admitCreate`
  reads the record live (section 8.1).
- **The profile.** The pod keeps its ServiceAccount until it ends. So a
  take-back of a profile raise suspends the session at once through the
  existing route, rescue first, with no wait for quiet. Its next pod runs on
  `spec.profile`. `--keep-running` leaves the pod until the session's next
  resume, and `show` says the pod still runs as the raised account.
- **Children it started.** They keep running and keep counting against the
  cap. They start nothing. They are never reaped by a take-back; the caller
  may reap them by the normal route, which rescues first.
- **Grants.** The take-back writes `spec.release` on the session's active
  and pending grants whose requester profile is the raised profile, as the
  session's own release does. A pod-bound grant also ends with the pod.
  Whether the broker matches a standing policy again when an active grant's
  requester changes profile is Unknown; this release does not depend on it.
- **An operator without the route, or with both flags off.** Suspend the
  session and resume it: its next pod runs on `spec.profile` (safety rule 12).

## 10. What readers show

**API.** `GET /v1/sessions/{name}`, `GET /v1/sessions` and the fleet view
carry one new optional object, `raise`, only on a session with a raise record
or an open raise card:

```json
"raise": {
  "state": "active",
  "profile": "workbench",
  "fromProfile": "full",
  "startSessions": true,
  "askedBy": "session/<asker>",
  "decidedBy": "authentik/<username>",
  "decidedAt": "2026-11-13T16:02:11Z",
  "ask": "ask-0123456789abcdef",
  "podProfile": "full",
  "ignored": ""
}
```

`state` may also be `taken-back`, `waiting` (a card is open; single-session
view only, since it needs a card lookup) or `ignored` (with `ignored` giving
the reason). `podProfile` is the current pod's profile label, on the
single-session view only. The top-level `profile` stays `spec.profile`.

**`agent-run show`:**

```
profile:      full (at creation)
raised:       workbench; may start sessions (decided by authentik/<username> at 2026-11-13T16:02:11Z, asked by session/<asker>, ask-0123456789abcdef)
pod runs as:  full; the raised profile applies at its next start (agent-run raise <name> --restart)
```

Other forms: "raise waiting for Tom (`<card>`)"; "raise answered, not
applied: `<reason>`"; "raise taken back by `<who>` at `<time>`; the pod still
runs as workbench until it is suspended"; "raise record ignored: `<reason>`".

**`agent-run list`** adds to its NOTE column (`internal/agentrun/sessions.go:68`,
`note` at `:82`): `raised: workbench`, `raised: may start sessions`,
`raised: workbench at next start`, or `raise taken back; pod still raised`.
No new column, so no script that reads the columns changes.

**`agent-run fleet`** adds one header line, `raised:   <name> (workbench),
<name> (may start sessions)`, and the same NOTE text.

**Portal.** The raise card on the Asks board is new (section 6.3). The
session summary does not change in the first build: `decodeSessionRow`
refuses any key outside its six (`web/portal/session/product.go:520-524`). A
later slice may add a mark; haynes-ops then pins the portal before the
operator that sends it.

**Front door.** `session_status` is unchanged in this design.

**The command.** Tom's word is "elevate"; the command is `raise`:

```
agent-run raise <session> --profile <name> --reason "<why>"
agent-run raise <session> --start-sessions --reason "<why>"
agent-run raise <session> --profile workbench --start-sessions --reason "<why>"
agent-run raise <session> --restart
agent-run raise <session> --take-back [--keep-running]
```

It reads the session first and sends its UID as `expectUID`. After an ask it
prints "Asked Tom on the approvals board (`<card>`). `<session>` keeps
running as it is until he answers." Its refusals are the sentences of section
6.5. `agent-run help raise` says what a raise is in under 80 words.

**Older readers.** An `agent-run` older than the slice that adds `raise`
reads `GET /v1/sessions/{name}` strictly on `show --json --once` and `usage
export` (`strictWireJSON`, `internal/agentrun/show_once.go:47`). It cannot
read a raised session on those two paths, and reads every other session as
before (safety rule 14). The test: an unraised session's view holds no
`raise` key, as `TestShowSaysTitleRenameOff` does for titles.

## 11. Slices

Each slice is one worker session, behind a flag that is off by default, and
releasable alone. Tests use fakes or envtest at two CPUs. A new package (for
example `internal/sessionraise`) gets its line in `docs/repo-layout.md`. Docs
that describe the changed behaviour change in the same PR.

| Slice | What it changes | Tests must prove | Other-family review | CRD copy | Pins |
| --- | --- | --- | --- | --- | --- |
| 1. Record and readers | Parse and validate the annotation; the effective state; the `raise` key in the views; `show`, `list` and `fleet` text; the two flags, parsed and off | Every shape rule of 6.1 accepts and refuses; a UID mismatch, a bad version, an unknown profile and both flags off are ignored, with the reason in `show`; an unraised session's view has no `raise` key; an annotation write leaves `metadata.generation` unchanged (envtest) | No: display only, grants nothing | No | Operator; agent image (agent-run) |
| 2. The right to start sessions | The exemption in `admitCreate` (section 8.1); the profile rule for a raised workbench parent; the extra sentence | A depth-2 caller with an active record creates a child with label `"3"`, its own `spec.parent`, its profile, under the cap; the fifth unfinished child is 429; a depth-3 caller is refused with or without a record; taken back, UID mismatch, flag off, `"02"` and missing labels are refused; the record is read live under the lock; scheduled work stays refused; a table test of every reader in 8.2 at depth 3 | Yes: overrides a global guard | No | Operator |
| 3. The profile at the next pod | The effective profile in `buildSessionPod`; Ruling 7 in `validate.go` and `named_coordinator.go` | The pod gets the raised profile's account, env, envFrom, remote block, mounts and labels; flag off, taken back and UID mismatch build from `spec.profile`; the hold pod and the volume are unchanged; a named coordinator profile is never used; a raised workbench session cannot name `workbench` for a child or launch a named coordinator identity, and a workbench-by-creation session still can | Yes: grants access | No | Operator |
| 4. The route, Tom's own decision, take-back | Both routes; the asker and target rules; rate limits; take-back with suspend and grant release; `agent-run raise` with `--restart` and `--take-back`; the haynes-ops admission fence of safety rule 15 (a haynes-ops PR, not this repo) | Every row of 6.5; Tom's ServiceAccount writes the record; any other asker gets "the approvals board is not built yet" until slice 5; a take-back suspends at once and releases the raised grants; `--restart` refuses on busy, waiting, attached and a stale heartbeat, and calls suspend then resume otherwise | Yes: a write route with an access rule, and it suspends live sessions | No | Operator; agent image |
| 5. The raise card | The `raise` kind in the Ask CRD and wire types; store caps; `POST /v1/asks` and withdraw refusals; the applier; the portal card | A card with the typed scope and no expiry is accepted; an info or approval Ask with a `raise` object is refused; the caps; a session cannot create, withdraw or answer it; one approved answer writes one record, and the repeat writes nothing; a changed UID, profile, depth, pod profile or digest writes nothing; a decline writes nothing; an answer without a username is refused (needs ADR-021 build PR 6) | Yes: a stored answer grants access | Yes, the Ask CRD, first | Operator; portal |
| 6. Listings | The portal session mark and the front door's `session_status`, if the coordinator wants them | The portal decoder accepts the new key and still refuses others | No | No | Portal before operator |

**Install order.** Ask CRD copy in haynes-ops; the admission fence of safety
rule 15; the operator, portal and agent image pins; then the flags, set by
the coordinator: `--session-raise-profiles=workbench` and
`--enable-session-raise-dispatch`.

**First live proof.** On an unused test session, never a working coordinator
and never `haynesnetwork-1113-150420`. Create it at depth 2 on the default
profile, through a test parent at depth 1. Read R0 (DESIGN-065 section 5's identity readings, plus the pod's
ServiceAccount and profile label). Ask for `--profile workbench
--start-sessions`. Tom answers the card once; this is a real raise, so it
needs him. Read R1: identity unchanged, pod unchanged. Run `--restart`. Read
R2: same name, UID, conversation id and both hashes; ServiceAccount
`dev-env-workbench`. Create one child: label `"3"`. From the child, try a
create: refused. Take back with the default suspend. Resume. Read R3:
ServiceAccount `dev-env-agent`; a create from the session is refused. Record
R0 to R3 on story 830, then reap the test sessions under the normal rules.

## 12. Open questions for the owner

Asked one at a time by phone prompt, in plain words, after the coordinator
accepts the rest.

- **Q-066-01. Who says yes.** "When a coordinator asks to give a session
  more power, should it wait for your tap on the approvals board (it is
  certainly you, and it costs you one tap), or may the coordinator do it on
  your word in its own chat (faster, but the platform cannot tell it was
  you)?" Recommendation: your tap. The raise can make a session a superuser,
  and the board is the place the saga already sends risky access (decisions
  97 and 98).
- **Q-066-02. Restart right after your yes.** "A new profile needs the
  session to restart. It keeps its conversation and its place in the Claude
  app, but a command it is running in the background stops. After you say
  yes, should the coordinator restart it the first time it is quiet, or leave
  it until it next restarts by itself?" Recommendation: restart it when
  quiet, done by the coordinator, never while the session is working or
  waiting on you.
- **Q-066-03. How long a raise lasts.** "Should a raise last until someone
  takes it back or the session is removed, or end by itself after a set time,
  such as 7 days?" Recommendation: until taken back or removed. A coordinator
  lives for days, and an end date would ask you again for the same thing.
- **Q-066-04. Workers of a raised session.** "When a raised session starts
  its own workers, should they be ordinary, guarded workers, or superusers
  like it?" Recommendation: ordinary and guarded. A raise is for one session
  you chose, and the workers it starts are new sessions you did not see.

## 13. What this rejects

| Rejected | Why |
| --- | --- |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie about who started the session, and where. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise `maxSessionDepth` to 3 for everyone | It is one of three guards against runaway agents (DESIGN-001 design line 4983-4986). One session's need does not justify a fleet change. |
| Start a new session to continue the work | That is what happened in the case, and it loses the conversation. |
| An AccessGrant of a new type | A grant is always requested by the session for itself (`grants.go:161-170`), which is the self-raise this design forbids. It lasts at most 8 hours. A standing GrantPolicy could approve it. The broker installs access into a pod; it does not choose a pod's own identity, and it does not write AgentSessions. |
| A separate CRD for raises | A new CRD, RBAC, CRD copy and watch, for the trust the fenced annotation already gives. The controller already chooses a pod's account from API-written metadata on that trust: the named coordinator admission label (`pod.go:391-393`). |
| A status field | A stale CRD prunes it (haynes-ops #3502), an older operator's status write drops it, and status is what the operator observed, not a decision. |
| Reuse the `approval` Ask kind | An approval grants no machine action and expires within 24 hours (ADR-010). ADR-021 added a kind for the same need; this follows it. |
| The operator restarts the session itself when idle | `docs/remote-session-upgrades.md` keeps automatic stops away from a user's remote session. A coordinator reads busy while a background command runs (`activity.go:251-263`), so an idle moment may never come. |
| Change the identity of the running pod | A pod's ServiceAccount is fixed for its life. No exec or token step changes what the pod is. |
| A workbench coordinator decides a raise | Ruling 1. |
| A standing policy approves raises | No automatic yes for superuser power. |
| A raise to a named coordinator identity | One holder at a time and three launch shapes (section 2.4). A raise would bypass both. |
| Raised sessions pass workbench on to their children | Ruling 7 and Q-066-04. |
| Meatball asks for a raise | Not now. Its caller kind has its own route list (`caller.go:33-35`). A front-door story may add the ask route later; Tom would still decide. |
