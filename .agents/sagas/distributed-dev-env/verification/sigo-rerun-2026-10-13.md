# Sigo rerun canary under Q-32: all items hold — 2026-10-13 UTC

This record follows the [Sigo private-repo canary record](sigo-private-canary-2026-10-12.md)
and Tom's Q-32 answer. It records the 2.25.0 deploy and the canary record that
came first, the runner and its review, the hold for declared maintenance, the
one canary, its boot log read, its one named read and one result read, and the
Root's acceptance. All seven DESIGN-011 items hold, so under DESIGN-011's
all-items gate the run is accepted for #173's private-repository evidence:
private GitHub authorization, a `Ready` catalog bootstrap on a private
multi-repository project and a fresh pinned task worktree. #173 stays open for
its other acceptance items.

## Contract

The Q-30 canary, `rule-canary-claude-sigo-1012`, failed item 2: catalog
bootstrap ended `Partial`. #237 fixed the cause in source, and #239 added
closed-set reason codes to `status.agent.projectBootstrap`. Q-32 asked Tom
through the Claude native prompt:

> The Sigo canary proved private GitHub access and a fresh pinned worktree, but
> catalog bootstrap ended Partial: one Sigo repo tracks a symlink our checkout
> check wrongly refused. The fix is merged and deploying now as 2.25. May I run
> one more Claude canary on the Sigo project after the deploy, to prove
> bootstrap reaches Ready and the new status codes work? One create, one named
> read, one result read, no retries.

Options: "Yes, after deploy (Recommended)", "Not now". Tom answered "Yes, after
deploy (Recommended)". Owner answer receipt `root-q32-owner-answer-r1.json`
(1278 bytes, SHA256 `48ab104f…`, recorded at 22:44:16 UTC on 2026-10-12) holds
the ruling.

It grants, after the 2.25 deploy is post-checked, one new Claude canary record
for project `sigo-alumni` through a reviewed haynes-ops PR. The record replaces
the consumed `rule-canary-claude-sigo-1012`, and `rule-canary-codex-1011` stays
byte-identical. The canary gets exactly one create, one named read and one
result read, with no retries. Global private admission stays off. It does not
grant reuse of a consumed canary ID, a retry, a second rerun, credential repair
or cleanup of any original.
[DESIGN-011](../designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173)
lists the seven acceptance items this record checks. Q-32 adds the status codes
to item 2 (below).

## Deploy first

dev-env v2.25.0 (release PR #238, tag commit `86df5507`) carries #236 (DESIGN-025
H1, the durable private executor terminal record), #237 (tracked symlinks in the
clean-checkout proof) and #239 (bootstrap reason codes). haynes-ops
[#3852](https://github.com/thaynes43/haynes-ops/pull/3852) (`217dbede`, the
AgentSession CRD) merged first, then
[#3854](https://github.com/thaynes43/haynes-ops/pull/3854) (`1815eab0`): operator
`sha-388444d@sha256:75c41e04…`, template `2.25.0-b7972dd7b5` with agent image
`2.25.0@sha256:25b20148…`, the H2 RBAC, the terminal record on and the R1 alert.
The deploy post-check passed.

The canary record came next. haynes-ops
[#3855](https://github.com/thaynes43/haynes-ops/pull/3855) merged as `156bd3a6`
at 00:22:55 UTC. It changed four fields of the consumed Sigo record: `id`
`rule-canary-claude-sigo-1012b`, `idempotencyKey`
`rule-canary-claude-sigo-1012b-once`, the template revision and the image.
`rule-canary-codex-1011` stayed byte-identical. The new policy digest is
`5b11e61c…` and the Session is
`dev-agents/private-canary-eb9d486a612e27fd2b52d27f675512be`. Its post-check
passed: two new operator pods Ready with no restarts on the new flag, the
ConfigMaps, claims and four retained Sessions unchanged, and the Claude canary
network selector moved. Peer pods had moved before the merge, because declared
maintenance (`act-000159-3770365`) shut down `talosw01`; the post-check
attributes every such move to it and leaves none unexplained.

The canary ran on this bound state: catalog revision `5fe34bd3…`, `sigo-alumni`
rules revision `d070e7a6…`, common revision `3b9b829c…`, templates
`b54bab49…`, operator args `acbc1696…`.

## Runner

Receipts are under `/home/dev/work/orders/dev-env-sigo-rerun-1012/`, which is
not published.

- **r3**, `run-sigo-rerun.py` (`8d24a618`), derived from the Sigo r2 runner. It
  reads the new record from `record-constants.json`, pins that file and the
  item 7 baseline at freeze, and adds the Q-32 status checks to item 2. It passed
  85 of 85 offline tests. Its independent native review asked for changes. One
  medium finding: freeze checked only the policy flag's hash, so a wrong ID, key
  or digest in `record-constants.json` would surface only after Create and spend
  it. One low finding: retained Sessions in the baseline were not shape-checked.
  The first baseline also lacked pod labels, so item 7 could not leave out the
  operator pods. r3 never ran.
- **r4**, `run-sigo-rerun-r2.py` (`735fb7c0`). Its correction note
  `CORRECTION-rerun-r2.md` fixes both findings: freeze and the result read's
  re-check compare the live record field by field, by `Record.Digest()` and by
  the whole operator argument list; retained entries are shape-checked; item 7
  reads `baseline-item7.json`, taken pre-merge at 00:21:53 UTC with the same
  script as the post-run snapshot. It passed 100 of 100 offline tests. Its
  independent native review (a Claude Opus 5.5 subagent that wrote neither
  runner) returned **APPROVE** with no blocking findings, both findings fixed
  and failing closed, and its own run of 100 of 100.
- Every step's preflight names r4 by SHA256. The client is one local
  `agent-run` built from tag v2.25.0 (`fa601b36…`).

## Held for declared maintenance

The review set one run condition: hold until the declared maintenance that
scoped `ceph` and `gasha01` ended. The canary's HOME is on `gasha01-rbd`, and
every session pod mounts the shared CephFS volume. Q-32 grants one create and no
retries, so the coordinator held freeze, remote and create.

| Time (UTC) | Event |
| --- | --- |
| 00:01:59 | `act-000159-3770365` declared: host and GPU maintenance; its scope includes `ceph` and `gasha01` |
| 00:37:01 | r4 review: APPROVE, hold until that activity ends and storage is healthy |
| 00:55:26 | `act-005526` replaces it, still scoping `ceph` and `gasha01`; the hold continues |
| 01:12 | Tom reports he is done turning nodes off |
| 01:19:21 | The activity's owner ends `act-005526` and declares `act-011921-3809066`: maintenance complete, other agents may resume; scope `talosw01`, `talosw04`, `llama-server`, `ollama-prime` |
| before 01:20:05 | The coordinator re-checks: nodes Ready and uncordoned, Ceph `HEALTH_OK`, no unhealthy CSI or rook pod, no activity scoping `ceph`, `gasha01` or `dev-agents` |
| 01:20:05 | The coordinator declares `act-012005-3809973` for the run, and ends it after the verdict |

The first two rows come from receipts. The 00:55:26 and 01:20:05 times are the
times in the activity IDs. The replacement and end of `act-005526`, Tom's
report, the re-check and the end of `act-012005-3809973` come from the
coordinator's work order, and no receipt binds them. The `act-011921-3809066` declaration was read
directly when the Root receipt was written.

## The run

| Step | State | Finished (UTC) | Spent |
| --- | --- | --- | --- |
| Freeze | `PASS_FREEZE_NO_AUTO_MERGE_PR_LIVE_INPUTS_MATCH` | 01:20:06 | nothing, read-only |
| Remote | `PASS_REMOTE_PRIVATE_TIP_RECORDED` | 01:20:09 | nothing from Q-32 |
| Create | `PASS_CREATED_ORIGINAL_201` | 01:20:09 | the ID `rule-canary-claude-sigo-1012b` |
| Wait | `PASS_WAIT_TASK_RESULT_PRESENT` | 01:21:32 | nothing from Q-32 |
| Boot log | `PASS_BOOTLOG_FILTERED_SAVED` | 01:21:36 | nothing from Q-32 |
| Named read | `PASS_NAMED_READ_ORDINAL_ZERO_CONTEXT_DERIVED` | 01:21:36 | the one named read |
| Result read | `PASS_RESULT_READ_SAVED` | 01:21:47 | the one result read |
| Verdict | `PASS_VERDICT_CANDIDATE_WRITTEN_UNACCEPTED` | 01:21:48 | nothing, local |

Freeze found no open haynes-ops PR with auto-merge on the frozen paths, and every
live input matched the record. Remote read both repositories from GitHub as
private and read the tip of `thaynes43/sigo-alumni` `main` with `git ls-remote`:
`c5da46c2d83f9d99e603e66fe75b8d7fb080fb2c`, the same tip as the Q-30 run.

Create returned HTTP 201 `CreatedOriginalSession` in 0.17 s:

- Session `dev-agents/private-canary-eb9d486a612e27fd2b52d27f675512be`.
- UID `454d12a2-60bc-4c4e-95c5-bb4b26428a65`.
- Parent `dev/dev-env`; createdAt `2026-10-13T01:20:09Z`.
- Requested model `claude-opus-5-5`, effort `xhigh`, one turn.

The HOME was new: the record post-check found no Session, Pod or claim for this
name, and Create found neither the Session nor its Pod.

The wait polled the AgentSession five times with read-only `kubectl`. On the
fifth poll the task result was `Succeeded` / `TaskSucceeded`, "the task exited
0 (success) after 1 turn", finished at 01:21:11 UTC, 62 seconds after
`createdAt`.
The binding was `Bound` with all ten checks true, and the bound common rules
were `Bound` with all seven checks true. The executor Pod
`3f88bf37-56c3-46ab-b885-057aa18fe30a` ran agentd v2.25.0 on `talosw02`.

## The boot log read

One read-only `kubectl logs` of the executor Pod ran; it is not an operator API
call. The Pod UID was the same before and after. Of 23 log lines, 20 were boot
steps; the runner kept the two for `project-bootstrap` and `repo`, screened no
value, and did not keep the unfiltered log.

| Step | State | What it reports |
| --- | --- | --- |
| `project-bootstrap` | `ok` | `catalog bootstrap outcome Ready` (one note) |
| `repo` | `ok` | fetched `thaynes43/sigo-alumni` `main` at `c5da46c2` on `2026-10-13T01:20:58.087169155Z`; recorded the task source at `c5da46c2`; worktree `/home/dev/work/private-canary-eb9d486a612e27fd2b52d27f675512be` on `agent/private-canary-eb9d486a612e27fd2b52d27f675512be` from `c5da46c2` at `c5da46c2` |

## The two reads

| Read | Return code | Time | Finished (UTC) | Retries |
| --- | --- | --- | --- | --- |
| Named Session | 0 | 0.07 s | 01:21:36 | none |
| Fixed result | 0 | 0.12 s | 01:21:47 | none |

The named read reported boot `ready`, agent state `exited`, branch
`agent/private-canary-eb9d486a612e27fd2b52d27f675512be` and head `c5da46c2`.
Its problems named one step, `mcp`: the known v2 MCP-secret parity warning that
the `cigar-journal` MCP token is not set in the pod. The `rule-canary-claude`
profile mounts only the GitHub token, and the prompt forbade tools. Its
`projectBootstrap` is `Ready` with catalog revision `5fe34bd3…`: repositories
`dev-env`, `sigo-alumni` and `sigoalumni-org` all `Ready`, projects `dev-env` and
`sigo-alumni` both `Ready`, and no reason code. The wait's last AgentSession read
carries the same. The named read bound the retained executor Pod and derived the
ordinal-zero context name.

The Session view carries provider-reported counts for the first managed
invocation: 2 input, 879 output, 10,373 cache-read and 21,640 cache-creation
tokens, and a CLI-equivalent cost estimate of 0.1927826 USD. As in the earlier
records, these are source assertions. They are not accepted as charged usage or
cost.

The result read returned that context:

- Name `published-context.d8d557dd249473368539a7b0260f45ed.9fb3fef5798731ce239d1ecc7c9915d7`,
  UID `770e893b-ce17-4604-a77e-9e82d57baf44`, kind `transcript-excerpt`, ordinal 0.
- 102 bytes in two lines, content SHA256
  `866a9df2e4f8010d0d8b3b86a530a208ff5910bcc9e5b6d62917271f7df65396`, matching
  the saved text. The Q-30 run's text had the same SHA256; this is a different
  context from a different Session.
- Completeness `PartialManagedOutput`, published at 01:21:11 UTC.
- Project and repository `sigo-alumni`, catalog revision `5fe34bd3…`, rules
  revision `d070e7a6…`.
- Observed model, usage, cost, stop state and initial pin state are Unknown.

The text stays in the order directory. Per DESIGN-011, this record quotes only
its marker tokens (below).

## Acceptance items

| # | Item | State | Evidence |
| --- | --- | --- | --- |
| 1 | Private repository | Holds | GitHub reported `private: true` for both repositories at 01:20:08.087 and 01:20:08.549, before Create started at 01:20:09.766 and within 300 s of it. |
| 2 | Bootstrap and task clone | Holds | `project-bootstrap` reported `ok`, outcome `Ready`; the `repo` step is `ok`; the Session's problems name neither step. Q-32's status checks hold in both reads: `projectBootstrap` well formed, `Ready`, the expected catalog revision, every declared repository and project `Ready`, no reason code. All 13 checks are true. Boot `ready` and the absent no-token note are supporting only. |
| 3 | Fetch and record lines | Holds | Both lines are present, for `thaynes43/sigo-alumni` `main`, with the same pin. |
| 4 | The task-source record | Holds | The record line carries the commit and comes before the worktree line, the fetch line carries `fetchedAt`, and the bound snapshot carries both revisions. No task-source problem. The file stays in the retained private HOME and was not read. |
| 5 | Fresh pin | Holds | `ls-remote` finished at 01:20:09.414 and Create started at 01:20:09.766; `fetchedAt` 01:20:58.087 is later than both. The pin equals the pre-Create tip, so no bracketing `ls-remote` was needed. |
| 6 | Worktree | Holds | The worktree line is on `agent/<session>` from the pin at the pin; the named read reports the same branch and head. |
| 7 | Peers unchanged | Holds | Against `baseline-item7.json` (pre-merge, 00:21:53 UTC) and the post-run snapshot (01:21:47 UTC), taken with the same script: no pod and no Session differences. Compared: the 10 pods in `dev-env-system`, `dev-agents` and `dev` that are neither operator nor Job pods, and the four retained Sessions (UID, generation, spec). Not compared: the operator pods (rolled by #3855), the canary's own pod, a Flux-made fixture pod that was re-created at 00:23:32 UTC and is not in the baseline, and the `upgrade-agent` namespace, which both snapshots hold but item 7 does not cover. |

## How Q-32 adjusts the items

Root acceptance `a3aa0b7d` lists four Q-32 adjustments, and DESIGN-011's rerun
note lists the same four. None relaxes the gate.

- Item 2 adds the `status.agent.projectBootstrap` checks, because Q-32 asks to
  prove "the new status codes work". The boot-log checks are unchanged.
- Item 7 covers four retained originals, not three: the Q-30 Session
  `private-canary-a1444f41…` joined the two 1011 originals and the 1012 canary.
- Item 7 leaves out the operator pods by their `app.kubernetes.io/name` label,
  as DESIGN-011's "every non-operator pod" says.
- Both item 7 snapshots carry the snapshot script's fixed purpose text, which
  names the Q-30 PR. They were taken for #3855 and this run.

Two run-plan inputs changed with the record and are not item changes: the
freeze list checks template revision `2.25.0-b7972dd7b5`, and the boot log read
targets the new Session's Pod.

## Recorded deviation

**`bootlog-before-named-read`.** DESIGN-011 says to take the boot log once,
"after the named read shows the task has finished". r4 keeps r2's run plan,
which takes it after the wait and before the named read: the wait saw the task
result `Succeeded` at 01:21:32, the boot log was read from 01:21:35.8 to
01:21:36.1 and the named read ran at 01:21:36.2. The Root names this order as a
recorded deviation and accepts it, as for the Q-30 run. Both conditions rest on
the same AgentSession status. The named read serves the `agent.task` that the
wait had already seen, and it then reported the same task (exit code 0, finished
01:21:11). The executor Pod kept its UID across the log read.

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
neither marker.

## What is accepted

Root acceptance receipt `a3aa0b7d` records verdict
`ACCEPTED_PRIVATE_REPO_CANARY_ALL_ITEMS`, with `furtherExecutionAuthorized`
false. It binds 215 files by path, SHA256 and bytes: every other file in the
order directory (179), the Q-32 owner answer, the snapshot script, the Sigo r2
runner and the Q-30 Root, the 2.25 deploy evidence (31 files) and the operator
CA. Its 46 integrity checks passed. It is saved beside them as
`root-sigo-rerun-acceptance-r1.json`.

Under DESIGN-011's all-items gate the run is accepted for #173's
private-repository evidence:

- **Private GitHub authorization.** In its private v2 pod, agentd fetched
  `thaynes43/sigo-alumni`, private at Create, at its private tip. Catalog
  bootstrap also brought the private `thaynes43/sigoalumni-org` to `Ready` in a
  HOME that was new at Create. The `rule-canary-claude` profile mounts only the
  haynes-dev-bot GitHub token.
- **A `Ready` catalog bootstrap on a private multi-repository project.** Every
  declared repository and project root is `Ready`, in the boot step and in
  `status.agent.projectBootstrap`. This is consistent with #237: the repository
  that the Q-30 diagnosis named is now `Ready`. Neither run recorded its tip, so
  no receipt shows that it still tracks the symlink. With everything `Ready`,
  the #239 field carries no reason code, so no non-`Ready` code was exercised in
  the cluster.
- **A fresh pinned task worktree.** The fetch ran after the Create and after the
  pre-Create `ls-remote`, and the pin equals that tip. agentd recorded it as the
  task source and made the task worktree on `agent/<session>` from it.
- **Peers preserved.**

Not accepted: a private repository run on Codex (none was made); served model,
usage, cost, history, stop, the multi-provider MVP or the owner pilot.

## What #173 now has

The rows come from three places, named in the Source column. "Acceptance list"
is the issue's own acceptance sentence. "Issue body" is a requirement stated
earlier in [#173](https://github.com/thaynes43/dev-env/issues/173): "New task
admission still fetches the selected repo and records the exact commit".
"DESIGN-011" is the private-repository evidence that
[DESIGN-011's Q-30 section](../designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173)
gates for #173. Only the "Acceptance list" rows are #173's acceptance items.

| #173 item | Source | State |
| --- | --- | --- |
| Both actual providers load the expected rule revision | Acceptance list | **Accepted.** The Codex original and Claude canary 1012 loaded the common and repository rules; both Sigo runs reported the project rules' marker too. |
| New task admission fetches the selected repo and records the exact commit | Issue body | **Accepted** for a private repository (this run, items 3 to 5). |
| Private GitHub authorization, `Ready` private catalog bootstrap, fresh pinned task worktree | DESIGN-011 | **Accepted** under DESIGN-011's all-items gate (this run). |
| Missing repo bootstrap works | Acceptance list | **Partly shown.** In this run's new HOME all three declared repositories were missing at boot and reached `Ready`. Not shown: an existing HOME that lacks a repository declared later. |
| Two bounded pods have independent Git directories and task worktrees | Acceptance list | **Open.** Each canary ran in its own pod with its own HOME, but no run compared two pods' Git directories and task worktrees. |
| A failed fetch blocks fresh implementation | Acceptance list | **Open.** No installed run has shown it. |
| Dirty, WIP and reference state is preserved | Acceptance list | **Open** for an installed run. The 2026-10-11 refresh acceptance covered a clean source only. |

Global private admission stays off, so general private project tasks still
refuse. #173 stays open.

## Still Unknown

- Served model (requested `claude-opus-5-5`).
- Usage and cost. The provider-reported counts above are source assertions,
  not accepted as charged usage or cost.
- Full native history.
- Physical stop beyond the terminal record. Stop state was Unknown at both
  reads. The terminal record, on since the 2.25 deploy, may later record the
  executor's end; no receipt here binds one.
- Capture completeness beyond the excerpt.
- Tool-use compliance (the prompt forbade tools).

## Counters

Directory 6: this run made no directory read. Claude Create 4 (1011, 1012,
sigo-1012 and sigo-1012b), Codex Create 1. Claude named and result reads 4
each. Codex named and result reads 1 each. No automatic retries and no reset.
All five permanent IDs are consumed: `rule-canary-claude-1011`,
`rule-canary-codex-1011`, `rule-canary-claude-1012`,
`rule-canary-claude-sigo-1012` and `rule-canary-claude-sigo-1012b`. The Q-32
create, named read and result read are consumed. No further read, retry,
Create, second rerun, repost, rearm, replacement or cleanup follows from this
record.
