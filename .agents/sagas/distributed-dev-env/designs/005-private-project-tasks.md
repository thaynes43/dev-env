# DESIGN-005: Declared project tasks in private worktrees

**Scope:** the first source slice of [#173](https://github.com/thaynes43/dev-env/issues/173).
**Decision:** [ADR-003](../adrs/003-session-coordination-private-repositories.md).
**Owner:** Codex, 2026-10-10, America/New_York.
**Checkpoint:** publish this contract within 30 minutes of starting; then deliver
one tested, independently reviewed PR. The 60-minute stall/three-failure rule
continues across agents, methods and resumes.

## Problem and contract

Project admission currently requires an enabled shared workspace. That prevents
an accepted catalog from supplying rules to an ordinary private task. Removing
that dependency must also avoid starting from a stale local default branch or
fetching an existing clone with the wrong origin.

The API resolves the project and selected repository from its accepted catalog,
checks the existing clone-owner boundary, selects the declared default branch
and records the immutable project snapshot. A project request creates a private
session without assigning `spec.workspace`, even if legacy shared templates
exist. A catalog is rule/repository authority; it is not shared-storage authority.
Existing caller, profile, depth, restore, model and managed-Codex controls remain.

New private project dispatch also requires the explicit
`--enable-private-project-tasks` operator flag, disabled by default. Catalog
validation and historical idempotent returns still run; refusal occurs before
reservation or creation. This gate is independent of RWX and preserves the
existing coordinator/native-fixture/budget flags during source promotion.

Budget-bound managed dispatch still depends on shared task identity. Until that
budget path is separately adapted, reject a new budget-bound private dispatch
before reserving an attempt or creating a session. Existing idempotent returns
remain available. This slice does not weaken the budget controller to make an
unsupported combination appear usable.

Agentd accepts that snapshot with shared-workspace settings off. Before fetching
an existing selected clone, it verifies its root, common Git directory and all
origin fetch/push URLs against the accepted repository identity. A missing
selected clone uses the staged reference clone (DESIGN-006, amended
2026-10-12; it first used the legacy private clone). Before a new task,
fetch the declared branch explicitly and resolve its remote-tracking commit.
Create the task branch/worktree from that immutable commit and record its source
identity in `~/.agentd/task-source.json` (DESIGN-006). Administrative preparation disables replacement refs, local hooks and
fsmonitor so they cannot substitute the fetched tree. A failed fetch blocks a
fresh task.

Resume reuses the existing worktree, source provenance and saved project
snapshot. It preserves WIP and does not reset to a newer catalog or source.
Exact saved rule text from the preceding renderer is recognized and retained
for both providers; modified or unrecognized saved text is refused.
Shared ownership, storage-marker and writer-lock checks remain on their existing
guarded paths; this slice does not enable them or the legacy sync actor.

The private state directory retains the project snapshot and composed project
rules. Existing managed-provider injection supplies those rules alongside the
platform guard and configured instructions, with native repository discovery
preserved. Tests establish supplied arguments and saved revisions. Actual Claude
and Codex instruction loading requires separate provider acceptance.

## Affected paths

- `internal/apiserver/validate.go`, `sessions.go`, server/command flag wiring and
  admission tests: remove
  automatic shared-workspace binding while retaining catalog/caller authority
  and refusing unsupported budget-bound private dispatch.
- `internal/agentd` project task, clone and source-resolution paths and tests:
  permit private preparation, prove repository identity and pin fresh source.
- Existing `internal/projectcatalog`, controller snapshot transport and provider
  composition are reused. Change them only if the private path requires it.
- This design, plan 11, the workflow guide and handoff record the delivered source
  boundary and remaining acceptance.

## Acceptance for this PR

1. A declared private project task is admitted and prepared with shared features
   off. Missing catalogs, unknown projects/repos, owner/identity mismatches and
   unapproved default-branch overrides are refused.
2. A missing selected repo is cloned. A changed remote default branch is freshly
   fetched and pinned despite a stale local branch; a failed fetch prevents a new
   task. Existing references and other worktrees are preserved.
3. Saved rules and managed-provider inputs contain the accepted global/project
   rules and retain native repo instruction discovery. Source/rule revisions
   remain attributable; source tests make no real-provider acceptance claim.
4. Resume preserves WIP and its saved snapshot. Existing shared-path and
   authorization tests still pass.
5. Focused tests and required `CI - Success` pass on the reviewed PR head. Read
   the actual advisory outcome and handle findings; an unavailable reviewer is
   not a verdict and does not authorize an unbounded repair investigation.

## Delivery and next finite slice

No CRD/schema change, new credential, storage trial or running-session restart is
part of this contract. Publish images through the existing reviewed release
chain. Any GitOps promotion must verify current schema/admission and preserve
running pod identities; source merge alone is not deployment or provider proof.
Promotion leaves private-project admission explicitly disabled. Existing
managed-Codex, coordinator and budget flags retain their dependency contract.

#173 stays open. Subsequent finite slices cover catalog-wide local startup
bootstrap/refresh, permanent local project roots, remaining client entry points
and actual both-provider rule loading on two independent bounded pods. History
retention (#174), task claims/messages (#175), budgets (#154), native recovery
(#160) and usage provenance (#176) keep their own acceptance. Retired #130 and
haynes-ops#3737 remain closed historical investigations.

## Sealed two-session pilot admission (#341, 2026-10-15)

This source-only follow-up to #338/#340 adds no general admission or runtime
permission. `--private-project-pilot=JSON` defaults empty. It requires general
`--enable-private-project-tasks=false`, managed Codex, retained common rules,
private-history retention, the accepted catalog and project directory support.
Startup also requires existing `--enable-task-budgets` and exact
`--task-budget-namespace=dev-env-budgets` (the seal lives in that existing protected
namespace). Their existing coordinator-host/budget-worker configuration must
already be valid; this source does not provision or assign a new budget.
The API must be enabled with its concrete HTTPS URL, catalog clone owner
`thaynes43`, authenticated client `dev/dev-env`, Session namespace `dev-agents`,
and directory metadata scope `dev-env`. Removing any required flag fails startup.
Old canary records are unchanged and cannot be reused by this route.

The policy fixes project/repository `dev-env`, caller `dev/dev-env` (trusted
client only), provider `codex`, model `gpt-6-luna`, effort `high`, profile
`rule-canary-codex`, size S (at most 2 CPUs/4Gi), and task timeout 15m. On
2026-10-16 Tom directed this model: coordination tests use `gpt-6-luna` so they
stop spending `gpt-6.1-sol`. Two reviewed
prompts and keys select fixed retained Session names `private-pilot-v1-a` and
`private-pilot-v1-b`. Neither requests nor policy can add a third slot or change
those scope limits. No restore, native host, tool pool or child dispatch follows.
This is two actors of one provider, not both-provider parity.

Policy time is an absolute UTC notBefore/expiresAt interval of at most 30m,
never time since process start. The API checks it before source reads and again
before Create. The controller checks it before resource allocation and again
before its first Pod Create. Already-running pods are preserved when the window
ends, the feature is disabled, or the operator rolls. Admission expiry refuses
new starts; it does not kill an existing actor. Each task still has its 15m limit. The whole Pod independently has a 1200-second
active deadline, RestartPolicyNever and 5-second termination grace; a failed
container cannot restart into a native resume TUI. The whole-Pod cap leaves
five minutes beyond the task timeout for clone/setup and final reporting; setup
exceeding that margin still fails closed at the whole-Pod cap. Admission is rechecked after
directory/retention I/O immediately before a missing HOME Create.

A pre-provisioned immutable ConfigMap `dev-env-budgets/dev-env-private-pilot-v1`
contains only `policySHA256`. The policy pins its actual UID, plus catalog,
rules, common-rule, template and image revisions. Its digest excludes only the
seal UID (the object must exist before its UID can be pinned). The service uses
uncached GET only: no create, reseed, update or deletion of the seal. A missing,
replaced, deleting, unreadable or mismatched seal or changed source fails closed.
Changing flags does not change the retained original policy/UID of either slot;
any existing mismatched slot refuses admission of the other. No restart resets
relative time or consumed identities. Exact UTC clocks are the existing service
clock assumption; expiry is not a later rollback task.

Each slot has an independent permanent `dev-env.haynesops.com/private-pilot-retained`
finalizer and immutable policy/request annotations. Atomic Create on its fixed
name arbitrates replicas. A lost response reads only that name and checks the
original exact binding; no generated suffix, new key or replacement UID. A
completed slot remains consumed. Deleting either retained original intentionally
refuses both new-slot admission and replay (409), preserving both identities
without guessing through incomplete deletion. Its directory source projection
also refuses a deleting binding; previously retained directory evidence is kept. There is no finalizer-release, deletion
or rearming operation in this source. Existing private-history launch-intent
fences protect the executor; pilot markers, fixed names and retained finalizers
remain recognized even if a marker or configuration is missing.

Creation binds retained common rules and directory enrollment explicitly. The
controller independently rechecks live source/seal authority, original Session
UID/generation/spec binding and pinned template shape before a new Pod. Pilot
Sessions cannot create children through legacy, pilot or canary API routes.
Ordinary claims, durable inbox/ACK and explicit handoffs need no #337 consumer.
Archive release/grace/reclamation remains held behind its unresolved findings.

Acceptance is finite fake-clock/fake-reader tests: default off and wrong scope;
missing/replaced seal or drift; boundary expiry and time passing during I/O;
concurrent two-slot create/completed replay/unknown confirmation; controller
pending-start refusal with existing peer preserved; no child escape; fresh bound
common rules and directory enrollment. Native independent review and required
exact-head CI precede guarded source acceptance. An installed operator and task
image containing these accepted changes, reviewed concrete seal/config diffs and
explicit runtime approval remain separate. No activation/RBAC/auth/egress/archive
or v1 action is authorized by this design.

The directory's legacy `revisions.canaryPolicy` wire name carries the pinned
fixed managed admission policy digest for pilot entries too. Their explicit
`taskKey` prefix is `pilot:`, never `canary:`. A private retained pilot policy
record supplies the original catalog UID/revision and image/template metadata;
its prompts and rule bodies never enter directory projection. No schema change
or new provider enum follows. Pilot retention markers protect HOME even before
first executor reconciliation or after the opt-in flag is removed.

Pilot post-reservation uncached read failures reuse DESIGN-019 known non-attempt
tracking: a non-NotFound observation failure before any Pod Create records the
same intent as not-created. A later healthy reconciliation may resume that exact
intent once within the original policy window. Missing or changed authority stays
latched; no new slot, UID, deadline or direct runtime retry follows.

## 2026-10-16: sealed pilot schema compatibility and preflight (#347)

The first approved pilot was rejected before either Session existed. The handler
set commonRules and sessionDirectory without projectCanary, while the installed
CRD required that canary binding. Fake-client handler tests and typed template
checks did not execute Kubernetes CEL admission. The earlier assertion that no
CRD update was needed was wrong; retain that failed runtime evidence.

A separate immutable privatePilot spec binding records seal UID, policy digest,
common revision and slot. It is mutually exclusive with projectCanary. CEL keeps
the exact dev-env/Codex/Sol-high/S/15-minute/private-profile contract and matching
common rules plus directory enrollment; it does not open ordinary admission.
The existing live seal/policy/catalog checks, fixed names, expiry, no-children
and controller guards remain authoritative. MatchSession verifies this binding
against the retained policy, and any privatePilot binding marks a pilot even
when other markers are missing.

Before creating actors, the supported CLI --dry-run=server uses a dedicated
POST /v1/private-pilot/preflight endpoint. It never calls the create endpoint
and never falls back if the endpoint is unsupported. The operator builds the
same exact object, performs Kubernetes Create with DryRunAll, and confirms the
slot remains absent. A preflight may inspect a configured policy before or after
its window, but never authorizes execution or changes the window. Real creates
still require the actual open clock. Preflight refuses consumed slots and returns
an explicit nonpersistent result, not an executor identity or an access grant.

Install the reviewed matching CRD before an operator needing this field. An old
installed schema must refuse preflight; source/envtest success does not prove an
installed update. No rollout, pilot reopening, seal replacement, new window,
credential or security change is included in this source correction.


## 2026-10-16: distinct retained pilot cohorts (#350)

The original sealed policy remains bound to its expired window, image and template.
A new window cannot reuse or replace that immutable seal. Tom approved a separate
bounded retry for October 16, 18:00–18:30 UTC (2:00–2:30 PM EDT), with the same two
Codex Sol/high actors and existing access/resource limits. This source change
provides distinct retained identities; source acceptance alone opens no admission.

PilotPolicy gains optional `cohort`. Empty or omitted means the original v1 cohort:
its JSON serialization, digest, seal `dev-env-private-pilot-v1`, Session names and
idempotency keys are unchanged. A named cohort is 1–32 lowercase DNS-label characters
with alphanumeric endpoints; `v1` is reserved so it cannot alias the legacy cohort.
It derives one immutable seal `dev-env-private-pilot-<cohort>`, two retained Sessions
`private-pilot-<cohort>-a` and `-b`, and corresponding `c7-` prefixed keys. Requests
cannot select a cohort or supply names: only the reviewed operator policy selects it.
The policy digest includes a nonempty cohort and retains all existing source/window
pins. The existing immutable spec binding already contains that digest, so no new
schema field, caller permission or resource grant is needed.

Resolve reads only that policy's seal. API admission, replay, both-slot fencing,
controller validation and retained-record interpretation use the same derived names.
Legacy records always decode their original empty cohort; they are never rebound to
the currently configured policy. A session's retained policy determines its slot,
including slot B. Cohort names remain pilot identities even if outer markers vanish,
and typed bindings/finalizers continue the existing fail-closed recognition.

Every cohort still has exactly two slots. Third requests, cross-cohort keys or
records, missing/replaced/wrong seals and changed policy/source pins fail closed.
Consumed records stay consumed. No seal creation/reseed/delete, retry permission,
children, new provider, resources, RBAC, egress, auth or v1 action is added. The
supported operator `/v1/private-pilot/preflight` uses only DryRunAll and must pass
before any future approved actor create; direct Kubernetes create is not a fallback.
The already approved retry requires its own exact GitOps cohort record, actual UID,
reviewed policy and inverse, signed release and installed pins. The window never
extends: failed validation or a missed window aborts and closes admission.
