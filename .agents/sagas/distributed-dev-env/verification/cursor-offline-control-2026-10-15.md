# Cursor offline control contract — 2026-10-15

Story #305 implements the bounded [offline provider preparation contract](../designs/026-offline-provider-preparation.md),
without a transport, controller, provider
enum or production launch change. A future adapter supplies sanitized metadata
projections of existing API GETs or ACP exchanges. These are explicitly projection
schemas, not arbitrary complete provider response bodies. Payload/history,
authentication data and unrecognized fields are refused.

API agent/run IDs and worker names bind exactly. Local ACP IDs occupy a separate
surface and cwd; they cannot substitute for cloud IDs. Repository, cwd and effective
tool-manifest pins remain caller bindings, not claims that GET/initialize proved
tool access. Model absence, cost, observed tools, runtime authority and phone
mirroring remain Unknown. A provider model field, if present, reports requested
model metadata only (`ProviderRequested`), never the model that actually served
inference. `ActualModelState` remains `Unknown`; the existing usage protocol's
`ProviderReported` served-model meaning is unchanged.

ACP protocol1 initialize/list request-response IDs must correlate independently.
An empty cwd-scoped list is valid and proves no web/local history mapping.
Load/list/HTTP/SSE advertisements remain advertisements. Freshness is checked
against a caller-supplied clock and finite age bound, never the wall clock here.

Operation plans preserve binding, provenance and receipt hash. Observe reuses a
receipt. ACP list can produce only offline request data; cloud listing is unproved.
Launch/steer/cancel and authenticate/new/load/prompt/tool/send/followup/worker
activation return NeedsDecision or refusal with no request. There is no local
allow flag, send callback, network, process, file, credential or environment access.

Existing evidence is reused: Tom's two API GETs succeeded; ACP initialize/list
returned sessions[]; the worker remained Idle without workspace opt-in. No live
request or workspace activation was repeated. Synthetic fixture identities are
unrelated to private sessions.

Validation: all six focused `TestCursor` methods passed with finite synthetic
subcases, using cached Go1.27.2, two CPUs, nice19, package parallelism one,
downloads off and the shared test lock. Native independent Sol review found an
exported observation rebinding gap. Both normalizers now seal the complete
normalized observation; planning verifies that consistency digest and local
session membership. The original projection hash remains separate. The digest
is not authentication or runtime authority. Rebinding and serialization fixtures
passed; the same reviewer confirmed closure with no remaining findings.
The implementation worker delivered a held draft on 2026-10-15 with advisory
skipped; the parent subsequently restored ordinary source ready/review/merge
delivery, including advisory review. The ready-event advisory found a requested
model provenance ambiguity and a missing durable design reference. The correction
adds the separate requested-model state, a semantic regression and the linked
offline design contract; native review and new exact-head CI precede acceptance.
Source proof does not establish installed
control, tool parity, phone mirroring, availability or cost. Smallest later
decisions remain the approved model-catalog handoff, separate workspace opt-in
trust/download decision, and exact bounded execution/spend experiment.
