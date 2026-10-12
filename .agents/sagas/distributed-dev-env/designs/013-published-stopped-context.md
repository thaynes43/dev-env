# DESIGN-013: Durable explicit context and stopped-source reads

**Scope:** next bounded #174 source unit after the installed retention guard.
Default off. ADR-003 controls privacy and coordination. This is an explicitly
published excerpt/result/memory/handoff contract, not complete native history
or database restoration. No provider turn, pairing or fleet UX follows.

## Contract

Use an immutable bounded PublishedContext resource in the protected
`coordination.dev-env.haynesops.com` group. Existing worker ConfigMap/old-group
read grants must not expose it. Store through the operator's uncached named
API access, with only namespaced get/create/list permission; no new worker
read, Secret, broad RBAC, update/delete, informer or storage mount is required.
*Amended 2026-10-12 ([ADR-004](../adrs/004-coordination-record-retention.md), Q-29): the operator alone gains a
namespaced delete, used only by its retention runnable ([DESIGN-024](024-coordination-record-retention.md)).*

An authenticated own live Session publishes selected UTF-8 text through an
explicit agentd stdin command. Accept only kind (`transcript-excerpt`, `result`,
`memory`, `handoff`), a bounded logical publication key and at most 64KiB bytes.
No path import, raw HOME/database/archive, credential or enrollment export,
provider/RPC call or model invocation belongs in this command. UTF-8 byte bounds
must be checked in API/store as well as CRD character limits.

Derive original Session name/UID, publisher PodUID and authenticated actor,
saved project/repo/catalog/rules and provider/requested-model attribution on
the server. Caller text is explicit publication, not verified provider output.
Actual observed model, initial fetch pin, complete history and executor stop
remain Unknown without independent evidence. Mark completeness ExplicitExcerpt
or Partial; never infer complete retention from one chunk or a missing Pod.
Retain a content hash, byte count and original publication time/provenance.

Original Session UID, typed kind and logical key determine immutable identity;
kind namespaces the key for separate result/memory/handoff publications. Retries
confirm only identical original content/metadata. Atomic Create and uncached
exact confirmation preserve uncertainty on lost acknowledgement. A changed body
cannot replace an existing publication or create a silent second revision.
Retain source binding and published chunks without source owner references or
expiry/delete on reap. Replaced Session names cannot become the original source.
*Amended 2026-10-12 (Q-29, ADR-004): 30 days after the source Session's UID is
first proved absent, the operator archives a publication with its SHA-256 and
then deletes it. Nothing else deletes one.*

Read/list use a separate explicit project payload-read scope, independent from
claim metadata and legacy session read/reap permissions. A live authenticated
reader Session proves its own UID/Pod/project; a configured coordinator proves
its own identity and explicit read scope. No legacy client/human privilege
silently grants this route. Cross-parent project reads are read-only. Validate
source scope from its immutable published provenance, not current source Pod
readiness. Source deletion, stop or operator reconstruction must not prevent
authorized reads. No source exec, resume, model turn or stopped-volume mount
is permitted by a read. Unrelated projects and body reads without the explicit
payload grant fail closed without exposing existence or private content.
*Amended 2026-10-12 (Q-29): after a release, single reads fall back to the
archive under the same scope and answer `archived: true`; without a shelf they
answer 503, never 404. `?archived=only` lists archived publications (DESIGN-024
section 6). The fixed canary result route stays live only.*

## Bounds, fences and identity normalization (2026-10-12)

These rules come from the #175 coordination review.

- **Explicit project.** Publication is enabled per project. A publisher's saved
  project must be in the explicit payload project list
  (`--context-payload-read-projects`), the same list readers need. Otherwise
  the publish is refused like any other unauthorized call.
- **Bounds.** Each publication holds up to 64 KiB, and none is ever deleted. A
  new publication is refused with 429 `limit_exceeded` when its source Session
  already has 128 publications, or its project has 1024. Nothing is created. A
  retry of an existing identity is never refused. Counting uses uncached,
  metadata-only, label-selected lists. New publications carry a source label
  (a digest of the original Session identity) beside the project label.
  Publications from before the label count toward the project bound only.
  Concurrent creates can pass the same count, so a bound can be passed by at
  most the number of racing creates. The bounds are defence in depth against an
  agent loop filling etcd. They are not a retention policy.
  *Amended 2026-10-12 (Q-29): publications are now deleted 30 days after their
  source is absent, after an archive. The project bound then counts live
  publications (DESIGN-024).*
- **At the bound.** The bounds are lifetime bounds, because publications are
  never deleted. DESIGN-010 "At the bound, recovery and rollback" has the
  behaviour, the record-count metrics, the manual recovery with its
  preservation duty, and the rollback floor. Here, agentd `context-publish`
  reports `limit_exceeded` with a fixed message that says not to retry, and
  exits 1. A managed canary output capture that hits the bound spends its one
  delivery attempt. agentd logs "managed output publication refused" with the
  same message, and the captured bytes stay in the private capture directory.
  Recovery never deletes a managed-output publication of a retained canary
  original.
  *Amended 2026-10-12 (Q-29): the per-Session bound stays a lifetime bound; the
  project bound counts live publications. Retention never releases a canary
  original's publication either: canary originals are protected Sessions and
  never absent (`privateHistoryProtected`).*
- **Final fence.** The fence before the atomic create compares the Session's
  binding: UID, generation and spec, deletion, labels, annotations (including
  the saved project snapshot), owner references and finalizers. It does not
  compare the whole resourceVersion. A heartbeat or controller status write
  between the reads is therefore no refusal. The fresh live caller check still
  refuses a stopped, suspended or deleting source. A managed output still
  rechecks that the fresh retained history names this Pod as last executor.
- **GitHub identity.** Provenance stores the catalog's GitHub repository
  lowercased, as claims and the inbox do. A mixed-case repository part in the
  catalog no longer turns every publication into a 403. The fixed canary result
  reader compares the lowercased form too.

## Paths and acceptance

- Protected coordination API types/deepcopy/immutable schema and generated CRD.
- Small atomic uncached publication store with bounded identity/content checks.
- Operator default-off settings, explicit read scope and namespaced store wiring.
- API publish/read/list wire types, precise ACL/UID fences and safe errors.
- Agentd explicit stdin publication plus authorized read client/command using
  its existing projected identity; no provider/RPC/Runner in the publisher.
- Focused fake-client/API/command tests and necessary real CRD checks.

Prove two API replicas/uncertain acknowledgements retain one immutable chunk;
UTF-8 bytes, hash and original source UID are enforced; content/key collisions,
foreign resources, replaced/deleting publisher, caller-supplied provenance and
wrong payload-read scope are refused; authorized stopped/absent-source reads
work after server reconstruction without exec or mutation; and errors/list
metadata expose no unrequested text/auth/path. Keep #174 retention/claim gates
and other operator flags intact. Sequential nice19/GOMAXPROCS2/p2 tests,
affected lint/codegen and independent native review precede required CI.
Claude advisory remains optional/down. No stress, wide loops or local images.

## Remaining acceptance

After signed publication, review the exact schema and narrow operator Role/API
activation in GitOps, preserving all current Pods/fixtures/ledgers. A disposable
publish-stop-read test establishes this excerpt contract, not native history
restoration. Managed Claude assistant-text and confirmed-thread Codex
agent-message parsers are available capture seams, but their trusted automatic
publication adapter is a subsequent source unit. Native rollout/history export,
memory adapters, complete session-directory coverage and owner pilot remain
open. Do not call explicit stdin text a proven provider transcript. Track built,
deployed and accepted separately in #174/#91/HANDOFF. Keep Q-24 pending and local
inference behind GPU readiness/household reservations. No v1 restart/cutover,
elevation, credential copy, destructive cleanup or retired storage retry.

Provider attribution remains neutral across Claude, Codex and the planned local
provider. A trusted Session supplies its requested provider/model; current
runtime/schema limitations are recorded separately. Qwen is a candidate, not
accepted inference; GPU readiness and household reservations still gate launch.
