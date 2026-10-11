# DESIGN-017: durable bounded session metadata discovery

Status: ratified source contract, 2026-10-11. This is one default-off #174 PR.
It starts from the finite native source audit b9c92c9120cb5d579beebac08c5983d2108b932d1409f487e594322f49ee3c59.
Existing list/get requires an extant AgentSession and inherits legacy control
scopes; retained context objects already survive independently. This increment
retains logical identity and bounded metadata without importing provider homes.

## Record and prospective enrollment

Add SessionDirectory to the existing protected coordination group/namespace.
Its deterministic identity is original source namespace plus Session UID;
reusing a source name with a new UID creates a distinct entry. Bounded immutable
spec records original namespace/name/UID, creation time, accepted project/repo/
base, exact requested provider/CLI/model/effort, parent/task identity if present,
and saved catalog/rules/template/image/project snapshot/common revisions.
Common source namespace/name/key/UID/RV/digests come from the accepted binding;
never recapture current ConfigMaps or store instruction bodies. Missing
historical fields remain explicitly Unknown. Store no prompt, credential,
HOME path, socket, pairing URL, raw transcript or provider database.

Prospective managed project canaries are the only enrollment route in this PR.
The operator feature/project scope is default-off. An immutable server-selected
Session enrollment latch is required before the first executor; callers cannot
opt in through their task request. Create and independently confirm the exact
record before any first Pod creation. Missing/foreign/uncertain directory
identity refuses execution and cannot replace a provider attempt. Disabling the
feature cannot erase an enrolled record/latch or reinterpret existing history.
Existing legacy/native/TUI or uncertain Sessions are not migrated or silently
labelled original. No source owner reference, expiry, automatic cleanup,
reaping policy or retention deletion is added.

## Witnesses and current state

Operator-only status stores bounded last witnessed source generation/RV/time,
owned original Pod UID/node/template/image/lifecycle, and separately labelled
reported heartbeat/task state and provider-declared conversation ID. The
conversation assertion retains source/Pod/time provenance; it is not verified
native history. Keep requested exact model distinct from actual served model,
usage and cost, which remain Unknown. Local provider metadata has an explicit
typed seam; never infer local inference from an opencode CLI name. Its runtime
adapter stays disabled until GPU readiness and household reservation gates.

CAS observation starts from a directory resourceVersion, then fresh exact
source UID and owned Pod proof, then one status write. On conflict or unknown
acknowledgement, preserve uncertainty and reobserve fresh; do not refetch a newer
record and overwrite it with an old witness. Failed/absent/foreign source never
erases the retained last witness or adopts a replacement identity. Original
executor ownership follows the existing private-history latch. No old-Pod
absence, Archived phase, expired heartbeat or retained HOME becomes StopConfirmed.

At read time provide current placement/state separately using fresh exact
source UID and owned Pod proof. Source absent, foreign or unavailable yields
explicit SourceMissing/Unknown and keeps the last witness. Loaded/Ready,
reported active/idle, witnessed termination and independently confirmed stop
are different facts. No exec, provider invocation, resume or lifecycle mutation
is performed by discovery.

## Independent discovery authorization and bounded reads

Add explicit project metadata-discovery scope plus existing exact trusted
client/coordinator identity mappings. Live peer readers must freshly prove
original owned Pod/Session identity and accepted project. Coordinators require
configured host authority and explicit project discovery scope; direct-child
control does not grant sibling discovery. Legacy session read/reap, claim
metadata and context payload scopes grant no directory access. Directory
access grants no task mutation, claim write, inbox content or context payload.
Authenticated unauthorized named probes hide existence; unauthenticated calls
remain unauthorized. Worker ServiceAccounts receive no direct CRD rights.

New GET/list API views whitelist metadata. Pages use limit 1..20, bounded opaque
continuation at most 4096 bytes, exact project/source query binding and fresh
reader authorization on every call. Return observation RV/provenance and next
cursor; do not promise newest-first Kubernetes ordering or scan/dedupe all
history in memory. Reject malformed/rebound cursors and preserve bounded empty
sparse-page continuation semantics.

A separate bounded context-reference page returns only exact original source
namespace+UID+project matches from existing PublishedContext storage: object
name/UID/kind/key/digest/bytes/Origin/Completeness/time and declared thread
assertion where present. No text bodies or inbox text. Reuse validated bounded
store paging and inspect at most one page, allowing an empty filtered page with
continuation. Independent payload authorization still gates text reads;
authorizing discovery alone cannot expose content. Source stopped/absent does
not prevent reading an authorized retained record/reference.

## Affected paths and acceptance

New source paths: api/coordination/v1alpha1/sessiondirectory_types.go,
internal/sessiondirectory/store.go, internal/apiserver/apiv1/sessiondirectory.go,
internal/apiserver/sessiondirectory.go, internal/controller/session_directory.go
and generated protected CRD. Integrate existing scheme/deepcopy, command flags,
API routes, immutable AgentSession latch/schema, controller first-executor path
and a bounded context-metadata adapter. Add only corresponding focused tests.
Operator-only CRD/status RBAC and signed schema/image installation are a later
separately reviewed GitOps increment; source adds no worker grant.

Prove immutable original identity/revisions and bounded real schema; same name/
new UID distinction; retained entry after source deletion/archive; stale CAS/
unknown ack/foreign source refusal and last-witness preservation; fresh current
Unknown versus last witness; independent discovery/payload/control ACLs; strict
bounded cursors/sparse pages; metadata whitelisting and no exec; requested model/
provider/local seam truthfulness. Test common/capture/HOME-only canary and
legacy control preservation where integration touches them. Use fake API/time
and one bounded cached real-schema admission check where meaningful, nice19,
GOMAXPROCS2/go-p2. No burners/stress/wide loops/local image builds.

Native independent review, exact-head required CI, optional Claude receipt and
signed release precede GitOps. Read findings, fix or concretely answer; no Claude
retry/auth repair while down. Preserve prior packets, failed native fixtures,
ledger and all logical counters. Three same-blocker failures or 60 minutes
without evidenced progress stops that attempt and asks one native owner question.

Source/deployment/actual acceptance remain separate. This PR provides no full
provider export, native conversation restoration, exceptional recovery, fleet
pairing UX or MVP claim. Q-24 and the stopped Q-25 route remain pending/held.
Actual cross-pod live/stopped discovery, unauthorized payload refusal and
operator upgrade during live work remain the disposable owner pilot. Keep
#174/#91 open.

DIRECTORY-R1 demonstrated that nested status CEL alone permits removal of the
optional whole status and loss of a retained witness. The directory root now
requires status presence once a last witness exists; nested checks preserve its
fields. Real API null/removal negatives preserve exact UID/RV/spec/full witness,
while witness-free initialization/removal and valid original-Pod advances remain
allowed. Original failing fixture and native HOLD receipt are retained. No
stateless store invents prior witness data for legacy empty status.

DIRECTORY-R2 found that embedding coordination CRD types in the wire DTO linked
Kubernetes into agent-run, violating the existing static CLI build guard. Wire
types now use the standard library and the server explicitly projects approved
metadata. The guard remains unchanged. A bounded cached CLI build/module check,
affected API/store checks and seven JSON projection cases passed; generated
schemas and unrelated incoming paths remain unchanged. Original HOLD evidence
is retained, and corrected native review and required CI still gate merge.
