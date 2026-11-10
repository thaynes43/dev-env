# ADR-020: Task sessions are cleaned up within 7 days, keeping only a bundle of unpushed work

- **Status:** Accepted 2026-11-10. Source, installation and acceptance are
  separate and open (OpenProject #518).
- **Date:** 2026-11-10
- **Decider:** Tom Haynes
- **Ratified:** Tom, 2026-11-10, in two parts, both quoted in full below: a
  message he typed in coordinator session `dev-env-fable-coordinator-1109`
  (00:52:51Z by that session's shell clock), then a native phone prompt
  (00:55:01Z), answered "Yes, backlog included (Recommended)". The coordinator
  recorded both on OpenProject #490, comments 6210 and 6211. The author of this
  ADR, a native subagent of that coordinator, did not see either at its source;
  the work order's quotes and the two tracker comments match word for word.
  Saga decision log: row 96, entered by the coordinator in the same change.
- **Supersedes, for task Sessions only** (`spec.mode: task`): the statements of
  [ADR-005](005-private-home-expiry.md), [ADR-006](006-private-home-expiry-corrections.md),
  [ADR-007](007-preserve-v1-archive-trust-boundary.md) and
  [ADR-018](018-two-tier-home-retention.md) named in the table below. They stay
  in force for every other Session.
- **Retains:** the durable stop proof, the guarded deletes, canary and pilot
  protection, the owner's hold and release-now, and D-62 for interactive
  Sessions (table below).
- **Design:** [DESIGN-061](../designs/061-task-session-cleanup.md)
- **Tracking:** OpenProject [#518](https://work.haynesnetwork.com/work_packages/518)
  (parent #99); the ruling is on #490.

## Context

A task Session is a headless `agent-run -p` Session. Since ADR-008 every new
task Session is private: its HOME volume `home-<session>` is kept when it is
suspended or reaped, and only the HOME expiry of ADR-005 and DESIGN-025 may
delete it. That expiry waits 720 hours after the recorded stop, copies the
provider history to an archive, and deletes nothing until the archived read is
accepted. The archive was never switched on, so nothing drained. On
2026-11-10 there were 502 Sessions and 499 volumes in `dev-agents`; 465 task
Sessions had finished or been purged, 404 of them created more than 7 days
earlier.

The 30 days were never Tom's words. Q-31 (ADR-005) put a proposal to him with
30 days in it, and he chose "Archive, then delete (Recommended)". The
coordinator corrected its own attribution on #490 (comment 6210).

## The ruling, as given

Typed by Tom in the coordinator's session:

> "I explicitly asked Meatball to only keep session data around for 7 days max after the session was purged or completed, the 30 day rule was not my rule I want to clean things up ASAP 99% of the time since it's all tracked in Git or OpenProject"

Then asked by phone prompt, in full:

> "Clean-up rule, from what you just said. Finished or purged task sessions: 465 today, 404 of them older than 7 days. Proposed: (1) a session you purge is deleted as soon as its agent has stopped; (2) a task that finished on its own is deleted 7 days later; (3) before a volume goes, any commits or files that never reached GitHub are saved as a small git bundle, kept 30 days; nothing else is kept, no conversation archive; (4) your interactive sessions are not touched by this. Deleting is permanent. How should I build it?"

The options were "Yes, backlog included (Recommended)", "Yes, but keep
nothing" and "New sessions only for now". Tom selected:

> "Yes, backlog included (Recommended)", described as: "Build exactly that. Once it is live, the 404 finished task sessions older than 7 days are deleted (after the unpushed-work check on each), then the rest as they reach 7 days."

"Purge" is what the product calls a reap (`agent-run reap`). "Interactive
sessions" are modes `local` and `remote`.

## Decision

1. **A purged task Session is deleted as soon as its agent has stopped.** No
   wait follows the stop proof. Its HOME goes, then the Session.
2. **A task Session that ended on its own is deleted 7 days later.** That
   covers a task that finished (suspended by the terminal task reclaim) and one
   whose Pod ended without finishing (phase `Failed`). The 7 days run from the
   operator's recorded terminal observation of its executor,
   `status.privateHistory.executorTerminal.observedAt`. That record is immutable
   and survives Pod loss and operator restarts. A resume or a message does not
   reset it, because a private task's executor never runs again.
3. **Before a HOME is deleted, two things must hold:**
   - the agent's stop is proven. The Session holds the confirmed terminal
     record of its recorded executor (ADR-005's durable stop proof). Before
     any pod mounts the HOME again, the node it ran on also no longer lists
     the HOME's volume as in use, so the old Pod's teardown is over
     (DESIGN-061 1.1, Q-OP515-1); and
   - the Git rescue ran in a hold pod bound to that HOME and its result is
     either `Verified`, with every bundle re-hashed on the shelf and matching
     its manifest, or `CleanAndPushed`, meaning every repository was fetched
     and found clean and pushed.
   If either cannot be shown, the Session and its HOME are kept and listed for a
   person. Nothing is deleted on a guess.
4. **Only the bundle is kept.** It holds the commits, tracked edits and
   untracked files that origin lacks. It lands on the shared volume under
   `rescue/<session>/<stamp>/`. It is kept for 30 days after the Session is gone,
   then pruned. Gitignored files, files outside a repository and the provider's
   conversation transcripts are not kept. The task's log copy on the shared
   volume goes with the Session.
5. **No longer done for task Sessions:** the 720-hour grace, the
   provider-history export and archive, and the archived-history read as a
   condition for deletion.
6. **Interactive Sessions are not touched.** Local and remote Sessions keep
   today's lifecycle (D-51 at a reap, D-62's rescue and delete 168 hours after a
   suspend). A private interactive Session marked before ADR-008 stays under
   DESIGN-025, which remains off.
7. **The backlog is included.** Existing finished and purged task Sessions are
   cleaned as soon as this is live: oldest first, purged ones ahead of finished
   ones, a few at a time.

8. **A task Session whose stop was never recorded is cleaned too** (Tom,
   2026-11-10, by phone prompt: "Yes, clean them too (Recommended)"; the full
   prompt is in DESIGN-061 section 9). For these, and only these, the stop
   proof of point 3 is replaced by: at least 7 days since the operator recorded
   the missed executor; no Pod of that name and none mounting the HOME; the
   node it ran on no longer lists the volume as in use; and the clean-up's own
   pod attaches the volume. The rescue of point 3 runs unchanged. It is a later
   source step (DESIGN-061 F6), after the main path is proven.

**Consequence for transcripts.** The conversation transcripts of a task
Session (Claude `projects/`, Codex `sessions/` and the rest) and its
`~/work/<session>.log` live only in its HOME. They are gone when the HOME is
deleted. No copy exists anywhere else.

## What this supersedes and what it keeps

| ADR | Superseded for task Sessions | Kept |
|---|---|---|
| ADR-005 | "At least 30 days have passed since the expiry's grace started"; "The grace is 30 days"; the trigger "Reaping, or ... Suspended and past its suspended-archive deadline" (now: Reaping, or the terminal record exists); "A resume of the Session ... clears that record" (a resume no longer cancels; a new Pod still does); steps 2 to 4 of "Before a HOME is deleted" (history export, archive, readable); step 5's re-verification of the archive (now: of the bundle); "Deletion is off by default and stays off until the archived-history read is accepted"; C-32 and C-35 | The durable stop proof and "absence alone never proves stop"; canary originals never expire; hold and release-now are Tom's alone; step 1 (Git rescue in a hold pod bound to the HOME UID) and step 5's guarded delete with UID and resourceVersion; no credential leaves a HOME; ADR-004 takes over once a Session is released (now for finished task Sessions too); C-31, C-33, C-36 |
| ADR-006 | Section 1's "Q-31's policy still triggers expiry for one past its archive deadline, but only if its executor ended on its own" (the trigger no longer waits for the archive deadline); sections 2 and 5 do not apply, since nothing is archived | The stop only at a reap; an owner hold blocks the stop; a suspended private Session is never stopped; the fifth guard exception for the expiry's hold pod; C-39, C-40, C-41 |
| ADR-007 | "Retains: Q-31's archive-before-delete gates" and C-46's list of independent gates (PVC, Retain, F4, cold measurements, H8): none gates task-session clean-up | v1 stays unchanged until it is decommissioned; the archive trust boundary for whatever remains archived |
| ADR-018 | "At 168 hours unresolved or unknown work enters a visible owner decision batch; expiry never means permission to delete"; "Existing cleanup requires specific owner approval of a UID-bound destructive batch. No unattended irreversible purge is authorized."; preservation by authenticated publication receipts and required history (the rescue verdict above is the preservation); the clock's preference for `finishedAt` (one clock, `executorTerminal.observedAt`) | Active Sessions are protected; a missing clock is an exception, never PVC creation or scan time; everything for non-task Sessions; DESIGN-044's offline package stays as it is |

ADR-008 is unchanged. D-92's shared read of archived history has nothing to
read for task Sessions.

## Consequences

| Id | Consequence |
|---|---|
| C-47 | Good: a purged task Session's HOME is gone within minutes of the stop, and a finished one's 7 days after it ended. The backlog of about 400 volumes drains. |
| C-48 | Good: Sessions, rescue bundles and task logs no longer pile up. A released Session's bundle is pruned 30 days later (D-09), and its coordination records follow ADR-004. |
| C-49 | Bad: transcripts of task Sessions are not kept. Anything an agent learned and did not write to Git or OpenProject is lost with the HOME. |
| C-50 | Bad: the operator deletes HOMEs and Sessions unattended. A mistaken reap of a live task now stops it and deletes its HOME minutes later, keeping only unpushed Git work. An owner hold set before the reap still prevents both. |
| C-51 | Bad: a rescue that refuses (a merge in progress, a nested repository, more than 50 MiB untracked, submodule work, or a repository outside `~/repos`) keeps that Session. A person must decide, so a few HOMEs can stand past 7 days. The alert names them. |
| C-52 | Neutral: Sessions with no recorded stop (`PreservationUnknown`, `RetentionPending`), canaries and pilots stay outside this path at first. They need a person or their own follow-up story (DESIGN-061 section 5). For the `PreservationUnknown` task Sessions Tom ruled the same day that they are cleaned too (point 8). |
| C-53 | Neutral: the history archive, its reads and the H7 checks have no task-session use left. They stay off and are removed by later stories, not by this decision. |
| C-54 | Bad: a finished task Session's own record goes with it: its retained outcome (PR links, summary), its usage and cost numbers and `agent-run show`. No other store holds them today (the portal database keeps logins only, and no metric carries usage or cost). What must outlive 7 days is written to the tracker item or Git first. Before the backlog drains, the coordinator saves a usage summary of the Sessions to be cleaned on OpenProject #518; a ledger of totals that survives clean-up is DESIGN-061's follow-up F7. |
