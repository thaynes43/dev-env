# DESIGN-026: Offline provider preparation contracts

Date: 2026-10-15. Scope: source-only stories #304 and #305 under the distributed
dev-env saga. This records their pre-reserved offline contracts; it authorizes no
provider, runtime adapter or experiment. D-33 still selects OpenCode. Spike S11
and backlog plan09 retain their serial prerequisites and activation gates.

## Existing architecture and boundaries

Pure data and validation belong in `internal/agentd/protocol`, alongside existing
session, usage and tool-manifest contracts. These helpers have no transport,
controller, filesystem, process, credential, environment or inference access.
They do not add values to the production Session provider enum or wire launch.
A later adapter must supply sanitized metadata through the existing architecture,
with separate admission, tool access, resource and operator decisions.

Ollama preparation binds an exact origin, backend, model ID/digest, context
capacity, required tool manifest and receipt provenance. Catalog advertisements
alone do not prove availability or tools. Missing required observations produce
reasons and no isolated candidate. Even a valid candidate remains data requiring
lease, household/resource, network/access and bounded execution decisions. The
Codex OSS candidate is experimental and does not replace the OpenCode decision.

## Cursor receipt and operation contract

Cloud API agent/run identities and local ACP session identities are separate
surfaces. API receipts correlate both GET identities and worker metadata; ACP
receipts correlate initialize/list request IDs, protocol version, cwd and listed
local membership. An empty list proves no cloud-to-local history mapping.
Repository, cwd and tool-manifest pins come from the caller. A supplied clock and
age bound determine receipt freshness, without querying any provider.

Both normalizers refuse unsupported, duplicate or noncanonical fields and seal
the normalized observation separately from the sanitized projection hash. The
digest detects changed observation data; it is neither authentication nor a grant
of authority. Planning rechecks semantics, freshness and that digest before
returning any plan. Synthetic fixtures establish these source contracts only.

| State | Meaning and limit |
| --- | --- |
| `CallerPinned` | Binding supplied by the caller; no provider tool-access attestation. |
| `ProviderRequested` | Agent model configuration metadata; not the model that served inference. |
| `Unknown` | No supported observation establishes the value; actual served model, cost, runtime authority, tool access and phone mirroring stay unknown. |
| `Advertised` | ACP reports a capability; no execution or effective tool access is proved. |
| `ReusedReceipt` | Observe returns the validated observation without a new request. |
| `OfflineOnly` | Local list yields request data only; no transport is performed or authorized. |
| `NeedsDecision` | Launch, steer, cancel and other held operations yield no request or authority. |

Cloud listing remains unproved. Launch, authentication, new/load/prompt/tool/send,
follow-up, steering, cancellation and worker/workspace activation are held or
refused. The existing usage protocol's `ProviderReported` served-model semantics
remain unchanged; Cursor requested metadata must never use that model label.

## Acceptance and later decisions

Finite offline fixtures must cover identity/cwd/request correlation, freshness,
strict parsing, observation rebinding, local membership, requested-versus-served
model provenance and held operation plans. Native independent review and required
exact-head CI precede source merge. Source acceptance proves no installed control,
consistent live tools, model availability, phone mirror, cost or usable provider.

Runtime work still needs an exact model/catalog handoff, applicable resource and
access evidence, separate workspace opt-in and a bounded authorized execution
story. This design grants none of those decisions and changes no existing hold.
