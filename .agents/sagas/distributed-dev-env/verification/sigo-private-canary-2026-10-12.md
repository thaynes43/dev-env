# Private-repo canary on the Sigo alumni project under Q-30 — 2026-10-12 UTC

This record follows the [Claude canary 1012 record](claude-canary-1012-2026-10-12.md)
and Tom's Q-30 answer. It records the deploy that came first, the corrected
runner, the one canary on a private project, its boot log read, its one named
read and one result read, and the Root's acceptance. The Root accepts private
GitHub authorization and a fresh task pin. Catalog bootstrap failed on a tracked
symlink, so a Ready bootstrap on a private project is not accepted. The fix is
merged in source and not deployed yet.

## Contract

Every canary before this one ran on `thaynes43/dev-env`, which is public. A
public clone works without a token, so no run had proved private GitHub
authorization or a fresh task pin. #173 still needed both. Q-30 asked Tom
through the Claude native prompt:

> To close #173 I need one more Claude canary on a private repo, proving private
> GitHub access and a fresh pinned task worktree. The dev bot can't create repos
> on your personal account. Could you create an empty private repo
> thaynes43/dev-env-private-probe (a README on main is enough)? Global private
> admission stays off; the canary reuses the spent 1012 slot.

Options: "I'll create it (Recommended)", "Use an existing private repo", "Not
now". Tom answered in free text: "All the Sigo alumni repos are private and all
are needed for dev-env, we use them now. Why can't you use those?" Owner answer
receipt `14903318` holds the answer and the coordinator's interpretation.

The interpretation: declare the private repositories `thaynes43/sigo-alumni`
and `thaynes43/sigoalumni-org` as catalog project `sigo-alumni`, and run one
Claude canary, `rule-canary-claude-sigo-1012`, against it. It gets one create,
one named read and one result read, with no retries and no directory read.
Global private admission stays off and repository visibility does not change.
It grants no second canary, no general private project task and no cleanup.
[DESIGN-011](../designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173)
lists the seven acceptance items this record checks.

## Deploy first

haynes-ops [#3845](https://github.com/thaynes43/haynes-ops/pull/3845) merged as
`0205d1a5`. It added the catalog project, replaced the consumed `claude-1012`
canary policy record with `claude-sigo-1012` (`codex-1011` stayed
byte-identical), added `sigo-alumni` to `--context-payload-read-projects` and
moved the Claude canary network selector. The post-check passed with no failed
check: every non-operator pod in `dev-env-system`, `dev-agents` and `dev` kept
its UID, restarts and images, and the three retained Sessions kept their UID,
generation and spec.

The canary ran on the deployed bound state:

- Operator `dev-env-operator:sha-538c4f6@sha256:d706ee40…`, template
  `2.24.1-7e04fb00dc`, two Ready replicas.
- Catalog revision `5fe34bd3…`, `sigo-alumni` rules revision `d070e7a6…`,
  common revision `3b9b829c…` (unchanged).
- Policy digest `154c3a12…`.

## Runner

Receipts are under `/home/dev/work/orders/dev-env-private-repo-canary-1012/`,
which is not published.

- **r1**, `run-sigo-canary.py` (`5f59e267`), passed 51 of 51 offline tests. Its
  independent native review returned changes required: one high finding, two
  medium and two low. The high one: r1's Create depended on a directory read,
  which Q-30 does not grant. r1 never ran.
- **r2**, `run-sigo-canary-r2.py` (`3ef04afb`), fixes all five. It drops the
  directory step; Create's own NotFound checks of the Session and Pod and an
  empty `project-canary` label selector are the absence proof. Freeze and
  remote are read-only and repeatable. It adds `result-forgo` and a single
  `wait-r2`. It passed 60 of 60 offline tests (`test-run-r2/`). Its review
  directory holds the reviewer's own test run (60 of 60), an AST partition of
  the changes and one read-only live check. No separate r2 review verdict file
  is in the order directory.
- Every step's preflight names r2 by SHA256. The client is the one local
  `agent-run` built from tag v2.24.1 for canary 1012 (`d21cdc28…`).

## The run

| Step | State | Finished (UTC) | Spent |
| --- | --- | --- | --- |
| Freeze | `PASS_FREEZE_NO_AUTO_MERGE_PR_LIVE_INPUTS_MATCH` | 21:00:05 | nothing, read-only |
| Remote | `PASS_REMOTE_PRIVATE_TIP_RECORDED` | 21:00:08 | nothing from Q-30 |
| Create | `PASS_CREATED_ORIGINAL_201` | 21:00:11 | the ID `rule-canary-claude-sigo-1012` |
| Wait | `PASS_WAIT_TASK_RESULT_PRESENT` | 21:01:35 | nothing from Q-30 |
| Boot log | `PASS_BOOTLOG_FILTERED_SAVED` | 21:01:38 | nothing from Q-30 |
| Named read | `PASS_NAMED_READ_ORDINAL_ZERO_CONTEXT_DERIVED` | 21:01:44 | the one named read |
| Result read | `PASS_RESULT_READ_SAVED` | 21:01:50 | the one result read |
| Verdict | `PASS_VERDICT_CANDIDATE_WRITTEN_UNACCEPTED` | 21:01:54 | nothing, local |

Freeze found no open haynes-ops PR with auto-merge touching the frozen paths,
and the live inputs matched. Remote read both repositories from GitHub as
private and read the tip of `thaynes43/sigo-alumni` `main` with `git ls-remote`:
`c5da46c2d83f9d99e603e66fe75b8d7fb080fb2c`.

Create returned HTTP 201 `CreatedOriginalSession` in 0.27 s:

- Session `dev-agents/private-canary-a1444f4131ec7281b5d1e3130c3b5ada`.
- UID `430297d9-aa29-4332-9b3b-37095c1b5963`.
- Parent `dev/dev-env`; createdAt `2026-10-12T21:00:11Z`.
- Requested model `claude-opus-5-5`, one turn.

The wait polled the AgentSession five times with read-only `kubectl`. On the
fifth poll the task result was `Succeeded` / `TaskSucceeded`, "the task exited
0 (success) after 1 turn", finished at 21:01:15 UTC, 64 seconds after Create.
The binding was `Bound` with all ten checks true, and the bound common rules
were `Bound` with all seven checks true. The executor Pod
`174b292b-d8e4-44f4-aa79-4e050b4224a8` ran on `talosw02`.

## The boot log read

DESIGN-011 makes this read part of the procedure, because agentd writes its
boot-step notes only to stdout. One read-only `kubectl logs` of the executor
Pod ran; it is not an operator API call. The Pod UID was the same before and
after. The runner kept only the two `boot step` lines for `project-bootstrap`
and `repo`, screened one note value, and did not keep the unfiltered log.

| Step | State | What it reports |
| --- | --- | --- |
| `project-bootstrap` | `fail` | `catalog bootstrap outcome Partial`, and one more note (screened, 67 bytes) |
| `repo` | `ok` | fetched `thaynes43/sigo-alumni` `main` at `c5da46c2` on `2026-10-12T21:01:03.943979536Z`; recorded the task source at `c5da46c2`; worktree `/home/dev/work/private-canary-a1444f4131ec7281b5d1e3130c3b5ada` on `agent/private-canary-a1444f4131ec7281b5d1e3130c3b5ada` from `c5da46c2` at `c5da46c2` |

## The two reads

| Read | Return code | Time | Finished (UTC) | Retries |
| --- | --- | --- | --- | --- |
| Named Session | 0 | 0.07 s | 21:01:44 | none |
| Fixed result | 0 | 0.12 s | 21:01:50 | none |

The named read reported boot `ready`, agent state `exited`, branch
`agent/private-canary-a1444f4131ec7281b5d1e3130c3b5ada` and head `c5da46c2`.
Its problems named two steps, `mcp` and `project-bootstrap`. It bound the
retained executor Pod and derived the ordinal-zero context name. The result
read returned that context:

- Name `published-context.c7c80a48b70d71fcb14f5c470b03a4ba.483817f8027a9bd5666e348d5bd3d4fe`,
  UID `e49b7dca-ad0f-4474-bb36-c1feca031518`, kind `transcript-excerpt`, ordinal 0.
- 102 bytes in two lines, content SHA256
  `866a9df2e4f8010d0d8b3b86a530a208ff5910bcc9e5b6d62917271f7df65396`, matching
  the saved text.
- Completeness `PartialManagedOutput`, published at 21:01:16 UTC.
- Project and repository `sigo-alumni`, catalog revision `5fe34bd3…`, rules
  revision `d070e7a6…`.
- Observed model, usage, cost, stop state and initial pin state are Unknown.

The text stays in the order directory. Per DESIGN-011, this record quotes only
its marker tokens (below).

## Acceptance items

| # | Item | State | Evidence |
| --- | --- | --- | --- |
| 1 | Private repository | Holds | GitHub reported `private: true` for both repositories at 21:00:07 and 21:00:08, before Create started at 21:00:11.014 and within 300 s of it. |
| 2 | Bootstrap and task clone | **Fails** | `project-bootstrap` reported `fail`, outcome `Partial`, and the Session's problems name it. The `repo` step is `ok`, boot is `ready` and the no-token note is absent, but those are supporting only. |
| 3 | Fetch and record lines | Holds | Both lines are present, for `thaynes43/sigo-alumni` `main`, with the same pin. |
| 4 | The task-source record | Holds | The record line carries the commit and comes before the worktree line, the fetch line carries `fetchedAt`, and the bound snapshot carries both revisions. No task-source problem. The file stays in the retained private HOME and was not read. |
| 5 | Fresh pin | Holds | `ls-remote` finished at 21:00:08.918 and Create started at 21:00:11.014; `fetchedAt` 21:01:03.944 is later than both. The pin equals the pre-Create tip, so no bracketing `ls-remote` was needed. |
| 6 | Worktree | Holds | The worktree line is on `agent/<session>` from the pin at the pin; the named read reports the same branch and head. |
| 7 | Peers unchanged | Holds | Against the pre-merge baseline (18:54:04 UTC) and the post-run snapshot (21:01:52 UTC): no pod differences and no Session differences. |

## Why item 2 fails, and the fix

agentd reports `Ready` only when every catalog repository and project root is
`Ready`. `thaynes43/sigoalumni-org` tracks one symlink (Git mode `120000`;
counted from GitHub's tree API, no path recorded). agentd's clean-checkout
proof, `projectIndexProof`, accepted only staged modes `100644` and `100755`,
and refused any other tracked entry as a conflict, submodule or special file.
So that repository's reference ended `Preserved`. The `sigo-alumni` project root
proves every member's anchor again, so it ended `Failed`, and the outcome was
`Partial`. `thaynes43/sigo-alumni` and `thaynes43/dev-env` track no symlinks, and
canaries on the `dev-env` project alone reached `Ready`. This cause comes from
a read-only analysis of the source and the tree modes. The pod's private
bootstrap report was not read, because nothing execs into a private pod.

The Session later reads phase `Failed`: its Pod reached its deadline and the
HOME is kept. That comes from one read-only `kubectl get` after the run, which
the [HANDOFF Q-30 section](../../../HANDOFF.md#q-30-private-repo-canary-on-the-sigo-alumni-project-run-and-not-accepted--2026-10-12)
reports. No receipt binds it.

[#237](https://github.com/thaynes43/dev-env/pull/237) (merge `7367fa1f`) makes
the proof accept a tracked symlink without following it
([DESIGN-006](../designs/006-private-catalog-bootstrap.md#tracked-symlinks-in-the-clean-checkout-proof)).
A HOME that 2.24.1 left behind resumes to `Ready` on its next refresh with no
operator step. The fix ships in the next release (release PR #238) and then
needs a haynes-ops template pin. Until that pin lands, every v2 pod that boots
with this catalog reports the same `project-bootstrap` problem. In this canary
the `repo` step and the task still ran.

## Recorded deviation

DESIGN-011 says to take the boot log once, "after the named read shows the task
has finished". r2's run plan takes it after the wait and before the named read:
the wait saw the task result `Succeeded` at 21:01:35, the boot log was read at
21:01:38 and the named read ran at 21:01:44. The Root names this order as a
recorded deviation, `bootlog-before-named-read`, and accepts it. Both
conditions rest on the same AgentSession status. The named read serves the
`agent.task` that the wait had already seen, and it then reported the same task
(exit code 0, finished 21:01:15). The executor Pod kept its UID across the log
read.

## Markers

Markers are secondary in this canary: rule loading is already accepted for both
providers, and items 1 to 7 do not depend on them. The output held exactly two
marker tokens and no other marker-shaped token:

- Common marker `dev-env-common-rules-20261011-4ca29d70`: present. The bound
  common rules are `Bound`, and that marker equals the expected one.
- Project marker `sigo-alumni-project-rules-20261012-9b122cd5`: reported. It
  comes from the `sigo-alumni` catalog project rules, so the project rules
  reached the agent.

The pre-run harness checked that the 227-byte prompt (`5a3da07a…`) carries
neither the common marker nor the project rules' marker.

## What is accepted

Root acceptance receipt `dd5dee24` records verdict
`PARTIAL_ACCEPTED_PRIVATE_AUTH_AND_FRESH_PIN`, with `furtherExecutionAuthorized`
false. It binds the owner answer, both runners and their tests and reviews,
every campaign gate and file, the baseline, the post-run snapshot and the
deploy post-check: every file in the order directory, 102 files, by path,
SHA256 and bytes. It is saved beside them as
`root-sigo-canary-acceptance-r1.json`.

- **Private GitHub authorization.** In its private v2 pod, agentd fetched
  `thaynes43/sigo-alumni`, private at Create, at its private tip. A private
  repository cannot be fetched without authorization, and the
  `rule-canary-claude` profile mounts only the haynes-dev-bot GitHub token.
- **A fresh task pin.** The fetch ran after the Create and after the pre-Create
  `ls-remote`, and the pin equals that tip. agentd recorded it as the task
  source and made the task worktree on `agent/<session>` from it.
- **Peers preserved.**

Not accepted:

- A `Ready` catalog bootstrap on a private multi-repository project (item 2).
- Authorization for `thaynes43/sigoalumni-org`. Its bootstrap clone is not in
  the kept lines, and it has no task pin.
- Provider success, served model, usage, cost, history, stop, the
  multi-provider MVP or the owner pilot.

## #173 status

Shown: loaded common and repository rules for both providers (the Codex original
and Claude canary 1012), private GitHub authorization and a fresh task pin
(this canary). Failed: catalog bootstrap of a project with a symlink-carrying
repository; fixed in source by #237, deploy pending. Pending: acceptance of a
`Ready` bootstrap on a private multi-repository project. That needs the fix
deployed and a new canary, which Q-30 does not grant: it needs Tom's approval
and its own record. #173's other acceptance items (two bounded pods with
independent Git directories, missing-repo bootstrap, a failed fetch blocking new
work, preserved local state) are outside this record. #173 stays open.

## Still Unknown

- Served model (requested `claude-opus-5-5`).
- Usage and cost.
- Full native history.
- Physical stop.
- Capture completeness beyond the excerpt.
- Tool-use compliance (the prompt forbade tools).

## Counters

Directory 6: this run made no directory read. Claude Create 3 (1011, 1012 and
sigo-1012), Codex Create 1. Claude named and result reads 3 each. Codex named
and result reads 1 each. No automatic retries and no reset. All four permanent
IDs are consumed: `rule-canary-claude-1011`, `rule-canary-codex-1011`,
`rule-canary-claude-1012` and `rule-canary-claude-sigo-1012`. The Q-30 create,
named read and result read are consumed. No further read, retry, Create,
repost, rearm, replacement or cleanup follows from this record.
