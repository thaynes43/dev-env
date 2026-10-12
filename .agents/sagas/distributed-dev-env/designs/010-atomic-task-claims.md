# DESIGN-010: Atomic explicit task ownership

**Scope:** first bounded #175 source unit. **Decision:** ADR-003.
Default off; no runtime/provider dispatch, pairing or fleet UX follows.

## Contract

Use a separate `coordination.dev-env.haynesops.com` API group with an immutable,
bounded TaskClaim. Existing workers can read ConfigMaps cluster-wide and every
resource in the old dev-env API group. Neither is a private store for the new
claim ACL. The new group is absent from worker grants. Give the operator only
namespaced TaskClaim get/create/list permission; add no Secret, worker, broad
RBAC or elevated runtime access. Use direct/uncached reads without an informer.

An explicit project, declared repository and logical task/issue key determine
one canonical identity. Do not infer semantic duplicates from prose. Catalog,
rules and source revisions are provenance, never part of uniqueness: changing
rules cannot create a second owner. A deterministic digest name retains the
full digest and verifies the complete identity to refuse collisions/foreign
objects. Do not use a Pod's 63-character name limit for this separate resource.

Atomic Kubernetes Create chooses one owner across independent parents. Bind
the original owner Session name/UID and authenticated actor, immutable saved
catalog/rules revisions and bounded source provenance. Derive these from
trusted Session/catalog authority, not caller-supplied provenance. Confirm
writes through uncached exact reads. Return 201 to the winner, 200 for a
confirmed same-owner retry and 409 with authorized existing-owner metadata to
a contender. An unconfirmed acknowledgement remains Unknown; it cannot change
the owner. Missing, stopped, deleting or reaped owners remain claimed. No
expiry, release, transfer or replacement is authorized without a future
verified old-executor stop contract.

New routes default off and have their own narrow authorization. Creation is
for an authenticated own Session or a freshly verified coordinator direct
child, with exact UID fencing. Cross-parent metadata discovery requires an
explicit project read scope. Legacy ordinary-session broad read/reap rights
grant no new claim rights. Retained/deleting owner metadata must remain readable
under the new read scope without permitting session mutation. Bound object
UIDs prevent a new Session with the same name from becoming the old owner.

Return bounded metadata and current observed owner state/uncertainty. Exclude
prompts, transcripts, rules text and authentication material. Heartbeat HEAD is
reported current HEAD; it is not a verified initial fetch pin. Keep initial
source pin Unknown unless a narrowly bound startup receipt proves it. Preserve
the task's identity for subsequent budget/history integration without
inventing counters or stop evidence.

## Same work, bounds and fences (2026-10-12)

These rules come from the #175 coordination review.

- **One owner across a repository rename.** The claim name still digests
  project, declared repository key and task key, so claims that already exist
  keep their names. A catalog revision can rename a repository key while the
  GitHub repository stays the same. A new session on the new key would then
  get a second deterministic name for the same `issue:N`, and a second owner.
  A test showed this happening. New claims now carry a work label: a digest
  of project, lowercased GitHub repository and task key. Before it creates a
  new identity, the store looks up that label. If the same work is already
  claimed under another key, it returns that claim unchanged, and the API
  answers 409 with its owner. Claims created before the work label carry the
  project label alone. That set can no longer grow, so the store also scans it
  (selector: project, and no work label), up to the project bound. The catalog
  refuses one GitHub repository under two keys at one revision, so Create stays
  atomic within a revision. Only two creates on the old and new keys that race
  while the catalog changes could both pass this non-atomic check.
- **Bounds.** A new claim is refused with 429 `limit_exceeded` when its owner
  Session already owns 512 claims, or its project holds 4096. Nothing is
  created. A retry of an existing claim, and a contender that gets 409 with the
  existing owner, are never refused. New claims carry an owner (source) label.
  Claims from before it count toward the project bound only. Counting uses
  uncached, metadata-only, label-selected lists under the operator's existing
  get, create and list grant. Concurrent creates can pass the same count by
  the number of racing creates.
- **Final fence.** The owner fence before Create compares the Session's
  binding (UID, generation and spec, deletion, labels, annotations, owner
  references and finalizers), not the whole resourceVersion. A heartbeat
  between the reads is no refusal. The reported HEAD then comes from the first
  read, and it stays labelled as a reported observation.

## Paths and acceptance

- `api/coordination/v1alpha1/`: GroupVersion, TaskClaim/List, bounded immutable
  schema and deepcopy; generated CRD under `config/crd/`.
- `internal/taskclaim/`: strict identity/provenance, atomic Create and uncached
  confirmation/read/list.
- `internal/apiserver/taskclaims.go`, API wire types/routes and dedicated ACL;
  operator scheme/settings/store wiring and focused tests.
- Later GitOps schema and namespaced Role promotion in a separate PR.

Prove contention through two API instances yields one owner; same-owner retry
and lost acknowledgement recover only that owner; operator reconstruction and
deleting/absent owners preserve the claim; replacement Session UID, foreign
objects and unauthorized create/read fail closed; full identity and bounded
provenance are immutable; and responses expose no prompt/rule/auth bodies.
Use focused fake-client and necessary real API schema checks, sequentially at
`nice -n 19`, `GOMAXPROCS=2`, `go test -p 2`. No burners, stress, wide/looped
tests, local image build or runtime trial. Required CI and native independent
review govern merge; Claude reviews are optional and currently unavailable.

## Remaining acceptance

This establishes explicit atomic ownership, not an automatic launch gate or
full #175 acceptance. The next finite unit adds attributed durable offline
messages, recipient/read acknowledgements and supported one-time delivery
without duplicate prompt injection. Task-budget/history linkage and verified
handoff follow separate slices. Publish schema/operator/least-privilege role,
then run a reviewed disposable two-parent contention proof before claiming
installed coordination. Keep #175/#91 open; preserve failed native fixtures,
ledgers, current sessions and Q-24's pending routing/UX decision.
