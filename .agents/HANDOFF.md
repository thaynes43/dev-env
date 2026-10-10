# HANDOFF: start here

The front door for any agent that opens this repo, in the cluster's dev-env pod or on
Tom's own machine. Read this page, then [CLAUDE.md](../CLAUDE.md) (the rules), then
the saga. To start building, follow
[KICKOFF.md](sagas/distributed-dev-env/KICKOFF.md).

## Active delivery to an owner-testable v2, 2026-10-09

**Resumed checkpoint, 2026-10-09 America/New_York:** the owner refreshed usage
and released the temporary PC-visibility hold. A device test found asymmetric
project-list visibility: Windows-created remote projects appear on Windows,
MacBook and iPhone; MacBook-created projects are missing from Windows while
their chats remain accessible. Registering the existing remote folder from
Windows is a workaround. The working pod host was not restarted or re-paired.
The owner considers this minor; do not make it a v2 prerequisite.

Project preparation #153 is source-merged at `2e1c395`. Fresh keeper account
login succeeded and reports `Ready`, generation 1; native process-group absence
and private staging/reservation cleanup passed. Remote host pairing is a
separate pending step. Child-decision source is under review in #155 at
`417c929`; local test/lint/build/generated checks pass after teardown and exact
parent-scope fixes. Retained-host source is draft #157; native lifecycle,
passive readiness and finite fixtures are built, with typed stop-acknowledgment
review still in progress. Automatic Claude advisory delivery failed three times
and stopped, tracked in #158. One native owner question requests an explicit
bounded separate Claude review; do not retry or interpret silence as approval.
Workflow updates are draft #156. Required
[task-budget and escalation enforcement](sagas/distributed-dev-env/requirements/2026-10-09-task-budgets.md)
is tracked in #154: 60 minutes without progress OR three failed attempts at the
same blocker, whichever comes first, across agents/pods/resumes. It is unbuilt
and is now part of the first owner test.

External-storage comparison is read-only so far. The separate Proxmox Ceph
cluster has active CephFS filesystems and existing NFS clients; Kubernetes has
its RBD CSI driver but no exposed external CephFS class. A health warning and
one down OSD must be accounted for before a trial. The
[bounded NFS comparison](../docs/trials/2026-10-09-external-storage-comparison.md)
records export-access and external-telemetry preflight blockers; a fresh TCP
probe alone is insufficient. Resolve those before more Rook diagnosis. No new storage or host
runtime was provisioned. Draft haynes-ops #3693 proposes one exact external
manager TCP/9283 allowance; it is not deployed and does not prove endpoint
readiness or replace missing gateway observations. These facts supersede the dated `NeedsLogin` and
project-Job review status below; other runtime/cutover gates remain open.

Tom clarified that Codex must continue until v2 provides a workflow he can test.
The documentation checkpoint did not end that assignment. Read the
[active delivery milestone](handoffs/2026-10-09-testable-v2.md): existing native
apps/CLI, two distinct Codex hosts sharing projects, managed fresh-source tasks,
real project/repo rule loading, owner-safe resume/rescue and phone questions.
Source work proceeds disabled by default while #130's storage diagnosis and
acceptance are completed. V1 remains available; full parity and cutover gates
still apply. Codex owns this work rather than leaving it for another implementer.

**Actual keeper rollout, 2026-10-09 23:46Z:** signed source #148 and haynes-ops
#3687 delivered keeper-only DNS and login-helper corrections. Exact artifacts,
admission and bounded connectivity checks passed; PVE minting remains disabled.
The fresh native device login presented a challenge, then expired without
credential adoption. Process-group absence, staging cleanup and reservation
clearance were verified; auth status is `NeedsLogin`, protected peers stayed
unchanged, and the activity ended. The owner prompt has no reply and its code
has expired. Start a fresh ceremony when the owner is available, rather than
reusing that code or automatically repeating login attempts. Managed Codex
runtime sessions and the two remote hosts remain disabled.

Strict-stop #140, catalog/rules #145, private-home retention #147, project CLI
and trusted catalog checks #150, and child-scoped API/managed Codex #152 are
merged source units. The model-free project Job is under final source review;
durable child decisions and retained Codex host supervision remain in progress.
Node Yama policy prevents the syscall evidence prepared in #146. The separate
in-process Git-marker fixture #149/#151 passed hosted checks and publication at
source `0e0931ad`; independent signature/admission/telemetry checks remain before
a bounded storage observation. No new storage trial follows automatically from
these source changes. Resolve #130 and the independent real-client,
lifecycle, host and phone gates before claiming the owner-test milestone.

**Latest delivery checkpoint, 2026-10-09 22:13Z:** shared-workspace core #135
merged `316c03c`; keeper-owned Codex auth #138 merged `bcb2599` and bounded
single-refresh #143 merged `f810766`. These are source
units with runtime opt-ins still off. Strict executor-stop rescue #140 merged
`7b3ce4c`. Its private-home archive/reap is deliberately blocked: task-only rescue
does not preserve provider enrollment/history. A retained-home detach route is
still required before shared reap acceptance. Catalog/rule implementation follows
[D-79](../docs/shared-project-catalog.md); catalog primitives merged #145 `084b66a`;
management/provider integration is in progress. Managed Codex execution and two
retained coordinator hosts remain unbuilt. No running agent or v1 restart follows from
these source merges.

The [third storage diagnostic result](../docs/trials/2026-10-09-cephfs-cost-result.md)
is incomplete. Peer Git status timed out inside index refresh without CPU
throttling; its exact filesystem wait is unproved. Normal UID-safe Job cleanup,
GitOps claim/PV cleanup and a five-minute post-window completed; all seven
protected pods were preserved and activity ended. Missing required telemetry and
60–70s household sampling intervals keep household acceptance open. #139 fixes
interrupted measurement receipts source-only. The older two-trial/prepared
statements below are dated history; no further run launches automatically.

D-80's [first host route](../docs/codex-coordinator-hosts.md) defines two retained
Codex coordinators with read-only shared files and dedicated child-scoped API
identities. Build that caller boundary, managed Codex native launch/resume,
retained host supervision and actual pairing/phone acceptance under plans 04/11.
Native app threads remain on their selected host; forwarding is later work.
D-81's [private-home retention](../docs/shared-private-home-retention.md) must be
implemented before shared reap: verified task rescue does not export provider
state. V1 stays available; no runtime host, drain or cutover is accepted here.

## Workflow guide, revised projects and shipped freshness, 2026-10-09

Read the [workflow guide](../docs/workflow-guide.md) before further architecture
work. It now draws the user journeys first, with two rendered images and four
Mermaid diagrams, working quick starts, feature states, and cutover gates.
The built orchestrator is the operator API/controller; `agent-run` or an agent
is its requester. There is no separate requester-agent service. The planned
console is unbuilt; the normal management surface still needs workflow review.
Owner questions must arrive as native phone prompts, not get buried in comments
or documentation.

Tom supplied [project requirements R1–R6](sagas/distributed-dev-env/requirements/2026-10-09-project-roots.md)
and added R7: multiple Codex remote links across pods sharing workspace files.
Both providers use one project concept; R2 supersedes Q-20. The supplied v1
baseline is `~/codex/<project>`, outside swept `~/work` tasks. Manual anchors
exist; haynes-ops #3633 was open/unmerged at audit. Catalog/sync/add, generated
trust, both-provider project rules, task-rule propagation, cross-pod ownership
and shared workspace storage are not built. [Plan 11](sagas/distributed-dev-env/backlog/11-project-workspaces.md)
and D-74 require revision of plan 04's single-hub and D-15/D-22's private Git/task
assumptions. Do not implement a single replacement hub as if it satisfied R7.
The primary management UI and credential migration are not selected.

Read [Accepted ADR-002](sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md)
for the concrete topology and storage alternatives. Q-21 ratified dedicated RWX
workspace storage with private provider homes and explicit task ownership.
Expiry alone cannot fence a partitioned writer; uncertain ownership blocks
transfer and reap. Tom answered the structured Q-21 prompt on 2026-10-09:
"Accept ADR-002 and the bounded CephFS trial (Recommended)". The bounded trial
is authorized first; normal rollout requires the delivery gates. ADR-001 is
unchanged, with its affected storage/cloning decisions superseded by ADR-002.
Phone delivery remains unverified; an owner answer does not establish device
delivery, and the earlier probe explicitly had a desktop reply.

[Node trust](../docs/keeper-node-trust.md) is installed on all five PVE nodes.
Tom answered Q-22, "Delegate the trust installation to Codex" (D-76). Haynes-ops
#3647 (`9dabb4d5`) delivered only the saved public key through a bounded Job and
pipe to the existing accounts. Each guarded append preserved the original key
bytes and passed standing-key SSH/read-only PVE checks. #3651 (`b8f83ff9`) removed
the temporary delivery app; its Job/pods, child Kustomization and ExternalSecret
are gone. Owner-policy cleanup is corroborated by fresh Secret metadata: the
public target is absent while the keeper-only CA target remains present. Cleanup
checks used metadata only. Backups/receipts remain on the nodes. Seven protected pod identities,
images and restart counts were preserved; minting stays off, private CA keeper-only.
The [bounded CephFS trial record](../docs/trials/2026-10-09-cephfs-feasibility.md)
records two incomplete attempts, cleanup and positive latency signals. The first
harness startup defect is fixed; the corrected run exceeded the original 15s
peer Git status cap. [Issue #130](https://github.com/thaynes43/dev-env/issues/130)
holds the specific diagnosis and next bounded proposal. No third run or normal
workspace rollout follows automatically. Neither task advances composite parity
rows by itself; provider/rule/host/fencing and household acceptance remain open.
Source-only [#131](https://github.com/thaynes43/dev-env/pull/131), `191b8c9e`,
now preserves timed-out command measurements in failure receipts. Its finite
simulated checks passed; this revised helper has not run in the cluster and does
not resolve the slow Git operation.

The independent stale-start fix is **shipped**: dev-env #124 merged
`759f084f`, release #123 produced **2.9.1** at `b6c0f3c`, and publish-agent run
`37944933337` passed smoke and exact-tag signing verification. Haynes-ops #3636
merged `b6e858b5` and reconciled only agent templates/shelf. Both use:

```text
ghcr.io/thaynes43/dev-env:2.9.1@sha256:a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf
```

A CPU-limited worker Job ran the published `agentd` binary against local Git
fixtures: failed fresh fetch produced zero launches, creation used a pinned
commit across a moving ref, offline resume preserved WIP/conversation, and
stopped-rebase resume preserved detached HEAD and operation state. All four
passed in five seconds at the stubbed model-launch boundary. The admitted final
pod had no injected init, no credentials/token/PVC, and no model/API calls.
Both trial Jobs/pods were removed and both activity declarations ended.

At final runtime verification `2026-10-09T14:52:53Z`, templates/shelf were Ready;
new shelf UID `572bd232-c8b4-4caa-8a68-8bdc8b0891eb` had zero restarts. V1 UID
`cf7abc47-0363-491a-9420-12db9731ea8d` and all five controller identities/images
were preserved, Ready, zero restarts. Sessions remained zero and grant/policy/job
counts unchanged. Controller pins remain `sha-eeb15e3`. This is v2 agentd
fresh-source/resume protection, not acceptance of the new shared-project flow.
Full [delivery and scope record](handoffs/2026-10-09-workflow-session-guide.md).

**Keeper CA projection shipped:** haynes-ops
[#3642](https://github.com/thaynes43/haynes-ops/pull/3642) merged `4bccaa899` at
`2026-10-09T16:15:42Z`. The keeper-only ExternalSecret selects the existing
item's private Base64 field once and public field unchanged; it became
`Ready=True / SecretSynced` at `16:16:02Z`. Keeper-only Flux/Helm reconciliation
passed. New keeper UID `8b5be82c-e9f3-437a-821e-328a15a0330a` is Ready, zero
restarts, unchanged controller image, with the read-only `/etc/dev-env-keeper/ssh-ca`
mount, mode `0440`, and no injected init container. Its existing CPU limit is
`200m`. Both PVE enable flags remain absent/default false. V1, shelf, operator
and broker UIDs/images/restarts were preserved; sessions/jobs/policies remain
zero. The activity declaration ended; no fixture was created and no key values
were accessed. The root check confirmed ESO status, keeper readiness and v1 UID.

CA storage, projection and PVE node trust are complete. Key-pair parsing,
private target/host-key/egress configuration and real-provider acceptance remain
pending. Disabled keeper readiness proves GitHub issuance, not CA validity.
All twenty parity closures remain open. Headlamp migration/retirement and Q-16
remain under their existing owner rulings. No v1/session restart occurred.
The historical parking evidence below remains dated baseline context.

## Parked for the Codex reset, 2026-10-08 America/New_York

Tom requested a safe stopping point and a reset handoff with 7% usage remaining.
Read the [reset work order](handoffs/2026-10-08-codex-reset.md) before resuming.
Q-19 is complete by owner confirmation: the fresh CA fields are saved in existing
`HaynesKube/dev-env`. No values were sent to or read by this coordinator. Do not
ask for another CA or another item. The next owner step is Proxmox node CA trust,
after concrete instructions are prepared; the original work order assigns that
step to Tom unless he delegates it. Keeper CA projection and minting remain off.
Twenty parity gaps still block cutover; Headlamp stays until guarded parity and
caller migration are proven.

## Target clarification on 2026-10-09 UTC

Tom confirmed that guardrails may be needed and Headlamp should go away once
guarded parity is reached (D-71, README decision 47). The target is a guarded
replacement for every accepted v1 task, including owner-directed Headlamp work.
Headlamp is a migration fallback, not the final architecture. Prove the replacement
and migrate callers before retiring it through GitOps. No approval route or blanket
standing admin access is selected. The existing owner requirements remain. The
Q-19 owner storage step is complete: Tom confirmed both fresh CA fields saved
in the existing `HaynesKube/dev-env` item (D-72). Instructions are in DESIGN-001
section 6.12. Projection and delegated PVE trust are now delivered, as recorded
above. Offline key-pair validation has now passed after keeper-only source
#133 (`3b0d043`) and deployment haynes-ops #3669 (`ab023471`): one invocation
returned exit 0/all validation booleans true, with other six protected pods
unchanged and activity ended. Configuration and real certificate/provider
acceptance still gate activation; no key values are exposed.

## State on 2026-10-08

- **dev-env v2 resumed on Codex on 2026-10-08**, under Tom's coordinator work
  order. Work proceeds in order: finish plan 02, assess approvals inside the
  Claude Code app, check v1 capability parity, then build keeper SSH minting.
  The Claude weekly limit still resets 2026-10-12. Resume at:
  - **Plan 02:** steps 1 to 11 and acceptance items 1 to 3 are done and deployed
    (#94, [backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md)).
    Step 12's laptop client (D-68) is built in #103 and released as signed
    agent `2.8.0` (#104). Its operator image is signed `sha-12a97c4`.
    [haynes-ops #3579](https://github.com/thaynes43/haynes-ops/pull/3579)
    deployed both pins; Flux, rollout and fleet checks passed. **Tom corrected
    the laptop requirement on 2026-10-08 (Q-17, README decision 45):** he uses
    `agent-run` or asks agents to start sessions; a web UI is another possible
    session-management client. Q-17 is withdrawn. Step 12 is delivered as an
    optional external path, with no claim of a real external-machine run.
    **Plan 02 is done under that corrected scope.** Step 13 passed with idle
    local session `dev-env-1008-125420`: create, list, show, attach, detach and
    reap through v1's existing CLI identity. Rescue `20261008-1256` was
    `CleanAndPushed`; the operator archived it and removed its pod and home
    volume. The v1 and shelf UIDs and every restart count were preserved.
    No laptop kubeconfig ceremony or test blocks progress. The
    [external CLI instructions](handoffs/2026-10-08-laptop-access.md)
    are optional.
  - **Plan 07:** steps 1 to 5 are built and H2 (the broker Deployment) is deployed
    and verified. **The docs-only approval spike is complete:**
    [R-03](sagas/distributed-dev-env/research/R-03-claude-code-approvals.md)
    records the native hooks, managed policy, forgeable soft gates and the
    guarded route's unresolved phone, authority and shared OAuth checks.
    **Q-18's earlier premise is withdrawn (D-70):** the Headlamp route already
    gives agents cluster-admin task scope on Tom's live directive for the task or
    access scope. D-71 targets a guarded replacement, then Headlamp retirement.
    No approval route is selected or enabled.
    The complete v1 parity audit is recorded in
    [R-04](sagas/distributed-dev-env/research/R-04-v1-capability-parity.md).
    Twenty gaps in backlog/07 block cutover. The audit's OPERATOR-only distinction
    was corrected after Tom pointed to Headlamp: effective parity includes accepted
    Headlamp exec/ServiceAccount workloads and GitOps self-merge. Direct grants,
    expiry and attribution are mechanism enhancements, not new Kubernetes powers.
    No blanket direct admin grant is authorized. The omitted baseline references are
    restored and verified after the
    rescue-mount fix (#108, haynes-ops #3584/#3583). A fresh full session passed
    presence/ADC permission checks and read-only Omni/API calls, then was rescued
    and removed. PVE LAN access and the remaining service checks still block
    full closure. D-69's Proxmox backend is built with minting disabled: broker
    jobs, keeper SSH/journal/cleanup and typed agentd/PVE support. Key-pair validation,
    private targets/host keys/egress, standing policies and real provider acceptance
    still block its activation. General hw-ssh also needs P-19's connection/revocation
    contract. See the 2026-10-09 delivery checkpoint above for installed node trust.
    No approval implementation choice blocks this work. Q-16 was ruled on 2026-10-08
    (parity first; approvals inside the Claude Code app), and PR #90 and haynes-ops
    #3550 were closed because their Pushover and web approval surface is ruled out.
    The resume point is the last coordinator comment on
    [issue #91](https://github.com/thaynes43/dev-env/issues/91). The spike proposes
    a bounded plan 03 login/Remote Control prerequisite before testing a guarded
    approver; no v1 refresh token is copied. Draft code: branch
    `agent/plan07-approvals-round3` (dev-env) and `agent/plan07-catalog-round3`
    (haynes-ops).
  - **Latest deployment, 2026-10-08:** operator, broker and keeper use reviewed
    main `sha-eeb15e3` (#111), signed/verified in publish run `37798469467`.
    Their updated/ready/available counts are 2/2, 2/2 and 1/1. Agent template and
    shelf use signed `2.9.0` (#110, publish run `37799418992`). The shelf's stale
    2.7.0 pin was updated with the template, as its deployment instructions require.
    Haynes-ops #3589 deployed and verified schema/RBAC/admission/journal first;
    #3595 applied the image pins at `d7b845a6`. Minting stays disabled, with no CA
    mount, trust, egress or policy enabled. P-14/P-15/P-18 remain open for activation
    and real provider acceptance. General hw-ssh remains unbuilt.
    An idle full S session on 2.8.0, `dev-env-1008-152312`, kept its pod UID and
    zero restarts through the controller upgrade. It remained Running and was
    correctly marked Outdated; v1 kept its original UID and zero restarts.
    Fresh full S session `dev-env-1008-153405` started at `2.9.0-5030ab2483` with
    zero restarts. Its grants directory is tmpfs; typed credential listing returned
    `[]` and Proxmox availability exited 4 without provider access. No prompt or
    inference was sent. Both fixtures were rescued as CleanAndPushed, archived and
    removed with their pods and home volumes. The fleet is empty; only the Ready
    shelf and shared volume remain. All five runtime Flux targets and Helm releases
    are Ready. Keeper GitHub readiness passed; no CredentialJobs exist. The scoped
    activity declaration was ended. CA projection, owner node trust and real provider
    acceptance remain required; CA storage is now owner-confirmed complete. Q-18's
    old prompt is withdrawn, with no route selected; it does not hold the next owner
    node-trust step. Ask the owner steps one
    at a time when they arise, with exact item/field names and no secret values.
    The earlier baseline restore (#3584/#3583) remains applied and verified;
    PVE LAN access and the remaining service checks still block full closure.
    New dependency PRs appeared after the bench; green
    release PR #102 (2.7.1) was merged, signed and included in the signed 2.8.0
    release. Issue #91 remains open; #90 and haynes-ops #3550 remain closed.

## State on 2026-10-07

- **Phase 1 is built: plan 01 is done (2026-10-07, #60).** From the v1 pod,
  `agent-run` (built from main) started task session `dev-env-1007-045709` on
  talosw02 at size M. It ran a real docs task on the static token and opened #57,
  which merged. An operator rollout restart during the task left its pod alone (same
  UID, no restart). A reap of a session with an uncommitted file wrote a verified
  bundle to the shared volume that gives the file back, and only then archived the
  volume. A session that cannot fit stayed Pending, and `agent-run` printed the
  scheduler's reason in 4 s. The session pod reached the web but no LAN address and no
  service outside the platform tier, and the guard refused its writes in the dev-env
  namespaces. S-8 is done: `gasha01-rbd` is 1.55 to 1.81 times slower than
  `ceph-block`, so every size stays on it. Plan 01's Acceptance lists the evidence
  (pod UIDs, nodes, the bundle path). The v1 pod and `dev-env-ops` did not restart.
  **Next is plan 02** ([interactive sessions and lifecycle](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md)).
- **Plan 07 round 1 was deployed and verified** (2026-10-07, coordinator work
  order after README decision 40). Steps 1 to 5 are built: CRDs (D-54),
  `/v1/grants` (D-56), broker mode (D-61), kube installation into a session's
  memory-backed grants volume (D-63), and egress grants with the operator's expiry
  backstop (D-64). Signed agent `2.5.0` and operator/broker `sha-763fe77` are
  deployed ([haynes-ops #3542](https://github.com/thaynes43/haynes-ops/pull/3542)).
  One idle local session verified baseline denials, private tmpfs files, kube
  scope/context, token revocation and egress release. An actual ten-minute egress
  grant expired with the broker stopped and the operator restarted after approval:
  the operator deleted its policy at 23:32:14Z while the audit record stayed Active.
  Restoring the broker ended it Expired; the session was reaped and its home PVC
  and temporary fixtures removed ([haynes-ops #3544](https://github.com/thaynes43/haynes-ops/pull/3544)).
  Session and v1 pod UIDs/restart counts were preserved through every rollout.
  Existing pods gain the grants volume on a later resume; rollouts do not restart them.
  Step 6 (human approval and the break-glass catalog remainder) was the next step;
  since 2026-10-08 it is redesigned (Q-16, see State above). Standing policies are the only approval path until then;
  the temporary smoke policies are gone. No new secret or Tom-only step was needed
  for this round. Q-15 remains ruled (A), so step 8's minting identity is settled.
  [backlog/07](sagas/distributed-dev-env/backlog/07-access-broker.md) tracks the remaining scope.
- **What the run found and fixed (2026-10-07).** haynes-ops still had #24's CRD, so
  the API server pruned the heartbeat's task result and the rescue verdict from
  status; haynes-ops #3502 synced it before any reap. A PR here that changes
  `config/crd/` now needs its haynes-ops copy before the operator pin that writes the
  new fields (CLAUDE.md, layout). The advisory review failed on release PRs, and
  release-please used the deprecated `app-id` input; #59 fixed both.
- **The session that waited for plan 02 is archived.** `dev-agents/dev-env-1007-050756`
  was the Pending check (size L, via haynes-ops #3504, reverted by #3505). Plan 02's
  step 11 rescued and archived it (D-51); its home volume is gone.
- **The dev-env v2 design is complete.** It covers the architecture (ADR-001), the
  details (DESIGN-001, D-01 onward), 16 spikes, backlog plans 00 to 10 and two
  research notes.
- **Q-01 to Q-16 are ruled.** Tom answered Q-01 to Q-11, Q-13 and Q-14 on
  2026-10-06, Q-15 on 2026-10-07 and Q-16 on 2026-10-08 (index below). Q-12 (branch protection) was settled on 2026-10-07: Tom
  made the repo public (B) after Actions billing stopped CI on the private repo. The
  settings only Tom can click are in a handoff for an agent on his laptop (below).
- **The repo is public since 2026-10-07.** Everything committed, history and PR
  branches included, is published, so keep secrets, household details and
  transcripts out (CLAUDE.md, hard rules). On 2026-10-07 gitleaks and a pattern grep
  over every branch and PR ref found no secret. The `@claude` workflow now runs only
  for the owner, members, collaborators and haynes-dev-bot. The code is MIT
  ([`LICENSE`](../LICENSE)); [`images/THIRD_PARTY.md`](../images/THIRD_PARTY.md) lists
  what the images bundle (the agent image carries proprietary Claude Code), and
  [#55](https://github.com/thaynes43/dev-env/issues/55) put the upstream license texts
  into the images (`/usr/share/licenses/<tool>/` in the agent image, the Go module
  texts and this repo's `LICENSE` and `THIRD_PARTY.md` in both; CI checks the paths).
- **ADR-001 is Accepted.** Tom ratified it on 2026-10-06: "Accept as written".
  DESIGN-001 is Accepted with it.
- **KICKOFF B1, the Go skeleton, is built** (#14). The module, the `AgentSession` types and
  CRD, the four binaries and the Makefile are on main; only `agent-run version` does
  real work. D-38 records that the keeper is its own binary. B2 added `ci.yml` and the
  operator Dockerfile; B3 added `publish.yml`, which pushes and signs
  `dev-env-operator:sha-<short>` from main. The agent image (B5, D-53, #40) is built and
  smoke-tested by CI on PRs and published by `publish-agent.yml` from a `v2.x.y` release
  tag. 2.0.0 shipped on 2026-10-07: release PR #52 tagged `v2.0.0`, and the publish run
  pushed and signed `ghcr.io/thaynes43/dev-env:2.0.0@sha256:8bab980d6beea9eb8f1576d38a3fd9837150193414728baf1bcc3e07e28a371a`
  (`cosign verify` with the `publish-agent.yml@refs/tags/v2.0.0` identity passes).
  haynes-ops #3501 pinned it in `dev-env-templates`, with a Renovate regex manager
  for that line. The operator package is public (Q-13; an anonymous pull works since
  2026-10-07).
  **B4 added Renovate and release-please** (#18): one repo version on the agent image's
  `2.x` line. The release App is live since 2026-10-07, so release PRs run CI like any
  PR. Agents squash-merge a green release PR themselves (Tom, 2026-10-07: "Merge, and
  let agents merge releases"; CLAUDE.md). #52 was merged on the overnight work order,
  before that ruling, to unblock the first end-to-end run; #61 (2.0.1) was merged under it.
  Renovate auto-merge was off at B4 and is on since 2026-10-07 (the Protect Main
  ruleset exists; minor and patch Go modules and GitHub Actions only).
- **Plan 01 step 1, the `AgentSession` CRD, is built** (#24). The schema enforces
  the per-session rules as CEL, and spec is immutable after create except
  `operatingMode` and `lifecycle` (D-39). `make test` runs an envtest suite against
  kube-apiserver 1.35 (the main cluster's minor) that proves each rule;
  `internal/testenv` starts it, for later suites too.
- **Plan 01 step 4, agentd, is built** (#25, #27, #28). `agentd run` renders the
  config (the `dev-init.sh` port), clones the repo and adds the worktree, runs the
  task once in tmux session `agent` on the static token, heartbeats to
  `POST /v1/sessions/{name}/heartbeat`, and forwards the pod's SIGTERM to the CLI.
  `agentd ctl status` and `ctl rescue` answer the operator (D-40 to D-43). The
  operator's heartbeat route is step 3's; the pod that sets agentd's inputs is
  step 2's.
- **Plan 01 step 5, rescue, suspend and archive, is built** (#32,
  #36). `agentd ctl rescue` writes one git bundle per clone of the refs origin
  lacks, and `manifest.json` last, to `rescue/<session>/<stamp>/` on the shared
  volume, and checks them there; `--stop-agent` stops the CLI first (D-48). The
  operator runs it by exec before a suspend deletes a pod that ran, writes the
  verdict to `status.rescue` before the delete, supersedes it on a resume,
  and archives only a reaped session's volume, after a verified rescue of its last
  pod (D-51). A reaped session whose last pod is gone, ended or never started waits
  with `RemovalBlocked` for plan 02's rescue pod; plan 02 also has the archive
  timer, bundle pruning and the `RescueFailed` page.
- **Plan 01 step 2 is built** (#29, #30). `dev-env-operator` runs a
  controller-runtime manager whose reconciler builds each session's pod and volume
  from `dev-env-templates` (D-44: the format, placement, no probes, 60 s grace). It
  creates what is missing and never updates a pod or volume; envtest proves that
  across an operator restart and a template change. Deleting a session is a reap
  (D-45): a finalizer keeps the session and its volume until rescue, and the
  guarded deletes are the only ones; step 5 filled in the rescue (D-51).
- **Plan 01 step 3, the `/v1` API, is built** (#31). Every operator replica serves
  `POST/GET /v1/sessions`, `GET/DELETE /v1/sessions/{name}`, agentd's heartbeat
  route and `GET /v1/fleet` over HTTPS on 8443, each call checked by a TokenReview
  for the audience `dev-env-operator` (D-46). Callers are Tom's `dev-env-human`,
  trusted clients (the workbench, and the v1 pod until cutover) and sessions by
  their pod's token; the token sets a new session's parent, depth and profile. A
  reap deletes the session, which the rescue finalizer holds (D-45). `agent-run`
  (step 7) imports the wire types from `internal/apiserver/apiv1`. Step 8.9 deployed
  it on 2026-10-07 with the Service and the Certificate that D-46 lists (D-46 and D-50
  as built).
- **Plan 01 step 6, the minimal keeper, is built** (#39). `dev-env-keeper` mints
  the haynes-dev-bot installation token v1's way (same permission set) and merges it
  into `dev-agents/dev-env-gh-token`, key `gh_token`, every 40 minutes or two thirds of
  its life, with retries from 10 s to 5 minutes. It reads the App's key from a
  mounted directory at every mint, refreshes only while it holds its Lease, is ready
  while its token lives, and logs nothing secret (D-52). 8.8 and 8.9 deployed it on
  2026-10-07 (haynes-ops #3480, #3497); its first mint was written at deploy.
- **Plan 01 step 7, `agent-run` v2, is built** (#34). `agent-run --repo <r> -p
  "<task>"` creates a task session, then waits up to 30 s and prints the node it runs
  on or the scheduler's reason; `list`, `show <name>`, `reap <name>...` and `fleet`
  read and reap; `-o json` prints the API's own document, and the exit codes are in
  D-50. A session pod uses its projected token; any other pod, such as the v1 pod,
  mints a token for its own ServiceAccount (the v1 pod can since 8.4, for audience
  `dev-env-operator` only). It trusts the API's CA from the file `DEV_ENV_API_CA_FILE`
  names; in the v1 pod, write that file first from ConfigMap `dev-agents/dev-env-api-ca`
  with `kubectl get configmap` (D-50; agent-run itself reads no ConfigMap); elsewhere, pass
  `--api-url` and `--token-file` until plan 02. v1's own `agent-run` is unchanged and
  still starts every session today.
- **The v1 bounce landed on 2026-10-06.** haynes-ops #3381, #3342, #3336, #3294 and
  #3274 merged at about 21:05Z, and #3330 at 21:15Z. #3241 was closed. The v1 pod
  restarted at 21:17Z on `ghcr.io/thaynes43/dev-env:0.6.8` with a CPU limit of 8.
- **Spikes S-1, S-1b and S-7 are done** (2026-10-06). S-1 and S-1b passed, so D-11
  takes the keeper-owned target (DESIGN-001 6.2). S-7 found no repo needs a mirror.
- **S-6, S-15 and S-16 are done** (2026-10-06). S-6 passed: `--resume` brings back the same
  Remote Control entry with its history. It also found that a SIGTERM to the CLI
  archives the entry and the resume unarchives it (so agentd forwards the pod's
  SIGTERM to the CLI), and that agentd must seed
  `oauthAccount` (DESIGN-001 6.2, 6.7). S-16: the keeper can read plan usage with
  `GET /api/oauth/usage` (7.3). S-15 passed: the CLI's archive call works on an
  access token and the entry leaves Tom's list; `claude --resume` alone unarchives
  it (6.7).
- **Plan 01 step 8 is done in haynes-ops** (2026-10-06 to 2026-10-07). Items 8.1 to
  8.10 and 8.8a are merged and verified (#3458, #3462, #3463, #3468 to #3471, #3474,
  #3477, #3479, #3480, #3491, #3494, #3497). 8.9 runs `dev-env-operator` (two replicas,
  `/v1` on `dev-env-operator.dev-env-system.svc.cluster.local:8443`) and
  `dev-env-keeper` from `dev-env-operator:sha-8b388b2`, and the v2 `agent-run fleet`
  answers from the v1 pod. The API's CA is pinned as ConfigMap
  `dev-agents/dev-env-api-ca` (D-50, as built). Step 9, the first end-to-end run, is
  done (first bullet).
  8.4 is the RBAC and the baseline guard: three ValidatingAdmissionPolicies and a
  Kyverno exec rule. Spike S-12 passed against it, 45 of 45 checks from a Job running
  as `dev-env-agent`, and the VAP sees `CONNECT` for exec (D-19). The v2
  ServiceAccounts exist now, so 8.9 names them and does not create them. Three
  gaps came out of 8.1 to 8.7: the `dev-agents` ceiling is a Kyverno policy, not a
  LimitRange (D-47); the config ConfigMaps had no owner, so 8.8a (D-49) now builds
  four of them in `dev-agents`, with a v2 `CLAUDE.md` and Codex's `requirements.toml`
  at `/etc/codex` in every pod; and `dev-env-templates` held a placeholder image
  digest until B5's follow-up, haynes-ops #3501 (2026-10-07), set `dev-env:2.0.0` and
  its digest.
- **v1 keeps running.** The single dev-env pod and the `dev-env-ops` executor are
  deployed from haynes-ops until the cutover (plan 05) and plan 10.
- **Q-08 is live.** Kyverno `default-cpu-request` (haynes-ops #3406) went live on
  2026-10-06. At 03:34Z no Running pod in the cluster was BestEffort.

## What happens first

1. **The laptop handoff** ([`handoffs/2026-10-06-tom-laptop-settings.md`](handoffs/2026-10-06-tom-laptop-settings.md)).
   All three parts are done (2026-10-07; the checklist below says how each was
   checked). The Protect Main ruleset is live and Renovate auto-merge is on.
2. **Plan 02: interactive sessions and lifecycle.** Plan 01, with KICKOFF B1 to B5,
   is done ([plan 01](sagas/distributed-dev-env/backlog/01-foundation.md)). Plan 02
   ([backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md))
   is done with the corrected in-cluster CLI acceptance check (see State on
   2026-10-08). Plan 07's docs-only approval spike and corrected parity audit are
   complete. Q-18's earlier prompt is withdrawn under D-70; parity restoration and
   keeper activation prerequisites remain. Run one plan at a time (README
   decisions 40, 44, 45 and 46).
3. **Collect the spike results.** S-1, S-1b and S-7 are done, and S-2 is already
   answered (the static token cannot register Remote Control). S-6, S-15 and S-16
   are done, and S-3 passed (keeper-owned Codex auth, DESIGN-001 D-12 step 2), so
   group 1 is complete. In group 2, S-12 passed on 2026-10-07 (its break-glass half
   runs with plan 07), and S-8 is done (2026-10-07: every size stays on
   `gasha01-rbd`). Phase 1's spikes are complete.
   Each result lands in its own PR. The order and pass criteria are in
   [KICKOFF section 2](sagas/distributed-dev-env/KICKOFF.md#2-track-a-spikes).
4. **The rest of the MVP, then beyond it.** The MVP ends at the cutover (plan 05);
   [the saga README](sagas/distributed-dev-env/README.md#plan-backlog) lists which
   plans are in it, their order, and the plans that follow.

## Where everything is

| Path | What it holds |
|---|---|
| [CLAUDE.md](../CLAUDE.md) | The rules for this repo, the single source. Codex reads [AGENTS.md](../AGENTS.md), which points there. |
| [.agents/sagas/README.md](sagas/README.md) | Saga conventions: statuses and the D/Q/C/S id schemes. |
| [distributed-dev-env/README.md](sagas/distributed-dev-env/README.md) | Tom's vision, the architecture at a glance, the hard news, the decision log and the plan index. |
| [distributed-dev-env/KICKOFF.md](sagas/distributed-dev-env/KICKOFF.md) | The work order for the first build session. |
| [adrs/001-distributed-dev-env.md](sagas/distributed-dev-env/adrs/001-distributed-dev-env.md) | The architecture decision (Accepted 2026-10-06), with the ratification summary at the top and consequences C-01 to C-21. |
| [adrs/002-shared-project-workspaces.md](sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md) | Accepted shared workspace/private runtime topology; bounded CephFS trial authorized, normal rollout gated on acceptance. |
| [designs/001-dev-env-v2.md](sagas/distributed-dev-env/designs/001-dev-env-v2.md) | The detail: components, API, lifecycle, credentials, RBAC, egress, GPUs. Spikes are in section 13, risks in 14, Q-01 onward and their resolutions in 15, the decisions (D-01 onward) in 16. |
| [research/R-01](sagas/distributed-dev-env/research/R-01-summoned-agents-audit.md) | An audit of summoned agents today, with v2 requirements V-01 to V-17. |
| [research/R-02](sagas/distributed-dev-env/research/R-02-remote-control-identity.md) | Remote Control identity, the evidence behind S-1, and proposals P-1 to P-12. |
| [research/R-03](sagas/distributed-dev-env/research/R-03-claude-code-approvals.md) | Claude Code in-app approval research: hooks, managed policy, forgery paths and the plan 03 core dependency. D-70 withdraws Q-18's earlier premise; no route selected. |
| [research/R-04](sagas/distributed-dev-env/research/R-04-v1-capability-parity.md) | Complete v1 parity audit: twenty cutover blockers, exact scopes, fixes and runtime closure evidence. |
| [backlog/00-spikes.md](sagas/distributed-dev-env/backlog/00-spikes.md) | S-1 to S-16: steps, safety rules and pass criteria. |
| [backlog/01-foundation.md](sagas/distributed-dev-env/backlog/01-foundation.md) | Plan 01: operator, agentd, `agent-run`, the agent image, task mode on the static token. |
| [backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md) | Plan 02: interactive sessions, idle detection, suspend, resume and optional external CLI access. Done under Tom's corrected scope. |
| [backlog/03](sagas/distributed-dev-env/backlog/03-remote-control.md) | Plan 03: Remote Control, the keeper-owned Max login and the console. |
| [backlog/04](sagas/distributed-dev-env/backlog/04-rolling-updates-codex.md) | Plan 04: drain on idle with resume, multiple Codex remote hosts and one refresh owner. |
| [backlog/05](sagas/distributed-dev-env/backlog/05-cutover.md) | Plan 05: cutover from v1 (needs Tom's written approval). |
| [backlog/06](sagas/distributed-dev-env/backlog/06-later.md) | Later items, each a future plan. |
| [backlog/07](sagas/distributed-dev-env/backlog/07-access-broker.md) | Plan 07: the access broker, grants and break-glass. |
| [backlog/08](sagas/distributed-dev-env/backlog/08-tool-pods.md) | Plan 08: tool pods (Blender, audio, image, whisper, printer, video). |
| [backlog/09](sagas/distributed-dev-env/backlog/09-gpu-local-llm.md) | Plan 09: the VRAM budget, LLM pools, satellites and opencode. |
| [backlog/10](sagas/distributed-dev-env/backlog/10-summoned-sessions.md) | Plan 10: summoned sessions move into v2 and `dev-env-ops` retires. |
| [backlog/11](sagas/distributed-dev-env/backlog/11-project-workspaces.md) | R1–R7 joint projects, shared workspace/remote pods, rules, freshness, lossless repair and cross-pod cleanup acceptance. |
| [workflow guide](../docs/workflow-guide.md) | High-level quick start, architecture/flow diagrams, features, current limits and cutover gates. |
| [.github/workflows/](../.github/workflows/) | CI (`ci.yml`, with the aggregate `CI - Success`), the operator image (`publish.yml`, from main), the agent image (`publish-agent.yml`, from a `v2.x.y` tag), release-please, the Claude advisory review and the `@claude` handler. |
| haynes-ops [`.agents/sagas/dev-env/adrs/001-v2-lives-in-own-repo.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/sagas/dev-env/adrs/001-v2-lives-in-own-repo.md) | Accepted: v2 lives here, and v1 and every manifest stay in haynes-ops. |

## Rulings (Tom, 2026-10-06 to 2026-10-08; full text in DESIGN-001 section 15)

| Id | Ruling |
|---|---|
| Q-01 | Build our own small operator, modelled on kubernetes-sigs/agent-sandbox. |
| Q-02 | Go for the operator, broker, agentd and a static `agent-run`. |
| Q-03 | When the image or config changes, drain each session on idle and resume its conversation on the new version. |
| Q-04 | No fleet cap. Every pod gets requests and limits from S/M/L presets (default M) and the scheduler places it. |
| Q-05 | A block volume per session on `gasha01-rbd`, plus a small shared CephFS volume in the cluster. |
| Q-06 | A dynamic VRAM budget per card, control-plane cards included. Larger models run on satellite machines. |
| Q-07 | The Proxmox operator token and the hw-ssh key leave session pods and come as short-lived broker grants. |
| Q-08 | A Kyverno LimitRange gives every non-system namespace a 50m default CPU request (live, haynes-ops #3406). |
| Q-09 | No household GPU app lends VRAM. Agents get only what is left, and that grows as GPUs are added. |
| Q-10 | Satellites are used only while awake and not in use by Tom, and are never woken. |
| Q-11 | The link that survives restarts is the Claude Max login. The keeper owns it, and the console renews it each month. |
| Q-12 | The repo is public (B), so the ruleset is enforced free. Tom made it public himself on 2026-10-07: "I made dev-env public so I can go to bed but make sure it's good and safe". Actions billing had stopped CI on the private repo. |
| Q-13 | Make `ghcr.io/thaynes43/dev-env-operator` public (A). "Public package write a prompt for an agent on my laptop to flip it": a laptop agent flips it after B3's first publish, because GitHub has no API for package visibility. |
| Q-14 | A GitHub App key secret for release-please (A): "GitHub App key secret (Recommended)". Repo variable `RELEASE_APP_ID`, repo secret `RELEASE_APP_PRIVATE_KEY`; the App also needs Issues read and write for `autorelease:` labels. |
| Q-15 | The keeper mints a Proxmox grant's token over SSH (A): "Keeper mints over SSH (Recommended)". With a certificate from its own SSH CA it runs `sudo pvesh create /access/users/dev-env@pve/token/<grant> --expire <end> --privsep 0` on a node and deletes the token at the grant's end. No new Proxmox user, and v2 never holds the long-lived token. Port 22 from the keeper to the nodes. |
| Q-16 | Parity first, approvals in the Claude Code app (Tom, 2026-10-08). "I am open to advanced security but I must be able to grant permission from my phone." No new guard cuts what agents do today (no Authentik, outpost, Traefik or postgres16 lockdown, no git review or CODEOWNERS gate); standing auto-approved grants cover everything a v1 agent does (OPERATOR kube tier, Proxmox via the keeper's SSH minting, hw-ssh), and any gap blocks the cutover. Human approval gates only capabilities beyond today's (secret reads, break-glass above OPERATOR). Approvals happen inside the Claude Code app, not Pushover plus a web page. Residual risk accepted: Flux is cluster-admin and agents self-merge. The approval surface is the next design spike (issue #91). **Coordinator correction, later 2026-10-08 (D-70):** the OPERATOR-only examples above omit accepted Headlamp cluster-admin access. Those operations are existing reachable powers, with Tom's live directive still required for the Headlamp task or access scope. This factual correction is appended to the original ruling; it is not a new owner ruling or blanket standing admin authorization. |
| Q-17 | Withdrawn after Tom corrected its premise (2026-10-08): use `agent-run` or ask agents to start sessions; a web UI is another possible session-management client. No laptop kubeconfig or test blocks plan 02. D-68 remains an optional external path whose real external-machine use is unverified. Q-16 remains in force. |
| Q-18 | Earlier prompt withdrawn by the coordinator after Tom corrected its OPERATOR-only premise (2026-10-08, D-70). Headlamp's cluster-admin route already exposes the claimed additional Kubernetes powers. No approval route or blanket direct admin grant is selected. Any future approval question must name an actual new capability or workflow and preserve effective parity and existing owner rules. R-03 remains design research; no human path or new guard is enabled. |
| Q-19 | **Complete by owner confirmation, 2026-10-08 America/New_York:** fresh CA fields `SSH_CA_PRIVATE_KEY_B64` and `SSH_CA_PUBLIC_KEY` saved in existing `HaynesKube/dev-env` (D-72 corrects the originally proposed separate item). No values were sent to or read by this coordinator; format and delivery validation still belong to keeper provisioning. Do not ask to generate/store it again. Minting stays disabled; node trust and activation follow separately. |

**Next:** effective parity and keeper activation prerequisites. Q-18's earlier
prompt is withdrawn; no approval route is selected. The spike and corrected v1 parity audit are complete;
parity restoration and keeper SSH minting are in progress. No gap is waived. Any future
approval proposal must preserve existing owner requirements; a change to the
historical break-glass login-freshness requirement needs its own explicit ruling.

**Settings only Tom can click** (no decision needed). An agent on his laptop does
them from [`handoffs/2026-10-06-tom-laptop-settings.md`](handoffs/2026-10-06-tom-laptop-settings.md),
which lists exactly what to change:

- [x] Part 1, before B4 and B5: on the `ghcr.io/thaynes43/dev-env` package, "Manage
      Actions access": give `thaynes43/dev-env` Write (before KICKOFF B5). Done: the
      2.0.0 publish pushed to it (2026-10-07).
- [x] Part 1: repo settings: allow auto-merge (Renovate's `platformAutomerge` needs
      it). Done: `allow_auto_merge` is true (2026-10-07).
- [x] Part 1: the Mend Renovate app covers this repo. Done: Renovate opens PRs here.
- [x] Part 1: the release-please GitHub App (Q-14), with repo variable
      `RELEASE_APP_ID` and repo secret `RELEASE_APP_PRIVATE_KEY` (before B4's
      release-please part). Until then the workflow's `GITHUB_TOKEN` fallback cannot
      open the release PR at all (it failed on 2026-10-06), so also allow Actions to
      create PRs (handoff part 1, step 5). Done: the App minted the token and opened
      release PR #52 on 2026-10-07.
- [x] Part 2, after B3's first publish: make `ghcr.io/thaynes43/dev-env-operator`
      public (Q-13). Done: an anonymous pull works (checked 2026-10-07).
- [x] Part 3, after `CI - Success` has reported once (B2): the Protect Main ruleset on
      the default branch, with `CI - Success` required. Done and checked 2026-10-07
      with `gh api repos/thaynes43/dev-env/rulesets`: ruleset id 24655718 is active on
      `~DEFAULT_BRANCH` with `deletion`, `non_fast_forward`, `required_linear_history`,
      `pull_request` (0 approvals) and `required_status_checks` (`CI - Success`,
      integration id 15368 = GitHub Actions, up-to-date off). Also checked: private
      vulnerability reporting is enabled. Set by Tom but not visible to the bot (its
      token reads `security_and_analysis` as null, and the fork-PR approval endpoint
      answers 403): approval for all outside contributors' fork PRs, secret scanning
      and push protection. Renovate's auto-merge was flipped on in the PR that
      recorded this: `automerge: true` in the first two `packageRules` entries of
      `.github/renovate.json5` (Go modules and GitHub Actions).

## Working rules (the summary; CLAUDE.md is the source)

- **Worktree per task.** Branch `agent/<task>` and never push to main.
- **Open ready PRs, not drafts.** The `Claude advisory review` check posts findings.
  Read every one. Fix it, or answer it on the thread with a concrete reason it is
  wrong. "Merging anyway" is not an answer.
- **Merge your own green PRs** with a squash merge, once required checks pass and
  every finding is handled.
- **Only questions wait on Tom.** Ask one at a time, when it comes up, with the
  recommended option first. Claude Code uses AskUserQuestion; Codex uses the
  available native question tool, with phone delivery verified before relying
  on that route. If unavailable, report the capability gap and keep only the
  dependent decision pending; prose in the stream is not delivery. Record each
  question as a `Q-NN` in the design, then fold Tom's
  answer back in as a dated ruling and update the decision log. Check a question's
  premise before you ask it.
- **Docs first.** A behaviour change starts in the saga and lands in the same PR as
  its code. An Accepted ADR is never edited; a new ADR supersedes it.

## Hard constraint: no CPU burners

No CPU burners, stress tools, busy loops or wide parallel test runs on any shared
node. Set a CPU limit on anything you run in the cluster. Cap test parallelism
(`go test -p 2`, vitest `--maxWorkers=2`) and run one suite at a time.

Why: from 23:42Z on 2026-10-05 to 00:12Z on 2026-10-06, a dev-env subagent's busy
loops plus parallel vitest runs saturated talosm02. Its BestEffort pods (EMQX,
traefik, authentik, cloudnative-pg) got no CPU and went into liveness-kill loops.
The kubelet itself peaked at only 0.18 cores.

## Inside the cluster or outside it

| Capability | In the v1 dev-env pod | Outside (Tom's machine) |
|---|---|---|
| gh, git push, GHCR | Yes, as haynes-dev-bot. No prefix: `~/.local/bin/gh` reads `/creds/gh_token` on every call (haynes-ops #3476, 2026-10-07). | Yes, with your own gh login, so PRs author as Tom. GHCR pulls of public images are anonymous. |
| Code, Go builds, unit tests, envtest | Yes, lightly, under the pod's CPU cap. | Yes. |
| `kubectl`, `flux` | OPERATOR tier: read plus targeted runtime writes. | Optional external CLI access needs a caller's existing admin kubeconfig (D-68). No cluster credential is supplied by this repo, and Tom's current workflow needs no laptop setup (Q-17). |
| `declare-activity` | v1 writes files on the pod's volume; v2 `agent-run declare-activity` calls the operator API. | The v1 file command is unavailable. The v2 API command is available to an authenticated external CLI caller. |
| MCP servers on cluster DNS (grafana-mcp, home-assistant, blender, audio, vexa, the haynesnetwork hop) | Yes. | No. |
| The Claude Max login and Codex `auth.json` | Yes, on the pod's volume. Never copied out. | No. Never fetch them. |

**Spikes by where they can run:**

- **Hand to an in-pod session.** These need the live Max login or Codex auth on the
  v1 volume, or a measurement taken inside the cluster: S-1 (and S-1b), S-3, S-6,
  S-7, S-15, S-16, and the S-2 re-probe on each CLI bump.
- **Through haynes-ops PRs that deploy test workloads.** An in-pod session then reads
  the results: S-8 and S-12 in phase 1, and S-9 and S-13 on talosw04 before plan 09.
- **Inside v2 session pods, once they exist:** S-10 (phase 2), S-5 (phase 3), S-4
  (phase 4) and S-11 (before plan 09).
- **Outside is fine:** S-14 steps 1 to 3, on Tom's own machines with Tom there. Step
  4 needs a session pod.

To hand a spike to the pod from outside, file an issue here that links the spike's
section in `00-spikes.md`. Then ask Tom (one question) to dispatch it:
`agent-run --repo dev-env --agent claude --effort xhigh -p "Run spike S-N per
thaynes43/dev-env#<issue>"`.

## Cross-repo boundaries

- **This repo** holds code (operator, broker, keeper, agentd, `agent-run`,
  `dev-env-satellite`), image builds, CI and the saga.
- **thaynes43/haynes-ops** holds every manifest and all pod config, changed only
  through PRs that Flux applies. Never `kubectl apply`. No v2 PR touches
  `kubernetes/main/apps/dev/dev-env/app/resources/**`, because that restarts v1 and
  every session in it.
- **haynes-ops is public.** Nothing secret goes there: no tokens, login URLs or codes.
- **Most v2 changes are two PRs:** the code and image here, then the pin bump in
  haynes-ops.
- **Cluster-scoped haynes-ops PRs** (CRDs, admission policies, clusterwide network
  policies, Kyverno) may be authored from outside. They are merged and verified by
  an in-pod session that can declare activity and read the cluster.

## Open items elsewhere that touch v2

- [haynes-ops #3392](https://github.com/thaynes43/haynes-ops/issues/3392): v1's
  OPERATOR tier lets an agent act as another namespace's ServiceAccount. The fix
  for v1 is Tom's decision. v2 answers it with the baseline guard (D-19, S-12).
- [haynes-ops #3414](https://github.com/thaynes43/haynes-ops/issues/3414)
  (**pending**): gives `dev-env-ops` its own second Max login, so `wo-*` and `esc-*`
  sessions register Remote Control. The wiring is merged as
  [#3422](https://github.com/thaynes43/haynes-ops/pull/3422). The login itself still
  needs Tom's relay ceremony (haynes-ops
  [`.agents/runbooks/agentic-remediation.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/runbooks/agentic-remediation.md),
  section "dev-env-ops Max login"). `rc-selftest.sh` printing
  `SELFTEST: REGISTERED` verifies it. The issue stays open until then. Until plan 10,
  Tom renews up to three Max logins a month.
- [haynes-ops #3415](https://github.com/thaynes43/haynes-ops/issues/3415): the
  executor is fragile. Config merges restart it, and a busy escalation lane holds
  new escalations without paging. Plan 10 retires the executor.
- [haynes-ops #3405](https://github.com/thaynes43/haynes-ops/issues/3405): Kyverno
  `kyverno.io/v1` ClusterPolicies need migrating to the CEL policy types. Write
  v2's new Kyverno policies on the new API where it can express them.
- Also relevant: [#3092](https://github.com/thaynes43/haynes-ops/issues/3092)
  (Kyverno reads legacy `.sig` cosign signatures, but cosign v3 writes bundles; this
  bears on how v2 images are signed) and the held v1 CPU cap
  [#3381](https://github.com/thaynes43/haynes-ops/pull/3381).
