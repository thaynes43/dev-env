# ADR-028: A running session is raised from outside it on Tom's word, announced to him first, and recorded in a signed, UID-bound annotation

- **Status:** Proposed 2026-11-13, for the coordinator's review. Revised
  twice the same day: after Tom's answer on how he gives the go-ahead, and
  after a design review by another model family with the coordinator's
  rulings on each finding (DESIGN-066 section 14). Nothing it decides is
  built or installed.
- **Date:** 2026-11-13
- **Decider:** The coordinator session `dev-env-fable-coordinator-1109`,
  under OpenProject #830, from a worker's draft. The only words of Tom's
  here are the two sentences quoted under Context. The coordinator's rulings
  on restart, duration and workers, and on the review (R1 to R11), are the
  coordinator's, not Tom's. Everything else is the coordinator's to accept or
  send back.
- **Amends:**
  1. DESIGN-001 section 3.4, the sentence that a session at depth 2 creates
     no children ("403 beyond 2"), and the matching comment on `LabelDepth`
     (`api/v1alpha1/labels.go:113-116`). One named exemption is added
     (decision 6). DESIGN-001 is accepted with ADR-001 and is not edited.
  2. DESIGN-060 section 7.1, that the front-door CallerPolicy has no
     configurable rights, and section 6, that Meatball never gets the
     workbench: the policy gains one optional right, to raise another
     session on Tom's word (decision 4). Meatball itself still never runs as
     the workbench. DESIGN-060 is amended in place in slice (c).
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

1. **Tom's word is the go-ahead; it is recorded, not verified, and Tom is
   told.** The platform records the performing agent's statement of his
   words, as the grant attestation route records "agent attestation, not
   verified human identity" (`internal/apiserver/grant_attestation.go:15-22`;
   D-88 in DESIGN-001). The operator stores no phone-prompt answer it could
   check them against. The control is the notice: a raise is written
   `pending` and becomes active only once a notice to Tom, naming the
   session, the performer, what was raised, the words and the take-back
   command, has been sent through the keeper's Pushover path with the Ask
   push's ledger and delivery window. A notice that cannot be sent leaves the
   raise pending; after 12 attempts or 24 hours it ends `unannounced` and
   never takes effect. A take-back is announced too, and never waits for it.
2. **A word is used once, for real.** The operator keeps the hash of each
   accepted word, normalised, per performer, in one ConfigMap only it writes,
   kept 90 days and bounded, and refuses the same words for any target. The
   same ledger holds the rate limits, so both survive a restart. The 24-hour
   bound on the stated time checks the operator's clock only and proves
   nothing about when Tom spoke.
3. **Who performs a raise.** The agent Tom gave his word to: a session
   created on the workbench profile, that is not the target and does not sit
   under it (an unreadable parent chain is a refusal); an always-on agent
   whose CallerPolicy grants the raise action; or Tom's own ServiceAccount.
   Never the target, a session under it, a session raised to workbench, an
   ordinary session, a named coordinator session, a coordinator host, a
   summoning caller, the portal or a trusted client. A relay is refused.
4. **The raise action in a CallerPolicy.** The front-door shape gains one
   optional field, `spec.raise`, with the profiles the caller may give (a
   subset of the operator's flag) and whether it may grant the right to
   start sessions. Absent, the caller has no raise route. A summon policy can
   never carry it. Setting it is a GitOps change in haynes-ops that Tom
   approves. With it, the caller may raise any eligible session and, by
   taking back a profile raise, suspend at once a session it did not start.
   Another always-on agent is admitted by its own front-door policy, which is
   a separate story.
5. **The record is the annotation `dev-env.haynesops.com/raise`, signed.**
   One JSON object written only by the operator: what was raised, the depth,
   Tom's words and their hash, where and when he said them, the performing
   caller from its token, the state and the times. It names the session's
   UID. It carries an HMAC-SHA256 code computed by the operator with a key in
   a Secret only the operator and the keeper read; a record with a missing or
   wrong code is ignored and logged. It is not a spec write, so
   `metadata.generation` does not move (ADR-027). The audit record is the
   annotation, one operator log line per change, and the keeper's record of
   the notice. A haynes-ops admission rule, installed before any flag,
   refuses a change to the annotation from any user but the operator's
   ServiceAccount, and any change to the depth label after create.
6. **The right to start sessions.** A session whose depth label is exactly
   `2` passes the depth check in `admitCreate` on the direct create when a
   live read of it, inside the create lock, shows a valid active record that
   grants it at depth 2. A failed read refuses. The take-back takes the same
   lock. Children carry the true depth label `3`, name it as parent, count
   against the same cap of four and start nothing. `maxSessionDepth` stays 2.
   Scheduled work keeps today's rule.
7. **The profile.** The operator builds a session's next pod from the
   record's profile when the record is valid, active, names the UID and the
   profile is on `--session-raise-profiles`. A running pod is never changed,
   and the operator never restarts a session to apply a raise. The
   coordinator's ruling: the acting agent applies a raised profile with the
   explicit restart step at the session's first quiet moment, never while it
   is busy or waiting on a question; if it does not, the profile applies at
   the next resume. While a raised profile waits for its pod, the ordinary
   suspend route refuses a busy or waiting session; the take-back route does
   not. A profile that runs as a named coordinator identity is never given.
8. **A raise never spreads, and power never outlives its record.** A session
   raised to workbench may not perform a raise or launch a named coordinator
   identity: the launcher class becomes workbench by creation, which today's
   code does not check. A raised session's children get the profile they
   name or the default, never the pod's label, and are refused on any
   profile that runs as the workbench account or a named coordinator
   identity (the coordinator's rulings). While either flag is set, the
   reconciler suspends a session whose pod runs on another profile and
   account than `spec.profile` without a valid active record for it.
9. **How long, and taking it back.** The coordinator's ruling: a raise lasts
   until it is taken back or the session is removed. Taking it back needs no
   word from Tom. It ends the right to start sessions at the next create,
   suspends at once a pod that runs the raised profile, with no option to
   keep it running, announces the take-back, and releases the grants the
   raised pod requested. Children already started keep running and start
   nothing.
10. **Off by default, and rollback is a procedure.** `--session-raise-profiles`
    is empty and `--enable-session-raise-dispatch` is off. With both off, or
    on an operator without this build, the record is ignored; taking back
    still works. What fails closed by itself is the right to start sessions
    and the next pod. Before a rollback or turning the flags off, the
    coordinator takes back every raise, because a pod already running raised
    keeps its account until it is suspended.

## Alternatives considered

| Alternative | Why it loses |
| --- | --- |
| A `raise` card on the Asks board, decided by Tom's tap | Tom chose his word to an agent. The grant card path it would reuse is not installed (the coordinator's read of 2026-11-13: no grant-Ask flag, and an Ask CRD with no enum on its kind field), and the portal's username retention is not in source. A tap can be added later as a stricter second factor without changing the record. |
| Accept only a phone-prompt answer the operator stored | The operator stores none. The notice to Tom is the control. |
| Compare "used once" on one session with place and time | The performer picks the time. Words alone, per performer, across every target, in a ledger that survives a restart. |
| Rely on the admission rule alone for the record | A rule outside the repo can be missing. The operator's code makes the record independent of it; the rule stays as a second layer. |
| `--keep-running` on a take-back | A pod would keep a raised account with no record. |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise the global limit to three levels | It is a guard against runaway agents for every session. One session's need does not justify it. |
| An AccessGrant type for raises | A grant is requested by the session for itself, lasts at most 8 hours, may be approved by a standing policy, and is installed by the broker into a pod; it never chooses the pod's own identity. |
| A separate CRD | A new CRD, RBAC, copy and watch, for the trust the code and the fence give. The controller already picks a pod's account from API-written metadata (the named coordinator admission label). |
| A status field | A stale CRD prunes it, an older operator's status write drops it, and status records what the operator saw, not a decision. |
| The tracker as the audit record | It is written as the agent and can be down. |
| A relay, or a trusted client performing | The record would name a party that did not hear Tom, and a client includes the v1 pod, none of the kinds he named. |
| The operator restarts a session to apply a raise | Automatic stops stay away from a user's remote session (`docs/remote-session-upgrades.md`), and a coordinator reads busy while a background command runs. |

## Consequences

| Id | Consequence |
| --- | --- |
| C-68 | Good: a session that started too low can be raised on Tom's word and keep its conversation, volume and phone entry (the entry across a profile change is to be measured, DESIGN-066 section 7.4). |
| C-69 | Good: Tom sees every raise before it takes effect, with the words the agent says he said, and every take-back. A false statement becomes visible to him. |
| C-70 | Good: no spec field, and no AgentSession or Ask CRD change. The right to start sessions and the next pod fail closed on a rollback or with the flags off. |
| C-71 | Bad: Tom's word is an agent's statement, not a verified identity. The record, the log line and the notice say which agent made it, but cannot prove he said it. |
| C-72 | Bad: a raise performed in quiet hours waits for the delivery window, and one whose notice cannot be sent never takes effect; the agent must ask Tom again. |
| C-73 | Bad: a profile raise needs a restart. A turn or question that starts inside the heartbeat gap before it is cut, and background commands end. The conversation and the volume stay. |
| C-74 | Bad: a rollback or turning the flags off without taking every raise back first leaves a pod on a raised account until it is suspended. The procedure in DESIGN-066 section 7.6 is the guard. |
| C-75 | Bad: sessions at depth 3 exist. Delegate decisions, attested maintenance, the legacy delegate and the instruction publisher refuse them, so their grants go to a standing or attested policy, or are denied at 30 minutes. |
| C-76 | Neutral: new state outside the AgentSession: a key Secret read by the operator and the keeper, the operator's ledger ConfigMap and the keeper's raise ledger ConfigMap, all set up in haynes-ops before any flag. |
| C-77 | Neutral: the CallerPolicy CRD gains `spec.raise`, so haynes-ops gets that copy before the operator pin that reads it. A stale CRD prunes the field, and the caller has no raise right. |
| C-78 | Neutral: an older `agent-run` cannot read a raised session with `show --json --once` or `usage export`. Unraised sessions are unchanged. |

## What I could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry. Unknown until the live proof.
- Whether Meatball's CallerPolicy is installed today.
- Whether the keeper's delivery window is set today, and whether its account
  may list AgentSessions today.
- Which accounts can read Secrets in `dev-env-system` today, and how
  haynes-ops creates a generated Secret there.
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.
