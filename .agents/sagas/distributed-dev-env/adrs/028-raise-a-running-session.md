# ADR-028: A running session is raised from outside it on Tom's word, recorded with his words in a UID-bound annotation

- **Status:** Proposed 2026-11-13, for the coordinator's review. Revised the
  same day after Tom's answer on how he gives the go-ahead. Nothing it
  decides is built or installed.
- **Date:** 2026-11-13
- **Decider:** The coordinator session `dev-env-fable-coordinator-1109`,
  under OpenProject #830, from a worker's draft. The only words of Tom's
  here are the two sentences quoted under Context. The coordinator's rulings
  on restart, duration and workers are the coordinator's, not Tom's.
  Everything else is the coordinator's to accept or send back.
- **Amends:**
  1. DESIGN-001 section 3.4, the sentence that a session at depth 2 creates
     no children ("403 beyond 2"), and the matching comment on `LabelDepth`
     (`api/v1alpha1/labels.go:113-116`). One named exemption is added
     (decision 5). DESIGN-001 is accepted with ADR-001 and is not edited.
  2. DESIGN-060 section 7.1, that the front-door CallerPolicy has no
     configurable rights, and section 6, that Meatball never gets the
     workbench: the policy gains one optional right, to raise another
     session on Tom's word (decision 3). Meatball itself still never runs as
     the workbench. DESIGN-060 is amended in place when that slice lands.
- **Supersedes:** nothing. D-39 is not amended: no spec field changes.
- **Design:** [DESIGN-066](../designs/066-raise-a-running-session.md).
- **Tracking:** OpenProject [#830](https://work.haynesnetwork.com/work_packages/830).

## Context

Tom, 2026-11-13, typed answer to a phone prompt in session
`dev-env-fable-coordinator-1109`, recorded on story 828 comment 7927. The
clause this ADR serves: "build a way to elevate agents permissions OUTSIDE
of their session (this is all dev-env v2 restrictions so I have no idea why
its so hard)". The full answer is in DESIGN-066 section 1.1.

Asked by phone prompt the same day how he gives the go-ahead each time a
session is raised, he typed, recorded on story 830 comment 7980: "My word to
a superuser or meatball or a meatball equivalent always on agent." The
question and the options offered are in DESIGN-066 section 1.2.

On 2026-11-13 the session `haynesnetwork-1113-150420` sat at depth 2 on the
ordinary profile. Its `POST /v1/sessions` was refused: two levels is the
limit (`internal/apiserver/sessions.go:699-702`). Nothing could raise it.
`spec.parent` and `spec.profile` are immutable (D-39), the depth label is
written once at create (`internal/apiserver/validate.go:384-388`), and a
pod's ServiceAccount is fixed for its life (`internal/controller/pod.go:378-394`,
`:546-547`). The fix was a second session, which lost the conversation.

## Decision

1. **Tom's word is the go-ahead, and it is recorded, not verified.** The
   platform records the performing agent's statement of his words, as the
   grant attestation route records "agent attestation, not verified human
   identity" (`internal/apiserver/grant_attestation.go:15-22`; D-88 in
   DESIGN-001). A raise without his words, where he said them and when is
   refused. A word is used once.
2. **Who performs a raise.** The agent Tom gave his word to: a session
   created on the workbench profile, that is not the target and does not sit
   under it; an always-on agent whose CallerPolicy grants the raise action;
   or Tom's own ServiceAccount. Never the target, a session under it, a
   session raised to workbench, an ordinary session, a named coordinator
   session, a coordinator host, a summoning caller, the portal or a trusted
   client. A relay is refused: the agent that heard him performs it.
3. **The raise action in a CallerPolicy.** The front-door shape gains one
   optional field, `spec.raise`, with the profiles the caller may give (a
   subset of the operator's flag) and whether it may grant the right to
   start sessions. Absent, the caller has no raise route. A summon policy
   can never carry it. Meatball's policy is first; another always-on agent
   is admitted by its own front-door policy, which is a separate story.
4. **The record is the annotation `dev-env.haynesops.com/raise`.** One JSON
   object written only by the operator, holding what was raised, Tom's
   words, where and when he said them, and the performing caller from its
   token. It names the session's UID, and every reader ignores a record
   whose UID is not the session's. It is not a spec write, so
   `metadata.generation` does not move (ADR-027). The audit record is that
   annotation plus one operator log line per raise and take-back; a tracker
   comment by the agent is welcome and is not the record. An admission rule
   in haynes-ops refuses a change to the annotation from any user but the
   operator's ServiceAccount before any flag is set.
5. **The right to start sessions.** A session whose depth label is exactly
   `2`, with an active record that grants it, passes the depth check in
   `admitCreate` on the direct create. Its children carry the true depth
   label `3`, name it as parent, count against the same cap of four and
   start nothing. `maxSessionDepth` stays 2. Scheduled work keeps today's
   rule.
6. **The profile.** The operator builds a session's next pod from the
   record's profile when the record is active, names the UID and the profile
   is on `--session-raise-profiles`. A running pod is never changed. The
   operator never restarts a session for a raise. The coordinator's ruling:
   the acting agent applies a raised profile with the explicit restart step
   at the session's first quiet moment, never while it is busy or waiting on
   a question; if it does not, the profile applies at the next resume. A
   profile that runs as a named coordinator identity is never given.
7. **A raise never spreads.** A session raised to workbench may not perform
   a raise, start a child on workbench or launch a named coordinator
   identity. The coordinator's ruling: a raised session's workers are
   ordinary, never superusers. Only `spec.profile` makes a session workbench
   by creation.
8. **How long, and taking it back.** The coordinator's ruling: a raise lasts
   until it is taken back or the session is removed. Taking it back needs no
   word from Tom. It marks the record taken back, ends the right to start
   sessions at the next create, suspends the session at once for a profile
   raise unless told to keep it running, and releases the grants the raised
   pod requested. Children already started keep running and start nothing.
9. **Off by default.** `--session-raise-profiles` is empty and
   `--enable-session-raise-dispatch` is off. With both off, or on an operator
   without this build, the record is ignored and today's rules hold; taking
   back still works. A pod already running as a raised account keeps it
   until it is suspended. A rate limit allows one record change per session
   per minute and three raises per performer per hour.

## Alternatives considered

| Alternative | Why it loses |
| --- | --- |
| A `raise` card on the Asks board, decided by Tom's tap | Tom chose his word to an agent. The grant card path it would reuse is not installed (the coordinator's read of 2026-11-13: no grant-Ask flag, and an Ask CRD with no enum on its kind field), and the portal's username retention is not in source. A tap can be added later as a stricter second factor without changing the record. |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise the global limit to three levels | It is a guard against runaway agents for every session. One session's need does not justify it. |
| An AccessGrant type for raises | A grant is requested by the session for itself, lasts at most 8 hours, may be approved by a standing policy, and is installed by the broker into a pod; it never chooses the pod's own identity. |
| A separate CRD | A new CRD, RBAC, copy and watch, for the trust the fenced annotation gives. The controller already picks a pod's account from API-written metadata (the named coordinator admission label). |
| A status field | A stale CRD prunes it, an older operator's status write drops it, and status records what the operator saw, not a decision. |
| The tracker as the audit record | It is written as the agent and can be down. The operator writes the annotation and the log line from the authenticated caller. |
| A relay, or a trusted client performing | The record would name a party that did not hear Tom, and a client includes the v1 pod, none of the kinds he named. |
| The operator restarts the session when idle | Automatic stops stay away from a user's remote session (`docs/remote-session-upgrades.md`), and a coordinator reads busy while a background command runs. |

## Consequences

| Id | Consequence |
| --- | --- |
| C-68 | Good: a session that started too low can be raised on Tom's word and keep its conversation, volume and phone entry (the entry across a profile change is to be measured, DESIGN-066 section 7.4). |
| C-69 | Good: no spec field, and no AgentSession or Ask CRD change. A rollback or a flag turned off fails closed. |
| C-70 | Bad: Tom's word is an agent's statement, not a verified identity. The record and the log line say which agent made it, but cannot prove he said it. |
| C-71 | Bad: a profile raise needs a restart. A turn or question that starts in the last minute before it is cut, and background commands end. The conversation and the volume stay. |
| C-72 | Bad: until it is suspended, a pod keeps a raised account after a take-back with `--keep-running`, after a rollback, or with the flags off. |
| C-73 | Bad: sessions at depth 3 exist. Delegate decisions, attested maintenance, the legacy delegate and the instruction publisher refuse them, so their grants go to a standing or attested policy, or are denied at 30 minutes. |
| C-74 | Neutral: the CallerPolicy CRD gains `spec.raise`, so haynes-ops gets that copy before the operator pin that reads it. A stale CRD prunes the field, and the caller has no raise right. |
| C-75 | Neutral: an older `agent-run` cannot read a raised session with `show --json --once` or `usage export`. Unraised sessions are unchanged. |

## What I could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry. Unknown until the live proof.
- Whether Meatball's CallerPolicy is installed today.
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.
- Which token shapes the secret check on Tom's words should name beyond the
  one the repo already scrubs.
