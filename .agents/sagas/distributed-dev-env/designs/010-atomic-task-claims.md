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
*Amended 2026-10-12 ([ADR-004](../adrs/004-coordination-record-retention.md), Q-29): a separate namespaced delete on the
four coordination kinds, for the operator's retention runnable only
([DESIGN-024](024-coordination-record-retention.md)).*

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

*Amended 2026-10-12 by [ADR-004](../adrs/004-coordination-record-retention.md) (Q-29), in the reap case only. When the
owner Session's UID is absent, proved by uncached reads, and 30 days have
passed since that was first observed, the operator archives the claim and
deletes it. The task can then be claimed again by a new owner
([DESIGN-024](024-coordination-record-retention.md)). Every other case above keeps its claim: a missing name
whose UID is unproven, a stopped, suspended, deleting, Reaping or retained
owner. A transfer from a present owner still needs the stop contract. Private
Sessions are never absent until a HOME expiry policy exists (DESIGN-008), so
their claims are never released yet.*

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

## At the bound, recovery and rollback (2026-10-12)

These rules cover TaskClaims, published contexts
([DESIGN-013](013-published-stopped-context.md)) and inbox messages
([DESIGN-014](014-durable-offline-inbox.md)). They come from the review of
PR #223.

- **Lifetime bounds.** Records are immutable and nothing deletes them: the
  operator's Role grants get, create and list, and no delete. A Session that
  reaches its bound can create no new record of that kind. A project that
  reaches its bound refuses every new record of that kind from any Session,
  for good. Each refused create gets 429 `limit_exceeded` and stores nothing.
  A retry of an existing identity still confirms, a claim contender still gets
  409 with the owner, and reads, lists and read acknowledgements still work.
  Clients name the refusal and say not to retry: agentd `context-publish`
  prints a fixed message, and the inbox CLI prints the API's error. A managed
  canary output capture at the bound spends its one delivery attempt (DESIGN-013).
  *Amended 2026-10-12 (Q-29, ADR-004): once retention runs, a per-Session bound
  stays a lifetime bound, because a Session's records are released only after it
  is absent. A project bound counts live records, which retention frees 30 days
  after their Sessions are gone ([DESIGN-024](024-coordination-record-retention.md) section 7).*
- **Visible before it is hit.** At scrape time the operator exports
  `dev_env_coordination_records{kind,project}`,
  `dev_env_coordination_records_session_max{kind,project}` (the busiest source
  Session; records without a source label are not attributed) and
  `dev_env_coordination_record_limit{kind,scope}`, for each enabled kind. It
  counts with the same uncached, metadata-only lists that Create uses. When a
  count is unconfirmed, that kind's count series are left out of the scrape. A
  partial count is never reported. An alert on the ratio is a haynes-ops change,
  parked with retention below.
  *Now specified (Q-29): `DevEnvCoordinationRecordsNearLimit` at 80% of the
  project bound, DESIGN-024 section 7. It needs only these metrics, so it can
  ship first.*
- **Recovery until retention is ruled.** Only a cluster admin (Tom) can delete
  these records, with kubectl in the operator namespace. No agent or worker
  ServiceAccount has delete on these kinds, and none may ask for it. Before a
  delete, the admin:
  1. Exports every record to be deleted (`kubectl get <kind> -n <operator
     namespace> -l coordination.dev-env.haynesops.com/project=<project> -o
     yaml`) to private storage outside every repository. Records hold private
     text, and this repository is public.
  2. Keeps every record that is still in use. That means a TaskClaim whose owner
     Session still exists, since deleting it lets a second owner claim the work.
     It means a published context whose source Session still exists, and every
     managed-output publication of a retained canary original, which the fixed
     result reader reads. It means an inbox message whose recipient still exists
     and has not acknowledged it.
  3. Deletes a message's read acknowledgement together with the message.

  The next create counts again, so nothing needs a restart.

  *Retention is ruled (Q-29, 2026-10-12). This manual path stays, for Tom only,
  as a break-glass. It bypasses the archive, so step 1's export is the only copy
  of what it deletes. Prefer the retention runnable (DESIGN-024).*
- **Retention, parked and then ruled on 2026-10-12.** See plan 11, "Coordination
  record retention". While it was parked, weighting the project bound by bytes
  was considered and not adopted. An agent that sends full-size records reaches a byte bound after
  the same number of records, so a byte bound would not make a project harder
  to exhaust. It would only spare honest small records. In exchange it would
  need a size label and a scan of every record on each create.
  *Ruled 2026-10-12 (Q-29): archive, then delete. [ADR-004](../adrs/004-coordination-record-retention.md) has the
  decision and [DESIGN-024](024-coordination-record-retention.md) the contract. The byte-bound reasoning still
  holds; the record-count bounds keep their defaults.*
- **Rollback floor.** New records carry the source label (all three kinds) and
  the work label (TaskClaims). Operators released before this change accept
  only the project label (claims and contexts) or the project and recipient
  labels (inbox). They fail a whole page on any other record. So once the new
  operator writes one record in a project:
  - During a rollout, while old and new replicas both serve, a request that
    reaches an old replica gets 503 on that project's claim, inbox and context
    lists and gets. Creates on an old replica skip the bounds and the same-work
    check.
  - After a rollback below the first release that carries this change, those
    503s last until the operator rolls forward again.

  Nothing is lost, and every failure is closed. Once the new operator has
  written a record, do not roll it back below that release.

## Paths and acceptance

- `api/coordination/v1alpha1/`: GroupVersion, TaskClaim/List, bounded immutable
  schema and deepcopy; generated CRD under `config/crd/`.
- `internal/taskclaim/`: strict identity/provenance, atomic Create and uncached
  confirmation/read/list and bounded `ListOwned`.
- `internal/apiserver/session_taskclaims.go`: negotiated detail projection.
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

## Read-only session and Page join (OP322, 2026-10-31)

The first OP322 slice projects the existing immutable TaskClaim; it adds no
registry, CRD, writer or access grant. GET session detail opts in with
`X-Dev-Env-Session-Task-Claims: v1`, following #759's strict-reader negotiation.
Unnegotiated detail, list and mutation responses keep their old shape. The CLI
opts in only for `show` (including `show --once`), through a per-call
context marker; startup and Remote Control polling do not request claims. It
prints Unknown when an older operator omits the field.

The existing explicit project claim-read authorization still applies, including
the live caller fence. An unreadable/unconfigured store, unconfirmed project or
denied metadata scope yields `taskClaims.state: Unknown`, never an empty claim
assertion. An authorized read uses one uncached project-label list, bounded at
4096 records, including legacy claims without source labels. Continuation,
oversize or invalid records invalidate the whole observation; there is no page
loop or per-claim owner lookup. Only exact Owner.uid matches are returned. Name
reuse cannot inherit an old UID's custody. A complete read with no UID match
also stays Unknown (`NoJoinedClaims`): missing correlation is not proof of no
work. Available means at least one canonical claim joined.

Each item reuses the claim metadata projection: identity
(project/repository/taskKey), name/UID, creation time, actor and reported head
with its source time. Observed Session phase is metadata; stop state stays
Unknown. Neither TTL, exit zero, a missing owner nor claim identity establishes
success, approval or transfer. Stopped/deleting owners keep their claims.

DESIGN-031 section 13 carries the matching Page projection: its existing
namespace/name/UID claim reference and a structured `claimOwner` copied from
the same claim batch. It makes no additional owner reads. API/MCP preserve the
same claim UID. Production Pages readers remain unconfigured. Portal rendering,
activation and installed acceptance are outside this slice.

## Remaining acceptance

This establishes explicit atomic ownership, not an automatic launch gate or
full #175 acceptance. The next finite unit adds attributed durable offline
messages, recipient/read acknowledgements and supported one-time delivery
without duplicate prompt injection. Task-budget/history linkage and verified
handoff follow separate slices. Publish schema/operator/least-privilege role,
then run a reviewed disposable two-parent contention proof before claiming
installed coordination. Keep #175/#91 open; preserve failed native fixtures,
ledgers, current sessions and Q-24's pending routing/UX decision.


## In-session executable client (#339, 2026-10-15)

The pilot needs a supported caller, not a copied bearer token or an arbitrary
HTTP command. `agentd task-claim-create` reads at most 4096 UTF-8 JSON bytes from
stdin: `{"identity":{"project":"dev-env","repository":"dev-env","taskKey":"task:example"}}`.
Owner and provenance are never supplied by this command: the server derives the
original authenticated Session owner. A coordinator create is outside this
in-session interface. The existing HTTP ownership/retention contract is unchanged.

`agentd task-claim-list --project dev-env [--limit 1..100] [--continue CURSOR]`
returns one bounded page. `agentd task-claim-get --project dev-env --claim NAME
[--uid UID]` reads one exact claim; UID fences are optional and never inferred.
No archived-only switch, release, takeover or automatic continuation is added.

All three commands use the existing projected-token file and verified HTTPS/CA
transport, reopening the bounded token per request. They expose no URL, token,
owner, generic path or provider-file flag. No redirects or automatic retries.
Valid bounded JSON replies, including HTTP409 original-owner metadata and API
refusals, remain on stdout; a non-2xx reply exits1. Invalid inputs exit2 before
HTTP. Transport/TLS/oversized/malformed replies exit1 without token or response
content in diagnostic errors. No result implies old-owner stop or transfer.

Acceptance uses synthetic tokens and local TLS fixtures for exact intent/routes,
rotated projected identity, conflict and refusal preservation, no automatic retry,
and pre-request rejection of malformed or spoofed intent/path/query. Required CI,
independent native review and available advisory findings bind the final head.
This is source/test acceptance only. The task image must later contain the new
agentd binary before pilot execution. No runtime/API/provider call, admission,
auth repair, permission change or retention consumer activation is authorized.
Claims, live durable inbox/ACK and explicit published handoffs do not depend on
#337; its failed captures and native defects remain held for later retention.
