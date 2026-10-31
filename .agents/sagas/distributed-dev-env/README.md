# Saga: distributed-dev-env (dev-env v2)

**2026-10-31 design-only #325:** [DESIGN-041](designs/041-upgrade-attempt-claims.md)
proposes Session-owned upgrade attempt claims and exact PR/head joins.
Q-325-01 and Q-325-02 are pending for Tom; no writer or activation follows.

**2026-10-31 design-only #321:** [DESIGN-040](designs/040-cursor-grok-inbox-actor.md)
compares three ways to give a Cursor/Grok agent a truthful identity on the
platform inbox and recommends a gated v2 agent kind. Q-321-01 is pending for
Tom; no credential, ACL change or activation follows from it.

**2026-10-30 design-only #269:** [DESIGN-039](designs/039-access-policy-change-guard.md)
compares guards against agent-authored access widening through GitOps, with
automatic routine deployment and an approved admin path. All enforcement and
activation remain proposed and subject to Tom's review; no live policy changes.

**Current source pointer, 2026-10-25 Story190 C1:** the [consolidated usage report](../../../docs/usage-consolidation-report.md) is a pure projector over caller-authorized allowance reads and initial-invocation observations. It does not accept Epic187, Story188, a collector, portal, or provider coverage. The [provider allowance contract](designs/030-provider-allowance-contract.md) remains a source-only typed observation and cached-read document. Packets B–F stay held. Older installed-state paragraphs below remain historical.

**Current2026-10-19 Story110:** [source/release/operator delivery and exact009
access hold](verification/owned-operator-replacement-2026-10-19.md#delivered-checkpoint--2026-10-19-0934-utc)
supersede older installed-state and release369-pending entries. Operator2.30.7 is
installed; protected work is unchanged. Owned-root response/update/history/
stop-continue/phone acceptance remains unproven.009 named-read approval is
pending; no root/token/model. Keep Saga85/Epic107/Story110, owner and counters.

**2026-10-23 source-only #177:** the [workflow result-custody record](verification/workflow-result-custody-2026-10-23.md) documents an offline handoff checker and coordinator procedure. It does not accept #117 or #101, and it does not change runtime orchestration.

**2026-10-18 ruling:** Q-24 is answered: Tom approved small separate native
hosts, each with its own computer entry and pairing. See
[DESIGN-006](designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing).
Older dated pending-Q24 entries below are historical. Runtime test actors are
limited to `gpt-6-luna` and the Haiku family. Since 2026-10-19 (Tom's
latest-family preference) new Haiku launches use `claude-haiku-5-5`; earlier
`claude-haiku-4-5` records stay as they happened. Architecture approval grants no
pairing, credential, deployment or live-cohort authority.

**Owner requirement, 2026-10-16:** [central cross-repository work coordination](requirements/2026-10-16-central-work-coordination.md)
records canonical OpenProject Saga/Epic/Story work, shared agent rules/tools, central
owner refinement and practical delivery. [Plan 11](backlog/11-project-workspaces.md#central-cross-repository-work-requirement)
is the implementation home; this documentation update enables no runtime changes.

Start continuation with the
[2026-10-13 Claude pickup brief](../../CLAUDE-RESUME-2026-10-13.md): deployed
state (2.28.0 image delivery), counters, rulings through Q-37/D-92 and pacing, and the next work
with its owner gates. Rule loading is accepted for both providers, all five
canary IDs are consumed, Q-24 is still pending, and the MVP is unaccepted.

*Historical, 2026-10-12:* Start continuation with the [Claude pickup brief](../../CLAUDE-RESUME-2026-10-12.md).
It consolidates current and earlier owner enumerations, the bounded roadmap,
source-review assignments and failed/stopped routes. Review and improve the
shipped code. Q-24 and Q-27 remain pending, both fixed IDs are consumed, and
actual both-provider rules/MVP acceptance remains open. Earlier checkpoints
below are historical; they do not reauthorize old next steps or reset counters.
*Update: Q-27 was answered later on 2026-10-12; see the next paragraph.*
*Update: both Codex reads then ran and returned both markers; see the #173
paragraph after the Q-27 ruling.*
*Update: Claude's rule loading was accepted later on 2026-10-12 under Q-28; see
the Q-28 paragraph after the #173 Codex paragraph.*

**Q-27 answered, 2026-10-12.** Tom answered through the Claude native prompt:
"Fix once, then read (Recommended)". The ruling allows one offline correction of
the Codex reader preparation, with a fixed list of already accepted evidence,
focused tests and independent review. Then it allows exactly one named read and
one result read of the Codex original. There are no retries and no counter reset.
Decision 60 and the
[Codex probe record](verification/codex-fixed-rule-probe-2026-10-12.md#q-27-one-final-reader-preparation-correction)
hold the ruling. Q-24 is still pending with no owner answer recorded; do not ask
it again.

**#173 Codex rule markers accepted, 2026-10-12.** Under Q-27, the one reader
correction passed its tests and independent review of both read plans. Then the
one named read and the one result read of the Codex original ran, with no
retries. Its retained output is exactly the common and the repository marker,
and the prompt held neither, so the Codex original loaded both rule sets in its
private pod. The prompt said not to use tools; compliance is unproven while full
history is Unknown. The
[Codex original result record](verification/codex-original-result-2026-10-12.md)
binds Root acceptance `48637874` and the review after the reads. This accepts
the markers only. Served model, usage, cost, full history, physical stop,
capture completeness, private GitHub authorization and a fresh task pin remain
Unknown or unaccepted. Claude's rule loading is still unproven, so actual
both-provider rules and the #173 MVP stay open. Counters: Directory 5; Create 1
each; named and result reads 1 each per provider. Nothing further is authorized.
*(Update: Claude's rule loading was accepted under Q-28; see the next
paragraph.)*

**#173 Claude rule markers accepted under Q-28, 2026-10-12.** Tom answered Q-28
through the Claude native prompt: "Yes, after fixes deploy (Recommended)" (decision
63). After dev-env v2.24.1 was deployed (haynes-ops #3841 and #3842), one new Claude
canary, `rule-canary-claude-1012`, ran with one create, one named read and one
result read, and no retries. The runner's first wait held on a kubectl flag; one
reviewed correction replaced only that read-only step. The retained output carries
the common and the repository marker, each behind a label, and the prompt held
neither. So both providers have now loaded the common and repository rules in
private v2 pods. The
[Claude canary 1012 record](verification/claude-canary-1012-2026-10-12.md) binds
Root acceptance `88909059`. Genuine private GitHub authorization and worktrees,
and a fresh task pin, remain open for #173 with its other acceptance items;
served model, usage, cost, full history, physical stop, capture completeness,
private capture acknowledgement and tool-use compliance stay Unknown.
Counters: Directory 6; Claude Create 2, Codex Create 1; Claude named and result
reads 2 each, Codex 1 each. Nothing further is authorized.

The [original Codex probe record](verification/codex-fixed-rule-probe-2026-10-12.md)
records 16 passing offline checks, independent full review and one program that
completed Directory 5 and original Codex HTTP 201 at 06:47:25 UTC. Saved native
review `c3bcc13b` and Root `f189f079` accept transport/original identity only;
actual output, executor and rules remain Unknown. Both fixed IDs are consumed.
The Claude quota result remains preserved. Reader preparation stopped after
three indexing failures; Q-27's native question requests one final offline
correction and is pending. No fourth attempt or followup read is authorized.
Actual both-provider rules and the owner pilot remain open; Q-24 is pending.
Earlier checkpoints below are historical and do not reset the consumed IDs.
*(Historical: Q-27 is answered; see the ruling above.)*
*(Update: the Codex reads ran; see the #173 paragraph above.)*

The [first original Claude canary](verification/first-original-canary-2026-10-12.md)
records reviewed logical Directory4 and original HTTP201 at 05:28:22 UTC. Claude's
fixed ID is permanently consumed; Codex's is unstarted *(historical: Codex's
original HTTP 201 followed at 06:47:25 UTC; see above)*. Independent saved review
and Root receipt acceptance bind the original UID only. Named executor/context
provenance was bound by one saved named read. One fixed stored result read
returned the weekly-limit response (reported reset 12pm UTC), with no rule
markers. Claude rule acceptance remains unaccepted; no consumed-ID retry or
paid API fallback is authorized. Source #213
and release #214 ship the unwired ownership proof in v2.24.0; private dispatch
remains disabled and no new deployment is required. This supersedes historical
zero-Create entries below. Claude advisory is optional/down; required CI and
native review remain gates. Q-24 and the owner pilot remain open.

The [private-budget ownership and runtime checkpoint](verification/private-budget-ownership-2026-10-12.md)
records the next bounded #154 source proof and default refusal, with private
dispatch still disabled. Focused checks pass; native review and required CI
remain source gates. It also supersedes the historical Directory TLS-only state:
two complete empty pages succeeded but expired before any Create. Three logical
reads and both expired windows remain recorded; no key, Create or provider turn
was consumed. The explicit sequencing successor needs independent review before
another read. Signed v2.23 publication is accepted separately, not deployed.
Actual rules/history/coordination/dispatch and concurrent owner acceptance remain
open, and Q-24 still gates native Codex isolation and fleet UX.

The [fixed result-reader delivery record](verification/canary-result-2026-10-12.md)
records merged #208, published v2.22.0, a bound local client and exact-source signed
operator publication. Image-only haynes-ops #3825 merged as `b80cac13` after required
CI, native source review and a fresh accepted baseline. One saved post observation
and independent source-equivalence review support separate root acceptance
`f610a2d6` of image delivery and preservation; its original exact-revision HOLD is
retained unchanged. Source #210 and release #211 ship the bounded named Session
reader in v2.23.0; its actual merged-source local client `f136b205` passed saved
build review `693f1b3e` and root local-build acceptance. It remains uninvoked;
the consumed Directory TLS attempt and trust correction remain runtime gates.
Neither permanent provider probe has run *(historical: both ran later on
2026-10-12; see above)*. Global private admission remains off,
actual rule loading and the owner pilot remain unaccepted, and Q-24 is pending.
Q-24 also gates fully isolated native Codex-session acceptance: one paired entry's
concurrent routing to separate app-server pods is unestablished. Separate paired
session hosts remain the recommendation; coordinator/workers share the native
failure boundary and require an explicit owner choice.

The [fixed-rule activation record](verification/fixed-rule-activation-2026-10-11.md)
binds Q-26's one final correction and accepted Ops #3824 deployment/preservation.
The single post-roll observer and independent saved review passed; all 11 peers
were preserved. DESIGN-022 fixes bounded result retrieval and the discovered
pre-create capture UID preview before any permanent provider probe. The one
ordinary Directory attempt at 03:31:01 UTC failed operator CA validation; its
receipt is preserved, contents remain Unknown and no retry or Create is authorized.
Actual both-provider rule acceptance has not run; Q-24 remains pending.

The [ID-only canary client slice](designs/021-canary-cli.md) makes the fixed
instruction-probe route safely callable: one trusted-ID POST, original receipt,
no create defaults, retry, redirect or polling. Actual rule acceptance stays gated
on reviewed configuration and a fresh authorized directory page per first create.

The [explicit directory client slice](designs/020-directory-cli.md) supplies the
safe read prerequisite for the fixed rule probes: one authorized API request,
no create fallback/retries/redirects, truthful continuation and Unknown state.
Source verification remains separate from actual discovery/provider acceptance.

**Earlier delivery, 2026-10-11:** signed v2.20.0 directory/usage/private-Pod
retention prerequisites are deployed through haynes-ops #3823. Global private
admission remained off; fixed-rule/discovery activation was accepted subsequently
through #3824 as recorded above. Directory cardinality
is Unknown after the expected operator-only Kubernetes list refusal; the frozen
observer and all failed evidence remain preserved. See the
[installation verification and next acceptance gates](verification/installation-2026-10-11.md).
Actual provider rules and the concurrent owner pilot remain unaccepted. Claude
reviews are optional while unavailable; required CI and native review still apply.

**Status:** design, Accepted 2026-10-06. Tom ratified ADR-001 as written (drafted
2026-10-05, his rulings folded in 2026-10-06): see the
[ratification summary](adrs/001-distributed-dev-env.md#ratification-summary) at the
top of ADR-001. Phase 1 is built: plan 01 (task mode) is done since 2026-10-07. The
operator and the keeper run in the cluster, the agent image 2.0.0 is released, and
the first task session ran from the v1 pod and opened its PR. The architecture is in
[ADR-001](adrs/001-distributed-dev-env.md) and the detail in
[DESIGN-001](designs/001-dev-env-v2.md). Tom ruled on every question, Q-01 to Q-11,
on 2026-10-06 and widened the scope: tool pods, a GPU budget, satellite inference
workers, local models, access without in-pod prompts, summoned sessions kept as a
first-class path, and one console for links, archives and the login renewal. Three
repo-setup questions: Q-13 (public package) and Q-14 (a GitHub App key secret) were
ruled on 2026-10-06, and Q-12 (branch protection) was settled on 2026-10-07, when Tom
made the repo public. Research notes
[R-01](research/R-01-summoned-agents-audit.md) (summoned agents) and [R-02](research/R-02-remote-control-identity.md) (Remote Control
identity) are folded into the design. The spikes in
[backlog 00](backlog/00-spikes.md) come first: S-1, S-3, S-6 and S-15 passed on 2026-10-06, S-7
and S-16 are done, and S-2 is answered (re-probed on CLI 2.1.292). The
first build session's work order is [KICKOFF.md](KICKOFF.md) (historical; continue
from the [pickup brief](../../CLAUDE-RESUME-2026-10-12.md)); the repo's front door is
[`.agents/HANDOFF.md`](../../HANDOFF.md).

**Working rules:** this repo's [CLAUDE.md](../../../CLAUDE.md). Docs first. Ask Tom
one question at a time with AskUserQuestion, and fold each answer back into the
design and the decision log below with its date.

The [workflow guide](../../../docs/workflow-guide.md) is the product-level quick
start: diagrams, current/planned features, session management and cutover gates.
Plans 01/02 provide the tested Claude task/terminal subset. Budget/native source
foundations are released, with remaining runtime acceptance in the handoff.

**Controlling scope, 2026-10-10:** [ADR-003](adrs/003-session-coordination-private-repositories.md)
records the owner's clarification: common rules and authorized context, peer
communication and task ownership, with independent per-pod repos/worktrees.
Bootstrap/fetch reference repositories and pin fresh source before new tasks.
The bounded next #175 source contract is
[DESIGN-014: durable offline inbox](designs/014-durable-offline-inbox.md).
Shared Git/RWX and its trial no longer gate delivery. [Plan 11](backlog/11-project-workspaces.md)
and [session coordination](../../../docs/session-coordination.md) define the first
coherent pilot. ADR-001/002 remain historical; ADR-003 controls conflicts.
[DESIGN-016](designs/016-managed-output-context-capture.md) defines the bounded
initial managed-output capture seam. Its partial publisher assertion is distinct
from complete/native history and preserves the independent payload ACL.

[DESIGN-015](designs/015-retained-common-rule-inputs.md) binds common instruction
bytes and their source provenance to new managed private tasks, and preserves
the original effective instructions on resume. It is a prerequisite for actual
Claude/Codex rule acceptance; empty rules, wrapper presence and source tests do
not satisfy that acceptance. Retention and task-claim guards are installed, but
actual history, claims, messaging and the concurrent owner pilot remain open.

[DESIGN-018](designs/018-initial-invocation-usage-provenance.md) defines the bounded
initial invocation cost fix: explicit Unknown/observed metrics, exact requested
identity and CLI estimate provenance, with no inferred billing or aggregation.

[DESIGN-019](designs/019-private-pod-retention-guard.md) corrects the source-
reachable original private Pod removal path with guards before legacy rescue
and deletion. Volume retention alone does not preserve executor evidence; this
source protection is separate from a stop proof and actual owner-pilot acceptance.

**v1** is the single dev-env pod deployed from haynes-ops
(`kubernetes/main/apps/dev/dev-env/`; saga `.agents/sagas/dev-env/` there). It keeps
running with its behavior, configuration and current access unchanged under
Q-35/ADR-007. Accepted MVP precedes decommissioning, which still needs Tom's
explicit approval in phase 5.

## Vision (Tom, 2026-10-05)

| # | Tom's point | Where the design answers it |
|---|---|---|
| 1 | An operator with an API that creates and destroys agent pods, one per session, each with its own CPU and memory requests and limits, spread across nodes, keeping the control-plane masters (EMQX, traefik) protected | DESIGN-001 sections 3, 7 |
| 2 | `agent-run` becomes a CLI that calls that API and runs anywhere: in an agent pod, on a laptop, from a phone-driven session | 3.4, 3.5 |
| 3 | The operator spins up and prunes the fleet, replacing worktrees, the stranded-session sweep and rescue branches | 4 |
| 4 | Rolling updates move sessions to a new version; upgrading the operator never takes sessions down | 5 |
| 5 | Codex needs will grow: the remote-control daemon, phone pairing, the requirements floor, MCP rendering, auth | 6.3 |
| 6 | Later: agents reserve time on the local LLMs and 3090 GPUs; design the seam now | 8.3 |
| 7 | (2026-10-06) Pods carry requests and limits; Kubernetes handles scheduling, so the cluster grows without tuning the dispatcher | 7.2, 7.3 |
| 8 | (2026-10-06) More advanced GPU handling when local LLMs arrive | 8.2, 8.3 |
| 9 | (2026-10-06) Agents spin up specialised tools on other pods, some with a GPU: image gen, whisper, Blender, 3D-printer tools, MiniMax and other video gen, audio gen | 8.1 |
| 10 | (2026-10-06) Agents may use the gasha01 storage | 6.6 |
| 11 | (2026-10-06) Agents skip their vendor's permission prompts; Tom keeps control of cluster and external access, which agents request; agents can reach the web; local models such as Qwen work the same way | 6.10 to 6.13 |
| 12 | (2026-10-06) GPU allocation adjusts dynamically to what the household needs VRAM for; nothing is lent, and more GPUs come online in the cluster; larger models run on satellite workers: the 128 GB M5 MacBook, the 5090 and 4090 PCs | 8.2 to 8.4 |
| 13 | (2026-10-06) Summoning a Max-plan agent from automation: "we need to preserve the functionality" | 3.7 |
| 14 | (2026-10-06) The Claude Code auth that survives restarts belongs to one owner, and its renewal is "baked into the front end", which also lists every session's link with an archive button | 3.8, 6.2, 6.7 |

## Architecture at a glance

- **dev-env-operator** (namespace `dev-env-system`, Go): control plane only. HTTPS
  API, `AgentSession`, `Activity`, `ToolPool`, `ToolSession`, `LLMLease` and
  `CallerPolicy` resources, idle detection, rescue, drain-and-resume, and the
  summoned-session lanes, watchdogs and digest that `dev-env-ops` runs today.
- **dev-env-broker** (same namespace, same binary, own Deployment): temporary access
  grants. Standing policies in git can approve requests; none are currently live.
  A management console is planned for sessions, links, archive and login renewal.
  Human approvals, if implemented, belong inside the Claude Code app under Q-16;
  no route is selected or deployed. D-70 requires effective Headlamp parity before
  replacing that path, rather than treating its existing powers as new access.
- **dev-env-keeper** (same namespace): designed as the single owner of rotating
  credentials, the Max login and GitHub App keys. GitHub token issuance is built;
  keeper-owned Max login remains future work. Sessions receive short-lived GitHub
  and privileged grants; accepted reader/ADC baseline references remain under R-04.
- **Session pods** (namespace `dev-agents`, worker nodes only): one agent each
  (Claude Code, Codex or opencode for local models), with no approval prompts,
  `tini`, an `agentd` supervisor, requests and mandatory limits from a size class,
  placed by the scheduler with no fleet cap, and its own volume on gasha01. v1's
  OPERATOR-tier verbs under a field-level guard, open web egress, and nothing in the
  three dev-env namespaces.
- **Tool pods** (namespace `dev-tools`, any node that fits): Blender, audio, image,
  video, transcription, 3D-printer tools and local model servers, started on demand
  and stopped when idle; GPUs counted in VRAM; household apps keep their full
  reservation and agents get what is left, which grows as GPUs are added.
- **Satellites**: Tom's M5 MacBook (128 GB) and his 5090 and 4090 PCs serve models to
  agents through a small `dev-env-satellite` program, only while they are awake
  and he is not using them, never woken.
- **`agent-run`**: one static Go CLI with v1's verbs, calling the API from anywhere.
- Shared state, item by item, is in DESIGN-001 section 6.

## Hard news

1. **The Max login cannot be shared by pods.** Two processes refreshing one Claude Max
   login revoked it mid-task on 2026-08-29. Remote Control needs that login; the
   static token cannot register Remote Control (settled: the docs say so, and v1's
   executor saw 45 of 45 sessions rejected). The keeper owns the login and pods get
   access tokens only. Spike S-1 proved the CLI behaviour this relies on (2026-10-06,
   DESIGN-001 6.2): an access-token-only file registers Remote Control on a cold home,
   and a merged token is picked up without a restart. Each refresh revokes the old
   token in every pod at once, so agentd must merge fast. The coordinator host pod
   stays the fallback for a CLI release that breaks this. v1 now
   has two logins (the dev-env pod's, and `dev-env-ops`'s from haynes-ops #3414, whose
   login ceremony is pending on 2026-10-06);
   v2's keeper replaces both with one, so Tom renews up to three a month until both
   v1 pods are gone.
2. **Native agent messaging stops at the pod boundary.** Claude Code ties its session
   registry, inbox sockets and locks to the pid namespace. Across pods, only Remote
   Control sessions talk natively; everything else goes through the operator.
3. **More parts, more to run.** An operator, a keeper, an in-pod supervisor and CRDs
   replace one pod and some bash. That is the price of limits, spreading and rolling
   updates.
4. **More parallel sessions burn the Max plan faster.** The plan's 5-hour and weekly
   windows are shared with Tom's own use. There is no fleet cap (Tom, 2026-10-06), so
   the plan's own wall is the brake; `agent-run fleet` shows it, and local-model
   sessions take bulk work off the plan.
5. **Each session clones fresh.** Large repos (haynes-quest: 934M of history) may need
   a shared mirror.
6. **Open web egress makes credentials the boundary.** Agents get the web they need,
   so a page that tricks an agent can leak whatever its pod holds. The allowlist never
   stopped that (github.com and both model vendors were always on it). So the
   root-equivalent credentials leave the default set and come as short-lived grants
   (Q-07, Tom 2026-10-06).
7. **GPU accounting touches household apps.** The scheduler cannot share GPUs fairly
   until every GPU workload, household ones included, declares its VRAM floor and
   burst. Today they pin cards by UUID and the scheduler sees nothing. Nothing is
   lent to agents (Q-09), so today the household reservations fill most cards and
   in-cluster agent GPU work stays small until Tom adds GPUs.
8. **The broker is powerful.** It creates temporary identities and bindings to
   named catalog roles. Human approval and the break-glass catalog are not deployed.
   The original restricted break-glass proposal does not replace Headlamp's
   cluster-admin task scope. Effective parity must be proved before removing that
   route; a separate broker workload alone does not establish an approval boundary.
9. **Session volumes live outside the cluster.** gasha01 is HDD-backed Proxmox Ceph.
   Its outage stops new sessions and stalls running ones (DESIGN-001 6.6).
10. **No fleet cap means workers can saturate.** On 2026-10-05 the pods that failed
   were BestEffort (no CPU request, CPU weight 1); the kubelet was fine. A busy v2
   fleet could saturate a worker the same way, so every household pod needs a CPU
   request first (Q-08: a Kyverno LimitRange, live since 2026-10-06 as haynes-ops #3406).
11. **The big local models live on Tom's own machines.** The cluster's free VRAM is
   small (one worker 3090, already busy with the house). Large models run on the
   satellites, which are there only when Tom is not using them, so local-model agents
   wait or fall back to smaller models when he is.

## Decision log

| # | Decision | Status | Outcome |
|---|---|---|---|
| 1 | Where v2's saga and code live | **DECIDED** 2026-10-05 (Tom) | A new repo, **thaynes43/dev-env** (private at first, public since 2026-10-07, row 19), keeping the image name `ghcr.io/thaynes43/dev-env`. Manifests and pod config stay in haynes-ops (GitOps). Recorded in haynes-ops as ADR-001 of its dev-env saga. |
| 2 | Architecture: one pod per session run by an operator | **DECIDED** 2026-10-06 (Tom) | [ADR-001](adrs/001-distributed-dev-env.md) |
| 3 | Settled design decisions D-01 to D-37 (D-34 to D-37 added 2026-10-06) | **DECIDED** 2026-10-06 (Tom) with ADR-001; D-02, D-18, D-19 and D-20 **REVISED** 2026-10-06 | [DESIGN-001 section 16](designs/001-dev-env-v2.md#16-decisions-settled-in-this-design) |
| 4 | Q-01: build the pod-and-volume layer, or adopt kubernetes-sigs/agent-sandbox | **DECIDED** 2026-10-06 (Tom) | Build a small operator modelled on agent-sandbox (A). |
| 5 | Q-02: language for the operator and CLI | **DECIDED** 2026-10-06 (Tom) | Go, for the operator and a static `agent-run` (A). |
| 6 | Q-03: what happens to running sessions when the image or config changes | **DECIDED** 2026-10-06 (Tom) | Drain on idle, then resume the conversation on the new version (A). |
| 7 | Q-04: default session size and fleet cap | **DECIDED** 2026-10-06 (Tom) | Fleet cap rejected. Every pod has requests and limits (S/M/L presets, default M, low PriorityClass); the scheduler places them; no allocation logic to retune (D-21). Scope added: GPUs for local LLMs, tool pods. |
| 8 | Q-05: where repos, worktrees and agent state live | **DECIDED** 2026-10-06 (Tom) | A block volume per session plus a small shared CephFS (A), and agents may use gasha01: session volumes on `gasha01-rbd`, the shared volume on in-cluster CephFS (D-22). |
| 9 | Access model: no prompts in the pod, control at the platform, grants on request | **DECIDED** 2026-10-06 (Tom) | [DESIGN-001 6.10 to 6.13](designs/001-dev-env-v2.md#612-access-no-prompts-in-the-pod-control-at-the-platform) (D-23 to D-27, D-33) |
| 10 | Tool pods, GPUs and local LLMs | **DECIDED** 2026-10-06 (Tom) | [DESIGN-001 section 8](designs/001-dev-env-v2.md#8-tool-pods-gpus-and-local-llms) (D-28 to D-32) |
| 11 | Q-06: GPU placement for agents | **DECIDED** 2026-10-06 (Tom) | Dynamic allocation, not a static per-node rule: a VRAM budget per card, control-plane cards included (D-34, narrowed by Q-09); satellite inference workers on his Mac and PCs (D-35). |
| 12 | Q-07: do the root-equivalent credentials (Proxmox operator token, hw-ssh key) stay in every session pod? | **DECIDED** 2026-10-06 (Tom) | A: they move behind the broker as short-lived credential grants. |
| 13 | Q-08: how does every household pod get a CPU request? | **DECIDED** 2026-10-06 (Tom) | A: a Kyverno-generated LimitRange with a 50m default CPU request in every non-system namespace; a cluster-wide v1 fix in haynes-ops, merged and live 2026-10-06 (#3406). |
| 14 | Q-09: which household GPU apps may lend their burst VRAM to agents while idle? | **DECIDED** 2026-10-06 (Tom) | None: "None but I bring online more GPUs in cluster". Agents get only what is left above every household app's full reservation; new cards join the budget automatically (D-34). |
| 15 | Q-10: when may agents use Tom's satellite machines? | **DECIDED** 2026-10-06 (Tom) | A: only while awake and not in use by Tom; never woken. |
| 16 | Ratify ADR-001 | **DECIDED** 2026-10-06 (Tom: "Accept as written") | [Ratification summary](adrs/001-distributed-dev-env.md#ratification-summary) |
| 17 | Q-11: which link that survives restarts did Tom mean? | **DECIDED** 2026-10-06 (Tom) | The Claude Code auth (the Max `/login` on the PVC). The keeper is its sole owner; pods get access tokens only; the monthly renewal is a console page behind Authentik, replacing the chat relay; the console lists every session's link and status with an archive button; the codex hub keeps its own single enrolment (D-11, D-37). |
| 18 | Summoned sessions are a first-class requirement | **DECIDED** 2026-10-06 (Tom: "we need to preserve the functionality") | [DESIGN-001 3.7](designs/001-dev-env-v2.md#37-summoned-sessions), D-36, R-01 V-01 to V-17, plan 10 |
| 19 | Q-12: branch protection on this repo, if Tom's plan does not enforce rulesets on a private one | **DECIDED** 2026-10-07 (Tom) | B, public: "I made dev-env public so I can go to bed but make sure it's good and safe". Actions billing had stopped CI on the private repo. The ruleset is enforced free, and it is live since 2026-10-07 (laptop handoff part 3). [DESIGN-001 section 15](designs/001-dev-env-v2.md#15-open-questions). |
| 20 | Q-13: visibility of `ghcr.io/thaynes43/dev-env-operator` | **DECIDED** 2026-10-06 (Tom) | A, public: "Public package write a prompt for an agent on my laptop to flip it". A laptop agent flips it after B3's first publish ([handoff](../../handoffs/2026-10-06-tom-laptop-settings.md)). |
| 21 | Q-14: how release-please gets release PRs checked by CI | **DECIDED** 2026-10-06 (Tom) | A, a GitHub App key secret: "GitHub App key secret (Recommended)". Names for B4: variable `RELEASE_APP_ID`, secret `RELEASE_APP_PRIVATE_KEY`. The App also needs Issues read and write (release-please creates `autorelease:` labels). |
| 22 | D-38: the keeper as its own binary or a mode of the operator | **DECIDED** 2026-10-06 (agent, delegated by KICKOFF B1) | Its own binary, `dev-env-keeper`, shipped in the operator image; the broker stays a mode of the operator ([DESIGN-001 3.1](designs/001-dev-env-v2.md#31-components)). |
| 23 | D-39: what the AgentSession schema enforces, and which spec fields may change | **DECIDED** 2026-10-06 (agent, plan 01 step 1) | The per-session rules of 3.3 and 3.7 as CEL validations; spec immutable after create except `operatingMode` and `lifecycle`; per-caller rules (priority, fallback model, no `full` in a policy) stay in CallerPolicy's schema ([DESIGN-001 3.3](designs/001-dev-env-v2.md#33-the-agentsession-resource)). |
| 24 | D-40: how agentd reads its session, and the layout its `dev-init.sh` port keeps | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | One JSON document in `AGENTD_SESSION` (`internal/agentd/protocol`); pod settings default to v1's paths; Playwright browsers linked from the image; onboarding and trust seeded ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 25 | D-41: how agentd reports its status | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | `POST /v1/sessions/{name}/heartbeat` every 60 s with the pod's projected token; `agentd ctl status` prints the same document ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 26 | D-42: how agentd runs a task | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | Once per volume, under `agentd run-agent` in tmux session `agent`, prompt on stdin; the pod's SIGTERM forwarded to the CLI, which gets 30 s ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 27 | D-43: how `agentd ctl rescue` commits | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | v1's rules, committed through a copy of the index so the worktree stays as it was; it prints the refs origin lacks for step 5's bundle ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 28 | D-44: how the operator builds a session's pod and volume, and what the templates hold | **DECIDED** 2026-10-06 (agent, plan 01 step 2) | One bare pod and one volume per session from a strict `dev-env-templates`; placement, security and grace in code; no probes; never updated or deleted after create; a template change only marks the session `Outdated` ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 29 | D-45: what deleting or suspending a session does before rescue exists | **DECIDED** 2026-10-06 (agent, plan 01 step 2) | Deleting is a reap: a finalizer on the session and its volume holds both until rescue; one guarded function deletes pods, and a source test keeps it the only one ([DESIGN-001 5.1](designs/001-dev-env-v2.md#51-operator-broker-and-keeper-upgrades-never-touch-sessions)). |
| 30 | D-46: the `/v1` API of plan 01 | **DECIDED** 2026-10-06 (agent, plan 01 step 3) | HTTPS on 8443 on every replica, a TokenReview per request; callers by class (Tom, clients such as the workbench and the v1 pod, sessions by their pod's token); the token sets a session's parent, depth and profile; idempotency by label per caller; reap is a delete the rescue finalizer holds ([DESIGN-001 3.4](designs/001-dev-env-v2.md#34-the-api)). |
| 31 | D-47: how the `dev-agents` CPU and memory ceiling is enforced | **DECIDED** 2026-10-06 (agent, plan 01 step 8.6) | A Kyverno policy (`dev-env-require-cpu-limit`, haynes-ops #3470), not the LimitRange 7.2 first called for: a LimitRange makes the API server fill every unset limit with its default, so the "no CPU limit" rule could never fire. It denies a pod with no CPU limit, a CPU limit above 8, or a memory limit above 24Gi ([DESIGN-001 7.2](designs/001-dev-env-v2.md#72-size-classes)). |
| 32 | D-48: what `agentd ctl rescue` writes to the shared volume, and how it checks it | **DECIDED** 2026-10-06 (agent, plan 01 step 5) | One bundle per clone of its unpushed refs, thin against `origin/HEAD`, in `rescue/<session>/<stamp>/` with `manifest.json` last; verified, copied, read back and verified again before the report says `ok`; `--stop-agent` stops the CLI first ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 33 | D-49: what the session config ConfigMaps hold, and where Codex's `requirements.toml` goes | **DECIDED** 2026-10-06 (agent, plan 01 step 8.8a) | Four ConfigMaps in `dev-agents` from a v2 app in haynes-ops: v2 copies of v1's `mcp.json`, subagents, Codex config and `bashrc.sh` (no `GH_TOKEN` export), and a new `CLAUDE.md` for a session pod. `requirements.toml` keeps v1's floor, because bubblewrap still cannot run under the pod's seccomp profile, in its own ConfigMap mounted at `/etc/codex` in every pod ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 34 | D-50: `agent-run` v2 of plan 01 | **DECIDED** 2026-10-06 (agent, plan 01 step 7) | `-p`, `list`, `show`, `reap`, `fleet` with v1's verbs, flags and defaults; aliases and unhonoured effort levels refused before sending, from the API's own table; retries keep the idempotency key; the scheduler's reason printed at once; the API found in a session pod, minted for in any other pod, named by flags elsewhere; exit codes 0 to 5 ([DESIGN-001 3.5](designs/001-dev-env-v2.md#35-agent-run-v2)). |
| 35 | D-51: how the operator rescues, suspends and archives | **DECIDED** 2026-10-06 (agent, plan 01 step 5) | `agentd ctl rescue --stop-agent` by exec before a suspend deletes a pod that ran; the verdict in `status.rescue` before the delete; a resume supersedes it; only a reap archives, after a verified rescue of the volume's last pod; a rescue pod and the archive timer are plan 02's ([DESIGN-001 4.4](designs/001-dev-env-v2.md#44-rescue-before-reap)). |
| 36 | D-52: the minimal keeper | **DECIDED** 2026-10-06 (agent, plan 01 step 6) | The haynes-dev-bot token minted v1's way, from an App directory read at every mint, into `dev-agents/dev-env-gh-token` by one merge patch; every 40 minutes or two thirds of its life, retries 10 s to 5 minutes with jitter; one replica behind a Lease (plans 03 and 04 fence each refresh); Secrets `patch` only; ready while its token lives; nothing secret logged ([DESIGN-001 6.4](designs/001-dev-env-v2.md#64-github-app-token)). |
| 37 | D-53: how the agent image is built and published | **DECIDED** 2026-10-06 (agent, KICKOFF B5) | `images/agent/Dockerfile`; smoke-tested in CI on PRs; `dev-env:2.x.y` only, from a release tag by `publish-agent.yml`, signed keyless; Kyverno trusts it on `refs/tags/v2.*` ([DESIGN-001 D-53](designs/001-dev-env-v2.md)) |
| 38 | S-8: where size L's session volume lives | **DECIDED** 2026-10-07 (spike S-8) | `gasha01-rbd` was 1.55 to 1.81 times slower than `ceph-block` for a clone, `pnpm install` and one test file, under D-22's line of two, so every size stays on `gasha01-rbd` ([00-spikes](backlog/00-spikes.md#s-8-gasha01-rbd-against-ceph-block-phase-1)). |
| 39 | Who merges release-please release PRs | **DECIDED** 2026-10-07 (Tom: "Merge, and let agents merge releases") | Agents squash-merge a green release PR themselves, like any other PR, after checking the version and changelog are sane and `CI - Success` is green on its head. Replaces the earlier "Tom's call" rule in CLAUDE.md. |
| 40 | Pacing while the weekly plan quota is high | **DECIDED** 2026-10-07 (Tom, through the coordinator: "one plan at a time") | Plan 02 continues alone; plan 07 pauses after step 3 with step 4's WIP on branch `agent/plan07-install`, and resumes when the coordinator says so (the quota resets 2026-10-12). Plans 02 and 07 stay parallel in the plan, not in time. |
| 41 | Q-15: how a Proxmox credential grant gets its short-lived token | **DECIDED** 2026-10-07 (Tom) | A, the keeper mints it over SSH: "Keeper mints over SSH (Recommended)". With a certificate from its own SSH CA it runs `sudo pvesh create /access/users/dev-env@pve/token/<grant> --expire <end> --privsep 0` on a node and deletes the token at the grant's end. No new Proxmox user, no long-lived token in v2, port 22 from the keeper to the nodes. Unblocks plan 07 step 8 (paused, decision 40). |
| 42 | Resume plan 07 on Codex | **DECIDED** 2026-10-07 (Tom through the coordinator work order) | Codex resumes step 4, then builds egress grants and the operator expiry backstop, and deploys/verifies the broker. Plan 02 continues in parallel; each PR rebases on main. Step 4 installs kube grants in tmpfs with stdin-only, UID-fenced exec (D-63). Round 1 finished steps 4 and 5 (D-64) and verified H2 runtime, including real expiry with the broker stopped; temporary fixtures and the test session were removed. Human approval deployment follows step 6. |
| 43 | Q-16: parity first, approvals in the Claude Code app; dev-env v2 benched | **DECIDED** 2026-10-08 (Tom, from his phone) | No new guard may cut what agents do today (no Authentik, outpost, Traefik or postgres16 lockdown, no git review or CODEOWNERS gate); standing auto-approved grants cover the whole v1 capability set, and human approval gates only capabilities beyond it. Approvals happen inside the Claude Code app, not Pushover plus a web page. Plan 07 step 6 is redesigned; PR #90 and haynes-ops #3550 were closed with pointers (issue #91). v2 is benched from 2026-10-08 until the plan usage resets (the Claude weekly limit resets 2026-10-12). Full text: DESIGN-001 Q-16. |
| 44 | Resume v2 with the Codex coordinator | **DECIDED** 2026-10-08 (Tom's work order) | Codex resumes after its usage reset. Finish plan 02 laptop access and acceptance first, then the docs-only Claude Code approval spike, the v1 capability parity check, and keeper SSH minting. Preserve Q-15 and Q-16; use GPT-6.1 Sol subagents, one task worktree each, and ask Tom one question at a time. Laptop access follows D-68. |
| 45 | Session access follows Tom's current workflow | **DECIDED** 2026-10-08 (Tom, correcting Q-17) | Use the `agent-run` CLI or ask agents to start sessions; a web UI is another possible session-management client. No laptop kubeconfig ceremony or laptop test blocks plan 02. Validate the existing in-cluster CLI workflow instead. D-68 remains an optional external path whose real external-machine use is unverified. This supersedes decision 44's laptop acceptance requirement and preserves the remaining priority order and Q-16. |
| 46 | Effective v1 parity includes Headlamp access | **RECORDED** 2026-10-08 (coordinator fact-check prompted by Tom's question; D-70) | Agents already use Headlamp's cluster-admin identity. Secret reads, drains, snapshots and broad workloads are existing reachable powers, not enhancements merely because the direct OPERATOR role lacks them. Parity preserves accepted tasks through an equivalent route, including Tom's live directive for Headlamp work; this is not blanket standing admin access or a new owner ruling. Temporary identities, expiry, attribution and an approval workflow improve the access mechanism. No blanket direct admin grant or approval route is selected; the coordinator withdraws Q-18's earlier premise. |
| 47 | Retire Headlamp after guarded parity | **DECIDED** 2026-10-09 UTC (Tom, D-71) | Guardrails may be necessary parts of the replacement. Replace Headlamp with guarded access preserving accepted v1 tasks and existing owner rules; prove parity and guardrails, migrate callers, then retire it through GitOps. Headlamp is a migration fallback, not the final architecture. No specific approval implementation or blanket standing admin grant is selected. |
| 48 | Reuse existing 1Password item for keeper CA | **RECORDED** 2026-10-08 America/New_York (Tom's correction, D-72) | Use `HaynesKube/dev-env` with the same two fresh CA fields. Current ExternalSecrets select individual properties; the CA will use a separate keeper-only Secret. Tom confirmed the fields saved; Q-19 is complete by owner confirmation. CA projection, node trust and real acceptance remain pending. |
| 49 | Workflow guide, Codex project homes and current source | **REQUESTED** 2026-10-09 America/New_York (Tom) | Before project finalization, provide a detailed high-level Markdown quick start and feature set with diagrams. Discuss Codex session management, v1 `/work/codex` project folders and protection from stale shared repositories. The initial `/work/codex` shorthand and app-choice premise were corrected by the supplied R1–R7 requirements below. Q-20 is superseded. The guide draws working and requested paths separately; no project/client rollout or new approval route is claimed. |
| 50 | Joint projects and multiple Codex remote pods | **REQUIRED** 2026-10-09 America/New_York (Tom) | Supplied R1–R6 define GitOps catalog, both providers, common/task rules, persistent clean-refreshed roots, task-only sweep and lossless canonical repair. R7 adds multiple Codex remote links spanning pods and sharing workspace files. R2 supersedes Q-20. D-74 requires revision of single-hub/private-Git assumptions. Operator API/controller is the built orchestration service; a coordinator agent is a requester role. Management surface/storage backend remain under workflow review; no new implementation is selected. |
| 51 | Shared-workspace topology and bounded CephFS trial | **ACCEPTED** 2026-10-09 America/New_York (Tom, Q-21/D-75); **SUPERSEDED** 2026-10-10 by decision 56 (ADR-003) | Structured answer: "Accept ADR-002 and the bounded CephFS trial (Recommended)". Shared project/reference/task files, private provider homes, multiple remote hosts and explicit ownership supersede affected ADR-001/D-15/D-22/D-12 decisions. Trial first; normal rollout needs acceptance. No v1 restart, cutover or management-app selection. Superseded by decision 56 and [ADR-003](adrs/003-session-coordination-private-repositories.md): shared Git/RWX and its trial no longer gate delivery; #130 closed as not planned. |
| 52 | Keeper-only saved CA projection | **SHIPPED** 2026-10-09 America/New_York | Haynes-ops #3642 (`4bccaa899`) projects the existing private/public fields into the keeper-only read-only mount. ESO and keeper Ready; minting flags false. Key-pair validation, owner node trust/private configuration/provider acceptance still required; all twenty parity rows remain open. |
| 53 | Keeper node trust delegated to Codex | **DELEGATED** 2026-10-09 America/New_York (Tom, Q-22/D-76) | Structured answer: "Delegate the trust installation to Codex". Review public-key-only delivery, preserve the existing account/key, verify v1 access per node and clean temporary resources. Private CA stays keeper-only; no sudo/sshd changes, node restart, token minting or enabling is included. Installation and certificate/provider acceptance require separate evidence. |
| 54 | Keeper PVE node trust delivered | **SHIPPED** 2026-10-09 America/New_York | Haynes-ops #3647/#3651 installed the saved public CA on all five PVE nodes and removed temporary delivery resources. Exact original key bytes and standing v1 access preserved; private CA keeper-only, minting off. Certificate/pair/provider acceptance remains open. |
| 55 | Task budgets and stalled-work escalation | **ACCEPTED** 2026-10-09 America/New_York (Tom, Q-23/D-84) | Structured answer: "60 minutes without progress or 3 failed attempts at the same blocker". History carries across agents, pods and resumes; children count toward their parent's budget. Stop, preserve work and ask one native phone question before further attempts. [Budget contract](requirements/2026-10-09-task-budgets.md). Ledger/stop foundations later shipped in 2.11.0; complete campaign enforcement and phone acceptance remain open in #154. |
| 56 | Owner scope correction | **ACCEPTED** 2026-10-10 America/New_York (Tom, D-85/ADR-003) | Share rules, coordination and authorized live/stopped context; keep per-pod repos and task worktrees. Five outcomes: safe upgrades, distributed load, guarded access, session visibility, model usage/cost and later local LLMs. Supersedes mandatory shared Git/RWX and its trial. Pilot acceptance differs from full v1 retirement; no runtime change or storage retry is authorized. |
| 57 | Q-24: Codex native routing for the MVP | **ANSWERED** 2026-10-18 UTC | Tom approved small separate native hosts with independent computer entries and pairing. [DESIGN-006 Q-24](designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing) records the ruling, test-model bounds and unchanged runtime/access holds. Usable native lifecycle acceptance remains open. |
| 58 | Q-25: local schema-host DNS route | **NO DECISION NEEDED** 2026-10-11 UTC | The failed local schema-host DNS route stopped after three attempts; the owner said he did not understand the question. Shipping uses the already-authorized hosted CI and saved real-API schema evidence. No DNS investigation or retry ([installation record](verification/installation-2026-10-11.md#owner-questions-and-stopped-checks)). |
| 59 | Q-26: one final activation-checker correction | **DECIDED** 2026-10-11 UTC (Tom) | "Allow one correction (Recommended)". One final R4 correction ran and Ops #3824 activation was accepted; the three failed preparations stay preserved. No R5, alternate method or retry ([fixed-rule activation record](verification/fixed-rule-activation-2026-10-11.md), [DESIGN-021](designs/021-canary-cli.md)). |
| 60 | Q-27: one final Codex reader-preparation correction | **DECIDED** 2026-10-12 (Tom, through the Claude native prompt) | "Fix once, then read (Recommended)". One offline correction with a fixed list of already accepted evidence, focused tests and independent review; then exactly one named read and one result read of the Codex original. No retries and no counter reset ([Codex probe record](verification/codex-fixed-rule-probe-2026-10-12.md#q-27-one-final-reader-preparation-correction)). |
| 61 | D-86: `agent-run` exit 6 for unconfirmed one-shot requests | **DECIDED** 2026-10-12 (agent, #174 review findings history-06 and its follow-up) | Amends D-50 (row 34), whose codes ran 0 to 5. A one-shot request (`msg`, `canary create`, a `--once` read) that was sent and got no confirmed answer (a lost answer, stop, any 5xx, an unreadable receipt) exits 6 and is not repeated; a message or create may have taken effect. It exits 5 only when nothing was sent: no token, no port-forward, or a connection that was never made. A 429 to one exits 1 ([DESIGN-001 3.5](designs/001-dev-env-v2.md#35-agent-run-v2), [DESIGN-021 amendment](designs/021-canary-cli.md)). |
| 62 | Q-29: coordination record retention | **DECIDED** 2026-10-12 (Tom, through the Claude native prompt) | "Archive, then delete (Recommended)". A TaskClaim, PublishedContext or InboxMessage (with its ack) is released 30 days after the Sessions it depends on are first proved absent by uncached reads. It is archived first (full object and SHA-256 manifest, on a new RWO volume only the shelf pod mounts, kept indefinitely); a leader-only operator runnable verifies the archive and deletes with UID and resourceVersion preconditions. Reads fall back to the archive (`archived: true`, 503 without a shelf). Per-Session bounds stay lifetime, project bounds count live records, 80% alert. Amends DESIGN-010's no-release rule in the reap case only. Known gap: private Sessions are never absent until a HOME expiry policy exists (DESIGN-008), plan 11 follow-up F1 ([ADR-004](adrs/004-coordination-record-retention.md), [DESIGN-024](designs/024-coordination-record-retention.md)). |
| 63 | Q-28: one new Claude rule canary | **DECIDED** 2026-10-12 (Tom, through the Claude native prompt) | Question: "Codex's canary passed: it loaded both the common and the repository rules in its private pod. Claude's single canary only returned a weekly-limit error, so Claude's rule loading in a v2 pod is still unproven, and its ID is used up. May I run one new Claude canary? It needs a new fixed ID in the operator config in haynes-ops (a rolling operator update that keeps running sessions), then one create, one named read and one result read, with no retries." Options: "Yes, after fixes deploy (Recommended)", "Yes, run it now", "Not now". Answer: "Yes, after fixes deploy (Recommended)" (owner answer receipt `daadc6b5`). One new canary `rule-canary-claude-1012`, added through a reviewed haynes-ops PR and run only after the review fixes deploy; exactly one create, one named read and one result read; no retries, no reuse of the 1011 ID, no quota or credential repair, no paid or model fallback, no cleanup. Carried out 2026-10-12 after v2.24.1 deployed: both markers returned and Root acceptance `88909059` accepted them ([Claude canary 1012 record](verification/claude-canary-1012-2026-10-12.md)). |
| 64 | Q-31: private session HOME expiry | **DECIDED** 2026-10-12 (Tom, through the Claude native prompt) | "Archive, then delete (Recommended)". Closes ADR-004's known gap (plan 11 F1). A private Session (no shared workspace, not a canary) that is Reaping, or Suspended past its archive deadline, is stopped, and the operator records the executor Pod UID's termination in status before the Pod can vanish: a retained Pod is not a durable stop proof, and absence alone never proves stop. 30 days after that record: Git rescue in a hold pod bound to the HOME UID, provider history (an allowlist, refused if a credential is in the tree) and Git bundle into a second shelf-only RWO archive volume with a `Retain` PV, readable under DESIGN-013's payload scope (`archived: true`); then the operator's leader deletes the HOME through the guarded path and releases the Session, and ADR-004 releases its records. Canary originals never expire. Owner-only hold and release-now. Deletion stays off until the archived read is accepted (#174) ([ADR-005](adrs/005-private-home-expiry.md), [DESIGN-025](designs/025-private-home-expiry.md)). |
| 65 | Q-30: private-repo canary on the Sigo alumni project | **DECIDED** 2026-10-12 (Tom, through the Claude native prompt, free-text answer) | Question: "To close #173 I need one more Claude canary on a private repo, proving private GitHub access and a fresh pinned task worktree. The dev bot can't create repos on your personal account. Could you create an empty private repo thaynes43/dev-env-private-probe (a README on main is enough)? Global private admission stays off; the canary reuses the spent 1012 slot." Options: "I'll create it (Recommended)", "Use an existing private repo", "Not now". Answer: "All the Sigo alumni repos are private and all are needed for dev-env, we use them now. Why can't you use those?" Interpretation: use the existing private repositories `thaynes43/sigo-alumni` and `thaynes43/sigoalumni-org` instead of a new probe repository, declared as v2 catalog project `sigo-alumni`, and run the one approved private-repo Claude canary `rule-canary-claude-sigo-1012` against it: one create, one named read, one result read, no retries. Global private admission stays off; repository visibility is unchanged. Owner answer receipt `root-q30-owner-answer-r1.json`, sha256 `1490331885be14f5939fe85c81d91f186274dac089286e2343ca373bdd0b3722`. GitOps in haynes-ops [#3845](https://github.com/thaynes43/haynes-ops/pull/3845). Run on 2026-10-12: bootstrap `Partial` on a tracked symlink, so not accepted; fixed in [#237](https://github.com/thaynes43/dev-env/pull/237) ([DESIGN-011 Q-30 amendment](designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173)). Root acceptance `dd5dee24`: items 1 and 3 to 7 hold and item 2 (bootstrap) fails, so the run is not accepted for #173 under DESIGN-011's gate; the Root accepts private GitHub authorization and a fresh task pin separately, as the named deviation `partial-acceptance-by-property` ([Sigo private-repo canary record](verification/sigo-private-canary-2026-10-12.md)). |
| 66 | D-87: corrections to ADR-005 after its review | **DECIDED** 2026-10-12 (agent, review findings of #233) | Amends row 64 by [ADR-006](adrs/006-private-home-expiry-corrections.md); narrows it and adds no authority. The operator stops a private executor only for a Reaping Session with no owner hold, after a confirmed stop intent; a Suspended one is never stopped, so the 72-hour idle timer and the 168-hour archive deadline can no longer kill a live Session nobody asked to end (a stop at that deadline needs its own owner question, plan 11 F3). The archives are not private against v1's OPERATOR tier, the broker or the keeper; an admission guard (H7, or R5 if first) proposed closing exec and archive mounts to every ServiceAccount but the operator, after a parity check; row 70/ADR-007 supersedes its v1 narrowing while retaining v2 protection. The terminal record sits beside `lastExecutor`; a once-only missed marker; a fifth guard exception for the expiry's hold pod; every HOME delete attempt re-verifies the archive; edit snapshots and other copies of user files, and unclassified agentd names, are not exported ([DESIGN-025](designs/025-private-home-expiry.md)). |
| 67 | D-88: closed-set reason codes for private catalog bootstrap | **DECIDED** 2026-10-12 (agent, after the Q-30 canary's undiagnosable `Partial`) | Amends DESIGN-006 and DESIGN-007; adds no authority. Each repository refresh records a step and a reason, each project root a reason with the member repository and its clean-checkout cause, and a Failed catalog a reason, all from closed sets. The `project-bootstrap` step's first note summarises the items that are not Ready, and `status.agent.projectBootstrap` (CRD enums, name pattern, catalog-limit MaxItems) and `agent-run show` carry them. Only catalog names, states, steps and codes leave the pod: no path, object id or detail text. A root whose member reference is Preserved is still refreshed, because skipping it would cut capability. haynes-ops gets the CRD copy before the operator pin ([DESIGN-006, reason codes](designs/006-private-catalog-bootstrap.md#reason-codes-diagnosis-without-exec)). |
| 68 | Q-32: one Sigo rerun canary after the 2.25 deploy | **DECIDED** 2026-10-12 (Tom, through the Claude native prompt) | Question: "The Sigo canary proved private GitHub access and a fresh pinned worktree, but catalog bootstrap ended Partial: one Sigo repo tracks a symlink our checkout check wrongly refused. The fix is merged and deploying now as 2.25. May I run one more Claude canary on the Sigo project after the deploy, to prove bootstrap reaches Ready and the new status codes work? One create, one named read, one result read, no retries." Options: "Yes, after deploy (Recommended)", "Not now". Answer: "Yes, after deploy (Recommended)" (owner answer receipt `root-q32-owner-answer-r1.json`, sha256 `48ab104f…`). After the 2.25 deploy is post-checked, one new Claude canary record for project `sigo-alumni` through a reviewed haynes-ops PR, replacing the consumed `rule-canary-claude-sigo-1012` (`codex-1011` byte-identical); exactly one create, one named read and one result read; no retries; global private admission stays off; no reuse of a consumed ID, second rerun, credential repair or cleanup. Item 2 also checks `status.agent.projectBootstrap` (row 67). Carried out 2026-10-13 UTC as `rule-canary-claude-sigo-1012b` (haynes-ops [#3855](https://github.com/thaynes43/haynes-ops/pull/3855)): all seven DESIGN-011 items hold and Root acceptance `a3aa0b7d` accepts #173's private-repository evidence ([Sigo rerun record](verification/sigo-rerun-2026-10-13.md)). |
| 69 | Pacing while the weekly plan quota is shared | **DECIDED** 2026-10-13 (Tom, through the Claude native prompt) | Question: "Today's dev-env push has used a lot of Opus subagent time: about 40 reviewed PRs, 4 deploys and 4 canaries. The Max plan is shared with your own Claude use, and hitting the weekly limit stops every session, yours included. How hard should I keep pushing after H5/H6 finish?" Options: "Slow down (Recommended)", "Keep full speed", "Pause after H5/H6". Answer: "Slow down (Recommended)": one Opus subagent unit (`claude-opus-5-5`) at a time, Sonnet (`claude-sonnet-5-5`) for routine work, no parallel Opus waves until Tom says otherwise. It limits concurrency, pauses nothing and changes no other gate. Like decision 40, it governs pace, not scope. Owner answer receipt `root-pacing-owner-answer-r1.json`, sha256 `a70a80cc…` ([HANDOFF rulings](../../HANDOFF.md#rulings), [2026-10-13 brief](../../CLAUDE-RESUME-2026-10-13.md#rulings-q-24-to-q-32-and-pacing)). |
| 70 | Q-35/D-89: preserve v1 during archive migration | **RECORDED** 2026-10-13 through oversight; **DIRECTLY CONFIRMED** at 21:33 UTC | Relay: "v1 must remain the same until it’s decommissioned after the MVP has been accepted". Direct asynchronous answer: "Keep v1 trusted during migration (Recommended)". Preserve v1 behavior, configuration and current access; accepted MVP precedes separately approved decommissioning. [ADR-007](adrs/007-preserve-v1-archive-trust-boundary.md) amends row 66's v1 archive narrowing: H7 protects untrusted v2 paths, preserving trusted v1 and legitimate controller descendants. No new privilege, isolation from v1, archive activation or automatic decommissioning follows. Original relayed turn remains unobserved; direct confirmation observed. Q-36 unchanged ([DESIGN-025 section 20](designs/025-private-home-expiry.md#20-h7-archive-trust-boundary-q-35)). |
| 71 | Q-36/D-90: existing Mac evidence and reader hold | **RECORDED** 2026-10-13 (owner answer relayed through oversight) | "Go for it" for holding the new reader and collecting pre-use workload/guard/webhook evidence through existing authorized Mac access. The relay arrived after #3904 had deployed and its single automatic Job had failed; installed grants have no withdrawal ruling. Hold further reader access/deployment, preserve the failed attempt, and diagnose the source mismatch through bounded existing reads. No undo, retry, credential export, mutation or later activation follows ([DESIGN-025 section 21](designs/025-private-home-expiry.md#21-existing-access-evidence-and-hold-q-36), [failed-attempt checkpoint](verification/h7-reader-failure-and-existing-access-2026-10-13.md)). Original owner turn and native phone delivery unobserved; no new ADR. |
| 72 | Q-37/D-91: one-time history-PV Retain delegation | **RECORDED AND EXECUTED** 2026-10-13 through the source Mac conversation | Tom answered "Yes" to one fixed Delete-to-Retain change for `dev-agents/dev-env-history-archive`'s bound PV, using existing Mac access. Source-corroborated approval relay; original user turn not directly exposed to Root. One verified executable launch, GET/PATCH/GET all 200, exit 0, same bound PV/claim verified Retain at 19:40:32 EDT (23:40:32.254177 UTC). Root and independent saved-evidence review accept only the PV gate. The attempt is consumed: no repeat, new right, v1/reader change, mount, flag or other activation ([DESIGN-025 section 22](designs/025-private-home-expiry.md#22-one-time-pv-retain-q-37-d-91), [acceptance](verification/h7-retain-acceptance-2026-10-13.md)). |
| 73 | D-92: shared history for trusted agents | **RECORDED** 2026-10-13 EDT through the Mac coordinator's owner clarification relay; original owner turn not directly observed by Root | All Tom's trusted dev-env agents may read shared history. Positive peer retrieval is the parity target; denied reads between trusted agents are not a gate. Preserve authentication, credential exclusion, integrity, write/delete protections, public/anonymous/external refusal and unchanged v1. No silent new identities, RBAC or trust expansion. Identify the concrete history-only authorization diff before execution. No question ID or new architecture/review project ([requirements and bounded plan](requirements/2026-10-13-shared-agent-history.md)). |
| 74 | Interim pod-owned login slice for Remote Control | **Superseded** 2026-10-19: the shared keeper-owned login (Q-11, delivered and live) is the model; the pod-owned slice was used once, for the isolated T08 test, with Tom's approval Q-110-02 | Source support only: a `remote` session runs `claude --remote-control` on a Claude login the pod's own CLI owns, set up only by an explicit coordinator request (`agent-run login`). The keeper-owned login (decision 17, Q-11) is delivered; the console stays in plan 03 ([backlog/03](backlog/03-remote-control.md#interim-pod-owned-login-slice-2026-10-19-delivered-then-superseded)). |
| 75 | Q-110-03: how Claude MVP acceptance is checked | **RECORDED** 2026-10-19 (Tom, as relayed in the Story110 T09 increment 4 work order; the Q-110-03 record lives in the coordinator's order files, not in this repo) | Claude MVP acceptance checks are ordinary repeatable tests. Approvals are needed only for new credentials, RBAC or trust. |
| 76 | Q-110-04: private-history retention for task sessions only | **DECIDED** 2026-10-19 (Tom, through the Claude native prompt) | "Exempt interactive (my pick)". Only a task Session is newly marked with private-history retention; remote and local Sessions suspend by rescue, pod delete and resume on the same HOME. Sessions already marked stay protected and canaries are unchanged. Amends the scope of DESIGN-008 and ADR-006 section 1 ([ADR-008](adrs/008-private-history-retention-for-task-sessions.md)). |
| 77 | Q-110-07: a v2-native identity for login, pair and attach | **DECIDED** 2026-10-19 (Tom: "build it", as relayed in the Story110 T24 work order; the question record lives in the coordinator's order files) | A `workbench` profile may run its sessions as `dev-agents/dev-env-workbench` (pods get/list, `pods/exec` create in `dev-agents`; no Secrets, nothing cluster-wide), so a session can run `agent-run login`, `pair` and `attach` without the v1 pod. Amends D-19 for that one profile; the accepted reach (exec into any session pod, so its credentials and grants) is stated in DESIGN-001 6.11. |
| 78 | v1's code-server after the cutover | **DECIDED** 2026-10-20 (Tom, through the Claude native prompt) | "I think we should do something other than code server. A proper front end that comes later, for now I can exec into the pod and use the terminal". No code-server in v2 and no workbench Deployment for it; v1's code-server goes with v1. A proper front end is later work (plan 13). Until then Tom uses a terminal in a v2 session pod: `kubectl -n dev-agents exec -it <session> -c agent -- bash`, or `agent-run attach <session>`. Replaces plan 05's workbench Deployment. |
| 79 | v1's Cursor "My Machines" workers | **DECIDED** 2026-10-20 (Tom, through the Claude native prompt) | "Let them go offline". The two workers (`cursor-worker-v1`, `cursor-worker-dev-env`) stop with v1 and are not moved to v2. Cursor on v2, if ever wanted, is its own story. |
| 80 | When to cut over from v1 | **DECIDED** 2026-10-20 18:29 EDT (Tom, relayed to the coordinator by a peer session) | "We won't cut over tonight but tomorrow after sessions wind down and write handoffs for new agents." No v1 change on 2026-10-20. The cutover is planned for 2026-10-21, after the v1 sessions wind down and write handoffs, and runs only with Tom's written approval and his go ([plan 05 runbook](backlog/05-cutover.md#cutover-runbook-2026-10-21)). This row is not that approval. |
| 81 | The workbench gets v1's OPERATOR tier (NB-04 G-6) | **RECORDED** 2026-10-20 (coordinator of story CR-1020, under Q-16, row 43) | Existing authority, not a new grant. haynes-ops #4141 binds the same 10 write rules as v1's `dev-env-operator` (pod delete, exec, proxy, workload restart patches, Flux and ExternalSecret patches, Jobs, CronJob patches) and v1's `database` and `observability` PVC Roles to `dev-agents/dev-env-workbench`. Amends row 77's "nothing cluster-wide" and DESIGN-001's D-19 workbench amendment. The coordinator's classification, not a ruling from Tom: the 2.38.3 handoff had held G-6 for him, on the condition that exec into the keeper stay impossible, and that condition is not met (the workbench can exec into the keeper, as v1 can). The CR-1020 W1 review weighed it and recommended parity: the keeper's `app` image is distroless, and Job create is the larger path, which v1 has too. The `dev-env-system` fence (review F2) would reduce today's parity rights; it is held for Tom's decision and is not a cutover prerequisite. |
| 82 | Upgrade-shepherd exact schema names and offline numeric defaults | **RECORDED** 2026-10-23 (Sol, under repair UPGRADE-ROLE-A-REPAIR01-20261023) | Source-only interface for [PR 520](https://github.com/thaynes43/dev-env/pull/520). Schema object keys match canonical JSON names byte for byte before typed decode. A lone case-variant, a same-value collision, an escaped spelling of the same decoded key, and a Unicode case-fold alias are invalid. The rule covers the root, envelope, snapshot, prior attempt, nested objects, and objects in arrays. Label maps stay case-sensitive. Exact duplicate keys stay invalid. The checker does not rewrite keys or digests. Retain 64KiB, 32 entries, a 120s observation-age cap, and 5s tolerance as conservative offline fixture and validation defaults. They are not production SLOs, runtime authorization, deployed heartbeat changes, or a fresh human ruling. The existing 45m default, 8h Activity maximum, and 2h cluster cap remain facts of the existing API. The checker's 8h ceiling does not override a shorter activity, task, or authorization expiry and does not prove an Activity scope. A 3h synthetic named-service fixture is not permission for cluster maintenance. Trusted live scope and expiry binding remains later. [#176](https://work.haynesnetwork.com/work_packages/176) design acceptance is not implementation acceptance. [#98](https://work.haynesnetwork.com/work_packages/98) remains the parity anchor. Caller migration remains [#115](https://work.haynesnetwork.com/work_packages/115). [Role contract](../../../docs/upgrade-shepherd-role.md). [Plan 10 source step](backlog/10-summoned-sessions.md). |
| 83 | Story189 Packet A provider-allowance contract | **RECORDED** 2026-10-24 (source-only; review, CI, and merge stay with Sol) | Typed `providerallowance.v1` and `providerallowance.read.v1` documents live in `internal/providerallowance`. Trust is a caller-supplied registry and context. Task UsageVersion2 and Story109 stay unchanged. Packets B–F stay held. Native188 Cursor/Grok parity and Claude availability remain Unknown. This row does not accept Epic187 or a first-version collector. [DESIGN-030](designs/030-provider-allowance-contract.md). |
| 84 | Story189 Packet A repair 01 | **RECORDED** 2026-10-24 (source correction on PR 543; review, CI, and merge stay with Sol) | Closed objects reject non-canonical field names, including case and Unicode folds, at every typed boundary. Allowlist and reset-credit policy failures clear per-measure actionability. Typed validation applies the same core shape checks as decode. Native188 remains Unknown. Packets B–F stay held. This row does not accept Epic187. [DESIGN-030](designs/030-provider-allowance-contract.md). |
| 85 | Story189 Packet A repair 02 | **RECORDED** 2026-10-24 (source correction on PR 543; review, CI, and merge stay with Sol) | Alias sums reject two or more members of one group, including subset, superset, and overlap. Schema-invalid nulls are rejected and nil required slices serialize as arrays. Ambiguous projection rules are not counted. AllowOverLimit does not drop the lower bound. Reset-credit inventory cannot feed allowance measures or derivations. Availability conflict suppresses advice. Shared pools require trusted entitlement bindings. Native188 remains Unknown. Packets B–F stay held. This row does not accept Epic187. [DESIGN-030](designs/030-provider-allowance-contract.md). |
| 86 | Story189 Packet A repair 03 | **RECORDED** 2026-10-24 (source correction on PR 543; review, CI, and merge stay with Sol) | A valid alias group counts once when two or more of its members are present. A missing registered member does not turn those aliases into independent projections. One present member stays a scalar. Ambiguous projection rules stay uncounted. Native188 remains Unknown. Packets B–F stay held. This row does not accept Epic187. [DESIGN-030](designs/030-provider-allowance-contract.md). |
| 87 | Story189 Packet A repair 04 | **RECORDED** 2026-10-24 (source correction on PR 543; review, CI, and merge stay with Sol) | Date-time values use RFC 3339 lexical shape. Lowercase separators and a leap second at UTC 23:59 are accepted. One-digit hours, comma fractions, and offset hour 24 or minute 60 are not. Original spellings stay unchanged. Native188 remains Unknown. Packets B–F stay held. This row does not accept Epic187. [DESIGN-030](designs/030-provider-allowance-contract.md). |
| 88 | Story198 source-only delivery guard | **RECORDED** 2026-10-25 (source-only correction; review, CI, and the guarded merge stay with Root) | `scripts/delivery/deliver_dev_env_pr.py` checks haynes-ops `63f6b2a5fc54cde0f3df4b0a8456320f7ae12285`, SHA-256 `1e451ca00c80d176d8f08a142f40cb491b94e8936610389e7c61137db90a21e0`, HEAD, and checkout-wide dirtiness, then invokes that pinned `scripts/upgrade-agent/deliver_reviewed_pr.py` once with `--source-only`. The supported top-level invocation and nested helper/preflight use isolated Python interpreters. The wrapper delegates the authorized squash merge; it does not implement it, reconcile, release, or deploy. Evidence is a coordinator attestation, not a grant. `pendingHolds` must be empty and `writableImplementer` must be false. Action-specific holds stay in force. Native evidence provenance stays Unknown. | |
| 89 | Agent attestation for grants a policy names (DESIGN-001 entry numbered "D-88"; the number was already row 67's) | **RECORDED** 2026-10-27 (Tom, 23:27:01Z, Sentinel_df0db56dd3088191bab31733860248ce) | "No just accept what the agent says add guidance for the agent to seek coordinator or user approval first". Recorded in DESIGN-001 as the grant-attestation entry, which reused the number D-88 after row 67 (catalog reason codes, 2026-10-12) had it first. A GrantPolicy with `spec.attested` approves only when the requesting session also records its claim that the user or a coordinator approved: an agent attestation, not verified human identity. Default off. The numbering collision, and renumbering as a follow-up, are noted in DESIGN-001's hw-ssh entry. |
| 90 | hw-ssh lease production scope (DESIGN-001 entry numbered "D-89"; the number was already row 70's) | **RECORDED** 2026-10-28 (Tom, 01:37:41Z, Sentinel_fd1573472fec819197c80e8b20ab7143) | "Approved." Recorded in DESIGN-001 as the hw-ssh entry, which reused D-89 after row 70 (Q-35) had it first. **Amends row 12** (Q-07, 2026-10-06: the root-equivalent credentials move behind the broker as short-lived credential grants, with a CA certificate for hw-ssh): for hw-ssh, a lease of the existing static shared key is now approved for full-profile haynes-ops sessions, up to one hour per grant, under the agent-attested model (row 89), with the keeper's `get` on the one hw-ssh Secret and a first pilot to HaynesIntelligence on SSH port 22. The CA certificate stays the stronger follow-up. Row 12 is unchanged. |
| 91 | Opt-in Claude statusline producer bridge | **RECORDED** 2026-10-29 (source-only; review, CI, and merge stay with Root) | Install-ready `claude-statusline-usage` accepts one bounded native callback and an explicit private cache path, projects it through the fixed `/usr/local/bin/agent-run usage -o json --claude-statusline` consumer, and can stream the original bytes to an explicit display argv when capture stops. It installs no hook and does not change Render, settings, or provider freshness. [DESIGN-030](designs/030-provider-allowance-contract.md). |

The full options, consequences and rulings for Q-01 to Q-16, and the premise
corrections withdrawing Q-17 and Q-18, plus Q-19's first owner CA provisioning
step, are in
[DESIGN-001 section 15](designs/001-dev-env-v2.md#15-open-questions). Q-20 to Q-23
are there too. Q-24 to Q-27 link their own records in rows 57 to 60, Q-29
links ADR-004 in row 62, Q-28 links its result record in row 63, Q-31
links ADR-005 in row 64, Q-30 links its DESIGN-011 amendment in row 65, and
D-87 links ADR-006, which corrects ADR-005, in row 66, D-88 links its
DESIGN-006 section in row 67, Q-32 links its result record in row 68,
the 2026-10-13 pacing ruling is row 69, and Q-35/D-89 links ADR-007
in row 70. Q-36/D-90 records the existing-access hold in row 71; Q-37/D-91
records the consumed Retain delegation in row 72. D-92 records trusted shared
history in row 73; its owner requirement is separate from capture-r2 acceptance.
Q-110-04 links ADR-008 in row 76. Row 82 records the 2026-10-23 source-only upgrade-shepherd schema names and numeric defaults. Row 83 records the 2026-10-24 Story189 Packet A source contract. Row 84 records the same-day canonical-name, actionability, and typed-shape correction. Row 85 records the same-day alias, null, projection, range, inventory, conflict, and entitlement-binding correction. Row 86 records the same-day present-subset projection count. Row 87 records the same-day RFC 3339 date-time spelling check. Row 88 records the 2026-10-24 source-only adoption of the pinned haynes-ops delivery guard. Row 91 records the 2026-10-29 source-only opt-in Claude statusline producer bridge.


## Plan backlog

v1 stays live throughout. No v2 plan edits haynes-ops'
`apps/dev/dev-env/app/resources/**` (that restarts the v1 pod and every session in it).

| Plan | Depends on | Parallel? |
|---|---|---|
| [00: spikes](backlog/00-spikes.md) | nothing | yes, with Tom's answers |
| [01: foundation, task mode](backlog/01-foundation.md) (done 2026-10-07) | Q-01, Q-02, Q-04, Q-05 (all decided); spikes S-7, S-8, S-12 | |
| [02: interactive sessions and lifecycle](backlog/02-interactive-lifecycle.md) (done 2026-10-08 under Q-17's corrected scope) | 01 | with 07 |
| [07: access broker](backlog/07-access-broker.md) | 01; Q-07 (decided) | with 02 |
| [03: Remote Control](backlog/03-remote-control.md) | 02, 07's deployed broker core; R-03/Q-18 propose separating login/Remote Control core from management UI; spikes S-1, S-5, S-6, S-15 | |
| [04: rolling updates and Codex](backlog/04-rolling-updates-codex.md) | 02, Q-03 (decided); spikes S-3, S-4 | with 03 |
| [05: cutover from v1](backlog/05-cutover.md) (prerequisites verified 2026-10-20; cutover planned 2026-10-21 with Tom's written approval, decision 80) | 03, 04, 07; Q-08's LimitRange live in haynes-ops; Tom's approval | |
| [08: tool pods](backlog/08-tool-pods.md) | 02; spike S-10 | with 03, 04 |
| [09: GPUs, satellites and local LLMs](backlog/09-gpu-local-llm.md) | 08; Q-06, Q-09, Q-10 (decided); spikes S-9, S-11, S-13, S-14 | |
| [10: summoned sessions](backlog/10-summoned-sessions.md) | 02, 03, 07; spike S-16 | with 04, 08 |
| [06: later](backlog/06-later.md) | 05 | each item on its own |
| [11: rules, session context and private repo freshness](backlog/11-project-workspaces.md) | ADR-003; existing lifecycle/API/catalog foundations | independent units with bounded acceptance |
| [12: owned native host executor source seam](backlog/12-owned-native-host.md) (source only; production selection off) | ADR-003; D-84 (#154, #160) | independent units with bounded acceptance |
| [13: phone and browser management](backlog/13-phone-browser-management.md) (plan, 2026-10-20; restarts from v2 installed by haynes-ops #4123/#4129, keeper login from v2 in 2.38.0) | 03's delivered parts | with 07, only if a human approval route is needed |

**Story198 source-only delivery (2026-10-25 correction).** `scripts/delivery/` verifies the pinned haynes-ops guard, including checkout-wide dirtiness, and invokes it once for `thaynes43/dev-env` through isolated Python interpreters. Decision 88. The top-level supported invocation is isolated too; it cannot isolate imports already loaded by a non-isolated caller. No reconcile, release, deployment, or second merge attempt. Action-specific holds stay in force. The offline Story 177 handoff checker is unchanged and still does not merge.

**Pilot and full replacement.** First demonstrate a coherent coordination workflow:
independent bounded pods, common loaded rules, fresh task starts, peer discovery,
messages and readable stopped-session context. Complete native recovery/budgets,
guarded required access and truthful usage under their own acceptance contracts.
The frontend and local-model backend remain visible follow-on work.

Full v1 retirement is plan 05 and requires usable replacements for the owner’s
required workflows and explicit approval. The twenty-row historical audit is a
migration inventory, not a universal gate for every pilot. Tool pods (08), summoned
sessions (10), GPUs/local LLMs (09) and later items (06) retain their own scope.

*Historical: DESIGN-017 shipped in #199, and the current order is the pickup
brief's [six-step roadmap](../../CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap).*
Next bounded #174 directory contract: [DESIGN-017](designs/017-durable-session-directory.md); default-off prospective managed enrollment, independent metadata discovery and retained exact-source context references. Source, installation and live acceptance remain separate.

Client source/release update: #204/#206 are merged and v2.21.0 includes explicit
single-request Directory and ID-only canary clients. [Evidence and remaining acceptance](verification/clients-2026-10-11.md)
keeps bound build, publication, historical v2.20 installation and runtime gates
separate. Actual both-provider rules/pilot remain unaccepted. Q-26 now authorizes
ONE final focused correction with the three failed preparations preserved;
Q-24 routing remains pending. Claude reviews remain optional while unavailable.
*(Historical: Q-26's correction is complete and Ops #3824 was accepted; see
decision 59.)*
