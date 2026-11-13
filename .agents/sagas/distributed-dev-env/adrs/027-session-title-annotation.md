# ADR-027: A changed title is an annotation, and spec.title stays immutable

- **Status:** Proposed 2026-11-13 by the coordinator session
  `dev-env-fable-coordinator-1109`, answering the generation question on
  dev-env PR 1127. It installs nothing.
- **Date:** 2026-11-13
- **Decider:** The coordinator session `dev-env-fable-coordinator-1109`,
  under OpenProject #787. This is the coordinator's ruling, not Tom's.
- **Supersedes:** [ADR-026](026-agents-rename-a-running-session.md) decision
  1 only: "`spec.title` may change after create, through one route."
  ADR-026 stays Accepted and is not edited. Its guarded typed rename
  (decisions 3 to 10) stands, with the two refinements below.
- **Amends:** [DESIGN-065](../designs/065-session-display-titles.md) rule
  8.1, and every sentence there that says `spec.title` changes after create.
  Each change is marked. The first text stays.
- **Design:** DESIGN-065 section 9.4.
- **Tracking:** OpenProject [#787](https://work.haynesnetwork.com/work_packages/787).

## Context

ADR-026 put a changed title in `spec.title`. A title write is then a spec
write, and Kubernetes bumps `metadata.generation`. Four places compare that
generation and would treat the title as a new spec:

1. The drain restart proof (`restartProofFor` in `internal/controller/drain.go`)
   matches only the generation it was recorded at.
2. An in-flight decision write (`writeDecisionAuthority` in
   `internal/apiserver/decisions.go`) denies when the live generation differs
   from the caller's snapshot.
3. A published canary (`publishedGeneration` in
   `internal/apiserver/canary_result.go`) is readable only at the generation
   it was published from.
4. A rescue record (`rescueRanIn` and `sharedRescued` in
   `internal/controller`) matches only the generation it ran at.

A title is display text. It must not move those.

## Decision

1. **`spec.title` does not change after create.** It stays the optional role
   text slice 1 stores at create. The two CEL relaxations that let it be
   added, changed or removed are not part of this build. D-39 is not amended.
2. **The changed title is the annotation `dev-env.haynesops.com/title`.**
   `PUT /v1/sessions/{name}/title` writes that annotation and keeps
   `title-set-by` and `title-set-at`. The access rule, the `expect` check,
   the `resourceVersion` precondition and retry, the one-change-per-minute
   limit, and the refusals for a summoned target and a session being reaped
   stay. The value is checked with `sessiontitle.Validate` before the write.
   An empty value means no title. A clear writes that empty value.
3. **The effective title** is the annotation when it is present and
   `Validate` accepts it, including empty. Otherwise it is `spec.title`.
   `expect` is compared with the effective title. Every reader validates the
   annotation. A value that fails is ignored, the session shows `spec.title`,
   and `agent-run show` says the annotation was ignored. Nothing types a
   value that failed validation. The heartbeat and the API view use the
   effective title. The view also carries the title given at create when the
   two differ.
4. **`shell` is quiet, like `idle`.** Story 787 comment 7645: the CLI record's
   status `shell` is a session between turns while a background shell command
   runs, and `idle` is the same with none running. The rename guard accepts
   `shell` on the same 30 seconds of quiet. `waiting`, `busy` and any unknown
   value still hold. There is no reason code `shell`.
5. **One Backspace sequence, and only for this pod's own unsent text.** When
   the second check fails and the input row still holds exactly the text just
   typed, with the cursor directly after it, agentd sends one Backspace per
   character, checks the row is empty, and records Held, reason `interrupted`.
   That does not count as one of the two attempts when the row came back
   empty. Every other failure leaves the line alone and records Failed,
   `left-in-input`. No Escape, no Ctrl, no second Enter, and no Backspace for
   text this pod did not just type.

## What this rejects

| Rejected | Why |
|---|---|
| Leave the title in spec and special-case the generation comparisons | The comparisons are the proof that a spec change happened. A title must not look like one. |
| Drop the generation comparisons | They guard drain, rescue, canary and decision. A title is not a reason to weaken them. |
| Hold every `shell` status | Coordinators are in `shell` almost all the time (comment 7645), so a rename would never be typed. |
| Leave an unsent rename in the input line whenever the second check fails | The text this pod just typed can be removed when the row still holds exactly that text. A person's text is left alone. |

## Consequences

- A title change does not bump `metadata.generation` and does not restart,
  drain or replace a pod.
- ADR-026's statement that `spec.title` may change is superseded here. The
  guarded typed rename is unchanged apart from `shell` and the one Backspace
  sequence.
- haynes-ops still needs the CRD copy before an operator pin, because the
  character rules and the rename status enum change. `spec.title` itself is
  immutable again, as on slice 1.
