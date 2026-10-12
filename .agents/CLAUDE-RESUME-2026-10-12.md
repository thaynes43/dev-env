# Claude continuation: dev-env v2 toward a working multi-provider MVP

Prepared 2026-10-12, America/New_York, from `origin/main` at `f6de1680a4a00a12d25bea5c43865d088c068db1`.
This is Tom's requested Claude pickup brief. It consolidates his current
requirements, the earlier enumerated lists, delivered work and stopped routes.
It grants no new runtime permission, retry budget or credential access.

**Updates after preparation, 2026-10-12.** The brief was prepared from
`f6de168` (#216). It merged as #217 (`ae7bec7`), so `main` includes the brief
itself; references to `f6de168` below describe its starting revision. Tom then
answered Q-27 through the Claude native prompt: "Fix once, then read
(Recommended)". That allows the one offline correction in roadmap step 1, using a
fixed list of already accepted evidence. Then it allows exactly one named read and
one result read of the Codex original, with no retries and no counter reset.
The Q-27 "pending" text below is the original record; the ruling sits under it.
Q-24 is still pending.
The correction then passed its tests and review, and both Codex reads ran once.
The Codex original returned both rule markers; see the dated counters update
below and the [Codex original result record](sagas/distributed-dev-env/verification/codex-original-result-2026-10-12.md).
*Update, Q-28, 2026-10-12:* Tom answered Q-28, "Yes, after fixes deploy
(Recommended)". After v2.24.1 deployed, one new Claude canary returned both
markers, so rule loading is accepted for both providers. See the Q-28 counters
update below and the
[Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md).
The MVP stays unaccepted.

## Your assignment

Continue implementation in bounded, reviewable increments. Independently review
the work Codex shipped, find bugs and improve it; do not assume merged code or
its tests establish a working MVP. Fix concrete findings and stale guidance,
ship through required CI and reviewed GitOps where needed, and verify the
affected behavior without restarting existing sessions. Do not return another
general architecture plan.

First fetch `origin/main` in `/home/dev/repos/dev-env`, then create a fresh
`/home/dev/work/<task>` worktree and `agent/<task>` branch from that revision.
Canonical clones are fetch-only. Completed worktrees are read-only evidence;
do not reuse, prune or repair them. Deploy manifests belong in
`thaynes43/haynes-ops`, also using a fresh task worktree.

Read this file in full, then [AGENTS.md](../AGENTS.md), [CLAUDE.md](../CLAUDE.md),
[HANDOFF](HANDOFF.md) in full, the
[workflow guide](../docs/workflow-guide.md), and
[ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md).
Read current [#91](https://github.com/thaynes43/dev-env/issues/91) and
[#173](https://github.com/thaynes43/dev-env/issues/173), including their linked
checkpoints. Read the relevant design and source for each selected increment.
Lower HANDOFF entries and verification records describe historical observations;
their old next steps do not override this brief or later owner instructions.

## Requirement history: which lists still control

The original conversation timestamps are not all preserved. Dates below describe
the recorded requirement; a bot comment's creation time is not the original
owner message time. Preserve these distinctions rather than inventing chronology.

| Owner enumeration or later record | Where to read it | Current interpretation |
| --- | --- | --- |
| 2026-10-09 project requirements R1–R6, with additional R7 | [Original project-root requirements](sagas/distributed-dev-env/requirements/2026-10-09-project-roots.md) | Declared projects, both providers, common rules in roots and task worktrees, durable roots and safe repository freshness remain goals. ADR-003 supersedes shared writable Git/RWX/path prescriptions and broad reset interpretations. |
| 2026-10-09 Q-23 stall/attempt requirement, with seven contract items | [Task budgets](sagas/distributed-dev-env/requirements/2026-10-09-task-budgets.md) | Still binding across agents, pods, methods and conversations. Stopped logical tasks do not get new counters when Claude takes over. |
| 2026-10-10 five outcomes and private-repository/shared-context scope reset | [Owner scope](sagas/distributed-dev-env/requirements/2026-10-10-owner-scope.md), ADR-003 | Controls conflicting earlier designs and storage investigations. |
| 2026-10-10 first implementation record, created 11:50:07 EDT | [#173 comment 6099322779](https://github.com/thaynes43/dev-env/issues/173#issuecomment-6099322779) | Bot-written delivery record. The first admission/preparation assignment has shipped; do not assign it again. |
| 2026-10-10 concurrency/failure-boundary record, created 12:51:51 EDT | [#91 comment 6099902031](https://github.com/thaynes43/dev-env/issues/91#issuecomment-6099902031) | Bot-written record of the owner's clarification. Complete native-session isolation is required; one visible Codex computer is an unaccepted possible UX, not proof of routing. |
| Owner's later continuation order in the Codex conversation | The numbered requirements, delivery order and boundaries below | Reproduced here so the pickup does not depend on that conversation or a local work order. Exact original message time is not established. |
| Owner's Claude-review outage clarification | AGENTS.md / CLAUDE.md, recorded 2026-10-11 | Claude PR advisory review is optional/down. Required CI and native independent review remain mandatory. |
| 2026-10-12 eight-row capability checkpoint | [Parity checkpoint](sagas/distributed-dev-env/verification/canary-result-2026-10-12.md#parity-acceptance-checkpoint) | Agent-authored acceptance tracker, not a new universal owner-approved pilot gate. Gemini was an unverified relay, not accepted provider scope. |
| Current Claude takeover request | This brief | Review, fix and improve the repo; use native phone questions for real decisions. No stopped experiment is reauthorized by changing drivers. |

The five owner outcomes remain:

1. Upgrade dev-env without killing running sessions.
2. Spread work across the cluster instead of straining one node.
3. Replace weak standing access with useful, scoped guardrails, including required
   Headlamp/hardware workflows. Omni remains Reader until a separate accepted scope.
4. Expose and manage live/stopped sessions and their authorized context through
   the existing API, with an eventual frontend using that API.
5. Attribute exact-model usage and cost; support local LLMs when GPUs are available.

The earlier R1–R7 list must be read with the later correction. A project supplies
repository identities and common rules, not a shared checkout. Identical paths in
different pods need not identify the same filesystem. Git rescue does not retain
provider transcripts or memory. A history read need not resume the exact native
conversation or spend another model turn. Frontend completion and every historical
parity row are not prerequisites for a smaller coherent owner pilot. Full v1
retirement remains a separate, explicitly approved milestone.

## Current owner requirements

1. Support many simultaneous sessions across Claude Code, Codex and local-model
   providers. Two sessions are a disposable pilot, not the capacity limit.
2. Each independent session needs its own bounded pod and complete failure
   boundary. One session exhausting memory must leave peers running. Separate
   tool/build executors do not isolate native chats sharing an app-server harness.
3. Keep provider homes, repositories, Git administration, task worktrees,
   credentials, enrollment and writable native state private.
4. Share versioned global/project/repository rules, connector configuration,
   session discovery, authorized live/stopped context, explicit task claims and
   durable attributed messages through the existing operator/API. No extra
   permanently reasoning coordinator service is required.
5. Preserve sessions during operator upgrades. Agent-image replacement needs its
   own safe idle/drain and retained-state contract; never roll busy peers to prove it.
6. Include the local provider in contracts now. Live inference, likely Qwen,
   waits for GPU readiness and respects household reservations. No backend/model
   acceptance is implied by a design or candidate model name.
7. Preserve subscription-backed Claude/Codex operation. Do not silently substitute
   paid APIs, clone enrollment or credentials, or create a second refresh owner.

For private repositories, reuse the existing catalog, clone, fetch and preservation
helpers. Bootstrap trusted declared repositories and safely refresh at startup
without shared-workspace preflight or locks. Preserve dirty/index state,
local-only commits, existing worktrees, operations in progress and ambiguous
authority. Record catalog/source/rule revisions. Immediately before every new
task, freshly fetch the selected repository and pin an immutable commit; startup
refresh is not a substitute. Resume preserves WIP. A private HOME containing the
public dev-env repository does not prove private GitHub authorization.

## What is actually delivered

This is saved, bounded evidence checked against repository/public records on
2026-10-12. It is not continuous observation of cluster state.

| Layer | Evidence and limit |
| --- | --- |
| Private admission/preparation source | Source [#178](https://github.com/thaynes43/dev-env/pull/178), v2.12.0 and [Ops #3767](https://github.com/thaynes43/haynes-ops/pull/3767) delivered the saved order's first assignment. Global private admission remains disabled. |
| Subsequent source | Catalog bootstrap, private roots/client guards, common-rule inputs, retention, directory, claims, inbox, managed context and provenance foundations are shipped. Designs 006–023 and the source-review map below identify them. Source/installed guards are not consumer acceptance. |
| Last accepted operator delivery | [Ops #3825](https://github.com/thaynes43/haynes-ops/pull/3825): source `cd966d17c8d1a2bca82c27cc01be4d9a11ae5256`, digest `cc03ae820a2c81e65af153c8f97eb6edb129c1158a0b8c51a3222df9e018e345`. Saved window accepted two Ready operator replicas and preservation of 11 peers; no continuous-current or live-provider pilot claim follows. |
| Installed task template | Signed agent v2.20.0, digest `823e015567fdb34951c4e6ce7e39891e3cc8a1e2d1d219bee127fe537cb31303`, template `2.20.0-ec26880d2a`. The template suffix is not the agentd source commit; actual embedded task source is `3aab27cebf66dcb44d8e50c02eddd00154e8c722`. |
| Latest agent publication | Source [#213](https://github.com/thaynes43/dev-env/pull/213), release [#214](https://github.com/thaynes43/dev-env/pull/214), signed v2.24.0 digest `36d26662ed9e3a61b88a6c7f4fd841295358fb072018853df79e19585385367c`. Ownership proof/inspector is unwired and refuses reservation. Published, not deployed; no private dispatch/recovery pass. |
| Bound local clients | D22 `0b7c8401…` and D23 `f136b205…` are accepted dev builds of exact merged sources, not globally installed or signed-client claims. Reuse their proofs; do not rebuild repeatedly. |
| Actual probe acceptance | Original Claude and Codex HTTP201 receipts, a pre-Codex complete Directory page, one Claude named read and one retained managed-result read. These accept transport/source identity only. Actual both-provider rules and the MVP remain unaccepted. |
| Current resume checkpoint | [#215](https://github.com/thaynes43/dev-env/pull/215) and [#216](https://github.com/thaynes43/dev-env/pull/216) are merged. #216 is `f6de1680a4a00a12d25bea5c43865d088c068db1`; native factual finding fixed, native R2 and required CI passed, available Claude advisory was error-only with no findings/verdict. |

*Update, Q-28, 2026-10-12: the "Actual probe acceptance" row above is the
original record. Actual rule loading is now accepted for both providers: the
Codex original under Q-27 and Claude canary 1012 under Q-28 each returned both
markers ([Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md)).
The MVP remains unaccepted.*

Read [installation](sagas/distributed-dev-env/verification/installation-2026-10-11.md),
[fixed activation](sagas/distributed-dev-env/verification/fixed-rule-activation-2026-10-11.md),
[result delivery](sagas/distributed-dev-env/verification/canary-result-2026-10-12.md),
[Claude result](sagas/distributed-dev-env/verification/first-original-canary-2026-10-12.md),
[Codex checkpoint](sagas/distributed-dev-env/verification/codex-fixed-rule-probe-2026-10-12.md),
and [budget source](sagas/distributed-dev-env/verification/private-budget-ownership-2026-10-12.md).
Later observations supersede older “unstarted”, “activation unmerged” and pin states.
Do not erase the old receipts or turn their original HOLD into a PASS.

## Exact runtime checkpoint: preserve these originals

| Provider | Original and consumed authority | Actual result / continuing uncertainty |
| --- | --- | --- |
| Claude | `rule-canary-claude-1011`; Session `private-canary-fc5935c175c6fc15f842b051bf85aa8d`; UID `45a614cb-f537-441e-a5c1-33b66a285521`; original201 at 05:28:22 UTC / 01:28:22 EDT. Create, named read and fixed result read are consumed. | Saved task Failed/exit1; retained 49-byte weekly-limit response. It does not prove instructions were absent. Served model, cost, physical stop, full native history and capture completeness remain unaccepted/Unknown. A reported reset time is not current readiness proof. |
| Codex | `rule-canary-codex-1011`; Session `private-canary-3bfe28755095977c9462c269cc24dee3`; UID `f0396ab3-1abf-41bf-be4e-93ce0e43608b`; original201 at 06:47:25 UTC / 02:47:25 EDT. Create is consumed; named/result reads are zero. | Native saved review `c3bcc13b` and Root `f189f079` accept original identity only. Executor, provider output, loaded rules, served model, quota, history, stop and usage/cost remain Unknown. Reader preparation is stopped, untested and inert under Q-27. |

*Update, 2026-10-12: the Codex row above is the original record. Under Q-27 the
reader correction passed, and the one named read and one result read ran. The
retained output returned both rule markers, so loaded common and repository rules
are accepted for the Codex original. Served model, usage, cost, full history,
physical stop, capture completeness, private GitHub authorization and a fresh
task pin stay Unknown or unaccepted. See the
[Codex original result record](sagas/distributed-dev-env/verification/codex-original-result-2026-10-12.md).*

*Update, Q-28, 2026-10-12: the Claude row above is the original record, and its
ID stays consumed. Under Q-28 a new Claude canary, `rule-canary-claude-1012`
(Session `private-canary-6f090716f22b84cb55321a14338ccf30`), ran one Create, one
named read and one result read. Its retained output returned both rule markers,
so loaded common and repository rules are accepted for Claude too. Served model,
usage, cost, full history, physical stop, capture completeness, private capture
acknowledgement, tool-use compliance, private GitHub authorization and a fresh
task pin stay Unknown or unaccepted. See the
[Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md).*

Global counters: Directory **5**; Claude Create **1**, Codex Create **1**;
Claude named/result **1 each**; Codex named/result **0 each**. Both permanent IDs
are consumed. Never repost, change key, rearm, replace, poll or clean up an original.
The Codex sequence passed 16 offline checks and independent review; it preserved
the one known Claude original on a complete fresh page before the conditional
Create. Create completed within 0.194240 seconds of page capture finish.
That page's Directory UID was `c7b87417-f221-4f8c-9de1-a86688979244`; source
generation was 1. Directory generation was not exposed; post-Create cardinality
remains Unknown. Configured private/bounded pods are not observed executor proof.

**Counters update, 2026-10-12.** The counters paragraph above is the original record. Under
Q-27, the one Codex named read and the one result read then ran, with no retries.
Counters are now Directory **5**; Claude Create **1**, Codex Create **1**; Claude
named/result **1 each**; Codex named/result **1 each**. The Codex output returned
both the common and the repository marker. The
[Codex original result record](sagas/distributed-dev-env/verification/codex-original-result-2026-10-12.md)
has the details and what remains Unknown.

**Counters update, Q-28, 2026-10-12.** The two counters paragraphs above are the
original records. Tom answered Q-28, "Yes, after fixes deploy (Recommended)".
After v2.24.1 deployed, one new Claude canary, `rule-canary-claude-1012`
(Session `private-canary-6f090716f22b84cb55321a14338ccf30`, UID
`de61708b-3dc5-45de-8a16-885c2bdc83a3`), ran one directory read, one Create, one
named read and one result read, with no retries. Counters are now Directory
**6**; Claude Create **2** (1011, 1012), Codex Create **1**; Claude named/result
**2 each**; Codex named/result **1 each**. All three permanent IDs are consumed.
Its output carried both markers. The
[Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md)
has the details.

**Counters update, Q-30, 2026-10-12.** The three counters paragraphs above are
the original records. Under Q-30, one Claude canary on the private Sigo alumni
project, `rule-canary-claude-sigo-1012` (Session
`private-canary-a1444f4131ec7281b5d1e3130c3b5ada`, UID
`430297d9-aa29-4332-9b3b-37095c1b5963`), ran one Create, one named read and one
result read, with no retries and no directory read. Counters are now Directory
**6**; Claude Create **3** (1011, 1012, sigo-1012), Codex Create **1**; Claude
named/result **3 each**; Codex named/result **1 each**. All four permanent IDs
are consumed. Root acceptance `dd5dee24` shows private GitHub authorization and a
fresh task pin. Catalog bootstrap ended `Partial` on a tracked symlink; #237
fixes it in source and its deploy is pending. A `Ready` bootstrap on a private
multi-repository project is still unaccepted. The
[Sigo private-repo canary record](sagas/distributed-dev-env/verification/sigo-private-canary-2026-10-12.md)
has the details.

## Next bounded work and the six-step roadmap

1. **Finish [#173](https://github.com/thaynes43/dev-env/issues/173).** Bootstrap and
   roots/client/common-rule foundations are delivered. Review them for bugs and
   finish actual provider acceptance. The immediate blocked unit is the Codex
   reader preparation, not another Create. Resolve Q-27's existing escalation;
   if explicitly allowed, make the one final offline correction, test changed
   predicates, obtain independent native review, then separately bind one named
   read and one fixed result read to the accepted actual original. Do not use
   Claude-only adapters unchanged, fabricate dynamic provenance or chain reads.
   Claude's quota result requires a separately authorized future trial if needed;
   its consumed ID cannot be retried. Genuine private GitHub access/worktrees and
   actual loaded common/repository rules remain acceptance gaps.
   *Update, 2026-10-12: the Codex reader unit is done. Q-27 was answered, the one
   correction passed, and both Codex reads ran once and returned both markers
   ([result record](sagas/distributed-dev-env/verification/codex-original-result-2026-10-12.md)).
   What remains for #173: Claude's rule loading (unproven), genuine private
   GitHub access and worktrees, and a fresh task pin. No further Codex read,
   retry or Create is authorized.*
   *Update, Q-28, 2026-10-12: Claude's rule loading is accepted too. After
   v2.24.1 deployed, the one new Claude canary returned both markers
   ([Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md)),
   so rule loading is accepted for both providers. Still open for #173:
   genuine private GitHub authorization and worktrees, a fresh task pin, and
   #173's other acceptance items.
   No further Claude or Codex read, retry or Create is authorized.*
   *Update, Q-30, 2026-10-12: Tom chose the existing private Sigo alumni
   repositories for the private-repo proof. One Claude canary,
   `rule-canary-claude-sigo-1012`, was prepared in haynes-ops #3845 and run the
   same day. Its bootstrap ended `Partial` on a tracked symlink, so it was not
   accepted; #237 fixes the proof (HANDOFF, Q-30 section)
   ([DESIGN-011 Q-30 amendment](sagas/distributed-dev-env/designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173)).*
   *Update, Q-30 Root acceptance, 2026-10-12: the Root accepted part of that run
   (`dd5dee24`). Private GitHub authorization and a fresh task pin are shown.
   Still open for #173: a `Ready` bootstrap on a private multi-repository
   project, after #237 deploys and with a new canary Tom approves, and #173's
   other acceptance items
   ([Sigo private-repo canary record](sagas/distributed-dev-env/verification/sigo-private-canary-2026-10-12.md)).*
2. **[#174](https://github.com/thaynes43/dev-env/issues/174): history and discovery.**
   Finish provider-history retention, durable live/stopped/archived directory and
   authorized stopped-context reads. Prove reads without starting a model turn
   or reviving the executor, and confirm retained history before home deletion.
   Partial managed output and Git rescue are insufficient for full native history.
3. **[#175](https://github.com/thaynes43/dev-env/issues/175): coordination.** Finish
   explicit task claims and durable messaging/offline inbox. Prove cross-parent
   contention refusal, ownership/provenance, recipient-stop durability and ACK
   behavior with actual authorized consumers. Do not infer semantic deduplication
   from arbitrary prompt text or transfer ownership on an uncertain stop.
4. **[#154](https://github.com/thaynes43/dev-env/issues/154) and
   [#160](https://github.com/thaynes43/dev-env/issues/160): budgets and recovery.**
   Wire whole-task budgets/private dispatch after preservation/authority contracts
   pass; enforce inherited counters and stop/escalation. Prove native recovery
   with retained WIP and truthful old-executor ownership. The unwired v2.24 proof
   and #170 CODEX_HOME source fix do not satisfy runtime acceptance.
5. **[#176](https://github.com/thaynes43/dev-env/issues/176) plus separate guarded
   required-access slices.** Prove exact served-model usage/cost provenance and
   avoid parent/child double counting. Distinguish subscription consumption,
   CLI equivalent cost, estimates and API billing; absent values are Unknown.
   Provide useful scoped access and prove use/refusal/expiry/revocation before
   retiring existing required workflows. No new standing privilege follows.
6. **Disposable owner pilot.** Demonstrate concurrent isolated providers, actual
   placement, loaded common rules, fresh private task worktrees, discovery,
   claims/messages, authorized stopped history and operator-upgrade preservation.
   Terminate one disposable session and show peers remain usable. Never use
   memory stress or valuable history. Local inference waits for GPU readiness;
   full frontend and v1 retirement retain separate acceptance/approval gates.

Before each implementation unit, publish its concrete contract, affected paths
and acceptance for one reviewable PR within 30 minutes. Implement, run meaningful
focused checks, obtain native independent review, handle findings and self-squash
merge when required CI passes. Deliver releases/signed artifacts/GitOps where
needed, then verify the affected deployment while preserving peers. Keep source,
publication, deployment and operational acceptance separate. Do not close parent
issues or claim a working MVP on partial/source-only delivery.

## Codex routing gate: Q-24 remains unresolved

Reuse the [finite pinned audit](sagas/distributed-dev-env/research/R-05-codex-native-routing.md)
and [DESIGN-006](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md).
Pinned Codex 0.160.1 native chats share one app-server's in-process harness.
Separate tool/build executors do not provide complete native-session isolation.
Retaining a computer name through sequential pod replacement does not prove
concurrent shared-enrollment routing.

The unestablished contract is one paired entry routing complete native chats to
independent app-server pods, including listing/resume, events, questions/answers,
history and ownership. Do not clone credentials/enrollment to test it. The pending
owner choice is separate persistent hosts/pairings versus a coordinator/worker
arrangement that retains the coordinator's shared native failure boundary.
Do not commit fleet UX, treat a peer-relayed owner quote as authority, or repeat
the already-issued Q-24 question. Check the owner's actual answer first; keep
the decision pending when no authoritative answer is available. The headless
Codex probe does not resolve native fleet routing.

## Failed or stopped routes: do not start these again

| Route | Preserved result and next boundary |
| --- | --- |
| Old private admission/preparation assignment | #178/v2.12/Ops #3767 delivered. Review it, do not implement it again. |
| Shared Git/RWX/NFS investigations | #130 is NOT_PLANNED; Ops #3737 closed unmerged. ADR-003 removed this prerequisite. No benchmark, storage retry, NAS diagnosis or repurposed old pilot. |
| Failed native recovery fixture | Original failed fixtures, ledgers and uncertain lifecycle receipts remain preserved. #170 is source only. No replacement, cleanup or claimed runtime pass. |
| Q-25 local schema-host DNS route | Stopped after three internal retries. Owner said the question was unclear. Required hosted CI and saved real-API schema evidence can be used; no new DNS investigation/retry is authorized. |
| Q-26 upgrade-checker drafts | Owner's one final R4 correction completed and Ops #3824 activation was accepted. No R5, new identity/grant, blanket metadata filtering or retry of that stopped observer. |
| Ops #3825 post-observation exact Flux revision HOLD | Original HOLD is retained. Saved running-image/preservation evidence plus finite direct-child source-equivalence review supported separate acceptance. Do not erase the HOLD or run another observer to force an exact revision match. |
| Directory TLS and expired windows | One initial TLS failure and two expired complete pages are preserved. Correct explicit public CA was accepted; freshness stayed 300 seconds. Do not backdate, extend or reuse an expired page. Counters remain 5, not zero. |
| Claude quota result | Original and reads are consumed. No quota retry, new key, auth repair, provider/model fallback, paid API or destructive cleanup. Quota output does not establish missing rules or present-day readiness. |
| Codex result-reader preparation / Q-27 | Three logical manifest-indexing failures stopped the unit. Drafts are untested/inert. No fourth attempt, reader execution or counter reset without an explicit bounded owner ruling. |

The Q-27 failures were: a historical source leaf outside the orders-only guard;
a relative source description; then a 41,412,165-byte historical receipt exceeding
the 32 MiB file cap. Stop receipt `dae50690` misattributed the last combined
assertion to a 512-ref limit; correction `0b6eeed4` establishes the actual file-cap
refusal. A 512-reference crossing was not proved; oversized contents were not read.
Preserve both receipts. Do not widen caps or recursively expand the entire
historical evidence graph. A proposed correction uses a finite explicit set of
accepted direct proofs/current source guards and reuses unchanged reviewed methods.

Q-27's native question was emitted; phone delivery and an owner answer are
unverified. Its exact proposal is one final offline reader correction, then tests
and independent review before any separately authorized reads. The current
handoff request does not answer that question. Check native owner replies first
and retain one outstanding escalation. Claude may establish delivery of that
pending decision through its supported native phone prompt; no answer means stop.

**Ruling, 2026-10-12.** Tom answered Q-27 through the Claude native prompt:
"Fix once, then read (Recommended)". One offline correction with a fixed list of
already accepted evidence, focused tests and independent review is allowed. After
that, exactly one named read and one result read of the Codex original are
allowed. There are no retries and no counter reset. The
[Codex probe record](sagas/distributed-dev-env/verification/codex-fixed-rule-probe-2026-10-12.md#q-27-one-final-reader-preparation-correction)
holds the ruling.

## Review and improve the shipped work

Treat Codex's implementations, docs and proof scripts as fallible. Review concrete
data/authority/preservation behavior rather than repeating unchanged full audits.

| Review unit | Source and design entry points | What to examine |
| --- | --- | --- |
| Private catalog/bootstrap/roots | `internal/agentd/projects.go`, `daemon.go`, `settings.go`, `project_authority.go`, `workspace.go`; `cmd/agentd`; `internal/projectcatalog`; designs 005–007 | Trusted authority, per-pod paths, dirty/index/worktree preservation, in-progress Git operations, startup versus selected-task fetch/pin, resume/WIP, accurate revision reporting. |
| Common rules and provider integration | `internal/commonrules`, provider config/render/run paths in `internal/agentd`; designs 009/011/015/016 | Correct private injection for both providers, immutable rule revisions, actual loaded acceptance, no prompt-supplied markers, no provider HOME/credential disclosure. |
| History/directory/claims/inbox | `internal/sessiondirectory`, `internal/apiserver`, `api/v1alpha1`; designs 008/010/013/014/016/017 | Authorization, original UID/spec binding, stopped/unknown lifecycle, bounded payloads, durable delivery/ACK, cross-parent contention, retention before archive. |
| Budget/native recovery/provenance | `internal/controller/private_taskbudget.go` and tests; task-budget/native paths; designs 002–004/018/019 | Stop proof, ownership races, inherited counters, unwired refusal, no duplicate executor, preserved WIP and truthful Unknown model/cost. |
| Once-only clients and runtime helpers | `internal/agentrun`, `internal/apiserver/canary_result.go`; designs 020–023; saved local helpers below | UTF-8/body limits before decode, unchanged caller identity, no redirects/retries/polls, original201 versus replay200, actual source/provenance, permanent fences and final freshness/expiry checks. |

This handoff update corrects the guide's old 2.13/2.14/empty-common-rule checkpoint
and labels DESIGN-021/022's superseded runtime states. Review remaining stale
guidance, including lower-HANDOFF “What happens first”/shared-RWX descriptions.
Label historical facts, link the latest acceptance and preserve original evidence. Do not modify an
Accepted ADR to rewrite history. Source-review findings should produce concrete
fixes or a reasoned answer on the PR; do not bypass tests or approval boundaries.

## Local evidence locations, if the original PVC is available

These are supporting artifacts, not the only place requirements are stored:

- `/home/dev/work/orders/dev-env-rule-canary-runtime-1011/`: original sequences,
  public-CA proofs, named/result receipts and Root acceptances.
- `sequenced-initial-codex-preparation-r1/`: frozen helper/contract/16-check campaign,
  native full review `55e27946`, saved result review `c3bcc13b`, original201 raw.
- `codex-post-original-read-preparation-r1/`: stopped named helper `6efcf4ec…`,
  result helper `80560088…`, null-gate proposals, exact deltas, stop and correction.
- Root Codex original acceptance `root-saved-first-codex-original201-receipt-acceptance-r1.json`
  (`f189f079…`); pending question `root-q27-reader-final-correction-question-pending-r1.json`
  (`9a3a7a1d…`) in that orders directory.
- `/home/dev/work/orders/dev-env-codex-original-checkpoint-1012/`: merged #216,
  native R1/R2, required CI and error-only advisory receipts.
- `/home/dev/work/orders/dev-env-private-budget-release-1012/`: signed v2.24
  publication/native/Root acceptance. No deployment follows from these files.
- `/home/dev/work/orders/dev-env-claude-handoff-1012/source-evidence-audit-r1/`:
  read-only native audit `9b1246ab…`, binding 56 source targets and seven repo
  verification records to the starting `f6de168` revision. Use its source map
  to focus review; it is not a runtime acceptance or another execution grant.
- `/home/dev/work/orders/dev-env-next-build-2026-10-10.md`: historical order;
  its scope/boundaries are useful, but its first source assignment is completed.

Read declared, necessary bounded files. Do not dump provider homes, credentials,
private transcripts or oversized historical snapshots. Missing local evidence
does not authorize fabricated verdicts, recreated fixtures or loosened guards;
record the gap and stop the dependent runtime action. Independent source review
and authorized bug fixes can continue.

## Working rules for the Claude driver

Use native Claude delegation and the current pod/repo model rules. Hard work,
storage/auth/concurrency reviews and user-visible work use exact
`claude-opus-5-5` at `xhigh`; routine exploration/code/tests can use exact
`claude-sonnet-5-5` at `high`. Do not use aliases, a cross-provider quota fallback
or Fable unless Tom asks for it by name. Check the Claude login with the safe
`claude-login-check` at session start; do not infer login health from failed
GitHub advisory reviews or repair authentication as a review prerequisite.

Questions go through Claude's native `AskUserQuestion`, one actionable question
at a time when a real decision arises, recommended option first. Record the
question and answer in the design/issue; verify actual phone delivery. A prose
question list or tool emission receipt is not proof of delivery. Operational
answers do not create privileged access grants.

Stop after 60 minutes without evidenced progress or three failures at the same
logical blocker, whichever comes first. Counters follow children, replacement
pods, methods and resumes. Preserve evidence/WIP/uncertain ownership, block
retries and replacement workers, and retain one pending owner escalation.
Do not count another script, heartbeat or tool call as progress.

No CPU burners, stress, busy loops, wide/looped tests or local image builds.
Use fake clocks, focused meaningful tests, `nice -n 19`, two-CPU/two-package Go
limits and resource limits on cluster fixtures. No v1 restart/cutover, elevated
permissions, credential/enrollment copies or destructive cleanup. No memory
stress for isolation acceptance. Keep private addresses/secrets/transcripts out
of this public repo.

Self-squash-merge reviewed ready PRs once `CI - Success` passes the exact head.
Claude GitHub advisory reviews are optional/down; read available findings,
fix or concretely answer them, but do not retry/repair that service or substitute
a skipped/error-only job for a verdict. Native independent review remains required.

Apply the held-draft exception to haynes-ops changes under
`kubernetes/main/apps/dev/dev-env/app/resources/**` or any change that would
restart the current dev-env pod. Complete and review the change, explain the
restart, leave it held for Tom's natural break; do not merge it yourself.
Normal source and safe operator-only GitOps delivery remain authorized.

Keep contracts, remaining acceptance, owner rulings and failure counters durable
in the saga, relevant issues, #91 resume index and HANDOFF. Report built,
published, deployed and accepted separately. Finish the bounded increment;
preserve unresolved decisions as actionable issues instead of a chat-only tail.
