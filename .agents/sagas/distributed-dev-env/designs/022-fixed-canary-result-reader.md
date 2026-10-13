# DESIGN-022: fixed canary result reader

Status: implementation contract ratified by the driving session, 2026-10-12 UTC.
Tracking: #173, #174; ADR-003 and DESIGN-016/020/021.

**Current checkpoint, 2026-10-12:** source/result-reader delivery is accepted
separately from runtime output. Both fixed IDs are consumed; Claude's one stored
result is a weekly-limit response, and Codex reads remain zero under stopped
Q-27 preparation. “Unstarted” below records the original contract state.
Follow the [pickup brief](../../../CLAUDE-RESUME-2026-10-12.md) and
[latest probe record](../verification/codex-fixed-rule-probe-2026-10-12.md).

## Concrete blocker

The initiating `dev/dev-env` client can create the two trusted fixed probes and
discover project metadata, but `contextReadScope` delegates to `claimCaller`,
which rejects client callers. The configured project payload scope does not
override that identity check. A successful task outcome also precedes capture
completion and proves no publication ACK. Source audit `8093f2b6` retains the
15-file evidence under `/home/dev/work/orders/dev-env-rule-canary-runtime-1011/review/`.
Both permanent provider attempts remain unstarted.

The first focused API run then exposed an existing prerequisite defect: a
capture-enabled fixed canary calls `checkAgentd` before Kubernetes assigns its
UID. The validation preview supplies a UID only for the old Workspace path, so
the capture protocol refuses initial creation. Earlier capture tests seeded a
retained Session directly and did not prove this boundary. Failed validation
`17ddd14e` is retained; neither permanent runtime ID was invoked.

## Bounded behavior

Add `GET /v1/canaries/{id}/result?sessionUID=<originalUID>&context=<contextName>`
and explicit `agent-run canary result --id <fixedID> --session-uid <originalUID>
--context <contextName> --json --once`.

All three identities are mandatory, bounded and explicit. Parse the verb and
arguments before auth. The client uses ordinary auth/TLS and one existing
no-retry call, refusing redirects. It does not create, resume, poll, list,
paginate, execute, retry or select a replacement. Invalid or unknown input
cannot fall through to creation. Reject duplicate JSON keys recursively before
typed receipt validation; preserve accepted raw JSON and null presence.
For this explicit subcommand, `--context` names the publication; clear its
common kubeconfig-context selector before connection/auth resolution. Other
commands retain their existing selector semantics.

The server accepts only the configured fixed record's initiating trusted parent
client. Reuse fresh caller/parent and private-canary policy authority helpers.
After ordinary authentication, reject any identity other than the configured
initiating client before generic Session/coordinator classification. Rejected
callers must not trigger Pod or source-Session discovery.
Require the explicit payload project grant and enabled publication/canary
capabilities. Derive the original Session name from the record; read it without
calling any start/create helper. Require its exact UID, parent, deterministic
name and idempotency label, private policy, project/repository and immutable catalog/common/template/image
authority to match the trusted record. Missing, changed or ambiguous authority
fails closed. Verify the caller/parent again after reading stored context.

The derived task identity is `canary:<id>`. TaskKey is a Directory field, not a
Session or publication field; validate its source through the fixed record and
existing Session policy matching without an additional Directory read. Original
template ConfigMap UID/resourceVersion is not retained in publication evidence;
do not invent it. Current template content/revision and common source authority
are checked through their existing helpers.

Fetch only the named stored context with `GetForProject`. Require deterministic
context identity, valid UID/resourceVersion/hash/bytes, exact original source
namespace/name/UID, source generation and parent, project/repository, requested
provider/model and retained provenance. Require managed assistant output with
its session actor and bounded invocation/ordinal/conversation assertions.
Reuse the publisher/store's immutable validation and DTO projection. Return
only the accepted published text and its existing receipt fields, plus fixed ID
and original Session UID; no prompt, paths, credentials or private files.

Stored publication authorization remains independent of source Pod liveness.
Original Session metadata remains the authority; an absent or replaced Session
holds the read. Its retained generation must match the publication. A later
Session spec mutation without accepted original-generation evidence holds the
read; stopped Pod support does not waive that check. Do not exec, remount a HOME/PVC, start a reader Pod or add an
identity, credential copy, Kubernetes grant or coordinator enrollment.

Common/template/image and canary policy are not publication fields. Bind those
from the original Session and accepted receipts; do not invent such publication
evidence. A successful read proves the returned stored publication only. Missing
publication or ACK remains Unknown. Requested model is not served model;
partial managed output is not complete native history or billed cost.
*Amended 2026-10-12 ([DESIGN-018](018-initial-invocation-usage-provenance.md), #176):
the reader sends the attribution header and accepts provider-reported attribution
that passes `apiv1.ValidContextAttribution`; inconsistent attribution is refused
like any other unconfirmed field.*

Generic context ACLs, Session/coordinator identity rules, general creation,
global private admission, task/template pins and the permanent records remain
unchanged. This introduces no fleet UX or local inference.

The one creation prerequisite correction extends the existing admission
validation preview to an empty-UID `ProjectCanary` with `CaptureOutput`. Check a
DeepCopy with a validation-only UID reserving the protocol's maximum 128 bytes.
Never mutate, persist or transport that preview identity. Keep the old Workspace
preview behavior and all other preflight checks. Actual controller/daemon
validation still requires the real API-assigned UID before provider execution.
General creation and global private admission stay unchanged.

## Affected source

Expected paths: `internal/apiserver/server.go`, a dedicated canary result handler
and tests, `internal/apiserver/apiv1/types.go`, private-canary read-only authority
helpers where needed, and `internal/agentrun` routing/client/help/tests. Reuse
`caller.go`, `publishedcontexts.go`, `contextpublication/store.go` and existing
policy matching without weakening their general authorization. No CRD, RBAC,
private Pod shape, credentials, storage or image build changes in this PR.
Also change the bounded `checkAgentd` preview in
`internal/apiserver/validate.go` and test initial capture-enabled admission,
original-object nonmutation, real assigned receipt UID and retained replay.

## Acceptance and shipping

Focused tests must prove pre-auth argument rejection, exact one GET/query,
redirect/no-retry behavior, raw JSON validation, authorized original stopped
source read, foreign parent/UID/project/context/policy rejection, missing grant
or capability rejection, and no creation, exec, Pod/HOME access or list calls.
Use fake clients and resources. No CPU burners, stress, wide or looped tests.
Inspect TestMain/dependencies before one low-parallelism cached focused run.

Freeze source and pre-test evidence, run meaningful checks, obtain independent
native review, read optional Claude findings if produced, and squash-merge after
required CI. Release and separately deliver the signed operator/client change
where needed. The preceding R4 configuration observer must independently pass
before a new upgrade; this is no fifth correction or alternate R4 route.

Actual original Session/context bindings, fresh Directory acceptance, result
retrieval readiness, independent final runtime-plan review and separate root
authorization precede either permanent POST. No initial provider attempt is
authorized by this contract. Keep #173/#174/#91 and HANDOFF current. Parent MVP
acceptance and Q-24 remain open; Claude review is optional while down.

## Reader availability amendment — 2026-10-12

Review findings rules-001 and history-03 (#174) showed two ways the retained
original could become permanently unreadable without any change to its
authority. This amendment keeps every authority above and changes only what the
read path compares.

**Common sources: content, not object identity.** The read path resolves the two
fixed common sources with the same uncached named GETs and refusals as before,
then requires the project digest, CommonRevision and each source's
namespace/name/key, SHA-256 and text to equal the bound snapshot. Source UID and
resourceVersion stay retained provenance (DESIGN-015) and are not compared here.
A change to another key of either ConfigMap (`mcp.json`, the agent files,
`config.toml`) or an identical re-create therefore no longer holds the read. A
change to `CLAUDE.md` or `AGENTS.header.md` bytes still holds it. The pre-Create
and pre-executor fences keep the strict object-identity check. For an unchanged
bound ConfigMap the reader accepts exactly what the deployed reader accepts.

**Generation after a reap.** A finalizer-held delete sets `deletionTimestamp` and
bumps `metadata.generation` exactly once, and publishers refuse a deleting
source. So when `deletionTimestamp` is set the publication's source generation
must equal the live generation minus one; otherwise it must equal the live
generation. A reaped, retained original stays readable. Any spec mutation before
or after the reap (a suspend changes `operatingMode`) still holds the read. The
stopped-source test uses the natural end the live canaries show: phase `Failed`
with the generation unchanged.

Catalog UID/resourceVersion and template content checks are unchanged here.

**Client.** A one-shot result read that reached the API never advises a retry:
a lost answer, stop or any 5xx exits 6 (unconfirmed), and a 429 exits 1
(DESIGN-021 amendment). It exits 5 only when nothing was sent (D-86).
