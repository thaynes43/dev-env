# DESIGN-066: Raise a running session from outside it

- **Status:** Proposed 2026-11-13, for the coordinator's review. Revised three
  times the same day: after Tom answered Q-066-01 (section 1.2), so the
  go-ahead is his word to an agent; then after two design reviews by another
  model family, with the coordinator's rulings on each finding (section 15).
  Nothing is built or installed. No flag, CRD change, route, command or
  haynes-ops change exists for it.
  [ADR-028](../adrs/028-raise-a-running-session.md) is Proposed with it.
- **Tracking:** [Story OP830](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/830).
  Tom's ask is recorded on story 828 comment 7927, and his answer on how he
  gives the go-ahead on story 830 comment 7980. Why the case's session
  started where it did is story 831 (section 1.4).
- **Origin:** Tom, 2026-11-13, quoted in sections 1.1 and 1.2. Nothing else
  here is Tom's. The coordinator's reading of his answer, its rulings on the
  former Q-066-02 to Q-066-04 and its rulings on the two reviews are marked as
  the coordinator's (`dev-env-fable-coordinator-1109`, 2026-11-13). The rest
  is proposed by a worker for the coordinator, which decides it.
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
  at `5449647f`. haynes-ops was read in a local clone at `72ad7c49` (the
  admission rules) and through the GitHub API at `3ad4b39f` (the secret
  shapes). The reviews are `~/.shared/handoffs/op830/design-review-grok.md`
  (on `91678100`) and `design-review-grok-2.md` (on `bbefbb0a`). The facts of
  the case and the installed flags and rights in section 2 are the
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
  fixed for its life. Its labels are not: they can be patched.
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
  and decides by the pod's account, not by `spec.profile` or the pod's
  labels (`caller.go:141-144`, `:255-282`).
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
  (the repo's test copy, `internal/broker/testdata/haynes-ops/operator.yaml:49-51`).
  So can any cluster admin, and Flux for an AgentSession declared in git
  (DESIGN-060 section 4 table, design line 116).
- DESIGN-042 is not on main. On the branch of open PR 782, its section 6.4
  proposes a new named delegate identity and adds no rule to the code above.
  The rule in force is the code.

### 2.5 What a go-ahead, a notice and a signature can build on today

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
window, if one is set (`--ask-notify-window`, `cmd/dev-env-keeper/main.go:271`;
`askNoticeWindow.allows`, `ask_notifications.go:115-125`; the tick returns at
`:202-204`). It writes a reservation to its own ConfigMap before every send,
so an attempt that was reserved is never replayed silently (`:149-151`,
`:270-287`), sends through the keeper's Pushover sender (`:313-337`), and
records the outcome as Sent (Pushover accepted it for delivery), Failed
(definitely not delivered: no credentials or a 4xx refusal) or Ambiguous (it
may have reached Pushover) (`:324-336`). Its ledger ConfigMap is precreated by
GitOps, and the keeper has get and update on that one name only
(`internal/keeper/ask_notification_store.go:51-56`). Its push is
content-free: the fixed text "An agent needs your answer" and a board link
(`ask_notifications.go:22-24`, `:313-315`). The summoned-session pager "copies
the Ask notifier" for operator-written session records, sends "any hour",
renders text from the records (`internal/keeper/summon_pages.go:32-47`,
`:617-700`, `:925`), and makes links in free text inert (`summonRedact`,
`:793`). The keeper ships in the operator image
(`images/operator/Dockerfile:1-2`, `:40`).

**Signatures.** The repo already signs with Ed25519 from the Go standard
library: home-retention receipts are verified with `ed25519.Verify` over a
domain-separated byte string, the prefix `dev-env/home-retention/v1/<domain>`
and a newline, then the JSON of a typed payload
(`internal/homeretention/verify.go:95-115`, `:130-134`). The keeper also
generates Ed25519 keys for SSH (`internal/keeper/ssh.go:119`).

**Admission rules.** haynes-ops already fences an operator-written annotation
with a ValidatingAdmissionPolicy: `dev-env-maintenance-annotations` lets only
`system:serviceaccount:dev-env-system:dev-env-operator` change the
maintenance decision annotation on an AccessGrant, with `failurePolicy: Fail`
and a binding in `Deny` mode (haynes-ops
`kubernetes/main/apps/dev-env-system/rbac/app/maintenance-annotations.yaml`
lines 1-48, local clone at `72ad7c49`; its first line reads "UNAPPLIED"). The
identity guard has the same kind (`identity-guard.yaml` lines 36-44).

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
  rejects). A heartbeat is up to 60 seconds old (DESIGN-065 section 9.1).

### 2.7 What this document could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry. It stays Unknown until the live proof (section 7.4).
- Whether Meatball's CallerPolicy is installed today.
- Whether the keeper's `--ask-notify-window` is set today. The raise notice
  does not use it (section 6.4).
- Whether the keeper's ServiceAccount may list AgentSessions today (the
  summoned-session pager needs it; its install is Unknown).
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- What the workbench profile's env, envFrom, mounts and remote block hold
  today (haynes-ops `templates.yaml`).
- Which accounts can read Secrets in `dev-env-system` today. Section 11 makes
  that an install check.
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.

## 3. Outcome

An action taken from outside a session, on Tom's word, raises what that
running session may do. The agent Tom gave his word to performs the raise
and records his words. A notice to Tom, with those words and the take-back
command, is accepted for delivery before the raise can take effect, and the
raise waits two more minutes, during which a take-back wins. The session
keeps its name, UID, conversation, volume, work in progress and its entry in
the Claude app. Two things can be raised:

1. its **profile**, for example from the default profile to `workbench`;
2. its **right to start sessions**, for a session at depth 2.

The session can never raise itself. `spec.parent`, `spec.profile`, the depth
label and the creation record are never rewritten. `maxSessionDepth` stays
2. A raised pod never outlives its record in any operator build that has the
watchdog, whatever the flags say.

## 4. Rulings proposed

Rulings 3, 7 and 8 contain the coordinator's rulings of 2026-11-13 on the
former Q-066-02 to Q-066-04. The coordinator's rulings on the two reviews
(R1 to R11, N1 to N11) are marked where they apply, and listed in section
15. Everything else here is proposed by a worker for the coordinator. None of
it is Tom's, except the sentences quoted in section 1.

**Ruling 1. Who performs a raise, on whose word.** Tom decides, by his word
(section 1.2). The platform cannot verify his word. It records the performing
agent's statement of it, as the grant attestation route records "agent
attestation, not verified human identity" (section 2.5). The operator already
runs that route (`--enable-grant-attestation=true`, the coordinator's read of
2026-11-13). What makes a false statement visible is the notice of Ruling 10.

These may perform a raise:

1. **A superuser session.** A session that is workbench by creation (Ruling
   7), that is not the target and does not sit under the target. "Under" is
   read live: the target is not on the performer's chain of parents, matched
   by name and UID, as `sessionAncestry` walks it
   (`internal/apiserver/grant_decision.go:299-320`). Any depth. A chain that
   cannot be read (`ancestryUnverified`, `grant_decision.go:311-312`) or is
   too deep is a refusal (R7).
2. **An always-on agent under a CallerPolicy that grants the raise action.**
   Meatball first: its front-door policy gains one optional field,
   `spec.raise` (Ruling 9). Absent means no raise. "A meatball equivalent
   always on agent" is another caller admitted by its own front-door policy,
   with its own `spec.raise`; it is not a new mechanism.
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
coordinator, and the coordinator asks Tom itself. The operator stores no
phone-prompt answer it could check the words against (section 2.5), so a
stored-answer check is not built (R1).

**Ruling 2. The record, its signature and the audit record.** One
annotation on the AgentSession, `dev-env.haynesops.com/raise`, holding one
JSON object (section 6.1). Only the operator writes it. It is not a spec
write, so `metadata.generation` does not move (ADR-027). Three things make it
count, and every reader checks all three:

1. it names the session's UID;
2. its Ed25519 signature, made with a private key only the operator reads,
   verifies over every field but the signature, Tom's words included, and
   the hash of the words matches the words (N1, N4, N10; section 6.6);
3. its `id` and `state` match the operator's own ledger (N3; section 6.5).

A record that fails any check is ignored, as if absent, and logged. So a
forged, replayed or edited annotation is ignored even where an admission rule
is missing (R9). The admission rules of section 11 stay as a second layer.

The record carries Tom's word, since that is the evidence: what he said,
verbatim; where; when; and the caller that performed the raise, taken from
the token, never from the body. A raise without his words is refused.

**The audit record is the operator's and the keeper's, not the agent's.**
The annotation holds the current or last raise of the session with its
words; the operator ledger holds each session's record id and state and every
used word; one operator log line is written per raise, activation and
take-back, with the same fields; and the keeper's ledger holds each notice
and its signed outcome. The performing agent may also write the words on its
tracker story, but that is not the record.

**Ruling 3. The profile, and when it applies.** The effective profile is the
record's profile when the record counts (Ruling 2), is `active`, and its
profile is on the operator flag `--session-raise-profiles`. Otherwise it is
`spec.profile`. The operator reads it when it builds a session pod
(`pod.go:320`). A running pod keeps its ServiceAccount, so a profile raise
takes effect at the session's next pod. The operator never restarts a
session to apply a raise.

The coordinator's ruling of 2026-11-13 on the former Q-066-02: a raised
profile is applied by the explicit restart step, `agent-run raise <session>
--restart` (section 7.3). The acting agent takes that step at the session's
first quiet moment, and never while the session is busy or waiting on a
question. If it is not taken, the profile applies at the session's next
resume. While a profile raise is pending or active and not yet on the pod,
the ordinary suspend route refuses a busy, waiting or attached session; the
take-back route does not (R5, N9; section 7.3).

`--session-raise-profiles` is empty by default. A profile that runs as a
named coordinator identity is never given, whatever the flag says. The
expected value is `workbench`.

**Ruling 4. The right to start sessions.** In `admitCreate`, a session caller
whose own depth label is exactly `2` passes the depth check when its session,
read with `s.Live.Get` inside the create lock (not the cached copy of section
2.1), carries a record that counts (Ruling 2), is `active`, says depth `2`
and grants `startSessions`, and `--enable-session-raise-dispatch` is on. A
failed read, of the session or the ledger, refuses the exemption (R7).
Section 8 gives the rule and every reader of the depth label. The child gets
the true label `3`, `spec.parent` the raised session, the same child cap and
every other admission. A session at depth 3 is never exempt and starts
nothing. The exemption serves the direct create only, not scheduled work,
because a schedule outlives the moment Tom gave his word.

**Ruling 5. Taking it back.** Anyone who may perform a raise under Ruling 1,
and the target itself, may take one back. Taking back needs no word from
Tom. It takes the create lock before it writes (R7). It writes the ledger
first and the annotation second, so the record stops counting at the ledger
write. It suspends at once, rescue first, a session whose pod runs on the
raised account; there is no option to keep it running (R3). A take-back of a
`pending` raise wins over any later notice outcome (N2). Children already
started keep running and keep counting against the cap. Tom is told. Section
9.

**Ruling 6. What readers show.** The view's `profile` stays the creation
profile. A raise is shown beside it, with Tom's words, in one new key that
appears only on a session that has a raise record. Section 10.

**Ruling 7. A raise never spreads.** A session raised to workbench is a
workbench session for its own pod, its Kubernetes rights and the API routes
that check the workbench account (section 8.4). It is not one for three
things: it may not perform a raise; it may not launch a named coordinator
identity (R6: today's code does not refuse it; slice (b) changes it); and its
children never get the workbench account.

A session is **workbench by creation** only when its `spec.profile`,
resolved to the templates' default when empty, names a profile that runs as
the workbench account. A templates read that fails counts as "not by
creation".

The coordinator's ruling of 2026-11-13 on the former Q-066-04, and its
rulings R4 and N8: a raised session's children never inherit the raised
profile. A **raised parent** is a session whose pod runs as an account other
than the account of its `spec.profile`; the pod's labels play no part. Its
child admission is its own branch, run before every other profile case
(section 8.2): the child gets the profile it names, or the templates' default
when it names none; the create is refused when that profile runs as the
workbench account or a named coordinator identity, on every path, an omitted
profile and a Codex host included; a failed templates read refuses the
create; the pod label is never copied. So no child of a raised session is
ever workbench by creation, and neither is any session below it.

**Ruling 8. How long a raise lasts.** The coordinator's ruling of 2026-11-13
on the former Q-066-03: a raise lasts until it is taken back or the session
is removed. It has no end date. It survives a suspend and resume, because the
annotation, the ledger entry and the UID stay. A restore makes a new session
with a new UID, which gets nothing. The 24-hour bound on `saidAt` (section
6.3) bounds the time the agent states against the operator's clock. It proves
nothing about when Tom spoke, and it does not bound the raise.

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
- **Who sets it** (R8). Setting `spec.raise` on Meatball's policy is a GitOps
  change in haynes-ops, and Tom approves it.
- **What it gives** (R8). With it, that caller may raise any eligible
  session, not only the sessions it started. Through a take-back of a
  profile raise, it may also suspend at once a session it did not start. Its
  other routes keep their own-sessions fence. The words need not name the
  target; the notice shows both.
- This amends DESIGN-060 section 7.1 ("no ... configurable rights") and
  section 6 ("never gets ... the workbench") for this one right: Meatball
  itself still never runs as the workbench. DESIGN-060 is amended in place in
  slice (c).

**A Meatball equivalent.** Today the front-door shape admits only Meatball:
the schema fixes its account (`callerpolicy_types.go:60`), and the API reads
one fixed object, `dev-env-system/meatball` (`callerpolicy.go:21-24`). A
second always-on agent needs that shape to admit another account, one policy
object per account, as the summon shape already names its object after its
account (`callerpolicy_types.go:11`). That is its own story, outside these
slices. When it lands, that agent's policy carries its own `spec.raise`.

**Ruling 10. The notice gates the raise** (R1, N2, N5, N11). A raise is
written `pending`. The keeper sends Tom a notice naming the target session
(name and title), the performing agent, what was raised, the words the agent
stated, when the raise will take effect and the one command that takes it
back. The keeper signs the outcome with its own key (N1). When the operator
reads a validly signed Sent, meaning Pushover accepted the notice for
delivery, it waits two minutes, then moves the record from `pending` to
`active` by compare-and-swap. A take-back in those two minutes, or at any
time before the swap, wins: a `taken-back` or `unannounced` record ignores a
later Sent. A notice that cannot be sent leaves the raise pending, and after
twelve attempts the raise ends `unannounced` and never takes effect. Sent is
not proof that Tom saw it: the phone may be silenced. Raise and take-back
notices go at any hour; the keeper's quiet window does not hold them (N5).
Section 6.4.

**Ruling 11. Power never outlives its record** (R3, N7). In every operator
build that has slice (b), whatever the flags say, the reconciler checks every
session its ledger says was ever raised, in any state. When such a session's
pod runs as an account other than the account of its `spec.profile`, and no
record that counts and is `active` names a profile on the flag whose account
is that pod's account, the reconciler suspends it, rescue first (section
7.5). Sessions never raised are never touched. With both flags off no record
is honoured, so every raised pod is uncovered and is suspended. A rollback
below the build of slice (b) stays a written procedure (section 7.6).

## 5. Safety rules

1. **No self-raise.** The target never performs its own raise. No session
   under the target performs one. No session raised to workbench performs
   one. A parent chain that cannot be read is a refusal.
2. **No word, no raise.** Every raise carries Tom's words, where he said
   them and when. A raise without them is refused.
3. **A word is used once.** The operator keeps the hash of every word it
   accepted, per performer, in its own ledger, and refuses the same words,
   normalised with punctuation and compatibility forms folded, for any target
   (R2, N6). The ledger survives a restart. A raise is refused when the
   ledger cannot be read.
4. **The agent that heard him performs it.** A session records only words
   Tom gave it; a policy caller only words he gave it in chat; Tom's own
   account only its own call. A relay is refused.
5. **The notice gates the raise.** A raise is not active until a notice with
   its words is accepted for delivery and two more minutes have passed,
   during which a take-back wins. A take-back is announced too.
6. **Only the named kinds perform** (Ruling 1). A CallerPolicy caller
   performs only with `spec.raise`, which Tom approves in haynes-ops.
7. **A record counts only when UID, signature and ledger agree** (Ruling 2).
   Anything else is ignored and logged.
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
12. **No restart to apply a raise; no power without a record.** The operator
    never suspends or restarts a session to apply a raise. `--restart` and
    the ordinary suspend refuse a session that is busy, waiting or attached
    while a profile raise waits for its pod. The watchdog suspends a pod that
    runs raised without a record that covers it (Ruling 11).
13. **Eligible sessions only.** A Claude session in `remote` or `local` mode
    that is not summoned (`spec.caller`), not a project canary or private
    pilot, not budget-bound, not a coordinator host's child, not on a named
    coordinator identity, not being reaped and not archived.
14. **Fenced state.** Admission rules in haynes-ops let only the operator's
    account change the annotation and its ledger, only the keeper's account
    change its notice ledger, only each owner change its public key, and
    nobody change the depth label after create. They are install steps of
    slice (a), each with its check (section 11).
15. **Rate limits that survive a restart.** Per target: one record change in
    any 60 seconds. Per performer: at most three raises in any rolling hour;
    take-backs are not counted. Both are read from the operator ledger.
16. **Schema order.** The AgentSession and Ask CRDs do not change. The
    CallerPolicy CRD gains `spec.raise` in slice (c), so haynes-ops gets that
    copy before the operator pin that reads it.
17. **Older readers.** A new key appears in the session view only on a
    session with a raise record (DESIGN-065 section 9.7). An `agent-run`
    older than the slice that adds it cannot read that one session with
    `show --json --once` or `usage export`.
18. **Review.** Each slice that grants access or overrides a guard gets an
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
  "noticeSentAt": "2026-11-13T15:53:10Z",
  "activeFrom": "2026-11-13T15:55:10Z",
  "takenBackBy": "",
  "takenBackAt": "",
  "keyId": "<first 16 hex of SHA-256 of the operator's public key>",
  "sig": "<Ed25519 signature, base64>"
}
```

- `state` is `pending` (waiting for its notice and its two minutes),
  `active`, `unannounced` (its notice could not be sent; it never took
  effect) or `taken-back`.
- `id` is the first 16 hex digits of SHA-256 over the session UID,
  `performedAt` and `wordsHash`. The ledgers, the notice and the log lines
  carry it.
- `profile` is empty or a DNS label. At least one of `profile` and
  `startSessions` is set. `fromProfile` is the pod's profile label when the
  raise was performed. `depth` is the session's depth label then.
- `owner` is section 6.3. `performedBy` is `caller.String()` of the
  authenticated caller, never from the body. Times are RFC 3339 in UTC.
  `noticeSentAt` is the time in the keeper's signed Sent; `activeFrom` is two
  minutes later.
- `keyId` and `sig` are section 6.6.

A record that does not count (Ruling 2) is ignored, logged, and `agent-run
show` says why. A new raise for a session whose record is `taken-back` or
`unannounced` replaces the record. The earlier one stays in the operator log.

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
- The checks of section 6.7 run, against a live read of the target and of
  the operator ledger.
- The operator writes the ledger first: the used word, the rate counters, and
  the session's record `id` with state `pending`. The word is spent once
  that write is confirmed. Then it writes the signed record as `pending`
  with the `resourceVersion` precondition and retry of `setOperatingMode`
  (`lifecycle.go:40-160`), writes one log line, and answers 202 with the
  session view: "Raised `<name>` to `<profile>`. Tom is being told; it takes
  effect two minutes after the notice is accepted, unless taken back."
- The same raise already pending or active with the same words: 200, nothing
  written.

**`DELETE /v1/sessions/{name}/raise`** takes a raise back:

```json
{"expectUID": "<uid the caller read>"}
```

It takes the create lock, writes `taken-back` to the ledger and then to the
signed record, writes its log line, suspends a session whose pod runs on the
raised account, hands the take-back notice to the keeper, and acts as
section 9 says. 200 with the view.

**Flags.** `--session-raise-profiles` (a comma list of profiles a raise may
give) and `--enable-session-raise-dispatch`, both off by default. With both
off, `POST` answers 403, "This operator does not raise sessions.", and does
nothing else. `GET` and `DELETE` still work: taking a raise back is never
switched off. The operator refuses every `POST` while it has no signing key
or cannot read its ledger. An operator build without slice (b) refuses to
start with `--session-raise-profiles` set: "Profile raises need the pod
change of DESIGN-066 slice (b)." An operator without the routes answers 404,
and `agent-run raise` says the operator does not have them.

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
| `words` | Required. 1 to 300 characters, the most the notice shows in full (N11); longer words are refused. Verbatim as Tom said or typed them. When what he gave was a choice in a phone prompt, `words` holds the question as asked and the option he chose, in that order, both verbatim. No control or format characters. Refused when it matches a secret shape (below). |
| `where` | Required. `phone-prompt`: Tom's answer to this session's phone prompt, or a message he typed to this session; only a session caller sends it, and the API sets `session` to the caller's own name. `chat`: a chat message to the always-on agent that performs the raise; only a policy caller sends it. `own-call`: only Tom's ServiceAccount sends it, and `words` is his own reason. Any other pairing of caller and `where` is refused. |
| `saidAt` | Required. When Tom said it, as the agent states. Not more than 5 minutes after the operator's clock and not more than 24 hours before it. This bounds the stated time against the operator's clock. It proves nothing about when Tom spoke (Ruling 8). |
| `ref` | Optional. A nonsecret reference to where the words are also kept, such as a tracker comment, in the shape `grantattest.ValidRef` allows (`attest.go:52`). |

**Normalised words** (N6). For the used-word check the operator normalises
the words: Unicode NFKC, so compatibility forms such as fullwidth letters
fold to their plain form; every punctuation character (Unicode category P)
removed; lower case; leading and trailing space removed; every run of white
space made one space. Then SHA-256. So `yes`, `Yes.` and a fullwidth `yes`
are one word. A phone-prompt answer differs from one raise to the next
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

The notice is sent through the keeper's Pushover sender with the write-ahead
page ledger the Ask push uses (section 2.5). The runnable that reads raise
records is new, in the keeper, and copies the Ask notifier, as the
summoned-session pager already did (`summon_pages.go:32-47`).

**What it reads.** Every 30 seconds, as the Ask push does, the leader keeper
lists the AgentSessions that carry the raise annotation, reads the operator
ledger uncached, and keeps only records that count (Ruling 2, checked with
the operator's public key). It takes every `pending` record it has not yet
sent, and every `taken-back` record and every watchdog suspend it has not yet
announced.

**Any hour** (N5). The raise notifier does not use the Ask push's window
(`--ask-notify-window`, `cmd/dev-env-keeper/main.go:271`): a raise Tom did not
ask for is exactly what should reach him at night. Like the summoned-session
pager, it sends at any hour.

**What it says.** Title: "[dev-env] raise: `<session>`". Message: "`<session>`
(`<title>`) was raised by `<performer>`: profile `<from>` to `<profile>`;
may start sessions one level deeper. It says you said: "`<words>`"
(`<where>`, `<saidAt>`). It takes effect at `<time>`, two minutes after this
notice was accepted. To stop it: agent-run raise `<session>` --take-back".
The words, at most 300 characters, are shown in full. The title and the
words both go through the summoned-session pager's redaction, which makes
links inert (`summonRedact`, `summon_pages.go:793`; N11). A take-back notice
says who took it back and when; a watchdog notice says the pod ran raised
without a record and was suspended.

**The signed outcome** (N1). For each notice the keeper writes a reservation
to its own ledger ConfigMap, `dev-env-system/dev-env-keeper-raise-notices`,
before the send, as `ask_notification_store.go:51-56` does, then the
outcome: the record `id`, the session UID, the attempt, the outcome (Sent,
Failed or Ambiguous) and the time, signed with the keeper's private key
(section 6.6). Only the keeper's account may write that ConfigMap (section
11).

**Activation** (N2, N5). The operator reads the keeper's ledger. On a Sent
whose keeper signature verifies, for a record that is still `pending` with
the same `id` in the operator ledger, it waits until two minutes after the
signed time, then swaps `pending` for `active` in the ledger and then in the
record, each with a `resourceVersion` precondition that fails if the state is
no longer `pending`. A failed swap is not retried as an activation: the
record is read again, and a `taken-back` or `unannounced` record ignores the
Sent.

**When a send fails.** Failed and Ambiguous both leave the raise `pending`.
The keeper tries again 5 minutes later (`askRequesterInterval`,
`ask_notifications.go:26`), up to 12 attempts in all. A second push for the
same raise is accepted; silence is not. After the twelfth attempt without a
Sent, about an hour, the operator swaps `pending` for `unannounced`, logs it,
and the raise never takes effect. Its words stay spent; a new raise needs a
new word from Tom.

**Take-back notices** never hold the take-back: it takes effect at once. The
notice is attempted the same way, and its outcome is logged.

### 6.5 The operator ledger

One ConfigMap, `dev-env-system/dev-env-session-raise-ledger`, precreated by
GitOps, with get and update for the operator on that one name and no create,
as the session allowances ConfigMap is set up
(`cmd/dev-env-operator/main.go:287`). Only the operator's account may write
it, by an admission rule (N6; section 11). Read uncached, written with the
`resourceVersion` precondition. It holds:

- **Raised sessions** (N3, N7): for every session ever raised, its name, UID,
  current record `id` and `state`, and the time of its last change. Kept
  while the session exists; at most 512 entries.
- **Used words** (R2): for each accepted raise, the performer, the hash of
  its normalised words, the target's UID and the time. Kept 90 days; at most
  2048 entries.
- **Raises per performer** (R10): the time of each raise, kept one hour.

Every record write first writes the matching `id` and `state` here. A ledger
that cannot be read refuses every raise and makes every reader treat every
record as not counting. When a list is full and nothing in it is old enough
to prune, every raise is refused with a plain sentence; entries are never
dropped early.

### 6.6 Signatures and keys

Two Ed25519 key pairs, from the Go standard library `crypto/ed25519`, the
scheme the repo already uses for home-retention receipts (section 2.5). No
secret is shared between the operator and the keeper (N1, N10).

| Pair | Signs | Private key | Public key | Verified by |
| --- | --- | --- | --- | --- |
| Operator | Every raise record | Secret `dev-env-system/dev-env-session-raise-operator-key`, read only by the operator | ConfigMap `dev-env-system/dev-env-session-raise-operator-public` | Every reader in the operator, and the keeper before it sends a notice |
| Keeper | Every notice outcome | Secret `dev-env-system/dev-env-session-raise-keeper-key`, read only by the keeper | ConfigMap `dev-env-system/dev-env-session-raise-keeper-public` | The operator before it activates a raise |

- **What is signed** (N4). One canonical byte string: the prefix
  `dev-env/session-raise/v1/record` and a newline, then the record's JSON with
  every field except `sig`, keys sorted, no spaces, Tom's verbatim words
  included. A notice outcome uses the prefix `dev-env/session-raise/v1/notice`
  and the outcome's JSON. This copies the home-retention form
  (`internal/homeretention/verify.go:95-115`). Every reader also recomputes
  `wordsHash` from `words` and refuses a mismatch.
- **How a pair is made.** Each process makes its own pair at its first start
  with `ed25519.GenerateKey`, writes the private key into its own Secret, and
  publishes the public key with its `keyId` in its own public-key ConfigMap.
  GitOps precreates both objects empty; the owner has get and update on those
  two names only, with no create, list or watch. The private key never leaves
  its process and its Secret; no person or agent handles it.
- **Rotation is a re-key.** To replace a key, the coordinator first takes
  back every raise (section 7.6), then empties the owner's Secret through
  GitOps; the owner makes a new pair at its next start. Records signed with
  the old key stop counting, so any raised pod that was missed is suspended
  by the watchdog. A take-back does not rotate the key (N3); the ledger
  match is what retires an old record.
- **Who else can read a private key.** The operator cannot check from inside
  which other accounts can read its Secret, so it does not try. It is an
  install step with an exact check (section 11).

Whoever can read a private key, run code in its owner's pod, or change the
owner's Deployment or public-key ConfigMap through haynes-ops can forge in
that owner's name. That is the owner's own trust root, and the haynes-ops
delivery path behind it (DESIGN-039, Proposed).

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
| `words` over 300 characters | 422 | "Tom's words must fit in 300 characters so the notice shows them whole." |
| `where` does not match the caller | 422 | "A `<caller kind>` records only words Tom gave it directly (`<allowed where>`); have the agent he told perform the raise." |
| `words` match a secret shape | 422 | "Tom's words look like they hold `<shape>`. Leave it out and send his words again." |
| `saidAt` too old or in the future | 422 | "The time you give for Tom's words must be within the last 24 hours. Ask him again." |
| The performer used these words before | 409 | "`<caller>` already used these words for a raise. A new raise needs a new word from Tom." |
| The ledger cannot be read | 503 | "The operator cannot read its raise ledger, so it raises nothing now. Try again." |
| A ledger list is full | 503 | "The operator's raise ledger is full. Raises resume as old entries expire on `<date>`." |
| `expectUID` differs | 409 | "Session `<name>` was replaced: its UID is not the one you read. Read it again." |
| Not eligible (safety rule 13) | 409 | For example "Session `<name>` is a summoned session; summoned sessions are never raised." |
| Profile not on the flag list, or not on the caller's policy | 422 | "This caller may not raise sessions to profile `<p>` on this operator." |
| Profile runs as a named coordinator identity | 422 | "Profile `<p>` runs as a named coordinator identity; a raise never gives one." |
| `startSessions` at depth 0 or 1 | 422 | "Session `<name>` sits at depth `<d>` and may already start sessions." |
| `startSessions` at depth 3 or a bad label | 422 | "Only a session at depth 2 may be raised to start sessions." |
| Within 60 seconds of a change on the target | 429 | "Session `<name>`'s raise changed less than a minute ago. Try again in `<n>` seconds." |
| A fourth raise by this performer in an hour | 429 | "`<caller>` has made three raises in the last hour. Try again after `<time>`." |
| No signing key | 503 | "This operator has no raise key yet." |
| Both flags off | 403 | "This operator does not raise sessions." |

A board tap could be added later as a stricter second factor without
changing the record: the record would gain the card's id, and a raise would
become active only after Tom's tap as well. Nothing here builds it (section
14).

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
  (`grants.go:161-170`) and the thread fence (`threads.go:132-139`). It is
  never the profile its children get (Ruling 7, section 8.2).

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

### 7.3 The restart step, and the suspend rule while a raise waits

`agent-run raise <session> --restart`, run by the agent that performed the
raise, or by Tom, after a profile raise is active. By the coordinator's
ruling (Ruling 3), the acting agent takes it at the session's first quiet
moment; if it is not taken, the profile applies at the next resume.

1. It reads the session view. It refuses unless the record is active, the
   pod's account is not yet the raised profile's account, the phase is
   Running, the agent state is `idle` (not `busy`, not `waiting`), no client
   is attached, and the last heartbeat is under 90 seconds old.
2. It calls the existing suspend and waits for Suspended, which comes after
   the rescue (D-51).
3. It calls the existing resume and waits for Running and, for a remote
   session, `registered`.

It never forces and never loops. Its refusal says which reading failed, for
example "Session `<name>` is busy. Try again when it is quiet; otherwise the
new profile applies at its next resume."

**The suspend rule while a raise waits** (R5, N9). While a session has a
record that counts, with a profile, in state `pending` or `active`, and its
pod does not yet run as that profile's account, `POST
/v1/sessions/{name}/suspend` (`lifecycle.go:27-29`) refuses when the stored
heartbeat says the agent is busy or waiting, or a client is attached: 409,
"Session `<name>` has a raise waiting for its next start and is busy (or
waiting on a question, or attached). Suspend it when it is quiet, or take the
raise back, which suspends at once." The take-back route is the exception,
and says so. The operator's own suspends (the idle timer, the watchdog) do
not use this route. The heartbeat gap remains (section 12).

### 7.4 The phone entry

Measured on ordinary sessions: a resume keeps the phone entry and the
conversation (section 2.6). Not measured across a change of profile or
ServiceAccount; Unknown until the live proof. Slice (b)'s live proof reads
it on an unused test session, with DESIGN-065 section 5's readings before
and after (name, UID, conversation id, and the first 12 hex of the SHA-256
of the Remote Control session id and of its link, never the values) plus the
pod's ServiceAccount and profile label.

### 7.5 The watchdog (R3, N7)

In every operator build that has slice (b), whatever the flags say, the
AgentSession reconciler checks every session the operator ledger lists as
ever raised, in any state, that has an agent pod (not a hold pod). It
suspends the session when both hold:

- the pod's ServiceAccount is not the account of `spec.profile`, resolved to
  the templates' default when empty; and
- no record that counts (Ruling 2) and is `active` names a profile that is on
  `--session-raise-profiles` and whose account is the pod's ServiceAccount.

The pod's labels play no part, so patching a label does not dodge it. A
session the ledger never listed is never touched, so a template change that
moves a profile to another account cannot suspend sessions by itself. With
both flags off no record is honoured, so every raised pod is uncovered and is
suspended. Removing a profile from `--session-raise-profiles` suspends the
sessions running raised on it. A record that does not count is no record.

The suspend is the operator's own: `spec.operatingMode` Suspended and
suspended-by `raise-without-record`, under the optimistic lock of an uncached
read (`summon_watchdog.go:265-290`). The usual suspend path rescues first and
removes the pod. The watchdog logs one line, records an Event, and hands a
notice to the keeper (section 6.4). The next pod is built from
`spec.profile`. When the ledger cannot be read, the watchdog suspends nothing
that pass and logs it; the pod builder honours no record meanwhile.

### 7.6 Rollback below the watchdog (R3)

An operator build without slice (b) has no watchdog. So before an operator
rollback below that build, the coordinator takes back every raise. This is
the install step of slice (b) for any rollback (section 11):

1. Find them. `agent-run list` shows `raised:` in its NOTE column (section
   10). A read-only `kubectl -n dev-env-system get configmap
   dev-env-session-raise-ledger -o json` lists every session ever raised. A
   read-only `kubectl -n dev-agents get pods -o
   custom-columns=NAME:.metadata.name,SA:.spec.serviceAccountName` shows
   each pod's account.
2. Take back each `pending` or `active` raise with `agent-run raise <name>
   --take-back`, which suspends a raised pod at once.
3. Check that no pod of a listed session runs as an account other than its
   `spec.profile`'s.
4. Only then roll back.

If that procedure is skipped, a pod already running raised keeps, until it
is suspended: its ServiceAccount and every Kubernetes right of it (for
workbench, pod exec in `dev-agents` and the rest of its Role); the API routes
that check the workbench account (section 8.4: keeper login, Codex remote
hosts and threads, titles of any session, any profile for its children); and
new grant requests carrying the raised profile label (`grants.go:167`). It
cannot start sessions at depth 3: an operator without slice (a) has no
exemption. Its next pod is built from `spec.profile`.

## 8. The dispatch exemption

### 8.1 The rule

In `admitCreate`, where it now refuses (`sessions.go:699-702`):

- If `c.kind` is a session and `c.depth` is 3 (its own label exactly `"2"`,
  as `strconv.Itoa` writes it), read the session with `s.Live.Get` and the
  operator ledger uncached, inside the create lock. Let it pass the depth
  check only when `--enable-session-raise-dispatch` is on, both reads
  succeed, the live UID equals the caller's session UID, the live depth label
  is still `"2"`, and the record counts (Ruling 2), is `active`, says depth
  `"2"` and has `startSessions`. A failed read refuses the exemption.
- The take-back takes the same create lock before it writes (Ruling 5), so on
  one replica a take-back and an admission cannot interleave. The lock is per
  replica (`sessions.go:681-685`); the remainder is a known limit (section
  12).
- Any other depth above 2 is refused as now. The message gains one sentence
  when a raise could help: "A session at depth 2 can be raised to start
  sessions (agent-run help raise)."
- The child cap (`sessions.go:703-710`) and every other admission run
  unchanged. `newSession` writes the true label `"3"` (`validate.go:387`)
  and `spec.parent` the raised session (`:268`).
- Only the direct create (`createSessionMode`). Scheduled work
  (`CreateScheduledSession`), the standby and summoned creates keep today's
  rule.

### 8.2 The profile of a raised session's child (R4, N8)

A session caller is a **raised parent** when its pod's ServiceAccount is not
the account of its `spec.profile`, resolved to the templates' default when
empty. The pod's labels play no part. That holds for an active profile raise
on the pod, and for a pod that still runs raised after a take-back until the
watchdog suspends it. A dispatch-only raise leaves the account as created, so
its children follow today's rule.

In `newSession`'s profile switch (`validate.go:330-348`), a new case comes
first, before the Codex host case:

- A templates read that fails refuses the create: the operator cannot tell
  whether the caller is a raised parent, or what the child's profile runs as.
- For a raised parent, the child's profile is the one it names, or the
  templates' default when it names none. A Codex host it starts gets the
  default, as today. `c.profile`, the pod label, is never copied.
- The create is refused (403) when that profile, named or default, runs as
  the workbench account (the templates allow a default on the workbench
  account, `templates.go:617-624`) or as a named coordinator identity: "A
  raised session's child never runs as the workbench or a named coordinator;
  name another profile."
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
| `resolveSession` profile (`caller.go:317-320`) | `c.profile` is the pod's label; used only where the rows below say | Its own profile, never workbench (8.2) |
| Child profile switch (`validate.go:330-348`) | The raised-parent case of 8.2 runs first, decided by the pod's account; no later case is reached | Never reached: it cannot create |
| Codex remote create (`validate.go:162`) | A session raised to workbench may start a Codex remote host, as a workbench session may; the host gets the default profile, refused if that runs as the workbench account (8.2) | Not workbench: refused |
| Named coordinator launcher (`named_coordinator.go:115-125`, used at `validate.go:364-366`) | **Today: allowed.** It returns the workbench-session launcher for any pod on the workbench account, and that shape needs only depth 1 for the new session (`:84-86`), so a raised depth-0 session could launch one. Slice (b) makes the launcher class workbench by creation (R6) | Cannot: its child would be depth 4 |
| Title of any session (`title.go:152`) | Allowed, as for a workbench session (Ruling 7 keeps it) | Not workbench: its own children only |
| Keeper login (`keeper_login.go:339`) | Allowed, as for a workbench session | Not workbench: refused |
| Codex RC thread (`codex_rc.go:29`) | Allowed, as for a workbench session | Not workbench: refused |
| Codex host profile and pool (`codexhost.go:234-242`, `:264`, `:271`, `:464`) | Chosen as for a workbench session; a host it starts runs on the default unless it names one | Its own profile |
| Grant requester profile (`grants.go:161-170`) | The pod's label, so standing policies for the raised profile apply; the take-back releases those grants (section 9) | Its own profile |
| Thread fence (`threads.go:132-139`) | Hosts on the raised profile, or its descendants | Hosts on its profile, or its descendants |
| Legacy delegate (`legacy_delegate.go:87-91`) | Requires the configured binding, depth `"1"` and `full`: a raised session is not that binding | Refused |
| Child decisions (`pod.go:89-93`) | Only for a session whose `spec.parent` is a configured coordinator account: never a raised Claude session | Its parent is a session name: never |
| Raise route (section 6.2) | A session raised to workbench may not perform a raise (Ruling 7) | Not workbench: refused |
| The watchdog (section 7.5) | Reads the pod's account, never its labels | Never raised itself: not listed, never touched |

### 8.5 The depth label itself (R9)

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
general: the signed record carries the depth label at raise time, and the
exemption requires both the live label and the record to say `"2"`; and an
admission rule of slice (a) refuses any change to or removal of
`dev-env.haynesops.com/depth` after create, from any user. Nothing in the code
changes it after create, so the rule costs nothing.

## 9. Taking it back

`agent-run raise <session> --take-back`, the front door's
`session_take_back`, or `DELETE /v1/sessions/{name}/raise`.

- **Who.** Anyone who may perform a raise under Ruling 1, and the target
  itself. No word from Tom is needed.
- **The lock and the order.** It takes the create lock, then writes
  `taken-back` to the operator ledger, then to the signed record. The record
  stops counting at the ledger write (Ruling 2).
- **A pending raise** that is taken back never takes effect, whatever notice
  outcome arrives later (N2).
- **The right to start sessions** ends at the next create.
- **The profile.** A pod that runs as the raised account is suspended at
  once through the existing route, rescue first, with no wait for quiet and
  no option to keep it running (R3). Its next pod runs on `spec.profile`. A
  pod that has not yet taken the raised profile is left alone. If the suspend
  write fails, the watchdog suspends it on its next pass.
- **The notice.** A take-back notice is handed to the keeper (section 6.4).
  It never holds the take-back.
- **The record** is kept, with Tom's words, `takenBackBy` and `takenBackAt`.
  One log line is written. The key is not rotated (N3).
- **Children it started.** They keep running and keep counting against the
  cap. They start nothing. They are never reaped by a take-back; the caller
  may reap them by the normal route, which rescues first.
- **Grants.** The take-back writes `spec.release` on the session's active
  and pending grants whose requester profile is the raised profile, as the
  session's own release does. A pod-bound grant also ends with the pod.
- **For a policy caller** such as Meatball, a take-back of a profile raise
  is an immediate suspend of a session it did not start (Ruling 9).

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
  "noticeSentAt": "2026-11-13T15:53:10Z",
  "activeFrom": "2026-11-13T15:55:10Z",
  "podAccount": "dev-env-agent",
  "ignored": ""
}
```

`state` may also be `pending`, `unannounced`, `taken-back` or `ignored` (with
`ignored` giving the reason: UID, signature, words hash or ledger mismatch).
`podAccount` is the current pod's ServiceAccount, on the single-session view
only. The top-level `profile` stays `spec.profile`. The view never carries
the signature or the key id.

**`agent-run show`:**

```
profile:      full (at creation)
raised:       workbench; may start sessions (performed by session/<performer> at 2026-11-13T15:52:40Z; active from 15:55:10Z)
on Tom's word: "<verbatim>" (phone prompt in <performer>, 2026-11-13T15:50:02Z; op:830#7980)
pod runs as:  dev-env-agent; the raised profile applies at its next start (agent-run raise <name> --restart)
```

Other forms: "raise pending: notice not yet accepted (attempt `<n>` of 12)";
"raise pending: notice accepted at `<time>`, takes effect at `<time>`";
"raise unannounced: the notice could not be sent, so it never took effect";
"raise taken back by `<who>` at `<time>`"; "raise record ignored:
`<reason>`". It never says Tom saw a notice.

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
raise is, that it needs Tom's own words given to this agent, and that it
takes effect two minutes after Tom's notice is accepted, in under 80 words.

**Older readers.** An `agent-run` older than the slice that adds `raise`
reads `GET /v1/sessions/{name}` strictly on `show --json --once` and `usage
export` (`strictWireJSON`, `internal/agentrun/show_once.go:47`). It cannot
read a raised session on those two paths, and reads every other session as
before (safety rule 17). The test: an unraised session's view holds no
`raise` key, as `TestShowSaysTitleRenameOff` does for titles.

## 11. Slices

Each slice is one worker session. Tests use fakes or envtest at two CPUs. A
new package (for example `internal/sessionraise`) gets its line in
`docs/repo-layout.md`. Docs that describe the changed behaviour change in the
same PR. No flag is set until every install step of slices (a) and (b) has
passed its check (R11).

**Slice (a): the record, the notice, the route and the right to start
sessions.**

- **Builds.** The record, its Ed25519 signature and its readers; the
  operator key pair and the keeper key pair, each made by its owner; the
  operator ledger; Tom's words with the NFKC and punctuation fold, the
  300-character cap and the secret check, whose shapes come from haynes-ops
  `SECRET_SHAPES` (`night-merge.mjs` 963-1003) and the front door's
  OpenProject shape, one test each; `GET`, `POST` and `DELETE
  /v1/sessions/{name}/raise` for workbench-by-creation sessions and Tom's
  account; rate limits; the keeper's raise notifier, any hour, with signed
  outcomes and its ledger; activation two minutes after a signed Sent by
  compare-and-swap; `unannounced` after twelve attempts; the dispatch
  exemption with the live reads in the lock; take-back under the lock, ledger
  first, with notice and grant release; the `raise` key in the views;
  `show`, `list`, `fleet`; `agent-run raise` without `--restart`; both flags,
  off.
- **Tests must prove.** Every row of 6.7; each caller kind refused; an
  unverified chain refused; a raise without words, over 300 characters, with
  a stale `saidAt`, with each secret shape, with a `where` that does not
  match the caller, and with words this performer used before on another
  target, also as `Yes.` and in fullwidth form, and after an operator
  restart, is refused; `performedBy` comes from the token; a record is
  `pending` until a signed Sent and two minutes, stays `pending` on Failed,
  Ambiguous and an unsigned or wrongly signed Sent, and becomes `unannounced`
  after twelve attempts; a take-back before activation wins over a later
  Sent; a record with a missing or wrong signature, a changed `words` with
  the old hash and signature, a UID mismatch, or an `id` and `state` that do
  not match the ledger (a replayed old active record) is ignored and logged;
  a ledger that cannot be read refuses every raise and every exemption; the
  record write leaves `metadata.generation` unchanged (envtest); an unraised
  session's view has no `raise` key; a depth-2 caller with an active record
  creates a child labelled `"3"` under the cap; a cached active record after a
  take-back does not pass; a depth-3 caller is refused; scheduled work stays
  refused; a table test of every reader in 8.3 at depth 3; rate limits hold
  across a restart.
- **Install steps, each with its check.** All in haynes-ops, before any flag.
  The admission rules follow the shape of `dev-env-maintenance-annotations`
  (section 2.5): a ValidatingAdmissionPolicy with `failurePolicy: Fail` and a
  binding with `validationActions: [Deny]`.
  1. *The record:* only `system:serviceaccount:dev-env-system:dev-env-operator`
     may change the annotation `dev-env.haynesops.com/raise` on an
     AgentSession.
  2. *The depth label:* nobody may change or remove
     `dev-env.haynesops.com/depth` on an AgentSession after create.
  3. *The operator ledger and its public key:* only the operator's account
     may update or delete `dev-env-session-raise-ledger` and
     `dev-env-session-raise-operator-public`.
  4. *The keeper ledger and its public key:* only
     `system:serviceaccount:dev-env-system:dev-env-keeper` may update or
     delete `dev-env-keeper-raise-notices` and
     `dev-env-session-raise-keeper-public`.
  5. *The objects:* the two private-key Secrets and the four ConfigMaps,
     precreated empty; get and update by name for their owner only; the
     operator's get on the keeper's ledger and public key; the keeper's get
     on the operator ledger and public key, and list on AgentSessions in
     `dev-agents`.
  6. *Who can read a private key:* a read-only pass over every RoleBinding in
     `dev-env-system` and every ClusterRoleBinding (`kubectl get rolebindings
     -n dev-env-system -o json`, `kubectl get clusterrolebindings -o json`),
     resolving each bound role's rules, lists every subject that can get,
     list or watch `secrets` in `dev-env-system`, or create `pods/exec`
     there. The only subjects allowed are the key's owner and the cluster's
     admin groups, named in the check's record on story 830. Where the
     checking account may impersonate, `kubectl auth can-i get
     secret/<name> -n dev-env-system --as=system:serviceaccount:<ns>:<sa>`
     confirms each. The operator cannot run this check on itself, so it is
     not a startup check.

  The check for rules 1 to 4: `kubectl get validatingadmissionpolicy
  <name> -o yaml` and `kubectl get validatingadmissionpolicybinding <name> -o
  yaml` show the expression naming only the owner account, `failurePolicy:
  Fail`, `validationActions: [Deny]`, and no type-checking warnings in the
  policy's status. The check for rule 5: each object exists by name, and the
  owner's first start has filled its key and published its public key. Even
  where a rule is missing, a forged or replayed annotation does not count,
  because it does not match the operator ledger (N3).
- **When run alone.** An operator build with slice (a) and without (b)
  refuses to start with `--session-raise-profiles` set (section 6.2). With
  `--enable-session-raise-dispatch` it grants only the right to start
  sessions, after the notice and its two minutes. It never changes a pod.
- **Other-family review.** Yes: a write route with an access rule, two new
  key pairs, a new notice path, and it overrides a global guard.
- **CRD copy.** No. **Pins.** Operator image (the keeper ships in it); agent
  image (agent-run).

**Slice (b): the profile at the next pod, the watchdog and the restart
step.**

- **Builds.** The effective profile in `buildSessionPod`; the watchdog of
  7.5, running whatever the flags say; the raised-parent child case of 8.2;
  workbench by creation for the named coordinator launcher (8.4) and for
  performing a raise; the suspend rule of 7.3; `agent-run raise --restart`;
  the operator now accepts `--session-raise-profiles`.
- **Tests must prove.** The pod gets the raised profile's account, env,
  envFrom, remote block, mounts and labels; flags off, taken back, a record
  that does not count, and a ledger that cannot be read build from
  `spec.profile`; the hold pod and the volume are unchanged; a named
  coordinator profile is never used; the watchdog suspends a listed
  session's pod that runs raised with no covering record, with the flags off,
  after its profile left the flag, and after its label was patched back to
  `spec.profile`, and never touches a session the ledger does not list, even
  after a template change; a raised parent's child that names no profile
  gets the default, never the pod label; a raised parent's child is refused
  on a workbench-account profile, the default included, and a raised
  parent's Codex host is refused when the default runs as the workbench
  account; a failed templates read refuses the create; a raised depth-0
  session cannot launch a named coordinator, and a workbench-by-creation
  session still can; the ordinary suspend refuses a busy, waiting or attached
  session while a profile raise is pending or active and not on the pod, and
  take-back does not; `--restart` refuses on busy, waiting, attached and a
  stale heartbeat, and otherwise calls suspend then resume.
- **Install steps.** The live proof below, on an unused test session. Before
  any operator rollback below this build, the procedure of section 7.6, with
  its step 3 as the check.
- **When run alone.** It is built on slice (a)'s record, keys and ledger and
  is never in a build without them.
- **Other-family review.** Yes: grants access, and suspends live sessions.
- **CRD copy.** No. **Pins.** Operator image; agent image.

**Slice (c): the raise action in a CallerPolicy.**

- **Builds.** `spec.raise` on the front-door shape and its CEL rule;
  `readCallerPolicy` checks it; `policyCallerAllowed` adds the three routes
  when it is set; `where: chat` for policy callers; the in-place amendment of
  DESIGN-060 sections 6 and 7.1, marked.
- **Tests must prove.** A policy without `raise` reaches no raise route;
  `raise` with `spec.summon` is refused by the schema; a bad `raise` fails the
  policy closed; a profile on the policy but not on the flag is refused, and
  the reverse too; a policy caller may raise and take back a session it did
  not start but still reads and reaps only its own; `chat` is accepted only
  from a policy caller; a stale CRD that prunes `raise` leaves no right.
- **Install steps.** The CallerPolicy CRD copy in haynes-ops, checked by
  `kubectl get crd callerpolicies.dev-env.haynesops.com -o yaml` showing
  `spec.raise`; then the operator pin; then the policy's `raise` field, a
  GitOps change Tom approves, checked by reading the policy back.
- **When run alone.** Without slice (a) there is no raise route, so the field
  grants nothing.
- **Other-family review.** Yes: a new right for an always-on agent.
- **CRD copy.** Yes, first. **Pins.** Operator image.

**Slice (d): the front door tools.**

- **Builds.** `session_raise` and `session_take_back` in
  `internal/frontdoor/tools.go`.
- **Tests must prove.** Each tool reads the UID, sends Tom's words unchanged
  with `where: chat`, and shows the route's refusal sentence as is;
  descriptions under 30 words.
- **Install steps.** The front door pin, after slice (c) is installed.
- **When run alone.** Against an operator without the routes, or a policy
  without `raise`, each tool answers the route's refusal and changes nothing.
- **Other-family review.** Yes: an always-on agent's path to superuser.
- **CRD copy.** No. **Pins.** Front door.

**First live proof.** On an unused test session, never a working coordinator
and never `haynesnetwork-1113-150420`. Create it at depth 2 on the default
profile, through a test parent at depth 1. Read R0 (DESIGN-065 section 5's
identity readings, plus the pod's ServiceAccount and profile label). A
workbench-by-creation coordinator asks Tom by phone prompt and, on his word,
runs `agent-run raise <test> --profile workbench --start-sessions` with his
words. Read R1: state `pending`, then a notice on Tom's phone, then `active`
two minutes after the signed Sent; identity and pod unchanged; the record,
the ledger and the log line hold his words. Run `--restart`. Read R2: same
name, UID, conversation id and both hashes; ServiceAccount
`dev-env-workbench`. Create one child with no profile: label `"3"`, default
profile. From the child, try a create: refused. Take back. Read R3: the
take-back notice arrived; the session was suspended; after a resume,
ServiceAccount `dev-env-agent`; a create from the session is refused. Record
R0 to R3 on story 830, then reap the test sessions under the normal rules.

## 12. Known limits

These remain by design or as accepted remainders of the reviews (the
coordinator's rulings of 2026-11-13).

1. **Tom's word is an agent's statement.** The platform cannot verify it.
   Two workbench-by-creation sessions, neither under the other, could each
   state words for a raise of the other. The control is the notice and its
   two minutes, and Sent is not proof that Tom read it.
2. **The heartbeat gap** (first review, finding 5). The suspend rule and
   `--restart` read the stored heartbeat, up to 60 seconds old, so a turn or
   a question that starts inside that gap can still be cut by a suspend. The
   conversation and the volume stay.
3. **One create across replicas** (first review, finding 7). The create lock
   is per replica. A create already in flight on another replica when a
   take-back is written can still make one depth-3 child. That child can
   start nothing and counts against the cap.
4. **Rollback below the watchdog.** An operator build without slice (b) has
   no watchdog; section 7.6 is the procedure.

## 13. Open questions for the owner

None remain. Tom answered Q-066-01 on 2026-11-13 (section 1.2). The
coordinator ruled on the former Q-066-02, Q-066-03 and Q-066-04 the same day
(rulings 3, 8 and 7), and on every review finding (section 15).

## 14. What this rejects

| Rejected | Why |
| --- | --- |
| A `raise` card on the Asks board, decided by Tom's tap (the first draft's Ruling 1) | Tom chose his word to an agent (section 1.2). The grant card path it would have reused is not installed: the coordinator read no grant-Ask flag and an Ask CRD with no enum on its kind field, and the portal's `preferred_username` (ADR-021 build PR 6) is not in source. A tap can be added later as a stricter second factor without changing the record (section 6.7). |
| Accept only a phone-prompt answer the operator stored | The operator stores no phone-prompt answer (section 2.5). The notice is the control (R1). |
| Refuse words that do not name the target | Words are free text. The notice shows the words and the target together (R8). |
| A shared HMAC key for the operator and the keeper | Either holder could sign for the other. Two Ed25519 pairs, no shared secret (N1, N10). |
| A startup check that the admission rules are installed, or that no other account can read a key | The operator cannot see the cluster's admission policies or other accounts' rights from inside. They are install steps with exact checks (slice (a)); the ledger match makes a forged record count for nothing even without a rule (N3). |
| Rotating the key on every take-back | The ledger's `id` and `state` already retire an old record (N3). |
| Activate the moment Pushover accepts the notice | Sent is not proof Tom saw it. Two minutes in which a take-back wins (N5). |
| Hold raise notices in the keeper's quiet window | A raise Tom did not ask for is what should reach him at night (N5). |
| A watchdog that also requires the pod label to differ | A label can be patched back while the account stays raised (N7). |
| A watchdog that checks every session | A template change would suspend sessions that were never raised (N7). |
| `--keep-running` on a take-back | A pod would keep a raised account with no record (R3). |
| "Used once" compared with place and time, on one session | The performer picks the time. Words alone, folded, per performer, across every target (R2, N6). |
| A relay: an agent records words Tom gave another agent | The agent that heard him performs the raise (safety rule 4). |
| A trusted client performs a raise | It includes the v1 pod (`caller.go:27-29`) and is none of the kinds Tom named. |
| Any CallerPolicy caller may raise | Only with `spec.raise`, which Tom approves. A summoning caller never. |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise `maxSessionDepth` to 3 for everyone | It is one of three guards against runaway agents (DESIGN-001 design line 4983-4986). |
| Start a new session to continue the work | That is what happened in the case, and it loses the conversation. |
| An AccessGrant of a new type | A grant is always requested by the session for itself (`grants.go:161-170`), lasts at most 8 hours, may be approved by a standing policy, and is installed by the broker into a pod; it never chooses the pod's own identity. |
| A separate CRD for raises | A new CRD, RBAC, CRD copy and watch, for the trust the signature and the ledger already give. |
| A status field | A stale CRD prunes it (haynes-ops #3502), an older operator's status write drops it, and status is what the operator observed, not a decision. |
| The tracker as the audit record | It is written as the agent and can be down. |
| The operator restarts the session itself to apply a raise | `docs/remote-session-upgrades.md` keeps automatic stops away from a user's remote session. The watchdog suspends only a pod that has no record covering it, which is the opposite case. |
| Change the identity of the running pod | A pod's ServiceAccount is fixed for its life. |
| A standing policy approves raises | No raise without Tom's word. |
| An end date on a raise | Ruling 8. |
| A raise to a named coordinator identity | One holder at a time and three launch shapes (section 2.4). |
| Raised sessions pass workbench on to their children | Ruling 7 and section 8.2. |

## 15. Review record

All rulings below are the coordinator's (`dev-env-fable-coordinator-1109`,
2026-11-13), not Tom's.

### 15.1 First pass

Grok 4.7 on head `91678100`, `~/.shared/handoffs/op830/design-review-grok.md`:
4 HIGH, 5 MEDIUM, 2 LOW.

| Finding | What it said | Ruling | Where the design answers it |
| --- | --- | --- | --- |
| 1, HIGH | The route never checks that Tom said the words; Tom is never told | R1 | Ruling 10; section 6.4; Ruling 1 (no stored answers); known limit 1 |
| 2, HIGH | "Used once" is not real; the 24-hour check bounds only the stated clock | R2 | Section 6.5; safety rule 3; section 6.3; Ruling 8 |
| 3, HIGH | A raised pod outlives its record; "fails closed" overclaims | R3 | Ruling 11; section 7.5; section 7.6; known limit 4; no `--keep-running` |
| 4, HIGH | A raised parent's child copies the pod label and becomes workbench | R4 | Section 8.2; Ruling 7; section 7.2 |
| 5, MEDIUM | The quiet rule binds only `--restart`; a turn in the heartbeat gap can be cut | R5 | Section 7.3; known limit 2 |
| 6, MEDIUM | A raised depth-0 session can launch a named coordinator today | R6 | Section 8.4; Ruling 7; slice (b) |
| 7, MEDIUM | Cached dispatch read; no lock on take-back; unreadable chain not refused | R7 | Ruling 4; section 8.1; Ruling 1; section 6.7; known limit 3 |
| 8, MEDIUM | `spec.raise` reach and who sets it | R8 | Ruling 9; section 9 |
| 9, MEDIUM | The record rests on an admission rule nobody checks; the depth label is unfenced | R9 | Ruling 2; section 6.6; section 8.5; slice (a) install steps |
| 10, LOW | Rate limits reset on restart | R10 | Section 6.5; safety rule 15 |
| 11, LOW | Slice gating, secret shapes, DESIGN-060 edit | R11 | Section 11; section 6.3 |
| Attack notes | Missing reader sites | with R6 | Section 8.4 |

### 15.2 Second pass

Grok 4.7 on head `bbefbb0a`, `~/.shared/handoffs/op830/design-review-grok-2.md`:
of the first pass, 2, 4, 6, 10 and 11 closed and 1, 3, 5, 7, 8 and 9 partly
closed; 11 new findings (4 HIGH, 6 MEDIUM, 1 LOW).

| Finding | What it said | Ruling | Where the design answers it |
| --- | --- | --- | --- |
| A1, now MEDIUM | Peers can still state invented words; the record goes active on Pushover's accept | N5, R1 | Ruling 10 (two minutes, take-back wins); known limit 1 |
| A3, now HIGH | With the flags off or below slice (b), no watchdog runs | N7 | Ruling 11 and section 7.5 (runs whatever the flags say); section 7.6 and known limit 4 for rollback below the build |
| A5 | The heartbeat gap remains | Accepted | Known limit 2 |
| A7 | One in-flight create on another replica after take-back | Accepted | Known limit 3 |
| A8 | The words need not name the target | R8 stands | Ruling 9; section 6.4 (the notice shows both) |
| A9 | Nothing checks that the admission rule is installed | R9, ruling on the remainder | Slice (a) install steps 1 to 6, each with its check; Ruling 2 (ledger match) |
| B1, HIGH | The keeper's Sent in its ledger is unsigned; a writer can activate a raise with no push | N1 | Section 6.6 (keeper key pair); section 6.4 (signed outcome); slice (a) install step 4 |
| B2, HIGH | Sent is sticky; activation does not require `pending` | N2 | Section 6.4 (compare-and-swap); Ruling 10; section 9 |
| B3, HIGH | A saved active annotation can be patched back after take-back | N3 | Ruling 2; section 6.5 (record `id` and `state`); slice (a) tests |
| B4, HIGH | The code does not cover the verbatim words; the hash is not rechecked | N4 | Section 6.6 (what is signed); Ruling 2 |
| B5, MEDIUM | Active at Pushover's accept, before Tom can act; the quiet window delays notices | N5 | Ruling 10; section 6.4 (two minutes, any hour); section 10 (never says Tom saw it) |
| B6, MEDIUM | Punctuation and fullwidth forms bypass "used once"; the ledger is unfenced | N6 | Section 6.3 (NFKC and punctuation fold); section 6.5; slice (a) install step 3 |
| B7, MEDIUM | The watchdog is dodged by patching the label back | N7 | Section 7.5 (account only, ledger-listed sessions, any flags) |
| B8, MEDIUM | The child branch runs after the Codex case and keys on the label | N8 | Section 8.2 (first, by account, every path, failed templates read refuses); section 8.4 |
| B9, MEDIUM | A pending raise and an attached client slip past the suspend rule | N9 | Section 7.3 |
| B10, MEDIUM | The keeper holds the signing key; no startup refusal | N1, N10 | Section 6.6 (two pairs; install check, not a startup check); slice (a) install step 6 |
| B11, LOW | Words cut at 300 of 500; the title is not redacted | N11 | Section 6.3 (300-character cap); section 6.4 (title redacted) |
| C | The rollback procedure and the admission rules sit in no slice; what each slice does alone | Ruling on C | Section 11 (install steps and "when run alone" per slice) |
