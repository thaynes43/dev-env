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
Old canary records are unchanged and cannot be reused by this route.

The policy fixes project/repository `dev-env`, caller `dev/dev-env` (trusted
client only), provider `codex`, model `gpt-6.1-sol`, effort `high`, profile
`rule-canary-codex`, size S (at most 2 CPUs/4Gi), and task timeout15m. Two reviewed
prompts and keys select fixed retained Session names `private-pilot-v1-a` and
`private-pilot-v1-b`. Neither requests nor policy can add a third slot or change
those scope limits. No restore, native host, tool pool or child dispatch follows.
This is two actors of one provider, not both-provider parity.

Policy time is an absolute UTC notBefore/expiresAt interval of at most30m,
never time since process start. The API checks it before source reads and again
before Create. The controller checks it before resource allocation and again
before its first Pod Create. Already-running pods are preserved when the window
ends, the feature is disabled, or the operator rolls. Admission expiry refuses
new starts; it does not kill an existing actor. Each task still has its15m limit.

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
completed/deleting slot remains consumed. There is no finalizer-release, deletion
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
