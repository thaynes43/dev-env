# DESIGN-066: Raise a running session from outside it

- **Status:** Proposed 2026-11-13, for the coordinator's review. Revised the
  same day after Tom answered Q-066-01 (section 1.2): the go-ahead is his
  word to an agent, not a tap on the Asks board. The coordinator ruled on the
  other three questions (section 4, rulings 3, 7 and 8). Nothing is built or
  installed. No flag, CRD change, route, command or haynes-ops change exists
  for it. [ADR-028](../adrs/028-raise-a-running-session.md) is Proposed with
  it.
- **Tracking:** [Story OP830](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/830).
  Tom's ask is recorded on story 828 comment 7927, and his answer on how he
  gives the go-ahead on story 830 comment 7980. Why the case's session
  started where it did is story 831 (section 1.4).
- **Origin:** Tom, 2026-11-13, quoted in sections 1.1 and 1.2. Nothing else
  here is Tom's. The coordinator's reading of his answer and its rulings are
  marked as the coordinator's. The rest is proposed by a worker for the
  coordinator session `dev-env-fable-coordinator-1109`, which decides it.
- **ADR:** [ADR-028](../adrs/028-raise-a-running-session.md), Proposed. It
  adds one named exemption to the depth rule of DESIGN-001 section 3.4 and
  one optional right to the front-door CallerPolicy of DESIGN-060 section
  7.1, so it is accepted before any slice merges.
- **Builds on:** [DESIGN-001](001-dev-env-v2.md) section 3.4, D-19, D-46 and
  D-88 (agent attestation); [DESIGN-060](060-meatball-front-door.md) section
  7.1 (the CallerPolicy); [DESIGN-065](065-session-display-titles.md) section
  9.4 and [ADR-027](../adrs/027-session-title-annotation.md) (a later change
  is an annotation, not a spec write).
- **Evidence:** dev-env `2774e599` (origin/main), source and docs only.
  DESIGN-042 is not on main; it was read on the branch of open dev-env PR 782
  at `5449647f`. The facts of the case and the installed flags and rights in
  section 2 are the coordinator's readings of 2026-11-13, quoted and not
  measured again. Nothing in the cluster, the tracker or any session was read
  or written for this document.

## 1. Problem and the case

### 1.1 The owner's ask

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

### 1.2 The owner's answer on the go-ahead

The coordinator asked Tom by phone prompt on 2026-11-13: "You asked for a
way to raise an agent's permissions from outside its session. The design is
drafted (nothing built yet). One choice is yours: how do you give the
go-ahead each time a session is raised, for example to superuser?" The
options offered were "My word to a superuser coordinator (Recommended)",
"Word now, Asks-board tap later" and "Only my tap on the Asks board". He
typed his own answer, recorded on story 830 comment 7980:

"My word to a superuser or meatball or a meatball equivalent always on
agent."

The coordinator's reading, not Tom's words: the go-ahead for a raise is
Tom's word. He gives it to one of three kinds of agent, and that agent
performs the raise and records his words: (1) a superuser session, that is a
session created on the workbench profile; (2) Meatball, the account the front
door runs as under its CallerPolicy; (3) another always-on agent that stands
where Meatball stands, admitted the same way. No tap on the Asks board is
required.

### 1.3 The case

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

### 1.4 Not in this design: why it started there

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

### 2.4 Who runs as the workbench account, and who may write a session

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
- The coordinator's read of 2026-11-13: the workbench account cannot patch or
  update AgentSessions or Asks in `dev-agents`. `kubectl auth can-i` answered
  no for patch and update on `agentsessions`, patch on `asks`, and update on
  `asks/status`.
- DESIGN-042 is not on main. On the branch of open PR 782, its section 6.4
  proposes a new named delegate identity and adds no rule to the code above.
  The rule in force is the code.

### 2.5 What an owner's go-ahead can travel through today

| Piece | In source on main | Installed (coordinator's read, 2026-11-13) |
| --- | --- | --- |
| Grant attestation: `POST /v1/grants/{name}/attestation`, `--enable-grant-attestation` | Yes (`internal/apiserver/grant_attestation.go:15-22`; `cmd/dev-env-operator/main.go:257`) | On: `--enable-grant-attestation=true` |
| Asks board, `--enable-asks` | Yes (`main.go:263`) | On: `--enable-asks=true` |
| Inbox questions, `--enable-inbox-questions` | Yes (`main.go:366`) | On: `--enable-inbox-questions=true` |
| Keeper push for new Asks, `--enable-ask-notifications` | Yes, default off (`cmd/dev-env-keeper/main.go:269`) | On: `--enable-ask-notifications=true` |
| Delegate decisions, `--grant-delegates` | Yes (`main.go:260`; `internal/apiserver/grant_decision.go`) | `--grant-delegates=dev-agents/dev-env-agent` |
| Grant Ask kind and the broker's apply, `--enable-grant-asks` | Yes (`api/coordination/v1alpha1/ask_types.go:62`; `cmd/dev-env-operator/broker.go:62`) | Not installed. The coordinator read no grant-Ask flag, and the installed Ask CRD has no enum on the Ask's kind field, so, in the coordinator's reading, the grant kind and its card are not installed |
| Portal grant card and `preferred_username` (ADR-021 build PR 6) | No: `web/portal` has no `preferred_username` | No |
| Meatball's front-door CallerPolicy (DESIGN-060 section 7.1) | Yes (`internal/apiserver/callerpolicy.go:30-58`, `:133-150`; `api/v1alpha1/callerpolicy_types.go:56-96`) | Unknown today. On 2026-11-10 no CallerPolicy object existed (saga decision 97) |

**Attestation.** The grant attestation route records "its claim that it
obtained the user's or a coordinator's approval ... The claim is agent
attestation, not verified human identity" (`grant_attestation.go:15-22`). The
attestor comes from the caller's token; the body carries the claimed approver
and a nonsecret reference (`grant_attestation.go:23-60`;
`internal/grantattest/attest.go:52`, `:93-107`). D-88 in DESIGN-001 (design
line 4556, table row at line 5896) is the owner's ruling that allows it. A
raise on Tom's word is the same kind of record: the platform cannot verify
his word, so it records the performing agent's statement of it.

**The CallerPolicy.** Meatball's caller class exists only while the live
policy `dev-env-system/meatball` is valid; it is read uncached, and removal
revokes the caller at once (`callerpolicy.go:28-58`). The schema fixes its
account to `dev-env-system/dev-env-meatball` (`callerpolicy_types.go:60`).
Its routes are a positive list: list and create sessions, read and reap only
sessions it started, and parent notes for those (`callerpolicy.go:133-150`,
`:171-173`, `:204-210`). DESIGN-060 section 7.1 says the front-door shape
"has no effort field or configurable rights", and section 6 says Meatball
"never gets exec, grants or the workbench". A summoning caller is a second
policy shape, `spec.summon`, one object per account named after it
(`callerpolicy_types.go:11`, `:93-116`; `internal/apiserver/summon.go:41`).

**Free text and secrets.** No check in the API refuses a secret-shaped value
in free text. What exists: the front door scrubs an OpenProject token shape
and its own bearer from command output (`internal/frontdoor/opwork.go:20`,
`:133-139`), and `grantattest.ValidRef` allows only a short reference of
letters, digits and `_ . : / # -` (`attest.go:52`, `:104-107`), which cannot
hold a sentence.

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
  Control entry. It stays Unknown until the live proof (section 7.4).
- Whether Meatball's CallerPolicy is installed today.
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- What the workbench profile's env, envFrom, mounts and remote block hold
  today (haynes-ops `templates.yaml`).
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.
- Which token shapes the secret check of section 6.3 should name beyond the
  one the repo already scrubs.

## 3. Outcome

An action taken from outside a session, on Tom's word, raises what that
running session may do. The agent Tom gave his word to performs the raise
and records his words. The session keeps its name, UID, conversation,
volume, work in progress and its entry in the Claude app. Two things can be
raised:

1. its **profile**, for example from the default profile to `workbench`;
2. its **right to start sessions**, for a session at depth 2.

The session can never raise itself. `spec.parent`, `spec.profile`, the depth
label and the creation record are never rewritten. `maxSessionDepth` stays
2. Every raise carries Tom's words, is logged, and can be taken back.

## 4. Rulings proposed

Rulings 3, 7 and 8 contain the coordinator's rulings of 2026-11-13 on the
former Q-066-02 to Q-066-04, marked as such. Everything else here is
proposed by a worker for the coordinator. None of it is Tom's, except the
sentence quoted in section 1.2.

**Ruling 1. Who performs a raise, on whose word.** Tom decides, by his word
(section 1.2). The platform cannot verify his word. It records the performing
agent's statement of it, as the grant attestation route records "agent
attestation, not verified human identity" (section 2.5). The operator already
runs that route (`--enable-grant-attestation=true`, the coordinator's read of
2026-11-13).

These may perform a raise:

1. **A superuser session.** A session that is workbench by creation (Ruling
   7), that is not the target and does not sit under the target. "Under" is
   read live: the target is not on the performer's chain of parents, matched
   by name and UID, as `sessionAncestry` walks it
   (`internal/apiserver/grant_decision.go:299-320`). Any depth.
2. **An always-on agent under a CallerPolicy that grants the raise action.**
   Meatball first: its front-door policy gains one optional field,
   `spec.raise` (Ruling 9). Absent means no raise. "A meatball equivalent
   always on agent" is another caller admitted by its own front-door policy,
   with its own `spec.raise`; it is not a new mechanism (Ruling 9 says what
   admitting it needs).
3. **Tom's own ServiceAccount** (`kindHuman`, `caller.go:24-26`). The call is
   his word.

These never perform a raise: the target; any session under it; a session
raised to workbench; an ordinary session; a named coordinator session; a
coordinator host; a summoning caller (`kindSummon`); the portal; a trusted
client (`kindClient`, which includes the v1 pod, `caller.go:27-29`, and is
none of the three kinds Tom named).

**The agent that heard Tom performs the raise.** A relay is refused: a
session records only words Tom gave it, in its own phone prompt or in a
message he typed to it; a policy caller records only words he gave that
agent in chat (section 6.3). An ordinary session that needs a raise tells its
coordinator, and the coordinator asks Tom itself.

Rejected: the Asks-board card of the first draft (section 13); a trusted
client; a relay; Tom's ServiceAccount only.

**Ruling 2. The record and the audit record.** One annotation on the
AgentSession, `dev-env.haynesops.com/raise`, holding one JSON object (section
6.1). Only the operator writes it, from the raise route and the take-back
route. It names the session's UID, and every reader compares that with
`metadata.uid` and ignores a record that does not match. It is not a spec
write, so `metadata.generation` does not move (ADR-027).

The record carries Tom's word, since that is the evidence: what he said,
verbatim and bounded; where he said it; when; and the caller that performed
the raise, taken from the token, never from the body. A raise without his
words is refused.

**The audit record is the operator's, not the agent's.** It has two parts,
both written by the operator from the authenticated caller, so the agent can
neither skip nor edit them: the annotation, which holds the current or last
raise of the session with its words; and one operator log line per raise and
take-back, with the same fields, which keeps the history after a record is
replaced or the session is removed. The performing agent may also write the
words on its tracker story, and should when it works from one, but that is
not the record: the tracker is written as the agent and can be down.

It is trusted as the named coordinator admission label is trusted
(`named_coordinator.go:55-58`, `pod.go:391-393`): only the operator writes
it. The workbench account cannot patch or update AgentSessions today
(section 2.4). Before any flag is set, an admission rule in haynes-ops also
refuses a change to this annotation from any user but the operator's
ServiceAccount (safety rule 13), so a later grant of patch to another account
does not open it.

Rejected: a spec write (D-39, and generation in four places, ADR-027); a
status field (a stale CRD prunes it, haynes-ops #3502, an older operator's
status write drops it, and status is what the operator saw, not a decision);
a separate object (a new CRD, RBAC, CRD copy and watch, for the same trust
the fenced annotation gives); an AccessGrant (section 13); the tracker as the
record.

**Ruling 3. The profile, and when it applies.** The effective profile is the
record's profile when the record is active, names this UID, and its profile
is on the operator flag `--session-raise-profiles`. Otherwise it is
`spec.profile`. The operator reads the effective profile in one place: when
it builds a session pod (`pod.go:320`). A running pod keeps its
ServiceAccount, so a profile raise takes effect at the session's next pod.
The operator never restarts a session for a raise.

The coordinator's ruling of 2026-11-13 on the former Q-066-02: a raised
profile is applied by the explicit restart step, `agent-run raise <session>
--restart` (section 7.3). The acting agent takes that step at the session's
first quiet moment, and never while the session is busy or waiting on a
question. If it is not taken, the profile applies at the session's next
resume.

`--session-raise-profiles` is empty by default. A profile that runs as a
named coordinator identity is never given, whatever the flag says: the route
and the controller each check the profile against the templates they read,
and a templates read that fails gives nothing. The one-holder rule and the
launch shapes of section 2.4 stay as they are. The expected value is
`workbench`. The workbench rule needs no change: the controller's account
choice and the API's classification both follow the pod (section 2.4). One
rule is new: Ruling 7.

**Ruling 4. The right to start sessions.** In `admitCreate`, a session caller
whose own depth label is exactly `2` passes the depth check when, read live
under the create lock, its record is active, names its UID and grants
`startSessions`, and the operator flag `--enable-session-raise-dispatch` is
on. Section 8 gives the rule and every reader of the depth label. The child
gets the true label `3`, `spec.parent` the raised session, the same child cap
and every other admission. A session at depth 3 is never exempt and starts
nothing. The exemption serves the direct create only, not scheduled work,
because a schedule outlives the moment Tom gave his word.

**Ruling 5. Taking it back.** Anyone who may perform a raise under Ruling 1,
and the target itself, may take one back. Taking back needs no word from Tom:
taking power back never waits for one. The record stays, marked taken back,
with who and when. The right to start sessions ends at the next create. A
profile raise ends at the next pod, so a take-back suspends the session at
once through the existing route, rescue first, unless the caller passes
`--keep-running`. Children already started keep running and keep counting
against the cap. Section 9.

**Ruling 6. What readers show.** The view's `profile` stays the creation
profile. A raise is shown beside it, with Tom's words, in one new key that
appears only on a session that has a raise record. Section 10.

**Ruling 7. A raise never spreads.** A session raised to workbench is a
workbench session for its own pod, its Kubernetes rights and the API routes
that check the workbench account. It is not one for three things: it may not
perform a raise; it may not start a child on the workbench profile; it may
not launch a named coordinator identity. The API tells the two apart by
`spec.profile`: a session is workbench by creation only when its
`spec.profile` names a profile that runs as the workbench account. A
templates read that fails counts as "not by creation".

The coordinator's ruling of 2026-11-13 on the former Q-066-04: a raised
session's own workers are ordinary, never superusers. So a child of a session
raised to workbench runs on the profile it names, except `workbench`, or on
the default.

**Ruling 8. How long a raise lasts.** The coordinator's ruling of 2026-11-13
on the former Q-066-03: a raise lasts until it is taken back or the session
is removed. It has no end date. It survives a suspend and resume, because the
annotation and the UID stay. A restore makes a new session with a new UID,
which gets nothing.

**Ruling 9. The raise action in a CallerPolicy.** The smallest addition to
the front-door shape of `CallerPolicySpec` (`callerpolicy_types.go:56-96`):

```yaml
spec:
  # ... the seven front-door fields of DESIGN-060 section 7.1, unchanged ...
  raise:
    profiles: [workbench]   # at most 2 DNS labels; empty means no profile raise
    startSessions: true     # may raise a depth-2 session to start sessions
```

- `raise` is optional. Absent, the caller may not raise, and its route list
  is exactly as today.
- A CEL rule allows `raise` only with the front-door shape, never with
  `spec.summon`, so a summoning caller can never raise.
- `readCallerPolicy` (`callerpolicy.go:30-58`) checks the field and fails
  closed on a bad one. A profile must also be on `--session-raise-profiles`;
  the policy narrows the flag and never widens it.
- `policyCallerAllowed` (`callerpolicy.go:133-150`) adds the three raise
  routes of section 6.2 only when the live policy has `raise`.
- Reach: a policy caller with `raise` may raise any eligible session, not
  only the sessions it started. Tom names the session in his word, and the
  case's session was not Meatball's. Its other routes keep their own-sessions
  fence.
- This amends DESIGN-060 section 7.1 ("no ... configurable rights") and
  section 6 ("never gets ... the workbench") for this one right: Meatball
  itself still never runs as the workbench; on Tom's word it may raise
  another session to it.

**A Meatball equivalent.** Today the front-door shape admits only Meatball:
the schema fixes its account (`callerpolicy_types.go:60`), and the API reads
one fixed object, `dev-env-system/meatball` (`callerpolicy.go:21-24`). A
second always-on agent needs that shape to admit another account, one policy
object per account, as the summon shape already names its object after its
account (`callerpolicy_types.go:11`). That is its own story, outside these
slices. When it lands, that agent's policy carries its own `spec.raise`, and
nothing in this design changes.

## 5. Safety rules

1. **No self-raise.** The target never performs its own raise. No session
   under the target performs one. No session raised to workbench performs
   one.
2. **No word, no raise.** Every raise carries Tom's words, where he said
   them and when. A raise without them is refused. A word is used once: after
   a take-back, a raise whose words, place and time equal those of the
   taken-back record is refused. A retry of the active raise with the same
   words is answered 200 and writes nothing.
3. **The agent that heard him performs it.** A session records only words
   Tom gave it; a policy caller only words he gave it in chat; Tom's own
   account only its own call. A relay is refused.
4. **Only the named kinds perform** (Ruling 1). No GrantPolicy, delegate,
   coordinator host, client, summoning caller, ordinary session or portal
   performs a raise. A CallerPolicy caller performs only with `spec.raise`.
5. **Bound to the UID.** The record carries the target's UID. Every reader
   compares it with the live session. A later session with the same name
   gets nothing. A record copied onto another object is ignored.
6. **Provenance stays true.** `spec.parent`, `spec.profile`, the depth label,
   the creation log line and the volume's labels are never written. The
   view's `profile` stays the creation profile.
7. **The global limit stays.** `maxSessionDepth` stays 2. One named
   exemption, for a session at depth 2 only. Nothing at depth 3 starts
   anything. The child cap is unchanged.
8. **Never a named coordinator identity.** A raise never gives such a
   profile, and a raised session never launches one.
9. **A raise never spreads** (Ruling 7). A raised session's workers are
   ordinary.
10. **No restart on its own.** The operator and the raise route never
    suspend, resume or replace a pod for a raise. `--restart` is an explicit
    step that refuses unless the session reads quiet. A take-back suspends at
    once, because taking power back must not wait for the session to agree.
11. **Eligible sessions only.** A Claude session in `remote` or `local` mode
    that is not summoned (`spec.caller`), not a project canary or private
    pilot, not budget-bound, not a coordinator host's child, not on a named
    coordinator identity, not being reaped and not archived. Everything else
    is refused.
12. **Rate limits.** Per target: one record change in any 60 seconds (429).
    Per performer: at most three raises in any rolling hour (429); take-backs
    are not counted.
13. **A fenced record.** Before any flag is set, haynes-ops refuses a change
    to the annotation `dev-env.haynesops.com/raise` from any user but the
    operator's ServiceAccount.
14. **Fail closed on rollback.** With both flags off, or on an operator
    without this build, the record is ignored: new pods use `spec.profile`,
    and the depth limit holds. A pod already running as a raised account
    keeps it until it ends (`pod.go:546-547`). On such an operator, a raise
    is taken back by suspending the session and resuming it. A stale
    CallerPolicy CRD prunes `spec.raise`, so the policy caller has no raise
    right.
15. **Schema order.** The AgentSession and Ask CRDs do not change. The
    CallerPolicy CRD gains `spec.raise` in slice (c), so haynes-ops gets that
    copy before the operator pin that reads it.
16. **Older readers.** A new key appears in the session view only on a
    session with a raise record (DESIGN-065 section 9.7). An `agent-run`
    older than the slice that adds it cannot read that one session with
    `show --json --once` or `usage export`.
17. **Review.** Each slice that grants access or overrides a guard gets an
    adversarial review by another model family before it merges (section 11).

## 6. Record and API

### 6.1 The record

The annotation `dev-env.haynesops.com/raise` holds one JSON object of at most
2 KiB:

```json
{
  "v": 1,
  "sessionUID": "<metadata.uid of the raised session>",
  "state": "active",
  "profile": "workbench",
  "fromProfile": "full",
  "startSessions": true,
  "owner": {
    "words": "<what Tom said, verbatim>",
    "where": "phone-prompt",
    "session": "<the performing session's name>",
    "saidAt": "2026-11-13T15:50:02Z",
    "ref": "op:830#7980"
  },
  "performedBy": "session/<performer>",
  "performedAt": "2026-11-13T15:52:40Z",
  "takenBackBy": "",
  "takenBackAt": ""
}
```

- `state` is `active` or `taken-back`.
- `profile` is empty or a DNS label. At least one of `profile` and
  `startSessions` is set.
- `fromProfile` is the profile label on the pod when the raise was performed.
- `owner` is section 6.3. `performedBy` is `caller.String()` of the
  authenticated caller, never from the body. Times are RFC 3339 in UTC.

A reader that finds any other shape, version or UID ignores the record, and
`agent-run show` says why. A new raise for a session whose record is taken
back replaces the record. The earlier one stays in the operator log.

### 6.2 The routes

**`GET /v1/sessions/{name}/raise`** answers the session's name, UID, depth
label, creation profile, current pod profile and raise record. It exists so
that a policy caller, whose session read covers only its own sessions
(`callerpolicy.go:204-210`), can read the UID it must send. Any caller that
may perform or take back a raise may read it.

**`POST /v1/sessions/{name}/raise`** performs a raise:

```json
{
  "expectUID": "<uid the caller read>",
  "profile": "workbench",
  "startSessions": true,
  "owner": {"words": "<verbatim>", "where": "phone-prompt", "saidAt": "<RFC 3339>", "ref": "op:830#7980"}
}
```

- `expectUID` is required; a mismatch is 409.
- The checks of section 6.4 run. The API writes the record with the
  `resourceVersion` precondition and retry of `setOperatingMode`
  (`lifecycle.go:40-160`), writes the log line of Ruling 2, and answers 200
  with the session view.
- The same raise already active with the same words: 200, nothing written.

**`DELETE /v1/sessions/{name}/raise`** takes a raise back:

```json
{"expectUID": "<uid the caller read>", "keepRunning": false}
```

It marks the record taken back, writes its log line, and acts as section 9
says. 200 with the view.

**Flags.** `--session-raise-profiles` (a comma list of profiles a raise may
give) and `--enable-session-raise-dispatch`, both off by default. With both
off, `POST` answers 403, "This operator does not raise sessions.", and does
nothing else. `GET` and `DELETE` still work: taking a raise back is never
switched off. An operator without the routes answers 404, and `agent-run
raise` says the operator does not have them.

**What the front door needs so Meatball can call them.** `spec.raise` on its
policy (slice c), the three routes on its route list when that field is set
(slice c), and two tools in `internal/frontdoor/tools.go`, beside
`session_status` and `session_stop`: `session_raise(name, profile,
start_sessions, words, said_at)` and `session_take_back(name, keep_running)`
(slice d). The tool reads the UID with `GET`, then calls `POST` or `DELETE`
with the front door's own token, sends `where: chat`, and passes Tom's words
through unchanged. Tool descriptions stay under 30 words (DESIGN-060
section 6).

### 6.3 Tom's words

| Field | Rule |
| --- | --- |
| `words` | Required. 1 to 500 characters, at most 1 KiB of UTF-8, verbatim as Tom said or typed them. No control or format characters. Refused when it matches a token shape (below). |
| `where` | Required. `phone-prompt`: Tom's answer to this session's phone prompt, or a message he typed to this session; only a session caller sends it, and `session` is set by the API to the caller's own name. `chat`: a chat message to the always-on agent that performs the raise; only a policy caller sends it. `own-call`: only Tom's ServiceAccount sends it, and `words` is his own reason. Any other pairing of caller and `where` is refused. |
| `saidAt` | Required. When Tom said it, as the agent states. Not more than 5 minutes in the future and not more than 24 hours old. |
| `ref` | Optional. A nonsecret reference to where the words are also kept, such as a tracker comment, in the shape `grantattest.ValidRef` allows (`attest.go:52`). |

**The secret check.** No check exists for free text in the API (section 2.5).
Slice (a) adds one function, used by the route and its tests, that refuses
`words` containing a known token shape. It starts with the shape the front
door already scrubs (`opapi-` followed by 16 or more letters and digits,
`internal/frontdoor/opwork.go:20`). Which other shapes it names is Unknown
today; slice (a) lists them with a test for each. The check cannot prove a
value is not secret; like `ValidRef`, it only keeps out the shapes it knows.

### 6.4 Refusals

Every refusal is one plain sentence.

| Case | HTTP | Sentence |
| --- | --- | --- |
| The caller is the target | 403 | "A session may not raise itself." |
| The caller sits under the target | 403 | "`<caller>` works under `<target>`, so it may not raise it." |
| The caller is a session raised to workbench | 403 | "`<caller>` was raised to workbench, not started on it, so it may not raise others." |
| A policy caller without `spec.raise` | 403 | "This caller's policy does not allow raising sessions." |
| Any other caller not in Ruling 1 | 403 | "Only a superuser session, an always-on agent whose policy allows it, or Tom may raise a session." |
| No `owner`, or empty `words` | 422 | "A raise needs Tom's words: what he said, where and when." |
| `where` does not match the caller | 422 | "A `<caller kind>` records only words Tom gave it directly (`<allowed where>`); have the agent he told perform the raise." |
| `words` match a token shape | 422 | "Tom's words look like they hold a secret. Leave the secret out and send his words again." |
| `saidAt` too old or in the future | 422 | "Tom's words must be from the last 24 hours. Ask him again." |
| The same words as a raise of this session that was taken back | 409 | "These words were already used for a raise of `<name>` that was taken back. A new raise needs a new word from Tom." |
| `expectUID` differs | 409 | "Session `<name>` was replaced: its UID is not the one you read. Read it again." |
| Not eligible (safety rule 11) | 409 | For example "Session `<name>` is a summoned session; summoned sessions are never raised." |
| Profile not on the flag list, or not on the caller's policy | 422 | "This caller may not raise sessions to profile `<p>` on this operator." |
| Profile runs as a named coordinator identity | 422 | "Profile `<p>` runs as a named coordinator identity; a raise never gives one." |
| `startSessions` at depth 0 or 1 | 422 | "Session `<name>` sits at depth `<d>` and may already start sessions." |
| `startSessions` at depth 3 or a bad label | 422 | "Only a session at depth 2 may be raised to start sessions." |
| Within 60 seconds of a change on the target | 429 | "Session `<name>`'s raise changed less than a minute ago. Try again in `<n>` seconds." |
| A fourth raise by this performer in an hour | 429 | "`<caller>` has made three raises in the last hour. Try again after `<time>`." |
| Both flags off | 403 | "This operator does not raise sessions." |

A board tap could be added later as a stricter second factor without
changing the record: the record would gain the card's id, and the route would
write only after Tom's tap. Nothing here builds it (section 13).

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
pod and installs it again into a new pod (`internal/broker/broker.go:566-580`).
A grant whose decision is bound to the requester's pod UID ends or is refused
after any pod change, as on any resume: delegate reach (`RequesterPodUID`,
`internal/broker/delegate_reach.go:175`) and maintenance (the coordinator's
`PodUID`, `grants.go:186`).

### 7.3 The restart step

`agent-run raise <session> --restart`, run by the agent that performed the
raise, or by Tom, after a profile raise is active. By the coordinator's
ruling (Ruling 3), the acting agent takes it at the session's first quiet
moment; if it is not taken, the profile applies at the next resume.

1. It reads the session view. It refuses unless the record is active, the
   pod's profile label differs from the effective profile, the phase is
   Running, the agent state is `idle` (not `busy`, not `waiting`), no client
   is attached, and the last heartbeat is under 90 seconds old.
2. It calls the existing suspend and waits for Suspended, which comes after
   the rescue (D-51).
3. It calls the existing resume and waits for Running and, for a remote
   session, `registered`.

It never forces and never loops. Its refusal says which reading failed, for
example "Session `<name>` is busy. Try again when it is quiet; otherwise the
new profile applies at its next resume." This is an owner-requested suspend
in the sense of `docs/remote-session-upgrades.md`: it acts on the raise Tom
gave his word for.

What remains. A heartbeat is up to 60 seconds old, so a turn or a question
that starts after it is cut by the suspend, as `docs/remote-session-upgrades.md`
states for the drain. The conversation and the volume stay; an open question
is lost and has to be asked again. Background commands end at the restart. A
coordinator with a background command reads busy (section 2.6), so it may
never pass step 1; then the profile applies at its next resume.

### 7.4 The phone entry

Measured on ordinary sessions: a resume keeps the phone entry and the
conversation (section 2.6). Not measured across a change of profile or
ServiceAccount; Unknown until the live proof. Slice (b)'s live proof reads
it on an unused test session, with DESIGN-065 section 5's readings before
and after (name, UID, conversation id, and the first 12 hex of the SHA-256
of the Remote Control session id and of its link, never the values) plus the
pod's ServiceAccount and profile label.

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
| Broker `ancestry` (`delegate_reach.go:63-67`) | Depth above 2 is "ambiguous ancestry depth": no delegate decides its grants and it is never a delegate. Its grants go to a standing policy, an attested policy, or the 30-minute deny | Yes |
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

`agent-run raise <session> --take-back [--keep-running]`, the front door's
`session_take_back`, or `DELETE /v1/sessions/{name}/raise`.

- **Who.** Anyone who may perform a raise under Ruling 1, and the target
  itself. No word from Tom is needed: taking power back never waits for one.
- **The record.** `state` becomes `taken-back`, with `takenBackBy` and
  `takenBackAt`. It is kept, with Tom's words. One log line is written.
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
  session and resume it: its next pod runs on `spec.profile` (safety rule 14).

## 10. What readers show

**API.** `GET /v1/sessions/{name}`, `GET /v1/sessions` and the fleet view
carry one new optional object, `raise`, only on a session with a raise
record:

```json
"raise": {
  "state": "active",
  "profile": "workbench",
  "fromProfile": "full",
  "startSessions": true,
  "owner": {"words": "<verbatim>", "where": "phone-prompt", "session": "<performer>", "saidAt": "2026-11-13T15:50:02Z", "ref": "op:830#7980"},
  "performedBy": "session/<performer>",
  "performedAt": "2026-11-13T15:52:40Z",
  "podProfile": "full",
  "ignored": ""
}
```

`state` may also be `taken-back` or `ignored` (with `ignored` giving the
reason). `podProfile` is the current pod's profile label, on the
single-session view only. The top-level `profile` stays `spec.profile`.

**`agent-run show`:**

```
profile:      full (at creation)
raised:       workbench; may start sessions (performed by session/<performer> at 2026-11-13T15:52:40Z)
on Tom's word: "<verbatim>" (phone prompt in <performer>, 2026-11-13T15:50:02Z; op:830#7980)
pod runs as:  full; the raised profile applies at its next start (agent-run raise <name> --restart)
```

Other forms: "raise taken back by `<who>` at `<time>`; the pod still runs as
workbench until it is suspended"; "raise record ignored: `<reason>`".

**`agent-run list`** adds to its NOTE column (`internal/agentrun/sessions.go:68`,
`note` at `:82`): `raised: workbench`, `raised: may start sessions`,
`raised: workbench at next start`, or `raise taken back; pod still raised`.
No new column, so no script that reads the columns changes. The words are
not in the list; `show` has them.

**`agent-run fleet`** adds one header line, `raised:   <name> (workbench),
<name> (may start sessions)`, and the same NOTE text.

**Portal.** Unchanged in these slices: `decodeSessionRow` refuses any key
outside its six (`web/portal/session/product.go:520-524`). A later slice may
add a mark; haynes-ops then pins the portal before the operator that sends
it.

**Front door.** `session_raise` and `session_take_back` (slice d) answer with
the same text as `show`. `session_status` is unchanged.

**The command.** Tom's word is "elevate"; the command is `raise`:

```
agent-run raise <session> --profile <name> --words "<Tom's words>" --said-at <time> [--ref <ref>]
agent-run raise <session> --start-sessions --words "<Tom's words>" --said-at <time> [--ref <ref>]
agent-run raise <session> --restart
agent-run raise <session> --take-back [--keep-running]
```

From a session it sends `where: phone-prompt`; from Tom's account,
`own-call`. It reads the session first and sends its UID as `expectUID`. Its
refusals are the sentences of section 6.4. `agent-run help raise` says what a
raise is, and that it needs Tom's own words given to this agent, in under 80
words.

**Older readers.** An `agent-run` older than the slice that adds `raise`
reads `GET /v1/sessions/{name}` strictly on `show --json --once` and `usage
export` (`strictWireJSON`, `internal/agentrun/show_once.go:47`). It cannot
read a raised session on those two paths, and reads every other session as
before (safety rule 16). The test: an unraised session's view holds no
`raise` key, as `TestShowSaysTitleRenameOff` does for titles.

## 11. Slices

Each slice is one worker session, behind flags that are off by default, and
releasable alone. Tests use fakes or envtest at two CPUs. A new package (for
example `internal/sessionraise`) gets its line in `docs/repo-layout.md`. Docs
that describe the changed behaviour change in the same PR.

| Slice | What it changes | Tests must prove | Other-family review | CRD copy | Pins |
| --- | --- | --- | --- | --- | --- |
| (a) The record, the route, the right to start sessions | The annotation and its readers; `GET`, `POST` and `DELETE /v1/sessions/{name}/raise` for workbench-by-creation sessions and Tom's account; Tom's words and the secret check; rate limits; the log line; the dispatch exemption in `admitCreate`; take-back (record, suspend, grant release); the `raise` key in the views; `show`, `list` and `fleet`; `agent-run raise` without `--restart`; both flags, off. The haynes-ops admission fence of safety rule 13 goes in before any flag is set (a haynes-ops PR, not this repo) | Every row of 6.4, including each caller kind refused; a raise without words, with a stale `saidAt`, with a token-shaped word, with reused words, and with a `where` that does not match the caller is refused; `performedBy` comes from the token; a UID mismatch, a bad version and both flags off are ignored, with the reason in `show`; the record write leaves `metadata.generation` unchanged (envtest); an unraised session's view has no `raise` key; a depth-2 caller with an active record creates a child with label `"3"`, its own `spec.parent`, under the cap, and the fifth unfinished child is 429; a depth-3 caller is refused with or without a record; taken back, UID mismatch, flag off, `"02"` and missing labels are refused; the record is read live under the lock; scheduled work stays refused; a table test of every reader in 8.2 at depth 3; a take-back suspends at once and releases the raised grants; a take-back works with both flags off | Yes: a write route with an access rule, and it overrides a global guard | No | Operator; agent image (agent-run) |
| (b) The profile at the next pod, and the restart step | The effective profile in `buildSessionPod`; Ruling 7 in `validate.go` and `named_coordinator.go`; `agent-run raise --restart` | The pod gets the raised profile's account, env, envFrom, remote block, mounts and labels; flag off, taken back and UID mismatch build from `spec.profile`; the hold pod and the volume are unchanged; a named coordinator profile is never used; a raised workbench session cannot perform a raise, name `workbench` for a child or launch a named coordinator identity, and a workbench-by-creation session still can; `--restart` refuses on busy, waiting, attached and a stale heartbeat, and otherwise calls suspend then resume | Yes: grants access, and the restart suspends live sessions | No | Operator; agent image |
| (c) The raise action in a CallerPolicy | `spec.raise` on the front-door shape and its CEL rule; `readCallerPolicy` checks it; `policyCallerAllowed` adds the three routes when it is set; `where: chat` for policy callers | A policy without `raise` reaches no raise route; `raise` with `spec.summon` is refused by the schema; a bad `raise` fails the policy closed; a profile on the policy but not on the flag is refused, and the reverse too; a policy caller may raise a session it did not start but still reads and reaps only its own; `chat` is accepted only from a policy caller; a stale CRD that prunes `raise` leaves no right | Yes: grants a new right to an always-on agent | Yes, the CallerPolicy CRD, first | Operator |
| (d) The front door tools | `session_raise` and `session_take_back` in `internal/frontdoor/tools.go` | Each tool reads the UID, sends Tom's words unchanged with `where: chat`, and shows the route's refusal sentence as is; descriptions under 30 words | Yes: an always-on agent's path to superuser | No | Front door |

**Install order.** The haynes-ops admission fence; the operator and agent
image pins for (a) and (b); then the flags, set by the coordinator:
`--session-raise-profiles=workbench` and `--enable-session-raise-dispatch`.
For Meatball: the CallerPolicy CRD copy, the operator pin for (c), the
policy's `raise` field in haynes-ops, then the front door pin for (d).

**First live proof.** On an unused test session, never a working coordinator
and never `haynesnetwork-1113-150420`. Create it at depth 2 on the default
profile, through a test parent at depth 1. Read R0 (DESIGN-065 section 5's
identity readings, plus the pod's ServiceAccount and profile label). A
workbench-by-creation coordinator asks Tom by phone prompt and, on his word,
runs `agent-run raise <test> --profile workbench --start-sessions` with his
words. Read R1: identity unchanged, pod unchanged, record and log line hold
his words. Run `--restart`. Read R2: same name, UID, conversation id and both
hashes; ServiceAccount `dev-env-workbench`. Create one child: label `"3"`.
From the child, try a create: refused. Take back with the default suspend.
Resume. Read R3: ServiceAccount `dev-env-agent`; a create from the session is
refused. Record R0 to R3 on story 830, then reap the test sessions under the
normal rules.

## 12. Open questions for the owner

None remain. Tom answered Q-066-01 on 2026-11-13 (section 1.2). The
coordinator ruled on the former Q-066-02, Q-066-03 and Q-066-04 the same day
(rulings 3, 8 and 7).

## 13. What this rejects

| Rejected | Why |
| --- | --- |
| A `raise` card on the Asks board, decided by Tom's tap (the first draft's Ruling 1) | Tom chose his word to an agent (section 1.2). The grant card path it would have reused is not installed: the coordinator read no grant-Ask flag and an Ask CRD with no enum on its kind field, and the portal's `preferred_username` (ADR-021 build PR 6) is not in source. A tap can be added later as a stricter second factor without changing the record (section 6.4). |
| A relay: an agent records words Tom gave another agent | The agent that heard him performs the raise (safety rule 3). A relay adds a party the record cannot name. |
| A trusted client performs a raise | It includes the v1 pod (`caller.go:27-29`) and is none of the kinds Tom named. |
| Any CallerPolicy caller may raise | Only with `spec.raise`. A summoning caller never. |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie about who started the session, and where. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise `maxSessionDepth` to 3 for everyone | It is one of three guards against runaway agents (DESIGN-001 design line 4983-4986). One session's need does not justify a fleet change. |
| Start a new session to continue the work | That is what happened in the case, and it loses the conversation. |
| An AccessGrant of a new type | A grant is always requested by the session for itself (`grants.go:161-170`), which is the self-raise this design forbids. It lasts at most 8 hours. A standing GrantPolicy could approve it. The broker installs access into a pod; it does not choose a pod's own identity, and it does not write AgentSessions. |
| A separate CRD for raises | A new CRD, RBAC, CRD copy and watch, for the trust the fenced annotation already gives. The controller already chooses a pod's account from API-written metadata on that trust: the named coordinator admission label (`pod.go:391-393`). |
| A status field | A stale CRD prunes it (haynes-ops #3502), an older operator's status write drops it, and status is what the operator observed, not a decision. |
| The tracker as the audit record | It is written as the agent and can be down. The operator's annotation and log line are written from the authenticated caller (Ruling 2). |
| The operator restarts the session itself when idle | `docs/remote-session-upgrades.md` keeps automatic stops away from a user's remote session. A coordinator reads busy while a background command runs (`activity.go:251-263`), so an idle moment may never come. |
| Change the identity of the running pod | A pod's ServiceAccount is fixed for its life. No exec or token step changes what the pod is. |
| A standing policy approves raises | No raise without Tom's word. |
| An end date on a raise | The coordinator's ruling (Ruling 8). |
| A raise to a named coordinator identity | One holder at a time and three launch shapes (section 2.4). A raise would bypass both. |
| Raised sessions pass workbench on to their children | Ruling 7. |
