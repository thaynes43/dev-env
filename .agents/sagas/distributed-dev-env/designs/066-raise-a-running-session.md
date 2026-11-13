# DESIGN-066: Raise a running session from outside it

- **Status:** Proposed 2026-11-13, for the coordinator's review. Revised
  twice the same day: first after Tom answered Q-066-01 (section 1.2), so the
  go-ahead is his word to an agent, not a tap on the Asks board; then after a
  design review by another model family, with the coordinator's rulings on
  each finding (section 14). Nothing is built or installed. No flag, CRD
  change, route, command or haynes-ops change exists for it.
  [ADR-028](../adrs/028-raise-a-running-session.md) is Proposed with it.
- **Tracking:** [Story OP830](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/830).
  Tom's ask is recorded on story 828 comment 7927, and his answer on how he
  gives the go-ahead on story 830 comment 7980. Why the case's session
  started where it did is story 831 (section 1.4).
- **Origin:** Tom, 2026-11-13, quoted in sections 1.1 and 1.2. Nothing else
  here is Tom's. The coordinator's reading of his answer, its rulings on the
  former Q-066-02 to Q-066-04 and its rulings on the review are marked as the
  coordinator's (`dev-env-fable-coordinator-1109`, 2026-11-13). The rest is
  proposed by a worker for the coordinator, which decides it.
- **ADR:** [ADR-028](../adrs/028-raise-a-running-session.md), Proposed. It
  adds one named exemption to the depth rule of DESIGN-001 section 3.4 and
  one optional right to the front-door CallerPolicy of DESIGN-060 section
  7.1, so it is accepted before any slice merges.
- **Builds on:** [DESIGN-001](001-dev-env-v2.md) section 3.4, D-19, D-46 and
  D-88 (agent attestation); [DESIGN-060](060-meatball-front-door.md) section
  7.1 (the CallerPolicy); [DESIGN-064](064-summoned-sessions-ops-profile.md)
  section 6.8 (the keeper's summoned-session pager, the model for the raise
  notice); [DESIGN-065](065-session-display-titles.md) section 9.4 and
  [ADR-027](../adrs/027-session-title-annotation.md) (a later change is an
  annotation, not a spec write).
- **Evidence:** dev-env `2774e599` (origin/main), source and docs only.
  DESIGN-042 is not on main; it was read on the branch of open dev-env PR 782
  at `5449647f`. The secret shapes were read in haynes-ops at `3ad4b39f`
  through the GitHub API. The review is
  `~/.shared/handoffs/op830/design-review-grok.md`, on head `91678100`. The
  facts of the case and the installed flags and rights in section 2 are the
  coordinator's readings of 2026-11-13, quoted and not measured again.
  Nothing in the cluster, the tracker or any session was read or written for
  this document.

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
- `resolveSession` reads the caller's session through `Server.get`, which
  reads the manager's cache and asks the API server only when the object is
  missing (`caller.go:342-354`). So a caller's session can be a cached copy.
- `admitCreate` (`internal/apiserver/sessions.go:651-713`) refuses a session
  caller whose `depth` is above 2 with 403 and the message of the case
  (`sessions.go:699-702`). It then counts the caller's unfinished children by
  `spec.parent` and agent, read live (`runningChildren`, `sessions.go:263-276`;
  `unfinished`, `:303-318`), and answers 429 at 4 (`:703-710`). The direct
  create and the scheduled create both run it under the create lock
  (`sessions.go:651-657`). That lock is per replica (`sessions.go:681-685`).
- At create the API writes the depth label from the caller
  (`internal/apiserver/validate.go:384-388`) and `spec.parent` from the
  caller, never from the body (`validate.go:258-270`;
  `api/v1alpha1/agentsession_types.go:314-319`). The only other writer of the
  label is the summoned create, which writes `"0"`
  (`internal/apiserver/summon.go:668`). No code changes the label after
  create (`git grep -n LabelDepth -- internal cmd api`).
- DESIGN-001 section 3.4 states the rule (design line 433-436, "403 beyond
  2") and keeps it as one of three guards "about runaway agents, not
  capacity" (design line 4983-4986).

### 2.2 Profile, identity and what a profile brings

- `spec.profile` is the creator's choice; empty means the templates' default
  (`agentsession_types.go:289-297`).
- A child's profile (`validate.go:315-348`): an ordinary session's child runs
  on its parent's profile, the profile label on the parent's pod
  (`caller.go:317-320`), and another profile is a 403 (`validate.go:340-347`).
  A workbench session's child runs on the profile it names, workbench
  included, or on the default (OP502, `validate.go:339`). A Codex host a
  workbench session starts runs on the default (`validate.go:331-334`). A
  child of a session on a named coordinator profile runs on the default
  (`validate.go:335-338`).
- The templates refuse a named coordinator identity on the default profile,
  but not the workbench account (`internal/templates/templates.go:617-624`).
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
- The API view shows `spec.profile` (`internal/apiserver/view.go:30`).
  Section 8.4 lists every reader of the pod's profile label and of the
  workbench account.
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
  and decides by the pod's account, not by `spec.profile`
  (`caller.go:141-144`, `:255-282`).
- A named coordinator identity (`dev-env-coordinator-<name>`) has its own
  rule: Claude only, one holder at a time, never inherited, and three launch
  shapes (Tom or a client at depth 0, or a depth-0 workbench session
  launching at depth 1) (`internal/apiserver/named_coordinator.go:23-37`,
  `:75-89`; D-19 in DESIGN-001 design line 3583-3600). The launcher class of
  a session caller is "workbench session" whenever its pod runs as the
  workbench account (`named_coordinator.go:115-125`), with no read of
  `spec.profile`. The admission label and the launcher annotation are
  trusted because "Only the API writes AgentSessions in the session
  namespace" (`named_coordinator.go:55-58`), and the controller runs a pod as
  the account only with that label (`pod.go:391-393`).
- The coordinator's read of 2026-11-13: the workbench account cannot patch or
  update AgentSessions or Asks in `dev-agents`. `kubectl auth can-i` answered
  no for patch and update on `agentsessions`, patch on `asks`, and update on
  `asks/status`.
- The operator's own Role in the session namespace can patch AgentSessions
  (the repo's test copy, `internal/broker/testdata/haynes-ops/operator.yaml:49-51`,
  as the review notes). So can any cluster admin, and Flux for an AgentSession
  declared in git (DESIGN-060 section 4 table, design line 116).
- DESIGN-042 is not on main. On the branch of open PR 782, its section 6.4
  proposes a new named delegate identity and adds no rule to the code above.
  The rule in force is the code.

### 2.5 What an owner's go-ahead and a notice can travel through today

| Piece | In source on main | Installed (coordinator's read, 2026-11-13) |
| --- | --- | --- |
| Grant attestation: `POST /v1/grants/{name}/attestation`, `--enable-grant-attestation` | Yes (`internal/apiserver/grant_attestation.go:15-22`; `cmd/dev-env-operator/main.go:257`) | On: `--enable-grant-attestation=true` |
| Asks board, `--enable-asks` | Yes (`main.go:263`) | On: `--enable-asks=true` |
| Inbox questions, `--enable-inbox-questions` | Yes (`main.go:366`) | On: `--enable-inbox-questions=true` |
| The keeper's Ask push, `--enable-ask-notifications` | Yes, default off (`cmd/dev-env-keeper/main.go:269-273`; `internal/keeper/ask_notifications.go`) | On: `--enable-ask-notifications=true`. Its delivery window flag: Unknown |
| The keeper's summoned-session pager (DESIGN-064 6.8) | Yes (`internal/keeper/summon_pages.go`) | Unknown |
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

**Phone-prompt answers are not stored.** A native phone prompt is answered
inside the session's own conversation, on its volume. The operator keeps no
record of it. The portal's conversation view is a private read of a bounded
projection made on request (`internal/apiserver/portal_conversations.go:22-23`;
`internal/agentd/protocol/portal_conversation.go:16-20`: "a bounded
projection, never a provider transcript"), not a stored answer a route could
match words against.

**The keeper's notification path.** The Ask push is a leader-only runnable
in the keeper (`internal/keeper/ask_notifications.go:149-160`, `:182-193`).
Each 30-second tick (`:25`) skips everything outside the allowed delivery
window, if one is set (`--ask-notify-window`, `--ask-notify-timezone`,
`cmd/dev-env-keeper/main.go:271-272`; `askNoticeWindow.allows`,
`ask_notifications.go:115-125`; the tick returns at `:202-204`). It writes a
reservation to its own ConfigMap before every send, so an attempt that was
reserved is never replayed silently (`:149-151`, `:270-287`), sends through
the keeper's Pushover sender (`:313-337`), and records the outcome as Sent
(Pushover confirmed it), Failed (definitely not delivered: no credentials or a
4xx refusal) or Ambiguous (it may have reached Pushover) (`:324-336`). Its
ledger ConfigMap is precreated by GitOps, and the keeper has get and update
on that one name only (`internal/keeper/ask_notification_store.go:51-56`).
Its push is content-free: the fixed text "An agent needs your answer" and a
board link (`ask_notifications.go:22-24`, `:313-315`). The summoned-session
pager "copies the Ask notifier" for operator-written session records, and
renders text from them (`internal/keeper/summon_pages.go:32-47`, `:617-700`,
`:925`), with links in free text made inert (`summonRedact`, `:793`). The
keeper ships in the operator image (`images/operator/Dockerfile:1-2`, `:40`).

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

**Free text and secrets.** No check in this repo refuses a secret-shaped
value in free text. The front door scrubs an OpenProject token shape and its
own bearer from command output (`internal/frontdoor/opwork.go:20`,
`:133-139`). `grantattest.ValidRef` allows only a short reference of letters,
digits and `_ . : / # -` (`attest.go:52`, `:104-107`). haynes-ops has a list
of secret shapes for the upgrade lane's public records: `SECRET_SHAPES` and
`secretShape` in
`kubernetes/main/apps/upgrade-agent/shepherd/app/resources/night-merge.mjs`
lines 963-1003, read at haynes-ops `3ad4b39f`. It names GitHub tokens,
private keys, JSON web tokens, URLs with credentials, bearer credentials,
credential assignments, Slack tokens, AWS keys, private network addresses
and in-cluster names.

### 2.6 Restarting a remote session

- Any caller may suspend or resume any session (`internal/apiserver/lifecycle.go:18-25`).
  A summoned session is never resumed (`:59-65`). A resume of a named
  coordinator session rechecks its admission (`:95-119`). A suspend rescues
  the pod first and keeps the volume (D-51, `lifecycle.go:20-21`).
- The operator suspends a session itself by writing `spec.operatingMode`
  Suspended and the suspended-by annotation under the optimistic lock "as the
  idle timer does"; the usual suspend path then removes the pod
  (`suspendClosedSummon`, `internal/controller/summon_watchdog.go:265-290`).
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
  rejects). So a coordinator seldom reads idle in `agent-run show`. A
  heartbeat is up to 60 seconds old (DESIGN-065 section 9.1).

### 2.7 What this document could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry. It stays Unknown until the live proof (section 7.4).
- Whether Meatball's CallerPolicy is installed today.
- Whether the keeper's `--ask-notify-window` is set today, and to what.
  Saga decision 99 records Tom's window of 07:00 to midnight.
- Whether the keeper's ServiceAccount may list AgentSessions today (the
  summoned-session pager needs it; its install is Unknown).
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- What the workbench profile's env, envFrom, mounts and remote block hold
  today (haynes-ops `templates.yaml`).
- Which accounts can read Secrets in `dev-env-system` today.
- How haynes-ops creates a generated Secret for `dev-env-system` (section
  6.6).
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.

## 3. Outcome

An action taken from outside a session, on Tom's word, raises what that
running session may do. The agent Tom gave his word to performs the raise
and records his words. Tom is told of every raise and every take-back
before a raise takes effect. The session keeps its name, UID, conversation,
volume, work in progress and its entry in the Claude app. Two things can be
raised:

1. its **profile**, for example from the default profile to `workbench`;
2. its **right to start sessions**, for a session at depth 2.

The session can never raise itself. `spec.parent`, `spec.profile`, the depth
label and the creation record are never rewritten. `maxSessionDepth` stays
2. While the feature is on, a raised pod never outlives its record. Every
raise carries Tom's words, is logged and announced, and can be taken back.

## 4. Rulings proposed

Rulings 3, 7 and 8 contain the coordinator's rulings of 2026-11-13 on the
former Q-066-02 to Q-066-04. Rulings 10 and 11, and parts of rulings 1, 2,
4, 5, 7 and 9, carry the coordinator's rulings of 2026-11-13 on the review
(section 14). Each is marked. Everything else here is proposed by a worker
for the coordinator. None of it is Tom's, except the sentences quoted in
section 1.

**Ruling 1. Who performs a raise, on whose word.** Tom decides, by his word
(section 1.2). The platform cannot verify his word. It records the performing
agent's statement of it, as the grant attestation route records "agent
attestation, not verified human identity" (section 2.5). The operator already
runs that route (`--enable-grant-attestation=true`, the coordinator's read of
2026-11-13). What makes a false statement visible is the notice of Ruling 10:
Tom sees every raise, the agent that performed it and the words it says he
said, before the raise takes effect.

These may perform a raise:

1. **A superuser session.** A session that is workbench by creation (Ruling
   7), that is not the target and does not sit under the target. "Under" is
   read live: the target is not on the performer's chain of parents, matched
   by name and UID, as `sessionAncestry` walks it
   (`internal/apiserver/grant_decision.go:299-320`). Any depth. A chain that
   cannot be read (`ancestryUnverified`, `grant_decision.go:311-312`) or is
   too deep is a refusal, not a pass (coordinator's ruling R7).
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

The review proposed that a phone-prompt raise quote an answer the operator
already stored. The coordinator did not adopt it (R1): the operator stores
no phone-prompt answer (section 2.5). The notice is the control.

**Ruling 2. The record and the audit record.** One annotation on the
AgentSession, `dev-env.haynesops.com/raise`, holding one JSON object (section
6.1). Only the operator writes it, from the raise route, the take-back route
and its own reconciler. It names the session's UID, and every reader
compares that with `metadata.uid` and ignores a record that does not match.
It is not a spec write, so `metadata.generation` does not move (ADR-027).

The record carries Tom's word, since that is the evidence: what he said,
verbatim and bounded; where he said it; when; and the caller that performed
the raise, taken from the token, never from the body. A raise without his
words is refused.

The record carries a message authentication code that the operator computes
with a key only it reads (coordinator's ruling R9, section 6.6). A record
with a missing or wrong code is ignored and logged. So the record does not
depend on an admission rule alone. The haynes-ops admission rule that
refuses a change to the annotation from any user but the operator's
ServiceAccount stays as a second layer, installed before any flag (safety
rule 14).

**The audit record is the operator's, not the agent's.** It has three
parts, none written by the agent: the annotation, which holds the current or
last raise of the session with its words; one operator log line per raise,
activation and take-back, with the same fields, which keeps the history
after a record is replaced or the session is removed; and the notice to Tom
with its outcome in the keeper's ledger (Ruling 10). The performing agent
may also write the words on its tracker story, and should when it works
from one, but that is not the record: the tracker is written as the agent
and can be down.

Rejected: a spec write (D-39, and generation in four places, ADR-027); a
status field (a stale CRD prunes it, haynes-ops #3502, an older operator's
status write drops it, and status is what the operator saw, not a decision);
a separate object for the record (a new CRD, RBAC, CRD copy and watch, for
the trust the code and the fence already give); an AccessGrant (section 13);
the tracker as the record.

**Ruling 3. The profile, and when it applies.** The effective profile is the
record's profile when the record is valid, active, names this UID, and its
profile is on the operator flag `--session-raise-profiles`. Otherwise it is
`spec.profile`. The operator reads the effective profile when it builds a
session pod (`pod.go:320`) and in the watchdog of Ruling 11. A running pod
keeps its ServiceAccount, so a profile raise takes effect at the session's
next pod. The operator never restarts a session to apply a raise.

The coordinator's ruling of 2026-11-13 on the former Q-066-02: a raised
profile is applied by the explicit restart step, `agent-run raise <session>
--restart` (section 7.3). The acting agent takes that step at the session's
first quiet moment, and never while the session is busy or waiting on a
question. If it is not taken, the profile applies at the session's next
resume. While a raised profile waits for the next pod, the ordinary suspend
route refuses a busy or waiting session; the take-back route does not
(coordinator's ruling R5, section 7.3).

`--session-raise-profiles` is empty by default. A profile that runs as a
named coordinator identity is never given, whatever the flag says: the route
and the controller each check the profile against the templates they read,
and a templates read that fails gives nothing. The one-holder rule and the
launch shapes of section 2.4 stay as they are. The expected value is
`workbench`.

**Ruling 4. The right to start sessions.** In `admitCreate`, a session caller
whose own depth label is exactly `2` passes the depth check when its session,
read live with `s.Live.Get` inside the create lock (not the cached copy of
section 2.1), carries a valid, active record that names its UID, says depth
`2` and grants `startSessions`, and the operator flag
`--enable-session-raise-dispatch` is on. A failed read refuses the exemption
(coordinator's ruling R7). Section 8 gives the rule and every reader of the
depth label. The child gets the true label `3`, `spec.parent` the raised
session, the same child cap and every other admission. A session at depth 3
is never exempt and starts nothing. The exemption serves the direct create
only, not scheduled work, because a schedule outlives the moment Tom gave his
word.

**Ruling 5. Taking it back.** Anyone who may perform a raise under Ruling 1,
and the target itself, may take one back. Taking back needs no word from
Tom: taking power back never waits for one. The take-back takes the create
lock before it writes, so a create that is still being admitted finishes
first and the next one sees the take-back (coordinator's ruling R7). The
record stays, marked taken back, with who and when. The right to start
sessions ends at the next create. A profile raise whose pod already runs the
raised profile is suspended at once, through the existing route, rescue
first. There is no option to keep it running (coordinator's ruling R3).
Children already started keep running and keep counting against the cap.
Tom is told (Ruling 10). Section 9.

**Ruling 6. What readers show.** The view's `profile` stays the creation
profile. A raise is shown beside it, with Tom's words, in one new key that
appears only on a session that has a raise record. Section 10.

**Ruling 7. A raise never spreads.** A session raised to workbench is a
workbench session for its own pod, its Kubernetes rights and the API routes
that check the workbench account (section 8.4). It is not one for three
things: it may not perform a raise; it may not launch a named coordinator
identity (coordinator's ruling R6: today's code does not refuse it, and slice
(b) changes it); and its children never get the workbench account.

The API tells the two apart by `spec.profile`: a session is workbench by
creation only when its `spec.profile`, resolved to the templates' default
when empty, names a profile that runs as the workbench account. A templates
read that fails counts as "not by creation".

The coordinator's ruling of 2026-11-13 on the former Q-066-04, and its
ruling R4 on the review: a raised session's children never inherit the
raised profile. Child admission for a raised parent is its own branch
(section 8.2): the child gets the profile it names, or the templates'
default when it names none; the create is refused when that profile runs as
the workbench account or a named coordinator identity; the pod label is
never copied. So no child of a raised session is ever workbench by creation,
and neither is any session below it, since a child that is not a workbench
session gives its own children its own profile (`validate.go:340-347`).

**Ruling 8. How long a raise lasts.** The coordinator's ruling of 2026-11-13
on the former Q-066-03: a raise lasts until it is taken back or the session
is removed. It has no end date. It survives a suspend and resume, because the
annotation and the UID stay. A restore makes a new session with a new UID,
which gets nothing. The 24-hour bound on `saidAt` (section 6.3) bounds the
time the agent states against the operator's clock. It proves nothing about
when Tom spoke, and it does not bound the raise.

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
- **Who sets it** (coordinator's ruling R8). Setting `spec.raise` on
  Meatball's policy is a GitOps change in haynes-ops, and Tom approves it.
- **What it gives** (coordinator's ruling R8). With it, that caller may
  raise any eligible session, not only the sessions it started: Tom names
  the session in his word, and the case's session was not Meatball's.
  Through a take-back of a profile raise, it may also suspend at once a
  session it did not start. Its other routes keep their own-sessions fence.
  The words need not name the target; they are free text, and the notice
  shows both the words and the target.
- This amends DESIGN-060 section 7.1 ("no ... configurable rights") and
  section 6 ("never gets ... the workbench") for this one right: Meatball
  itself still never runs as the workbench; on Tom's word it may raise
  another session to it. DESIGN-060 is amended in place in slice (c).

**A Meatball equivalent.** Today the front-door shape admits only Meatball:
the schema fixes its account (`callerpolicy_types.go:60`), and the API reads
one fixed object, `dev-env-system/meatball` (`callerpolicy.go:21-24`). A
second always-on agent needs that shape to admit another account, one policy
object per account, as the summon shape already names its object after its
account (`callerpolicy_types.go:11`). That is its own story, outside these
slices. When it lands, that agent's policy carries its own `spec.raise`, and
nothing in this design changes.

**Ruling 10. Tom sees every raise** (coordinator's ruling R1). A raise is
written as `pending`, not `active`. It becomes active only when a notice to
Tom has been handed to the keeper's notification path (section 6.4). The
notice names the target session (name and title), the performing agent, what
was raised (profile, right to start sessions), the words the agent stated,
and the one command that takes it back. A take-back also sends a notice. A
raise never sits active and unannounced: a notice that cannot be handed
leaves the raise pending, and after its bound the raise ends `unannounced`
and never takes effect. Slice (a) builds the notice; no raise route is
flagged on before it exists.

**Ruling 11. Power never outlives its record while the feature is on**
(coordinator's ruling R3). The reconciler suspends a session whose pod runs
on a profile other than `spec.profile`, as another account, unless a valid,
active record names this UID and that profile is on the flag (section 7.5).
"Fails closed" is claimed only for the right to start sessions and for the
next pod. For an operator rollback or flags turned off, the operator's duty
is a procedure: take back every raise first (section 7.6).

## 5. Safety rules

1. **No self-raise.** The target never performs its own raise. No session
   under the target performs one. No session raised to workbench performs
   one. A parent chain that cannot be read is a refusal.
2. **No word, no raise.** Every raise carries Tom's words, where he said
   them and when. A raise without them is refused.
3. **A word is used once.** The operator keeps the hash of every word it
   accepted, per performer, in its own ledger, and refuses the same words
   (normalised) for any target (section 6.5). Place and time are not part of
   the comparison. The ledger survives an operator restart.
4. **The agent that heard him performs it.** A session records only words
   Tom gave it; a policy caller only words he gave it in chat; Tom's own
   account only its own call. A relay is refused.
5. **Tom is told first.** A raise is not active until its notice is handed
   to the keeper (Ruling 10). A take-back is announced too.
6. **Only the named kinds perform** (Ruling 1). No GrantPolicy, delegate,
   coordinator host, client, summoning caller, ordinary session or portal
   performs a raise. A CallerPolicy caller performs only with `spec.raise`,
   which Tom approves in haynes-ops.
7. **Bound to the UID and signed.** The record carries the target's UID and
   the operator's code (section 6.6). Every reader checks both against the
   live session. A later session with the same name gets nothing. A record
   copied onto another object, or written by anyone but the operator, is
   ignored and logged.
8. **Provenance stays true.** `spec.parent`, `spec.profile`, the depth label,
   the creation log line and the volume's labels are never written. The
   view's `profile` stays the creation profile.
9. **The global limit stays.** `maxSessionDepth` stays 2. One named
   exemption, for a session at depth 2 only, read live. Nothing at depth 3
   starts anything. The child cap is unchanged.
10. **Never a named coordinator identity.** A raise never gives such a
    profile, and a raised session never launches one.
11. **A raise never spreads** (Ruling 7). A raised session's children never
    inherit the raised profile and are never workbench.
12. **No restart for a raise, no power without a record.** The operator never
    suspends or restarts a session to apply a raise. `--restart` is an
    explicit step that refuses unless the session reads quiet, and the
    ordinary suspend refuses a busy or waiting session while a raised profile
    waits for its pod. A take-back suspends at once. While the feature is on,
    the watchdog suspends a pod that runs raised without a valid active
    record (Ruling 11).
13. **Eligible sessions only.** A Claude session in `remote` or `local` mode
    that is not summoned (`spec.caller`), not a project canary or private
    pilot, not budget-bound, not a coordinator host's child, not on a named
    coordinator identity, not being reaped and not archived. Everything else
    is refused.
14. **Fenced labels and record.** Before any flag is set, haynes-ops refuses
    a change to the annotation `dev-env.haynesops.com/raise` from any user
    but the operator's ServiceAccount, and any change to or removal of the
    label `dev-env.haynesops.com/depth` after create, from any user (section
    8.5).
15. **Rate limits that survive a restart.** Per target: one record change in
    any 60 seconds. Per performer: at most three raises in any rolling hour;
    take-backs are not counted. Both are read from the ledger of section 6.5.
16. **Rollback is a procedure.** Before an operator rollback or turning the
    flags off, every raise is taken back (section 7.6). What fails closed by
    itself: the right to start sessions and the next pod.
17. **Schema order.** The AgentSession and Ask CRDs do not change. The
    CallerPolicy CRD gains `spec.raise` in slice (c), so haynes-ops gets that
    copy before the operator pin that reads it.
18. **Older readers.** A new key appears in the session view only on a
    session with a raise record (DESIGN-065 section 9.7). An `agent-run`
    older than the slice that adds it cannot read that one session with
    `show --json --once` or `usage export`.
19. **Review.** Each slice that grants access or overrides a guard gets an
    adversarial review by another model family before it merges (section 11).

## 6. Record and API

### 6.1 The record

The annotation `dev-env.haynesops.com/raise` holds one JSON object of at most
2 KiB:

```json
{
  "v": 1,
  "id": "<16 hex digits>",
  "sessionUID": "<metadata.uid of the raised session>",
  "state": "active",
  "profile": "workbench",
  "fromProfile": "full",
  "startSessions": true,
  "depth": "2",
  "owner": {
    "words": "<what Tom said, verbatim>",
    "wordsHash": "<SHA-256 of the normalised words, hex>",
    "where": "phone-prompt",
    "session": "<the performing session's name>",
    "saidAt": "2026-11-13T15:50:02Z",
    "ref": "op:830#7980"
  },
  "performedBy": "session/<performer>",
  "performedAt": "2026-11-13T15:52:40Z",
  "announcedAt": "2026-11-13T15:53:10Z",
  "takenBackBy": "",
  "takenBackAt": "",
  "keyId": "<first 8 hex of SHA-256 of the key>",
  "mac": "<43 base64url characters>"
}
```

- `state` is `pending` (waiting for its notice), `active`, `unannounced`
  (its notice could not be handed in time; it never took effect) or
  `taken-back`.
- `id` is the first 16 hex digits of SHA-256 over the session UID,
  `performedAt` and `wordsHash`. The notice and the log lines carry it.
- `profile` is empty or a DNS label. At least one of `profile` and
  `startSessions` is set. `fromProfile` is the pod's profile label when the
  raise was performed. `depth` is the session's depth label then.
- `owner` is section 6.3. `performedBy` is `caller.String()` of the
  authenticated caller, never from the body. Times are RFC 3339 in UTC.
- `keyId` and `mac` are section 6.6.

A reader that finds any other shape, version, UID or code ignores the
record, logs it, and `agent-run show` says why. A new raise for a session
whose record is `taken-back` or `unannounced` replaces the record. The
earlier one stays in the operator log.

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
- The checks of section 6.7 run, against a live read of the target.
- The operator writes the word to its ledger first (section 6.5); the word is
  spent once that write is confirmed. Then it writes the record as `pending`
  with the `resourceVersion` precondition and retry of `setOperatingMode`
  (`lifecycle.go:40-160`), writes one log line, and answers 202 with the
  session view: "Raised `<name>` to `<profile>`; it takes effect when Tom has
  been told."
- The same raise already pending or active with the same words: 200, nothing
  written.

**`DELETE /v1/sessions/{name}/raise`** takes a raise back:

```json
{"expectUID": "<uid the caller read>"}
```

It takes the create lock, marks the record `taken-back`, writes its log
line, suspends a session whose pod runs the raised profile, hands the
take-back notice to the keeper, and acts as section 9 says. 200 with the
view.

**Flags.** `--session-raise-profiles` (a comma list of profiles a raise may
give) and `--enable-session-raise-dispatch`, both off by default. With both
off, `POST` answers 403, "This operator does not raise sessions.", and does
nothing else. `GET` and `DELETE` still work: taking a raise back is never
switched off. The operator also refuses every `POST` while it has no MAC key
(section 6.6). An operator without the routes answers 404, and `agent-run
raise` says the operator does not have them.

**What the front door needs so Meatball can call them.** `spec.raise` on its
policy, set by a haynes-ops change Tom approves (slice c); the three routes
on its route list when that field is set (slice c); and two tools in
`internal/frontdoor/tools.go`, beside `session_status` and `session_stop`:
`session_raise(name, profile, start_sessions, words, said_at)` and
`session_take_back(name)` (slice d). The tool reads the UID with `GET`, then
calls `POST` or `DELETE` with the front door's own token, sends `where:
chat`, and passes Tom's words through unchanged. Tool descriptions stay
under 30 words (DESIGN-060 section 6).

### 6.3 Tom's words

| Field | Rule |
| --- | --- |
| `words` | Required. 1 to 500 characters, at most 1 KiB of UTF-8, verbatim as Tom said or typed them. When what he gave was a choice in a phone prompt, `words` holds the question as asked and the option he chose, in that order, both verbatim. No control or format characters. Refused when it matches a secret shape (below). |
| `where` | Required. `phone-prompt`: Tom's answer to this session's phone prompt, or a message he typed to this session; only a session caller sends it, and the API sets `session` to the caller's own name. `chat`: a chat message to the always-on agent that performs the raise; only a policy caller sends it. `own-call`: only Tom's ServiceAccount sends it, and `words` is his own reason. Any other pairing of caller and `where` is refused. |
| `saidAt` | Required. When Tom said it, as the agent states. Not more than 5 minutes after the operator's clock and not more than 24 hours before it. This bounds the stated time against the operator's clock. It proves nothing about when Tom spoke (Ruling 8). |
| `ref` | Optional. A nonsecret reference to where the words are also kept, such as a tracker comment, in the shape `grantattest.ValidRef` allows (`attest.go:52`). |

**Normalised words.** For the used-word check (section 6.5) the operator
normalises the words to Unicode NFC, lower case, with leading and trailing
space removed and every run of white space made one space, and hashes them
with SHA-256. A phone-prompt answer differs from one raise to the next
because its question names the session. A bare "yes" can be used once per
performer.

**The secret check.** Slice (a) adds one function, used by the route and its
tests, that refuses `words` matching a secret shape. Its list is taken from
haynes-ops `SECRET_SHAPES` (`night-merge.mjs` lines 963-1003, at
`3ad4b39f`; section 2.5) plus the OpenProject token shape the front door
already scrubs (`internal/frontdoor/opwork.go:20`), with one test for each
shape. Like `ValidRef`, it only keeps out the shapes it knows; it cannot
prove a value is not secret.

### 6.4 The notice

The notice is handed to the keeper's notification path: the Pushover sender,
the write-ahead page ledger and the delivery window that the Ask push uses
(section 2.5). The runnable that reads raise records is new, in the keeper,
and copies the Ask notifier, as the summoned-session pager already did
(`summon_pages.go:32-47`).

**What it reads.** Every 30 seconds, as the Ask push does, the leader keeper
lists the AgentSessions that carry the raise annotation, verifies each
record's code with the same key as the operator (section 6.6), and takes
every record that is `pending` (a raise notice) or that became `taken-back`
or was suspended by the watchdog since its last notice (a take-back notice).

**What it says.** Title: "[dev-env] raise: `<session>`". Message: "`<session>`
(`<title>`) was raised by `<performer>`: profile `<from>` to `<profile>`;
may start sessions one level deeper. It says you said: "`<words>`"
(`<where>`, `<saidAt>`). To take it back: agent-run raise `<session>`
--take-back". The words are cut to 300 characters with a visible mark (the
whole words stay in the record), and links in them are made inert the way
the summoned-session pager treats free text (`summonRedact`,
`summon_pages.go:793`). A take-back notice says who took it back and when, or
that the watchdog suspended a pod that ran raised without a valid record.

**What "handed" means.** The keeper has confirmed its write-ahead
reservation of this notice in its own ledger ConfigMap,
`dev-env-system/dev-env-keeper-raise-notices`, precreated by GitOps with get
and update for the keeper on that one name (as `ask_notification_store.go:51-56`),
then made the Pushover request and recorded the outcome Sent: Pushover
confirmed it (`ask_notifications.go:326-327`). The operator, which may get
that one ConfigMap by name, reads the outcome for the record's `id`. On Sent
it writes `state: active`, `announcedAt` and a new code. Only then does the
raise take effect.

**When the send fails.** Failed (definitely not delivered) and Ambiguous (it
may have reached Pushover) both leave the raise `pending`. The keeper tries
again 5 minutes later (`askRequesterInterval`, `ask_notifications.go:26`), up
to 12 attempts. A second push for the same raise is accepted; silence is
not. After the twelfth failed attempt, or 24 hours after `performedAt`,
whichever is first, the operator writes `state: unannounced`, logs it, and
the raise never takes effect. Its words stay spent; a new raise needs a new
word from Tom.

**When the quiet-hours window holds it.** Outside the allowed window the
keeper makes no attempt, as the Ask push does (`ask_notifications.go:200-204`).
The raise stays `pending`, and becomes active only after a Sent inside the
window. A raise performed late at night waits for the morning. Attempts
outside the window are not counted. The 24-hour bound still applies.

**Take-back notices** never hold the take-back: it takes effect at once. The
notice is attempted the same way, and its outcome is logged.

### 6.5 The ledger: used words and rate limits

One ConfigMap, `dev-env-system/dev-env-session-raise-ledger`, precreated by
GitOps, which the operator alone writes, with get and update on that one name
and no create, as the session allowances ConfigMap is set up
(`cmd/dev-env-operator/main.go:287`). Read uncached, written with the
`resourceVersion` precondition (coordinators' rulings R2 and R10). It holds:

- **Used words:** for each accepted raise, the performer, the hash of its
  normalised words, the target's UID and the time. Kept 90 days, at most
  2048 entries. A raise is refused when its performer has used the same hash
  before, for any target. When the list is full and nothing is old enough to
  prune, every raise is refused with a plain sentence until entries age out;
  entries are never dropped early.
- **Changes per target:** the time of each record change, kept one minute.
- **Raises per performer:** the time of each raise, kept one hour.

The ledger write comes before the record write. A word whose ledger write is
confirmed is spent, even if the record write then fails.

### 6.6 The message authentication code

- **What it covers.** HMAC-SHA256 over a canonical encoding of every field
  of the record except `mac`: the session UID, what is raised, the depth,
  the hash of the words, the performer, the times and the state. Encoded as
  43 base64url characters. The operator computes it on every write.
- **Where the key lives.** A Secret, `dev-env-system/dev-env-session-raise-key`,
  with data keys `current` and, during a rotation, `previous`, each 32 random
  bytes. It is mounted read-only into the operator pod, which reads it at
  start and again when the file changes. The keeper mounts the same Secret to
  verify records before it announces them. No other account reads it; the
  coordinator checks with `kubectl auth can-i get secrets` for the agent and
  workbench accounts before any flag is set. Which accounts can read Secrets
  in `dev-env-system` today is Unknown.
- **How it is created.** Once, by the coordinator, through haynes-ops' path
  for a generated Secret in `dev-env-system`, never in git. Which path that
  is (an ExternalSecret from 1Password, or another) is Unknown here.
- **How it is rotated.** Write a new `current` and move the old value to
  `previous`. The operator signs with `current` and accepts either. In its
  next pass it signs every `pending` and `active` record again with
  `current`, and logs how many still verify only with `previous`. When that
  count is zero, `previous` is removed.
- **A missing or wrong code.** The record is ignored, as if absent, and one
  log line names the session. The watchdog of section 7.5 then suspends a pod
  that runs raised on it, and Tom is told. With no key, every record is
  ignored and every `POST` is refused: "This operator has no raise key."

The code defends against any writer of the annotation who cannot read the
key: Flux applying a manifest, a cluster admin by mistake, or an account
given patch rights later. Whoever can read the key or run code in the
operator pod can forge a record; that is the operator's own trust root.

### 6.7 Refusals

Every refusal is one plain sentence.

| Case | HTTP | Sentence |
| --- | --- | --- |
| The caller is the target | 403 | "A session may not raise itself." |
| The caller sits under the target | 403 | "`<caller>` works under `<target>`, so it may not raise it." |
| The caller's parent chain cannot be read | 403 | "The parent chain of `<caller>` cannot be read, so the operator cannot tell that it is not under `<target>`. Try again." |
| The caller is a session raised to workbench | 403 | "`<caller>` was raised to workbench, not started on it, so it may not raise others." |
| A policy caller without `spec.raise` | 403 | "This caller's policy does not allow raising sessions." |
| Any other caller not in Ruling 1 | 403 | "Only a superuser session, an always-on agent whose policy allows it, or Tom may raise a session." |
| No `owner`, or empty `words` | 422 | "A raise needs Tom's words: what he said, where and when." |
| `where` does not match the caller | 422 | "A `<caller kind>` records only words Tom gave it directly (`<allowed where>`); have the agent he told perform the raise." |
| `words` match a secret shape | 422 | "Tom's words look like they hold `<shape>`. Leave it out and send his words again." |
| `saidAt` too old or in the future | 422 | "The time you give for Tom's words must be within the last 24 hours. Ask him again." |
| The performer used these words before | 409 | "`<caller>` already used these words for a raise. A new raise needs a new word from Tom." |
| The used-word list is full | 503 | "The operator's list of used words is full. Raises resume as old entries expire on `<date>`." |
| `expectUID` differs | 409 | "Session `<name>` was replaced: its UID is not the one you read. Read it again." |
| Not eligible (safety rule 13) | 409 | For example "Session `<name>` is a summoned session; summoned sessions are never raised." |
| Profile not on the flag list, or not on the caller's policy | 422 | "This caller may not raise sessions to profile `<p>` on this operator." |
| Profile runs as a named coordinator identity | 422 | "Profile `<p>` runs as a named coordinator identity; a raise never gives one." |
| `startSessions` at depth 0 or 1 | 422 | "Session `<name>` sits at depth `<d>` and may already start sessions." |
| `startSessions` at depth 3 or a bad label | 422 | "Only a session at depth 2 may be raised to start sessions." |
| Within 60 seconds of a change on the target | 429 | "Session `<name>`'s raise changed less than a minute ago. Try again in `<n>` seconds." |
| A fourth raise by this performer in an hour | 429 | "`<caller>` has made three raises in the last hour. Try again after `<time>`." |
| No MAC key | 503 | "This operator has no raise key." |
| Both flags off | 403 | "This operator does not raise sessions." |

A board tap could be added later as a stricter second factor without
changing the record: the record would gain the card's id, and a raise would
become active only after Tom's tap as well as the notice. Nothing here builds
it (section 13).

## 7. The pod change

### 7.1 Where the effective profile is read

`buildSessionPod` (`pod.go:320`), which builds the agent pod for a first
start, a resume and an image move, and the watchdog of section 7.5. The hold
pod that rescues a volume never runs as a profile's account
(`pod.go:385-387`) and is unchanged. The volume is never rebuilt
(`volume.go:14-17`) and keeps the creation profile's label. No reader of
that label is in this repo; one in haynes-ops is Unknown.

### 7.2 What changes in the new pod

The new pod is the effective profile's pod in every way the profile decides:

- its ServiceAccount, and `DEV_ENV_WORKBENCH=1` for workbench (`pod.go:385-394`);
- env, envFrom and the remote block (`pod.go:434-443`), and mounts
  (`:510-522`);
- the profile's labels, so its egress tier and the broker's standing
  policies (`pod.go:341-342`, `templates.go:350-352`);
- the profile label, so the requester profile of its next grants
  (`grants.go:161-170`) and the thread fence (`threads.go:132-139`). The
  label is never the profile its children get: Ruling 7 and section 8.2
  (coordinator's ruling R4).

What the workbench profile holds is in haynes-ops `templates.yaml`, not in
this repo. DESIGN-060 section 4 lists what a workbench session can do that an
ordinary one cannot, including pod exec, `keeper-login` and the hardware SSH
key in its envFrom (its table, rows at design line 112-117). Section 8.4
lists the API routes that check the workbench account. The Claude token does
not change (`pod.go:676-682`).

Grants: the broker installs an active kube grant into the session's current
pod and installs it again into a new pod (`internal/broker/broker.go:566-580`).
A grant whose decision is bound to the requester's pod UID ends or is refused
after any pod change, as on any resume: delegate reach (`RequesterPodUID`,
`internal/broker/delegate_reach.go:175`) and maintenance (the coordinator's
`PodUID`, `grants.go:186`).

### 7.3 The restart step, and the suspend rule while it waits

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
new profile applies at its next resume."

**The suspend rule while a raise waits** (coordinator's ruling R5). While a
session has a valid, active record with a profile, and its pod's profile
label is not that profile, `POST /v1/sessions/{name}/suspend`
(`lifecycle.go:27-29`) refuses when the stored heartbeat says the agent is
busy or waiting: 409, "Session `<name>` has a raise waiting for its next
start and is busy (or waiting on a question). Suspend it when it is quiet,
or take the raise back, which suspends at once." The take-back route is the
exception, and says so. The operator's own suspends (the idle timer, the
watchdog) do not use this route. This closes the path where a plain suspend
and resume applies the raised profile with no quiet check.

What remains. The heartbeat is up to 60 seconds old, so a turn or a question
that starts after it can still be cut by the suspend, as
`docs/remote-session-upgrades.md` states for the drain. The conversation and
the volume stay; an open question is lost and has to be asked again.
Background commands end at the restart. A coordinator with a background
command reads busy (section 2.6), so it may never pass step 1; then the
profile applies at its next resume.

### 7.4 The phone entry

Measured on ordinary sessions: a resume keeps the phone entry and the
conversation (section 2.6). Not measured across a change of profile or
ServiceAccount; Unknown until the live proof. Slice (b)'s live proof reads
it on an unused test session, with DESIGN-065 section 5's readings before
and after (name, UID, conversation id, and the first 12 hex of the SHA-256
of the Remote Control session id and of its link, never the values) plus the
pod's ServiceAccount and profile label.

### 7.5 The watchdog (coordinator's ruling R3)

While either flag is set, the AgentSession reconciler checks each session
that has an agent pod (not a hold pod):

- the pod's profile label is not `spec.profile`, resolved to the templates'
  default when empty; and
- the pod's ServiceAccount is not the account of that resolved
  `spec.profile`; and
- no valid, active record names this UID with the pod's profile, or that
  profile is not on `--session-raise-profiles`.

When all three hold, it suspends the session the way the operator already
suspends a session itself: `spec.operatingMode` Suspended and suspended-by
`raise-without-record`, under the optimistic lock of an uncached read
(`summon_watchdog.go:265-290`). The usual suspend path rescues first and
removes the pod. It logs one line, records an Event, and hands a notice to
the keeper (section 6.4). The next pod is built from `spec.profile`.

Both the label and the account must differ, so a template change that moves
a profile to another account, which changes no pod label, never suspends a
session by itself. Removing a profile from `--session-raise-profiles`
suspends the sessions running raised on it. A record with a bad code counts
as no record.

### 7.6 Rollback and turning the flags off (coordinator's ruling R3)

The watchdog runs only while the feature is on. So before an operator
rollback below the build of slice (b), or before both flags are turned off,
the coordinator takes back every raise:

1. Find them. `agent-run list` shows `raised:` in its NOTE column (section
   10). A read-only `kubectl -n dev-agents get agentsessions -o json`
   filtered on the annotation `dev-env.haynesops.com/raise` finds every
   record, whatever its state. `kubectl -n dev-agents get pods -o
   custom-columns=NAME:.metadata.name,SA:.spec.serviceAccountName,PROFILE:.metadata.labels.dev-env\.haynesops\.com/profile`
   shows pods whose account or profile is not their session's.
2. Take back each active or pending raise with `agent-run raise <name>
   --take-back`, which suspends a raised pod at once.
3. Check that no pod still runs as an account other than its
   `spec.profile`'s.
4. Only then turn the flags off or roll back.

If that procedure is skipped, a pod already running raised keeps, until it
is suspended: its ServiceAccount and every Kubernetes right of it (for
workbench, pod exec in `dev-agents` and the rest of its Role); the API routes
that check the workbench account (section 8.4: keeper login, Codex remote
hosts and threads, titles of any session, any profile for its children); and
new grant requests carrying the raised profile label (`grants.go:167`). It
cannot start sessions at depth 3: the exemption reads the record, which is
ignored. Its next pod is built from `spec.profile`.

## 8. The dispatch exemption

### 8.1 The rule

In `admitCreate`, where it now refuses (`sessions.go:699-702`):

- If `c.kind` is a session and `c.depth` is 3 (its own label exactly `"2"`,
  as `strconv.Itoa` writes it), read the session with `s.Live.Get` inside the
  create lock. Let it pass the depth check only when
  `--enable-session-raise-dispatch` is on, the read succeeds, the live UID
  equals the caller's session UID, the live depth label is still `"2"`, and
  the record is valid (code included), active, names that UID, says depth
  `"2"` and has `startSessions`. A failed read refuses the exemption.
- The take-back takes the same create lock before it writes the record
  (Ruling 5), so on one replica a take-back and an admission cannot
  interleave. The lock is per replica (`sessions.go:681-685`): two replicas
  can still race, as they already can for the child cap, and at most the one
  create in flight on the other replica completes.
- Any other depth above 2 is refused as now. The message gains one sentence
  when a raise could help: "A session at depth 2 can be raised to start
  sessions (agent-run help raise)."
- The child cap (`sessions.go:703-710`) and every other admission run
  unchanged. `newSession` writes the true label `"3"` (`validate.go:387`)
  and `spec.parent` the raised session (`:268`).
- Only the direct create (`createSessionMode`). Scheduled work
  (`CreateScheduledSession`), the standby and summoned creates keep today's
  rule.

### 8.2 The profile of a raised session's child (coordinator's ruling R4)

A session caller is a **raised parent** when its pod's profile label is not
its `spec.profile`, resolved to the templates' default when empty. That
holds for an active profile raise on the pod, and for a pod that still runs
raised after a take-back. A dispatch-only raise leaves the label as created,
so its children follow today's rule.

In `newSession`'s profile switch (`validate.go:330-348`), a new case comes
after the Codex host and named-coordinator cases and before the workbench
case:

- The child's profile is the one it names, or the templates' default when it
  names none. `c.profile`, the pod label, is never copied.
- The create is refused (403) when that profile runs as the workbench
  account, the default included (the templates allow a default on the
  workbench account, `templates.go:617-624`), or as a named coordinator
  identity: "A raised session's child never runs as the workbench or a named
  coordinator; name another profile."
- The profile must be in the templates, as for every create
  (`validate.go:349-355`).

So a child of a raised session is never workbench by creation, and Ruling 7
holds for every session below it.

### 8.3 Every reader of the depth label

`git grep -n LabelDepth -- internal cmd api` at `2774e599`, and the readers of
the caller's depth:

| Reader | For a child at depth 3 | Fail closed |
| --- | --- | --- |
| `sessionDepth` and `resolveSession` (`caller.go:325`, `:332-340`) | `c.depth` is 4; `admitCreate` refuses (`sessions.go:700-702`). It starts nothing | Yes |
| `newSession` (`validate.go:384-388`) | Writes `"3"` | Not a reader |
| `NamedCoordinatorLaunchAllowed` and `NamedCoordinatorSessionEligible` (`named_coordinator.go:75-110`) | No shape admits depth 3, so it never runs as a named coordinator identity. It cannot launch one either: its child would be depth 4 | Yes |
| Broker `ancestry` (`delegate_reach.go:63-67`) | Depth above 2 is "ambiguous ancestry depth": no delegate decides its grants and it is never a delegate. Its grants go to a standing policy, an attested policy, or the 30-minute deny | Yes |
| Broker `topLevelSession` and `listedCreator` (`internal/broker/maintenance_attestation.go:121-129`, `:181-189`) | Not top-level, not a listed creator: no attested maintenance | Yes |
| Broker `CheckLegacyDelegate` (`legacy_delegate.go:91`) | Requires `"1"`: refused | Yes |
| `instructionPublisherImageEligible` (`internal/controller/instruction_image.go:43-53`) | Requires `"0"` or `"1"`: not eligible | Yes |
| API `sessionAncestry` (`grant_decision.go:299-320`, bound `maxDelegateAncestry` = 4 at `:61-64`) | Walks its three session parents, within the bound. Used by `delegateReach` (`:259-284`), the thread fence (`threads.go:139`) and the raise route's "under the target" check; an unverified chain refuses a raise | Follows the chain; the broker refuses a depth-3 grant anyway |
| `buildSummonSession` (`internal/apiserver/summon.go:668`) | Writes `"0"` for a summoned session | Not a reader |
| Usage rollup (`internal/apiserver/usage.go:146-157`) | Breadth-first, capped at 64 sessions, no depth bound: its usage rolls up to its ancestors | Not a guard |
| The raised session itself | Every reader sees depth 2, as before | Unchanged |
| haynes-ops (Kyverno, NetworkPolicy, dashboards) | Unknown: not in this repo | Unknown |

The comment on `LabelDepth` (`labels.go:113-116`, "A session at depth 2
creates no children") and DESIGN-001 section 3.4 gain the exemption by
reference to ADR-028. DESIGN-001 is accepted and is not edited.

### 8.4 Every reader of the pod's profile and of the workbench account

| Site | For a raised session | For a child at depth 3 |
| --- | --- | --- |
| `resolveSession` profile (`caller.go:317-320`) | `c.profile` is the raised profile; used only where the rows below say | Its own profile, never workbench (8.2) |
| Child profile switch (`validate.go:330-348`) | The new raised-parent case of 8.2; the workbench case at `:339` is never reached for it | Never reached: it cannot create |
| Codex remote create (`validate.go:162`) | A session raised to workbench may start a Codex remote host, as a workbench session may; the host runs on the default (`:331-334`) | Not workbench: refused |
| Named coordinator launcher (`named_coordinator.go:115-125`, used at `validate.go:364-366`) | **Today: allowed.** It returns the workbench-session launcher for any pod on the workbench account, and that shape needs only depth 1 for the new session (`:84-86`), so a raised depth-0 session could launch one. Slice (b) makes the launcher class workbench by creation (coordinator's ruling R6) | Cannot: its child would be depth 4 |
| Title of any session (`title.go:152`) | Allowed, as for a workbench session (Ruling 7 keeps it) | Not workbench: its own children only |
| Keeper login (`keeper_login.go:339`) | Allowed, as for a workbench session | Not workbench: refused |
| Codex RC thread (`codex_rc.go:29`) | Allowed, as for a workbench session | Not workbench: refused |
| Codex host profile and pool (`codexhost.go:234-242`, `:264`, `:271`, `:464`) | Chosen as for a workbench session; a host it starts runs on the default unless it names one | Its own profile |
| Grant requester profile (`grants.go:161-170`) | The raised profile, so standing policies for it apply; the take-back releases those grants (section 9) | Its own profile |
| Thread fence (`threads.go:132-139`) | Hosts on the raised profile, or its descendants | Hosts on its profile, or its descendants |
| Legacy delegate (`legacy_delegate.go:87-91`) | Requires the configured binding, depth `"1"` and `full`: a raised session is not that binding | Refused |
| Child decisions (`pod.go:89-93`) | Only for a session whose `spec.parent` is a configured coordinator account: never a raised Claude session | Its parent is a session name: never |
| Raise route (section 6.2) | A session raised to workbench may not perform a raise (Ruling 7) | Not workbench: refused |

### 8.5 The depth label itself (coordinator's ruling R9)

The label is written at create only (`validate.go:387`, `summon.go:668`), and
no code changes it later. Most readers fail closed on a missing or bad label
(the broker's `ancestry`, `NamedCoordinatorSessionEligible`,
`listedCreator`). One does not: `sessionDepth` takes a missing or bad label
as 0 (`caller.go:332-340`). So whoever can patch an AgentSession can already
let a depth-2 session start sessions by removing or garbling its label, with
no raise at all. That writer is the operator's own account, a cluster admin,
or Flux for a session declared in git; not the workbench account (section
2.4).

A raise does not widen this. Two things close it for the exemption and in
general:

1. The record carries the depth label at raise time inside its code, and the
   exemption requires both the live label and the record to say `"2"`.
2. The haynes-ops admission rule of safety rule 14 refuses any change to or
   removal of `dev-env.haynesops.com/depth` after create, from any user.
   Nothing in the code changes it after create, so the rule costs nothing.

## 9. Taking it back

`agent-run raise <session> --take-back`, the front door's
`session_take_back`, or `DELETE /v1/sessions/{name}/raise`.

- **Who.** Anyone who may perform a raise under Ruling 1, and the target
  itself. No word from Tom is needed: taking power back never waits for one.
- **The lock.** It takes the create lock before it writes (section 8.1).
- **The record.** `state` becomes `taken-back`, with `takenBackBy` and
  `takenBackAt` and a new code. It is kept, with Tom's words. One log line
  is written. A `pending` raise that is taken back never takes effect.
- **The right to start sessions** ends at the next create.
- **The profile.** A pod that runs the raised profile is suspended at once
  through the existing route, rescue first, with no wait for quiet. There is
  no option to keep it running (coordinator's ruling R3). Its next pod runs
  on `spec.profile`. A pod that has not yet taken the raised profile is left
  alone.
- **The notice.** A take-back notice is handed to the keeper (section 6.4).
  It never holds the take-back.
- **Children it started.** They keep running and keep counting against the
  cap. They start nothing. They are never reaped by a take-back; the caller
  may reap them by the normal route, which rescues first.
- **Grants.** The take-back writes `spec.release` on the session's active
  and pending grants whose requester profile is the raised profile, as the
  session's own release does. A pod-bound grant also ends with the pod.
  Whether the broker matches a standing policy again when an active grant's
  requester changes profile is Unknown; this release does not depend on it.
- **For a policy caller** such as Meatball, a take-back of a profile raise
  is an immediate suspend of a session it did not start (Ruling 9).
- **On an operator without the route, or with both flags off.** Section 7.6.

## 10. What readers show

**API.** `GET /v1/sessions/{name}`, `GET /v1/sessions` and the fleet view
carry one new optional object, `raise`, only on a session with a raise
record:

```json
"raise": {
  "id": "<16 hex digits>",
  "state": "active",
  "profile": "workbench",
  "fromProfile": "full",
  "startSessions": true,
  "owner": {"words": "<verbatim>", "where": "phone-prompt", "session": "<performer>", "saidAt": "2026-11-13T15:50:02Z", "ref": "op:830#7980"},
  "performedBy": "session/<performer>",
  "performedAt": "2026-11-13T15:52:40Z",
  "announcedAt": "2026-11-13T15:53:10Z",
  "podProfile": "full",
  "ignored": ""
}
```

`state` may also be `pending`, `unannounced`, `taken-back` or `ignored` (with
`ignored` giving the reason, such as a bad code). `podProfile` is the current
pod's profile label, on the single-session view only. The top-level
`profile` stays `spec.profile`. The view never carries the code or the key
id.

**`agent-run show`:**

```
profile:      full (at creation)
raised:       workbench; may start sessions (performed by session/<performer> at 2026-11-13T15:52:40Z; Tom told at 15:53:10Z)
on Tom's word: "<verbatim>" (phone prompt in <performer>, 2026-11-13T15:50:02Z; op:830#7980)
pod runs as:  full; the raised profile applies at its next start (agent-run raise <name> --restart)
```

Other forms: "raise pending: waiting for Tom to be told (attempt `<n>` of
12)"; "raise unannounced: Tom could not be told, so it never took effect";
"raise taken back by `<who>` at `<time>`"; "raise record ignored:
`<reason>`".

**`agent-run list`** adds to its NOTE column (`internal/agentrun/sessions.go:68`,
`note` at `:82`): `raised: workbench`, `raised: may start sessions`,
`raised: workbench at next start`, `raise pending`, or `pod raised without
record`. No new column, so no script that reads the columns changes. The
words are not in the list; `show` has them.

**`agent-run fleet`** adds one header line, `raised:   <name> (workbench),
<name> (may start sessions)`, and the same NOTE text.

**Portal.** Unchanged in these slices: `decodeSessionRow` refuses any key
outside its six (`web/portal/session/product.go:520-524`). A later slice may
add a mark; haynes-ops then pins the portal before the operator that sends
it. Tom learns of raises from the notice.

**Front door.** `session_raise` and `session_take_back` (slice d) answer with
the same text as `show`. `session_status` is unchanged.

**The command.** Tom's word is "elevate"; the command is `raise`:

```
agent-run raise <session> --profile <name> --words "<Tom's words>" --said-at <time> [--ref <ref>]
agent-run raise <session> --start-sessions --words "<Tom's words>" --said-at <time> [--ref <ref>]
agent-run raise <session> --restart
agent-run raise <session> --take-back
```

From a session it sends `where: phone-prompt`; from Tom's account,
`own-call`. It reads the session first and sends its UID as `expectUID`. Its
refusals are the sentences of section 6.7. `agent-run help raise` says what a
raise is, that it needs Tom's own words given to this agent, and that Tom is
told before it takes effect, in under 80 words.

**Older readers.** An `agent-run` older than the slice that adds `raise`
reads `GET /v1/sessions/{name}` strictly on `show --json --once` and `usage
export` (`strictWireJSON`, `internal/agentrun/show_once.go:47`). It cannot
read a raised session on those two paths, and reads every other session as
before (safety rule 18). The test: an unraised session's view holds no
`raise` key, as `TestShowSaysTitleRenameOff` does for titles.

## 11. Slices

Each slice is one worker session, behind flags that are off by default.
Tests use fakes or envtest at two CPUs. A new package (for example
`internal/sessionraise`) gets its line in `docs/repo-layout.md`. Docs that
describe the changed behaviour change in the same PR. **No flag is set until
slices (a) and (b) are both installed with the haynes-ops fence, the key and
the two ledger ConfigMaps; slice (b) is never flagged on without (a), the
notice and the code check** (coordinator's ruling R11).

| Slice | What it changes | Tests must prove | Other-family review | CRD copy | Pins |
| --- | --- | --- | --- | --- | --- |
| (a) The record, the notice and the route | The annotation, its code (section 6.6) and its readers; `GET`, `POST` and `DELETE /v1/sessions/{name}/raise` for workbench-by-creation sessions and Tom's account; Tom's words, the normalised hash and the secret check, whose shape list is taken from haynes-ops `SECRET_SHAPES` (`night-merge.mjs` 963-1003) and the front door's OpenProject shape, one test each; the ledger ConfigMap with used words and rate limits; the keeper's raise notice runnable and its ledger ConfigMap; activation on Sent; `unannounced` after 12 attempts or 24 hours; the dispatch exemption with the live read in the lock; take-back under the lock, with suspend, notice and grant release; the suspend rule of 7.3; the `raise` key in the views; `show`, `list`, `fleet`; `agent-run raise` without `--restart`; both flags, off | Every row of 6.7; each caller kind refused; an unverified chain refused; a raise without words, with a stale `saidAt`, with each secret shape, with a `where` that does not match the caller, and with words this performer used on another target (also after an operator restart) is refused; `performedBy` comes from the token; a record is `pending` until the keeper's ledger shows Sent, stays `pending` on Failed and Ambiguous and outside the window, and becomes `unannounced` at its bound; a take-back is effective with no notice yet; a record with a missing or wrong code, a UID mismatch or both flags off is ignored and logged; rotation accepts `previous` and re-signs; the record write leaves `metadata.generation` unchanged (envtest); an unraised session's view has no `raise` key; a depth-2 caller with an active record creates a child labelled `"3"` under the cap; a cached active record after a take-back does not pass (live read); a failed live read refuses; a depth-3 caller is refused; scheduled work stays refused; a table test of every reader in 8.3 at depth 3; the rate limits hold across a restart; suspend refuses a busy or waiting session while a raise waits, and take-back does not | Yes: a write route with an access rule, a new notice path, and it overrides a global guard | No | Operator (the keeper ships in it); agent image (agent-run) |
| (b) The profile at the next pod, the watchdog and the restart step | The effective profile in `buildSessionPod`; the watchdog of 7.5; the raised-parent child case of 8.2; workbench by creation for the named coordinator launcher (8.4) and for performing a raise; `agent-run raise --restart` | The pod gets the raised profile's account, env, envFrom, remote block, mounts and labels; flags off, taken back, bad code and UID mismatch build from `spec.profile`; the hold pod and the volume are unchanged; a named coordinator profile is never used; the watchdog suspends a pod that runs raised with no valid active record, with a bad code, or after its profile left the flag, and leaves a pod whose account moved by a template change alone; a raised parent's child that names no profile gets the default, never the pod label; a raised parent's child is refused on a workbench-account profile, the default included; a raised depth-0 session cannot launch a named coordinator, and a workbench-by-creation session still can; `--restart` refuses on busy, waiting, attached and a stale heartbeat, and otherwise calls suspend then resume | Yes: grants access, and suspends live sessions | No | Operator; agent image |
| (c) The raise action in a CallerPolicy | `spec.raise` on the front-door shape and its CEL rule; `readCallerPolicy` checks it; `policyCallerAllowed` adds the three routes when it is set; `where: chat` for policy callers; the in-place amendment of DESIGN-060 sections 6 and 7.1, marked | A policy without `raise` reaches no raise route; `raise` with `spec.summon` is refused by the schema; a bad `raise` fails the policy closed; a profile on the policy but not on the flag is refused, and the reverse too; a policy caller may raise and take back a session it did not start but still reads and reaps only its own; `chat` is accepted only from a policy caller; a stale CRD that prunes `raise` leaves no right | Yes: grants a new right to an always-on agent | Yes, the CallerPolicy CRD, first | Operator; then the haynes-ops policy edit Tom approves |
| (d) The front door tools | `session_raise` and `session_take_back` in `internal/frontdoor/tools.go` | Each tool reads the UID, sends Tom's words unchanged with `where: chat`, and shows the route's refusal sentence as is; descriptions under 30 words | Yes: an always-on agent's path to superuser | No | Front door |

**Install order.** In haynes-ops: the admission rule of safety rule 14; the
key Secret; the two ledger ConfigMaps; the operator's get and update on its
ledger and get on the keeper's raise ledger, and the keeper's get and update
on its raise ledger, list on AgentSessions and read of the key. Then the
operator image pin for (a) and (b). Then the flags, set by the coordinator:
`--session-raise-profiles=workbench` and `--enable-session-raise-dispatch`.
For Meatball: the CallerPolicy CRD copy, the operator pin for (c), the
policy's `raise` field, which Tom approves, then the front door pin for (d).

**First live proof.** On an unused test session, never a working coordinator
and never `haynesnetwork-1113-150420`. Create it at depth 2 on the default
profile, through a test parent at depth 1. Read R0 (DESIGN-065 section 5's
identity readings, plus the pod's ServiceAccount and profile label). A
workbench-by-creation coordinator asks Tom by phone prompt and, on his word,
runs `agent-run raise <test> --profile workbench --start-sessions` with his
words. Read R1: state `pending`, then `active` once Tom's phone shows the
notice; identity and pod unchanged; record and log line hold his words. Run
`--restart`. Read R2: same name, UID, conversation id and both hashes;
ServiceAccount `dev-env-workbench`. Create one child with no profile: label
`"3"`, default profile. From the child, try a create: refused. Take back.
Read R3: the take-back notice arrived; the session was suspended; after a
resume, ServiceAccount `dev-env-agent`; a create from the session is refused.
Record R0 to R3 on story 830, then reap the test sessions under the normal
rules.

## 12. Open questions for the owner

None remain. Tom answered Q-066-01 on 2026-11-13 (section 1.2). The
coordinator ruled on the former Q-066-02, Q-066-03 and Q-066-04 the same day
(rulings 3, 8 and 7), and on every review finding (section 14).

## 13. What this rejects

| Rejected | Why |
| --- | --- |
| A `raise` card on the Asks board, decided by Tom's tap (the first draft's Ruling 1) | Tom chose his word to an agent (section 1.2). The grant card path it would have reused is not installed: the coordinator read no grant-Ask flag and an Ask CRD with no enum on its kind field, and the portal's `preferred_username` (ADR-021 build PR 6) is not in source. A tap can be added later as a stricter second factor without changing the record (section 6.7). |
| Accept only a phone-prompt answer the operator stored (review finding 1) | The operator stores no phone-prompt answer (section 2.5). The notice to Tom is the control (coordinator's ruling R1). |
| Refuse words that do not name the target (review finding 8) | Words are free text. The notice shows the words and the target together (coordinator's ruling R8). |
| A startup check that the admission rule is installed (review finding 9) | The record's code makes it independent of the rule; the rule stays as a second layer, installed before any flag (coordinator's ruling R9). |
| `--keep-running` on a take-back | A pod would keep a raised account with no record (coordinator's ruling R3). |
| "Used once" compared with place and time, on one session | The performer chooses the time. Words are compared alone, per performer, across every target, in a ledger that survives a restart (coordinator's ruling R2). |
| A relay: an agent records words Tom gave another agent | The agent that heard him performs the raise (safety rule 4). A relay adds a party the record cannot name. |
| A trusted client performs a raise | It includes the v1 pod (`caller.go:27-29`) and is none of the kinds Tom named. |
| Any CallerPolicy caller may raise | Only with `spec.raise`, which Tom approves. A summoning caller never. |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie about who started the session, and where. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise `maxSessionDepth` to 3 for everyone | It is one of three guards against runaway agents (DESIGN-001 design line 4983-4986). One session's need does not justify a fleet change. |
| Start a new session to continue the work | That is what happened in the case, and it loses the conversation. |
| An AccessGrant of a new type | A grant is always requested by the session for itself (`grants.go:161-170`), which is the self-raise this design forbids. It lasts at most 8 hours. A standing GrantPolicy could approve it. The broker installs access into a pod; it does not choose a pod's own identity, and it does not write AgentSessions. |
| A separate CRD for raises | A new CRD, RBAC, CRD copy and watch, for the trust the code and the fence already give. The controller already chooses a pod's account from API-written metadata: the named coordinator admission label (`pod.go:391-393`). |
| A status field | A stale CRD prunes it (haynes-ops #3502), an older operator's status write drops it, and status is what the operator observed, not a decision. |
| The tracker as the audit record | It is written as the agent and can be down. The operator writes the annotation and the log line from the authenticated caller, and the keeper records the notice (Ruling 2). |
| The operator restarts the session itself to apply a raise | `docs/remote-session-upgrades.md` keeps automatic stops away from a user's remote session, and a coordinator reads busy while a background command runs (`activity.go:251-263`). The watchdog suspends only a pod that has no valid record, which is the opposite case. |
| Change the identity of the running pod | A pod's ServiceAccount is fixed for its life. No exec or token step changes what the pod is. |
| A standing policy approves raises | No raise without Tom's word. |
| An end date on a raise | The coordinator's ruling (Ruling 8). |
| A raise to a named coordinator identity | One holder at a time and three launch shapes (section 2.4). A raise would bypass both. |
| Raised sessions pass workbench on to their children | Ruling 7 and section 8.2. |

## 14. Review record

A design review by another model family (Grok 4.7) on head `91678100`,
`~/.shared/handoffs/op830/design-review-grok.md`: 4 HIGH, 5 MEDIUM, 2 LOW.
The rulings R1 to R11 are the coordinator's
(`dev-env-fable-coordinator-1109`, 2026-11-13), not Tom's.

| Finding | What it said | Ruling | Where the design answers it |
| --- | --- | --- | --- |
| 1, HIGH | The route never checks that Tom said the words; two workbench peers can raise each other on invented words; Tom is never told | R1 | Ruling 10; section 6.4; safety rule 5; Ruling 1 (no stored answers, the notice is the control); section 13 |
| 2, HIGH | "Used once" is not real: the performer picks the time, older words are lost from the record, other targets are open; the 24-hour check bounds only the stated clock | R2 | Section 6.5 (ledger); safety rule 3; section 6.3 (normalised words, the clock sentence); Ruling 8 |
| 3, HIGH | A pod keeps a raised account after `--keep-running`, rollback or flags off; no reconciler suspends it; "fails closed" overclaims | R3 | Ruling 11; section 7.5 (watchdog); section 7.6 (procedure and the gap); Ruling 5 and section 9 (no `--keep-running`); safety rules 12 and 16 |
| 4, HIGH | A raised parent's child that names no profile copies the raised pod label and becomes workbench by creation | R4 | Section 8.2; Ruling 7; section 7.2 (sentence removed); slice (b) tests |
| 5, MEDIUM | The quiet rule binds only `--restart`; a plain suspend and resume applies the raise with no check; a turn in the heartbeat gap can be cut | R5 | Section 7.3 (suspend rule, take-back exception, what remains); Ruling 3 |
| 6, MEDIUM | The table claims a raised parent never launches a named coordinator; today's code allows it for a raised depth-0 session | R6 | Section 8.4 row; Ruling 7; slice (b) |
| 7, MEDIUM | The dispatch check reads the cache; take-back takes no lock; an unreadable chain is not a refusal | R7 | Ruling 4 and section 8.1 (live read, refuse on failure, shared lock); Ruling 1 and section 6.7 (unverified chain); Ruling 5 |
| 8, MEDIUM | `spec.raise` gives a fleet-wide raise and suspend; who sets it is unstated | R8 | Ruling 9 (GitOps change Tom approves; what it gives); section 9; section 13 (words need not name the target) |
| 9, MEDIUM | The record rests on an admission rule no slice installs or checks; the depth label is unfenced | R9 | Ruling 2; section 6.6 (code and key); section 8.5 (depth label); safety rules 7 and 14 |
| 10, LOW | The rate limits have no store and reset on restart | R10 | Section 6.5; safety rule 15 |
| 11, LOW | Slice (b) can be flagged alone; the secret shapes and the DESIGN-060 edit sit in no slice | R11 | Section 11 (flag rule, rows (a) and (c)); section 6.3 (secret shapes) |
| Attack notes | The reader table misses the child-decision parents and the `isWorkbenchSession` sites | (with R6) | Section 8.4 |
