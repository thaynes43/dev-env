# ADR-028: Tom raises a running session from outside it, and the raise is a UID-bound annotation

- **Status:** Proposed 2026-11-13, for the coordinator's review. Nothing it
  decides is built or installed.
- **Date:** 2026-11-13
- **Decider:** The coordinator session `dev-env-fable-coordinator-1109`,
  under OpenProject #830, from a worker's draft. The only words of Tom's
  here are the sentence quoted under Context. Everything else is the
  coordinator's to accept or send back, not Tom's.
- **Amends:**
  1. DESIGN-001 section 3.4, the sentence that a session at depth 2 creates
     no children ("403 beyond 2"), and the matching comment on `LabelDepth`
     (`api/v1alpha1/labels.go:113-116`). One named exemption is added
     (decision 4). DESIGN-001 is accepted with ADR-001 and is not edited.
  2. [ADR-010](010-agent-asks-and-approvals-board.md), as
     [ADR-021](021-grants-through-the-asks-board.md) already amended it for
     `grant`: an Ask may also be the record of a raise that Tom decides, and
     its stored answer drives one machine action. ADR-010 and ADR-021 are
     accepted and are not edited.
- **Supersedes:** nothing. D-39 is not amended: no spec field changes.
- **Design:** [DESIGN-066](../designs/066-raise-a-running-session.md).
- **Tracking:** OpenProject [#830](https://work.haynesnetwork.com/work_packages/830).

## Context

Tom, 2026-11-13, typed answer to a phone prompt in session
`dev-env-fable-coordinator-1109`, recorded on story 828 comment 7927. The
clause this ADR serves: "build a way to elevate agents permissions OUTSIDE
of their session (this is all dev-env v2 restrictions so I have no idea why
its so hard)". The full answer is in DESIGN-066 section 1.1.

On 2026-11-13 the session `haynesnetwork-1113-150420` sat at depth 2 on the
ordinary profile. Its `POST /v1/sessions` was refused: two levels is the
limit (`internal/apiserver/sessions.go:699-702`). Nothing could raise it.
`spec.parent` and `spec.profile` are immutable (D-39), the depth label is
written once at create (`internal/apiserver/validate.go:384-388`), and a
pod's ServiceAccount is fixed for its life (`internal/controller/pod.go:378-394`,
`:546-547`). The fix was a second session, which lost the conversation.

## Decision

1. **Tom decides, and nothing else does.** His answer to a `raise` card on
   the Asks board, recorded as `authentik/<username>` as ADR-021 records a
   grant answer, or a call by his own ServiceAccount to the raise route. No
   GrantPolicy, attestation, delegate, coordinator, client, Meatball or
   session decides. One answer is used once.
2. **Who may ask.** Tom; a trusted client; a session created on the
   workbench profile that is not the target and does not sit under it. Never
   the target, a session under it, a session raised to workbench, or any
   other caller class. Asking only puts a card on Tom's board.
3. **The record is the annotation `dev-env.haynesops.com/raise`.** One JSON
   object written only by the operator. It names the session's UID, and every
   reader ignores a record whose UID is not the session's. It is not a spec
   write, so `metadata.generation` does not move (ADR-027). An admission rule
   in haynes-ops refuses a change to it from any user but the operator's
   ServiceAccount before any flag is set.
4. **The right to start sessions.** A session whose depth label is exactly
   `2`, with an active record that grants it, passes the depth check in
   `admitCreate` on the direct create. Its children carry the true depth
   label `3`, name it as parent, count against the same cap of four and
   start nothing. `maxSessionDepth` stays 2. Scheduled work keeps today's
   rule.
5. **The profile.** The operator builds a session's next pod from the
   record's profile when the record is active, names the UID and the profile
   is on `--session-raise-profiles`. A running pod is never changed. Neither
   the route nor the operator restarts a session; `agent-run raise --restart`
   is an explicit suspend and resume, refused unless the session reads
   quiet. A profile that runs as a named coordinator identity is never given.
6. **A raise never spreads.** A session raised to workbench may not ask for a
   raise, start a child on workbench or launch a named coordinator identity.
   Only `spec.profile` makes a session workbench by creation.
7. **Taking it back** needs no approval. It marks the record taken back,
   ends the right to start sessions at the next create, suspends the session
   at once for a profile raise unless told to keep it running, and releases
   the grants the raised pod requested. Children already started keep
   running and start nothing.
8. **A fourth Ask kind, `raise`.** Created only by the operator, with a typed
   scope bound to the target's UID and a digest of what is raised, no expiry,
   and caps of one open card per session, three per asker and twelve in the
   namespace. Sessions cannot create, withdraw or answer it. A leader-only
   applier writes the record from an approved answer only when the live
   session still matches.
9. **Off by default.** `--session-raise-profiles` is empty and
   `--enable-session-raise-dispatch` is off. With both off, or on an operator
   without this build, the record is ignored and today's rules hold. A pod
   already running as a raised account keeps it until it is suspended.

## Alternatives considered

| Alternative | Why it loses |
| --- | --- |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise the global limit to three levels | It is a guard against runaway agents for every session. One session's need does not justify it. |
| An AccessGrant type for raises | A grant is requested by the session for itself, lasts at most 8 hours, may be approved by a standing policy, and is installed by the broker into a pod; it never chooses the pod's own identity. |
| A separate CRD | A new CRD, RBAC, copy and watch, for the trust the fenced annotation gives. The controller already picks a pod's account from API-written metadata (the named coordinator admission label). |
| A status field | A stale CRD prunes it, an older operator's status write drops it, and status records what the operator saw, not a decision. |
| Reuse the `approval` Ask kind | An approval grants no machine action and expires within 24 hours (ADR-010). |
| The operator restarts the session when idle | Automatic stops stay away from a user's remote session (`docs/remote-session-upgrades.md`), and a coordinator reads busy while a background command runs. |
| A workbench coordinator decides | The raise can make a session a superuser and overrides a global guard. The saga sends risky access to Tom (decisions 97 and 98). |

## Consequences

| Id | Consequence |
| --- | --- |
| C-68 | Good: a session that started too low can be raised and keep its conversation, volume and phone entry (the entry across a profile change is to be measured, DESIGN-066 section 7.4). |
| C-69 | Good: no spec field, no AgentSession CRD change and no generation change. A rollback or a flag turned off fails closed. |
| C-70 | Bad: a profile raise needs a restart. A turn or question that starts in the last minute before it is cut, and background commands end. The conversation and the volume stay. |
| C-71 | Bad: until it is suspended, a pod keeps a raised account after a take-back with `--keep-running`, after a rollback, or with the flags off. |
| C-72 | Bad: sessions at depth 3 exist. Delegate decisions, attested maintenance, the legacy delegate and the instruction publisher refuse them, so their grants go to a standing policy, to Tom, or are denied at 30 minutes. |
| C-73 | Neutral: the raise card depends on the portal keeping `preferred_username` (ADR-021 build PR 6), which is not in source on main. Until then only Tom's own ServiceAccount can decide. |
| C-74 | Neutral: the Ask CRD gains `raise`, so haynes-ops gets that copy before the operator pin, and the operator is not rolled back below that build while a raise card is open. |
| C-75 | Neutral: an older `agent-run` cannot read a raised session with `show --json --once` or `usage export`. Unraised sessions are unchanged. |

## What I could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry. Measured only without a profile change.
- Whether the workbench ServiceAccount can patch AgentSessions or write Ask
  status today. The RBAC is in haynes-ops.
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- Whether `--enable-asks`, `--enable-grant-asks` and
  `--enable-ask-notifications` are set today.
- How an older operator's board treats an Ask of an unknown kind.
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.
