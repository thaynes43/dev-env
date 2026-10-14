# Claude continuation, 2026-10-13: dev-env v2 after release 2.27.0

Prepared 2026-10-13 UTC from `origin/main` at `c20fafd` (release 2.27.0, #253),
by the Claude coordinator session `task-dev-env-1012-105043` (2026-10-12 10:50
EDT to 2026-10-13). It replaces the
[2026-10-12 brief](CLAUDE-RESUME-2026-10-12.md) for current state, rulings and
next work. That brief still holds the requirement history, the failed and
stopped routes and the source-review map; read those sections there. This brief
grants no runtime permission, retry budget or credential access.

## Your assignment

Continue dev-env v2 toward Tom's five outcomes in bounded, reviewed units. Take
the [next work](#next-work-in-order) in order, one Opus subagent unit
(`claude-opus-5-5`) at a time, with Sonnet (`claude-sonnet-5-5`) for routine
work (the [pacing ruling](#rulings-q-24-to-q-32-and-pacing)). Review what earlier units
shipped; merged code and its tests do not prove a working MVP. Keep source,
release, deploy and acceptance separate in every record. Do not write another
general architecture plan.

## How to start

1. Run `claude-login-check`. On exit 1 or 2, tell Tom first.
2. Run `declare-activity list`. Honor every other agent's scope; never end
   another agent's activity.
3. `git -C ~/repos/dev-env fetch origin`, then a fresh worktree:
   `git -C ~/repos/dev-env worktree add ~/work/<task> -b agent/<task> origin/main`.
   Canonical clones are fetch-only. Old worktrees are evidence: do not reuse,
   prune or repair them. haynes-ops changes get their own fresh worktree.
4. Read, in this order:
   - this brief;
   - [CLAUDE.md](../CLAUDE.md);
   - [HANDOFF](HANDOFF.md), the top entries down to "Claude pickup and
     controlling roadmap — 2026-10-12";
   - [ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md),
     [ADR-004](sagas/distributed-dev-env/adrs/004-coordination-record-retention.md),
     [ADR-005](sagas/distributed-dev-env/adrs/005-private-home-expiry.md) and
     [ADR-006](sagas/distributed-dev-env/adrs/006-private-home-expiry-corrections.md);
   - [DESIGN-024](sagas/distributed-dev-env/designs/024-coordination-record-retention.md)
     and [DESIGN-025](sagas/distributed-dev-env/designs/025-private-home-expiry.md)
     (sections 12 to 16 before H7);
   - issues [#91](https://github.com/thaynes43/dev-env/issues/91) (the resume
     index), [#173](https://github.com/thaynes43/dev-env/issues/173),
     [#174](https://github.com/thaynes43/dev-env/issues/174),
     [#175](https://github.com/thaynes43/dev-env/issues/175) and
     [#176](https://github.com/thaynes43/dev-env/issues/176).
5. Before you state anything below as current, re-check it with one read-only
   command (`gh pr view`, `git log`, `kubectl get`).

## Owner requirements and the five outcomes

Unchanged since 2026-10-12. Read them in the earlier brief:
[requirement history](CLAUDE-RESUME-2026-10-12.md#requirement-history-which-lists-still-control),
the five outcomes under it, and
[current owner requirements](CLAUDE-RESUME-2026-10-12.md#current-owner-requirements).
The [failed or stopped routes](CLAUDE-RESUME-2026-10-12.md#failed-or-stopped-routes-do-not-start-these-again)
stay stopped.

## Deployed state

Read with `kubectl get` and `gh pr view` at about 11:40 UTC on 2026-10-13.

| Part | Now | Delivered by |
|---|---|---|
| Operator | `ghcr.io/thaynes43/dev-env-operator:sha-2221b83@sha256:fd8bdecaf2f68341ada2e53f39fa06e11b8b35416b88918fccb0c9eac5a7d779`, 2/2 Ready. Code equals v2.27.0; the binary reports `v2.26.0-2-g2221b83`. Memory limit 256Mi, no `GOMEMLIMIT` yet | haynes-ops #3886 |
| AgentSession CRD | generation 13, with `privateHistory.expiry` and `expiryClearedAt` | haynes-ops #3885 |
| Task template | revision `2.27.0-e83cacfe3e`, agent `ghcr.io/thaynes43/dev-env:2.27.0@sha256:b635dd8183c5f8aa16281e09dfc061bd3694d7a00e47a34f5563d0536ac91718` | haynes-ops #3887 |
| Shelf (`dev-agents/dev-env-shelf`) | agent 2.9.1 (`a975de7d…`), `RollingUpdate`, no archive volume | H7 changes it; Renovate haynes-ops #3710 is held for H7 |
| Broker, keeper | broker `sha-eeb15e3` 2/2; keeper `sha-257287a` 1/1; unchanged this session | |
| Flags on | `--enable-private-terminal-record=true` (H2), `--enable-private-history-retention`, `--enable-retained-common-rules`, `--enable-private-project-canaries`, `--enable-coordinator-callers`, `--enable-managed-codex-tasks`, `--enable-session-directory`, `--enable-task-claims`, `--enable-published-contexts` (payload reads for `dev-env` and `sigo-alumni`), `--enable-durable-inbox`, `--enable-task-budgets` (the fixture only) and `--enable-native-lifecycle-fixture`. The operator's live args are authoritative | |
| Flags off | `--enable-private-project-tasks=false` (global private admission). Absent, so off: `--enable-private-executor-stop`, `--enable-home-history-archive`, `--enable-history-archive-reads`, `--enable-home-expiry-delete` | |
| Canary policy | `rule-canary-claude-sigo-1012b` and `rule-canary-codex-1011`, both consumed | haynes-ops #3855 |
| Alerts | `DevEnvRescueFailed`, `DevEnvOperatorMetricsAbsent`, `DevEnvCoordinationRecordsNearLimit` (DESIGN-024 R1) | haynes-ops #3854 |
| Latest release | v2.27.0 (#253), signed agent and operator images | |

Two failed Jobs are kept as evidence and keep `KubeJobFailed` firing:
`dev-agents/dev-env-private-project-roots-check-1011` and
`dev-agents/dev-env-project-bootstrap-check-1011`. Leave them.

## Counters and consumed IDs

Directory **6**. Claude Create **4** (1011, 1012, sigo-1012, sigo-1012b), named
**4**, result **4**. Codex Create **1**, named **1**, result **1**. All five
canary IDs are consumed: `rule-canary-claude-1011`, `rule-canary-codex-1011`,
`rule-canary-claude-1012`, `rule-canary-claude-sigo-1012` and
`rule-canary-claude-sigo-1012b`. Never reuse, rearm, retry or clean up one.

The five retained AgentSessions in `dev-agents`. Canary originals never expire
(ADR-005), so none of them can serve H8.

| Session | UID | Canary | Phase and record |
|---|---|---|---|
| `private-canary-fc5935c175c6fc15f842b051bf85aa8d` | `45a614cb-f537-441e-a5c1-33b66a285521` | claude-1011 | `Pending`, `executorMissed` (pod lost in the talosw02 reboot, about 18:14Z 2026-10-12) |
| `private-canary-3bfe28755095977c9462c269cc24dee3` | `f0396ab3-1abf-41bf-be4e-93ce0e43608b` | codex-1011 | `Pending`, `executorMissed` (same reboot) |
| `private-canary-6f090716f22b84cb55321a14338ccf30` | `de61708b-3dc5-45de-8a16-885c2bdc83a3` | claude-1012 | `Failed`, task `Succeeded`, `executorTerminal` |
| `private-canary-a1444f4131ec7281b5d1e3130c3b5ada` | `430297d9-aa29-4332-9b3b-37095c1b5963` | claude-sigo-1012 | `Failed`, task `Succeeded`, `executorTerminal` |
| `private-canary-eb9d486a612e27fd2b52d27f675512be` | `454d12a2-60bc-4c4e-95c5-bb4b26428a65` | claude-sigo-1012b | `Failed`, task `Succeeded`, `executorTerminal` |

Next free IDs: Q-38, D-93, ADR-008, DESIGN-026, README decision 74. Each new
question takes the next free Q id when it is asked; none is reserved.

D-92 / README 73 records the relayed owner clarification: all Tom's trusted
dev-env agents may read shared history. Per-agent denied-read isolation is not
a parity gate. [Requirements and bounded plan](sagas/distributed-dev-env/requirements/2026-10-13-shared-agent-history.md)
retain existing authentication/trust membership, credential exclusion, integrity,
write/delete protections, public/anonymous/external refusal and unchanged v1.
Root did not directly observe the original owner turn; no question ID was used.

Q-37/D-91 is answered through the source Mac conversation and consumed.
The [Retain acceptance](sagas/distributed-dev-env/verification/h7-retain-acceptance-2026-10-13.md)
records the sole reviewed client launch: GET/PATCH/GET all 200, exit 0, same
bound history PV and claim with Retain at 19:40:32 EDT (23:40:32.254177 UTC).
Root accepted that client postcondition and the independent saved-evidence
audit without another PV GET. The original owner turn was not directly exposed
to Root; the Mac preserved and corroborated the approval relay. No second
launch, permanent right or later H7 activation follows. See
[DESIGN-025 section 22](sagas/distributed-dev-env/designs/025-private-home-expiry.md#22-one-time-pv-retain-q-37-d-91).
The [identity mapping checkpoint](sagas/distributed-dev-env/verification/h7-personal-identity-mapping-2026-10-13.md)
adds current Omni metadata and pinned mapping evidence; personal authenticated
request and phone-path acceptance remain unobserved.

**H7 pickup update, 2026-10-13:** the
[read-only prerequisite checkpoint](sagas/distributed-dev-env/verification/h7-prerequisites-2026-10-13.md)
is recorded. Tom answered Q-33 directly in this chat: his personal Kubernetes
account is permanent; Headlamp retires after full v2 adoption. The original native
prompt never reached him. Q-34 asked for a workstation identity check; Tom cannot
run it from his phone and directed the coordinator to continue. Withdraw that
request and use accessible read-only evidence. Only the identity-dependent policy
waits for a verified username; the unmounted history PVC can proceed. No
haynes-ops change had been made at this checkpoint. Plan 11 F4 and both
DESIGN-025 cold resource measurements remain prerequisites before archive
activation, including the 2 GiB export/put/verify under hold/shelf limits.

## Rulings Q-24 to Q-32 and pacing

Receipts are under `/home/dev/work/orders/` (local PVC, not in git). The
[HANDOFF rulings table](HANDOFF.md#rulings) has the full text. Q-25 and Q-26
predate this session; their rows point there.

| Id | Answer | Receipt | Meaning |
|---|---|---|---|
| Q-24 | **No answer** | none | Codex native routing for the MVP stays undecided. See [question 3](#questions-for-tom-one-at-a-time). |
| Q-25 | No decision needed | see HANDOFF | The local schema-host DNS route stopped after three attempts; no retry. [HANDOFF rulings](HANDOFF.md#rulings). |
| Q-26 | "Allow one correction" (2026-10-11) | see HANDOFF | One final R4 checker correction; Ops #3824 activation accepted; no further retry. [HANDOFF rulings](HANDOFF.md#rulings). |
| Q-27 | "Fix once, then read" | `dev-env-rule-canary-runtime-1011/root-q27-owner-answer-r1.json` (`eaf4e677…`) | One reader correction, then one named and one result read of the Codex original. Done: both markers (#227). |
| Q-28 | "Yes, after fixes deploy" | `dev-env-claude-canary-2-1012/root-q28-owner-answer-r1.json` (`daadc6b5…`) | One new Claude canary after 2.24.1. Done: both markers (#232). |
| Q-29 | "Archive, then delete" | `dev-env-coordination-retention-1012/root-q29-owner-answer-r1.json` (`16346a60…`) | Coordination records archive, then delete, 30 days after their Sessions are proved absent (ADR-004, DESIGN-024, #230). |
| Q-30 | free text: use the Sigo alumni repos | `dev-env-private-repo-canary-1012/root-q30-owner-answer-r1.json` (`14903318…`) | Private-repo proof on project `sigo-alumni`. First run `Partial` (#234, #240). |
| Q-31 | "Archive, then delete" | `dev-env-coordination-retention-1012/root-q31-owner-answer-r1.json` (`03255c15…`) | A private HOME is archived, then deleted, 30 days after its executor's recorded stop (ADR-005, DESIGN-025, #233). ADR-006 (#235): only a reap stops a private executor. |
| Q-32 | "Yes, after deploy" | `dev-env-private-repo-canary-1012/root-q32-owner-answer-r1.json` (`48ab104f…`) | One Sigo rerun after 2.25. Done: all seven DESIGN-011 items hold (#247). |
| Q-33 | Personal Kubernetes account remains; Headlamp retires after full v2 adoption | `dev-env-h7-personal-identity-1013/root-q33-owner-answer-r1.json` (`da28de08…`) | Direct owner answer in this chat; exact username still needs evidence. No shared Headlamp owner exception follows. |
| Q-34 | Cannot run a workstation check from the phone; keep working | `dev-env-h7-personal-identity-1013/root-q34-owner-direction-r1.json` (`ac078e5d…`) | Withdraw the workstation request. Discover the identity from accessible evidence and continue independent H7 prerequisites. |
| Q-35 | Relay: "v1 must remain the same until it’s decommissioned after the MVP has been accepted"; direct asynchronous confirmation at 21:33 UTC: "Keep v1 trusted during migration (Recommended)" | `dev-env-h7-archive-guard-1013/q35-relayed-answer-r1.json` (`b8b96a86…`); `q35-direct-owner-confirmation-r1.json` (`d7728c57…`) | Original relayed turn remains unobserved; direct confirmation observed. ADR-007/D-89 preserves v1 current behavior and access; no new authority, activation or automatic decommissioning. Q-36 intact. |
| Q-36 | "Go for it" for holding the new reader and using existing Mac evidence | `dev-env-h7-archive-guard-1013/q36-relayed-direction-r1.json` (`96ea0658…`) | Relayed operational ruling D-90; original owner turn/native delivery unobserved. Reader already deployed and failed before relay arrived. Hold further reader access/deployment; no undo/retry/activation. |
| Q-37 | "Yes" to the exact one-time history-PV Delete-to-Retain scope, through the source Mac conversation | `dev-env-h7-archive-guard-1013/q37-owner-approval-mac-r1.json` (`c86616cb…`) | Source-corroborated relay, original user turn not directly exposed to Root. D-91/README 72; one reviewed client launch verified Retain, attempt consumed. No new right or activation. |
| Pacing | "Slow down" (asked 04:00Z, answered 11:02Z, 2026-10-13) | `dev-env-claude-handoff-1013/root-pacing-owner-answer-r1.json` (`a70a80cc…`) | One Opus subagent unit (`claude-opus-5-5`) at a time, Sonnet (`claude-sonnet-5-5`) for routine work, no parallel Opus waves until Tom says otherwise. README decision 69. |

## Delivered 2026-10-12 to 2026-10-13

- **Independent review of Codex's source**, six units (roots, rules,
  coordination, budget, history, usage and access): 29 findings, 27 confirmed,
  2 high. An interrupted project-root advance could strand the root (#220). The
  budget ledger's confirm read-back misreported accepted writes (#221). All
  fixed in #219, #220, #221, #222, #223, #224, #225 and #229; released as 2.24.1
  (#228).
- **Rule loading accepted for both providers.** Codex original under Q-27
  (#227); Claude canary 1012 under Q-28 (#232).
- **Private repositories (#173).** Q-30 canary on `sigo-alumni`: private GitHub
  authorization and a fresh pin, bootstrap `Partial` (#234, #240). Fixes: S1
  accepts tracked symlinks safely (#237); S2 adds privacy-safe bootstrap reason
  codes to status (#239). Q-32 rerun: all seven items hold, bootstrap `Ready`,
  pin `c5da46c2` equal to the tip read before Create (#247).
- **Coordination retention (#175).** ADR-004 and DESIGN-024 (#230). R1, the
  `DevEnvCoordinationRecordsNearLimit` alert, is deployed. R2 to R7 are not built.
- **Private HOME expiry (#174).** ADR-005 and DESIGN-025 (#233), corrected by
  ADR-006 (#235). H1 terminal record (#236); H2 deployed with the record on;
  H3 reap-only stop request (#244); H4 agentd history export and archive
  (#246); H5 expiry up to `Archived` (#251); H6 `/v1/history` reads,
  `agent-run history` and the guarded delete (#254). H3, H5 and H6 are in the
  installed operator with every flag off. H4's commands are in the task
  template's agent image; the shelf, which runs `history-archive`, still runs
  agent 2.9.1 until H7.
- **Usage (#176).** Provider-reported served model, tokens and CLI-equivalent
  cost, billing basis `Subscription`, `Unknown` when absent (#245). Keeping it
  after a reap is parked in #248.
- **Docs.** Stale guidance labelled and Q-27 recorded (#218); the 2.25.0 deploy
  (#241).
- **Releases and deploys**, each through reviewed haynes-ops PRs in the order
  CRD, operator, template, with declare-activity and peer checks before and
  after:

  | Release | dev-env PR | haynes-ops | Operator |
  |---|---|---|---|
  | 2.24.1 | #228 | #3841, #3842 (canary record 1011 to 1012) | `sha-538c4f6` |
  | 2.25.0 | #238 | #3852, #3854 (node and lease `get`, record on, R1) | `sha-388444d` |
  | Sigo records | | #3845 (catalog and canary), #3855 (sigo-1012b) | |
  | 2.26.0 | #249 | #3877, #3878, #3880 | `sha-20c710f` |
  | 2.27.0 | #253 | #3885, #3886, #3887 (post-check PASS) | `sha-2221b83` |

- **Housekeeping.** haynes-ops #3881: dev-env v2 image bumps are manual-only.
  Renovate safe bumps merged (#97, #98, #101, #117 to #120, #250); #242
  replaced #161. Codex bump: #100 must not merge as it is; #243 tracks the
  coordinated bump. #231 (maintenance-aware escalation, draft) got a scope
  review on the PR: about 12 to 18 PR units on top of unbuilt plan 10, best
  taken as a plan 10 caller-migration gate after the six-step roadmap.

## Next work, in order

**Current checkpoint, 20:18 EDT October 13:** the corrected existing-Mac
[capture-r2 is accepted](sagas/distributed-dev-env/verification/h7-mac-capture-r2-2026-10-13.md)
for its bounded configuration/pre-mount metadata window: Ready, 26 GETs all
200, nine complete lists/13 objects, five stable protected objects, zero
archive references under the reviewed predicates. No extra PV request,
mutation or retry. Installed admission/lookup behavior remains separate;
the held reader, failures and original canaries remain preserved.

The concrete D-92 history-only correction was identified before implementation:
existing exact configured API clients, live Sessions and configured Coordinators
may read shared history across projects, including projectless histories.
Authentication, live Session checks and publication/write/delete helpers stay
unchanged; omitted CLI list project selects the existing projectless bucket.
Release and review installation with current flags preserved. Then use one
approved private history sample and positive peer retrieval with
matching digest; retain F4, resource, persistence/restore and H8 requirements.
No new architecture/review project or per-agent denied-read matrix.

**Current H7 evidence route:** [the reader-failure checkpoint](sagas/distributed-dev-env/verification/h7-reader-failure-and-existing-access-2026-10-13.md)
records #3904's installed read grants and failed single automatic Job. No
workload baseline was collected; the first policy's normalized spec mismatched
source, and actual timezone injection exceeded reviewed execution confinement.
Q-36/D-90 holds further reader access/deployment and uses existing authorized Mac
evidence. The relay arrived after deployment and does not authorize undo.
Independent review supports a historical nine-kind Mac baseline with zero
archive references and matching protected lineage. The reviewed source-only
correction merged in [dev-env #267](https://github.com/thaynes43/dev-env/pull/267),
`dad78a39`, at 22:14:26 UTC; its required CI, full CEL/transport fixtures and
Python 3.9/current hosted suites passed. The verified Mac tools are ready.
That exact source/artifact and invocation produced the accepted capture-r2
above. Installed request outcomes remain separate. No held-reader change, retry or new grant; preserve the
failed Job and all other retained runtime.

**H7 progress at 20:23 UTC, 2026-10-13:** the compatible guard merged in
[haynes-ops #3902](https://github.com/thaynes43/haynes-ops/pull/3902), `650f3a04`.
The [source and registered hook checkpoint](sagas/distributed-dev-env/verification/h7-compatible-guard-source-and-hook-2026-10-13.md)
records the exact-head 1,593 passing hosted tests/subtests, final independent
no-findings review, observed Kyverno GET permissions, Flux Ready/Healthy and the
generated Fail hook. Actual installed request acceptance remains pending;
capture-r2 above accepts the bounded pre-mount baseline. The separate reader's failed attempt
and existing-access hold are recorded above; use the current route.
Forbidden policy/ReplicationController reads are not empty or successful results.
No workstation commands or administrator-token borrowing are the verification path.

The unmounted history PVC was installed at 17:24 UTC
by haynes-ops #3898 (`a287a4f6`), Bound with no Pod references. Read the
[installed checkpoint](sagas/distributed-dev-env/verification/h7-archive-pvc-2026-10-13.md).
Its bound PV Retain is now accepted under the one-time, consumed Q-37/D-91
exception. Finish guard delivery
and baseline evidence before mounting it, under Q-35 and
[ADR-007](sagas/distributed-dev-env/adrs/007-preserve-v1-archive-trust-boundary.md).
F4 and both cold resource measurements remain gates.
The guard review found operator-account borrowing paths available to v1.
[Q-35](sagas/distributed-dev-env/designs/025-private-home-expiry.md#20-h7-archive-trust-boundary-q-35)
now has a relayed owner answer: v1 stays unchanged until decommissioned after
accepted MVP. The coordinator verified the oversight relay; the original owner
turn and native delivery were not observed. Preserve v1 behavior, configuration
and current access; add no privilege or automatic decommissioning.
[Issue #261](https://github.com/thaynes43/dev-env/issues/261) holds the evidence.
The initial read-only checkpoint performed no bypass probe. The
[measurement source setup](sagas/distributed-dev-env/verification/h7-measurement-setup-2026-10-13.md)
merged in [#262](https://github.com/thaynes43/dev-env/pull/262), `01c6bc99`;
the compatible guard source and registered hook now have the checkpoint above.
The [compatible guard contract](sagas/distributed-dev-env/verification/h7-compatible-guard-contract-2026-10-13.md)
now governs its source implementation, with fixed trusted infrastructure routes,
controller-chain proof, audited activation baseline and separate real webhook
acceptance. Small source checks establish neither cold measurement nor deployed
acceptance.

Each unit: publish its contract, one reviewed PR, focused tests, native
independent review, `CI - Success`, self-squash-merge. haynes-ops units also
need declare-activity and peer checks before and after.

1. **H7, haynes-ops** (DESIGN-025 section 12). None of it is under
   `apps/dev/dev-env/**`, so these PRs merge normally.
   - Read-only checks first: every identity that can update AgentSessions; the
     identity Tom's phone and Headlamp path uses; the archive guard's parity
     check (DESIGN-025 section 7): an RBAC listing of every identity with
     `pods/exec` or Job `create` in `dev-agents`, and that no v1 workflow or
     runbook execs into the shelf or runs a Job that mounts the claim.
   - PRs, in this order. The archive guard ships after its parity check and
     before the claim is mounted (section 12), so the shelf mount comes last:
     1. the `dev-env-history-archive` PVC (shelf-only, RWO), installed unmounted
        by #3898; PV Retain was verified under Q-37/D-91, whose sole delegated
        attempt is consumed;
     2. the archive guard, `dev-env-archive-guard` (it covers DESIGN-024's
        archive too, so it ships with whichever of H7 and R5 comes first),
        preserving v1 under ADR-007/Q-35; source merged in #3902 and hook
        registered; capture-r2 accepts bounded installed configuration/baseline,
        while actual admission/lookup behavior remains pending. The dedicated
        metadata audit failed and stays held under Q-36; do not repeat it;
     3. the shelf mount, `--history-archive`, `Recreate` and the 2.27.0 image
        (this absorbs #3710);
     4. the `dev-env-home-expiry-guard` admission policy, refusing every
        `system:` identity;
     5. the section 10 alerts except stalled (`DevEnvHomeExpiryNeedsOwner`,
        which includes `plan_too_large`; `DevEnvHistoryArchiveFilling`;
        `DevEnvPrivateExecutorEndedUnrecorded`;
        `DevEnvHomeExpiryOverrideInvalid`) and DESIGN-024's
        `DevEnvShelfUnavailable` for shelf absence;
     6. `GOMEMLIMIT` about 200MiB on the operator, then
        `TestHistoryRelayMeasure` once in a CPU-limited Job with the
        operator's limits, before the archive flag.
   - **Owner gates**, each one AskUserQuestion when it arises: Q-35 preserves
     v1 unchanged; the compatible guard must not remove its existing access,
     whether or not the scoped scan found a workflow. The owner-only history
     PV Retain step was completed by the one-time Q-37/D-91 delegation; do not
     repeat it. If
     anyone but Tom can update AgentSessions, the policy must match his identity.
     Q-33 settles the personal-account choice; Q-34 withdraws the workstation
     request. Discover the exact username from accessible evidence before that
     policy, without stopping independent prerequisites;
     just before the stop flag, a read-only list of Reaping private Sessions
     with a live executor, named in the PR so Tom can hold any (ask only if
     the list is not empty).
   - Then flags on, each its own PR, in this order:
     `--enable-private-executor-stop`, `--enable-home-history-archive`,
     `--enable-history-archive-reads`.
2. **H8, H9, H10.** H8 is the installed record of an archived read (#174's read
   acceptance). It needs a reaped, non-canary private Session. Global private
   admission is off and canaries never expire, so **owner gate**: Tom chooses
   the session source ([question 2](#questions-for-tom-one-at-a-time)). The
   Session also needs the 30-day grace or Tom's `release-now` annotation, which
   only he can set. H9 (delete on, `DevEnvHomeExpiryStalled`) only after H8
   merges. H10 is the installed record of the first expiry and of the ADR-004
   release 30 days later.
3. **Coordination retention R2 to R7** (#175, DESIGN-024): R2 to R4 source, R5
   and R6 haynes-ops (R5 shares the shelf's `Recreate` and the archive guard
   with H7), R7 the record. Then #175 consumer acceptance: claim contention and
   inbox ACK and durability with real consumers.
4. **#154 and #160: budgets and native recovery.** The v2.24 ownership proof
   is still unwired and refuses reservation. Wire it, then prove recovery with
   retained WIP.
5. **#173, what remains.** Two bounded pods compared for independent Git
   directories and worktrees; missing-repo bootstrap in an existing HOME; a
   failed fetch blocking new work; dirty and WIP state surviving an installed
   run; a runtime proof of the tracked-symlink case. A new canary for any of
   these is an **owner gate**. Settle
   [#226](https://github.com/thaynes43/dev-env/issues/226) first (item 8).
6. **MCP parity in v2 pods
   ([#257](https://github.com/thaynes43/dev-env/issues/257)).** Observed
   2026-10-13: all five retained Sessions are on the canary profiles, which
   carry no MCP tokens by design, and each reports boot step `mcp` warning
   `$CIGAR_JOURNAL_TOKEN is not set`. The `full`, `dev` and `ops` profiles wire
   `dev-env-cigar-secret` (and `full` and `dev` `dev-env-mcp-secret`); both
   ExternalSecrets report `SecretSynced`. No non-canary v2 session's `mcp`
   step has been seen yet, so parity is not shown. The UniFi token is in Tom's
   held draft haynes-ops #3884, which also edits `templates.yaml`: coordinate
   merges with any template pin, and do not merge it yourself (it bounces the
   v1 pod).
7. **#176 acceptance and guarded access (roadmap step 5, outcome 3).**
   Provider-reported usage (#245) is installed since 2.26.0 but not accepted:
   acceptance against a real provider is separate (#245's body). The guarded
   required-access slices are not started: useful scoped access, with use,
   refusal, expiry and revocation proved before any required v1 workflow
   retires ([2026-10-12 roadmap](CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap),
   step 5).
8. **The rest.** [#226](https://github.com/thaynes43/dev-env/issues/226) needs
   a design decision: whether canary result reads keep the catalog
   ConfigMap's UID and resourceVersion as read authority or compare content
   only. Settle it before the next canary's result read (items 2 and 5). #248
   (usage after reap); #243 (Codex bump; not #100 as is); dev-env Renovate
   #252 and #255; Q-24 and global private admission (owner); roadmap step 6,
   the disposable owner pilot.

## Standing boundaries

- No canary reuse, rearm or retry. A new canary needs Tom's answer and a new ID.
- No destructive cleanup of originals: retained Sessions, HOMEs, failed Jobs,
  receipts and old worktrees stay. New receipts are new files.
- Honor every other agent's declare-activity scope. Wait for it to end; never
  end it yourself. The Q-32 Create waited out a Ceph and `gasha01` window.
- Freeze for canary result reads: a result read re-checks the catalog,
  templates, common rules and operator args, so change none of those paths
  between a canary's Create and its result read.
- Held drafts: a haynes-ops change under `kubernetes/main/apps/dev/dev-env/**`
  restarts the v1 pod and every session in it. Open it as a draft, explain
  why, and let Tom merge it.
- CPU: no burners, stress, busy loops or wide test runs. Focused tests, one at a
  time, through a shared `flock`, under `nice -n 19` with `GOMAXPROCS=2 -p 1`.
  Reuse the envtest assets. A CPU limit on every Job.
- Pacing: one Opus subagent unit (`claude-opus-5-5`) at a time, Sonnet
  (`claude-sonnet-5-5`) for routine work, no parallel Opus waves.
- Full model ids. No secrets, private addresses or transcripts in this public
  repo. No credential or enrollment copies, no paid-API fallback.
- Claude advisory review is optional, not a gate. It ran on most PRs on
  2026-10-12 and 2026-10-13, several with findings: read them, then fix each or
  answer it.
  Native independent review stays mandatory.

## Lessons

- An override flag such as `--request-timeout` makes `kubectl` in this pod skip
  its in-cluster config and dial `localhost:8080`. Bound time with a
  subprocess timeout instead.
- A workflow or agent "write" stage must say "do NOT merge". One merged before
  review (#233) and needed ADR-006 (#235).
- A canary's result read re-checks catalog, templates, common rules and
  operator args. Freeze those paths from Create to result.
- An undeclared node reboot (talosw02, about 18:14Z 2026-10-12) removed the two
  retained 1011 canary pods. A retained Pod is never a stop proof; only the
  terminal record is (ADR-006).
- The Claude advisory review was documented as down but ran on most PRs. Check
  the PR before you assume either way.
- Evidence and receipts go under `/home/dev/work/orders/<unit>/`, as new files.

## Local evidence

On the dev-env PVC under `/home/dev/work/orders/`. Supporting files only; the
repo records are the source of truth.

- `dev-env-rule-canary-runtime-1011/`: the Q-27 answer and the Codex reads.
- `dev-env-claude-canary-2-1012/`: Q-28 answer and the Claude 1012 canary.
- `dev-env-coordination-retention-1012/`: Q-29 and Q-31 answers.
- `dev-env-private-repo-canary-1012/`: Q-30 and Q-32 answers, the first Sigo run.
- `dev-env-sigo-rerun-1012/`: the rerun runner (r4), baseline, post-check and
  Root acceptance `a3aa0b7d`.
- `dev-env-deploy-2.24.1-1012/`, `dev-env-deploy-2.25-1012/`,
  `dev-env-deploy-2.26-1013/`, `dev-env-deploy-2.27-1013/`: release facts,
  baselines, post-checks and independent reviews.
- `dev-env-claude-handoff-1013/`: the pacing receipt.
- `dev-env-cli-trust-1012/q24-reported-owner-direction-unverified-r1.json`: a
  peer relay about Q-24, data only.

The six-unit review's findings are in the PR bodies of #219 to #225 and #229.

## Questions for Tom, one at a time

Ask each with AskUserQuestion when it arises, recommended option first. Check
its premise with a read-only command first, record it in the design or issue,
and fold the answer back as a dated ruling.

1. **H7 owner steps**, in order as they come up: Q-35 already preserves v1
   unchanged; do not re-ask the same migration choice or remove an unused v1
   permission on the strength of the parity scan. The remaining steps are the
   override policy's identity (personal account chosen in Q-33; discover the
   exact username from accessible evidence under Q-34); `Retain` on the
   archive PV; a hold for any Reaping private Session with a live executor
   (only if the check finds one).
2. **A private Session for H8 and the pilot.** H8 needs a reaped, non-canary
   private Session, and global private admission is off. Ask Tom how he wants
   one made, for example one bounded admission for a single disposable session
   that he then releases with `release-now`. Ask when H7's flags are on and H8
   is next, not before.
3. **Q-24, Codex native routing.** Still unanswered. The 2026-10-11 question
   went out through a native question tool whose phone delivery was never
   verified ([DESIGN-006 Q-24](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing)). A peer
   relay (2026-10-12, data only) reports a direction: bounded paired hosts,
   perhaps one per node, provided pairing is a one-time step. A peer-relayed
   owner quote is not authority (2026-10-12 brief). Do not repeat the old
   question. Keep Q-24 pending and build nothing that assumes an answer.
   *Recommendation of session `task-dev-env-1012-105043`, not an owner
   ruling:* when Codex routing becomes the next unit, ask one new question,
   under the next free Q id, that quotes the relay and asks Tom to confirm or
   correct it.
