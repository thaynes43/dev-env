# ADR-028: A running session is raised from outside it on Tom's word, gated by a notice to him, and recorded in a signed, ledger-matched annotation

- **Status:** Accepted 2026-11-13 by the coordinator session
  `dev-env-fable-coordinator-1109`, after two design reviews by another model
  family and after Tom approved the design as he will see it (his answer
  "Approve, build all of it (Recommended)" to a phone prompt, OpenProject
  #830). Proposed the same day, for the coordinator's review. Revised three
  times the same day: after Tom's answer on how he gives the go-ahead, and
  after two design reviews by another model family, with the coordinator's
  rulings on each finding (DESIGN-066 section 15), then once more for the
  advisory reviewer's threads on the pull request (section 15.3). Nothing it
  decides is built or installed.
- **Date:** 2026-11-13
- **Decider:** The coordinator session `dev-env-fable-coordinator-1109`,
  under OpenProject #830, from a worker's draft. The only words of Tom's
  here are the two sentences quoted under Context and his answer to the
  approval prompt quoted under Status. The coordinator's rulings
  on restart, duration and workers, and on the two reviews (R1 to R11, N1 to
  N11), are the coordinator's, not Tom's. Everything else is the
  coordinator's to accept or send back.
- **Amends:**
  1. DESIGN-001 section 3.4, the sentence that a session at depth 2 creates
     no children ("403 beyond 2"), and the matching comment on `LabelDepth`
     (`api/v1alpha1/labels.go:113-116`). One named exemption is added
     (decision 7). DESIGN-001 is accepted with ADR-001 and is not edited.
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

1. **Tom's word is the go-ahead; it is recorded, not verified.** The platform
   records the performing agent's statement of his words, as the grant
   attestation route records "agent attestation, not verified human
   identity" (`internal/apiserver/grant_attestation.go:15-22`; D-88 in
   DESIGN-001). The operator stores no phone-prompt answer it could check
   them against.
2. **A notice gates the raise.** A raise is written `pending`. The keeper
   sends Tom a notice, at any hour, naming the session and its title, the
   performer, what was raised, the words, when it takes effect and the
   take-back command, through its Pushover sender with a write-ahead ledger.
   It signs each outcome with its own key. Two minutes after a validly
   signed Sent, the operator swaps `pending` for `active`; a take-back before
   the swap wins, and a `taken-back` or `unannounced` record ignores a later
   Sent. Sent is not proof Tom saw it. After twelve failed attempts the raise
   ends `unannounced` and never takes effect. A take-back is announced too.
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
5. **The record counts only when three things agree.** The record is the
   annotation `dev-env.haynesops.com/raise`, written only by the operator. It
   counts only when it names the session's UID; its Ed25519 signature, made
   with a private key only the operator reads, verifies over every field but
   the signature, Tom's verbatim words included, and the words hash matches
   the words; and its `id` and `state` match the operator's own ledger. The
   record is encoded without HTML escaping and bounded at 4 KiB encoded,
   checked before the word is spent. A
   replayed, edited or forged annotation counts for nothing, even where an
   admission rule is missing. It is not a spec write, so
   `metadata.generation` does not move (ADR-027).
6. **Two key pairs, two ledgers, no shared secret.** The operator signs
   records; the keeper verifies them with the operator's public key. The
   keeper signs notice outcomes; the operator verifies them with the keeper's
   public key. Each process's leader makes its own Ed25519 pair
   (`crypto/ed25519`, as the repo's home-retention receipts already use) into
   an empty Secret with a `resourceVersion` precondition, and every replica
   signs only with the key it reads back; an empty, unreadable or mismatched
   key refuses. A key is replaced by taking back every raise and moving the
   owner to a newly named, empty Secret through haynes-ops. The
   operator ledger holds each raised session's record `id` and `state`, every
   used word, and the rate limits; the keeper ledger holds each notice and
   its signed outcome. Words are used once per performer across every target,
   compared after NFKC and a punctuation fold, at most 300 characters. A
   raise is refused when the operator ledger cannot be read. Admission rules
   in haynes-ops, installed before any flag with an exact check each, let
   only each owner write its ledger, public key and the record, and nobody
   change the depth label after create. Who else can read a private key is
   an install check, not a startup check.
7. **The right to start sessions.** A session whose depth label is exactly
   `2` passes the depth check in `admitCreate` on the direct create when live
   reads of it and of the operator ledger, inside the create lock, show a
   record that counts, is active and grants it at depth 2. A failed read
   refuses. The take-back takes the same lock. Children carry the true depth
   label `3`, name it as parent, count against the same cap of four and start
   nothing. `maxSessionDepth` stays 2. Scheduled work keeps today's rule.
8. **The profile.** The operator builds a session's next pod from the
   record's profile when the record counts, is active and the profile is on
   `--session-raise-profiles`. A running pod is never changed, and the
   operator never restarts a session to apply a raise. The coordinator's
   ruling: the acting agent applies it with the explicit restart step at the
   session's first quiet moment, never while busy or waiting; if it does not,
   the profile applies at the next resume. While a profile raise is pending
   or active and not on the pod, the ordinary suspend route refuses a busy,
   waiting or attached session; the take-back route does not. A profile that
   runs as a named coordinator identity is never given.
9. **A raise never spreads through the platform's routes, and power never
   outlives its record.** A session raised to workbench may not perform a
   raise or launch a named coordinator identity: the launcher class becomes
   workbench by creation, which today's code does not check. That binds the
   platform's own routes only. A session raised to workbench holds every
   Kubernetes right of the workbench account, exec into other pods included,
   and can act through another session's pod; that is what raising to that
   profile means, it is Tom's choice each time, and the notice says the
   session "will run as the superuser account" (the coordinator's ruling). A raised parent is a session whose pod was not
   built for its `spec.profile`, by account or by profile label, decided
   before any other profile case; its children get
   the profile they name or the default, never the pod's label, and are
   refused on any profile that runs as the workbench account or a named
   coordinator identity, on every path; a failed templates read refuses. In
   every operator build that has it, whatever the flags say, the watchdog
   suspends a session the ledger lists as ever raised whose pod was not built
   for its `spec.profile`, account and profile label both, unless an active
   record covers the profile the pod was built for; a profile label set by
   anyone but the operator is a mismatch. Sessions never raised are never
   touched.
10. **How long, taking it back, and rollback.** The coordinator's ruling: a
    raise lasts until it is taken back or the session is removed. Taking it
    back needs no word from Tom and is exempt from every rate limit: ledger
    first, then the record; it suspends at once a pod on the raised account,
    announces the take-back, and releases the grants the raised pod
    requested. When a session takes back its own profile raise, the suspend
    ends its own turn and the answer may not reach it; the take-back stands.
    The key is not rotated on take-back.
    Both flags are off by default. Rollback below the build that has the
    watchdog is a written take-back-first procedure.

## Alternatives considered

| Alternative | Why it loses |
| --- | --- |
| A `raise` card on the Asks board, decided by Tom's tap | Tom chose his word to an agent. The grant card path it would reuse is not installed (the coordinator's read of 2026-11-13), and the portal's username retention is not in source. A tap can be added later as a second factor without changing the record. |
| Accept only a phone-prompt answer the operator stored | The operator stores none. The notice is the control. |
| A shared HMAC key | Either holder could sign for the other. |
| A startup check for the admission rules or for other readers of a key | The operator cannot see them from inside. Install steps with exact checks; the ledger match covers a missing rule. |
| Rotate the key on take-back | The ledger's `id` and `state` already retire an old record. |
| Activate at Pushover's accept | Sent is not proof Tom saw it. Two minutes in which a take-back wins. |
| Hold raise notices in the quiet window | A raise Tom did not ask for should reach him at night. |
| A watchdog keyed on the pod label, or on every session | A label can be patched back; a template change would suspend sessions never raised. |
| `--keep-running` on a take-back | A pod would keep a raised account with no record. |
| Rewrite `spec.parent`, `spec.profile` or the depth label | Provenance would lie. D-39 forbids the spec writes, and a spec write moves `metadata.generation` in four places (ADR-027). |
| Raise the global limit to three levels | It is a guard against runaway agents for every session. |
| An AccessGrant type for raises | A grant is requested by the session for itself, lasts at most 8 hours, may be approved by a standing policy, and is installed by the broker into a pod; it never chooses the pod's own identity. |
| A separate CRD or a status field | A new CRD for the trust the signature and the ledger give; a status field is pruned by a stale CRD and dropped by an older operator. |
| A relay, or a trusted client performing | The record would name a party that did not hear Tom; a client includes the v1 pod. |

## Consequences

| Id | Consequence |
| --- | --- |
| C-68 | Good: a session that started too low can be raised on Tom's word and keep its conversation, volume and phone entry (the entry across a profile change is to be measured, DESIGN-066 section 7.4). |
| C-69 | Good: every raise is announced with the words the agent says Tom said, and waits two minutes after the notice is accepted, during which a take-back wins. |
| C-70 | Good: a replayed, edited or forged record counts for nothing, because the signature, the words hash and the operator ledger must all agree. |
| C-71 | Good: in any build that has the watchdog, a raised pod never outlives its record, flags on or off. |
| C-72 | Bad: Tom's word is an agent's statement, not a verified identity, and Sent is not proof he read the notice (DESIGN-066 known limit 1). |
| C-73 | Bad: a raise whose notice cannot be sent never takes effect, and its words stay spent; the agent must ask Tom again. |
| C-74 | Bad: a profile raise needs a restart, and a turn starting inside the heartbeat gap can be cut (known limit 2). One create in flight on another replica can still make one depth-3 child after a take-back (known limit 3). |
| C-75 | Bad: sessions at depth 3 exist. Delegate decisions, attested maintenance, the legacy delegate and the instruction publisher refuse them. |
| C-76 | Neutral: new state outside the AgentSession: two private-key Secrets, two public-key ConfigMaps and two ledger ConfigMaps, with admission rules, all set up in haynes-ops before any flag. |
| C-77 | Neutral: rollback below the build that has the watchdog is a take-back-first procedure (known limit 4). |
| C-78 | Neutral: the CallerPolicy CRD gains `spec.raise`, so haynes-ops gets that copy before the operator pin that reads it. An older `agent-run` cannot read a raised session with `show --json --once` or `usage export`. |
| C-79 | Bad: a session raised to workbench holds the whole workbench account in Kubernetes; "never spreads" binds only the platform's routes (DESIGN-066 known limit 5). |

## What I could not determine

- Whether a resume onto another ServiceAccount and profile keeps the Remote
  Control entry. Unknown until the live proof.
- Whether Meatball's CallerPolicy is installed today.
- Whether the keeper's account may list AgentSessions today.
- Which accounts can read Secrets in `dev-env-system` today; slice (a) makes
  it an install check.
- Whether any haynes-ops policy reads the depth label or a volume's profile
  label.
- Whether the broker matches a standing policy again when an active grant's
  requester changes profile.
