# ADR-015: Authenticated delegates decide grants within their session tree

- **Status:** Accepted 2026-10-31 for source preparation only. Activation held.
- **Amends:** ADR-012 and ADR-013 where maintenance extensions require the human route.
- **Tracking:** OpenProject #329, origin #233; DESIGN-042 S2.
- **Authority:** Tom's message-based grant workflow and the coordinator's released S2 implementation work order. This records source behavior, not activation consent.

An allowlisted, pod-bound coordinator host or named coordinator session may record another session's grant decision through `POST /v1/grants/{name}/decision`. A host reaches only its direct child sessions; a named coordinator session reaches only strict descendants. Self, ancestor, sibling and unrelated requests are refused. Top-level requests use attestation after a message to the requester.

S4's exported launch rule validates named coordinator sessions. The API also checks live pod/session identity and the sole account holder. The decision binds the grant UID, scope digest, shorter-or-requested TTL and required message ref, and occupies one optimistic-lock-protected annotation slot across replicas. A delegate authenticates its own actor; its claim to relay Tom's approval remains an agent assertion. Reserved classes await S7; this change supplies no human-only fallback requirement for an otherwise supported delegate decision.

HTTP 202 records the decision with a Pending grant view. Only the broker makes it Active, after a matching delegated policy and fresh identity, separation, tree and sole-holder checks. The broker repeats those checks before materialization and while active. A deleted or replaced delegate, suspended session, second holder, removed delegate or lost policy cannot retain the lease. Read failures retry rather than imply permission.

Maintenance extensions use a fresh grant and fresh bound decision through the same delegate route. They remain bounded by the exact envelope, maximum TTL, live maintenance participants and one lease per target. They never mutate or prolong the old executor. Self-attested extensions remain refused. The authenticated human route remains an alternative before the decision slot is filled. A delegate decision occupies that same set-once slot even if the broker finds no covering policy or a busy target; another maintenance decision is then refused until the requester releases the grant and requests afresh, or the pending window expires. It cannot override or erase that recorded decision. Kube and egress retain their separate approval-page path.

Configured hosts outside the session namespace need reviewed broker GET permission for their named pod before activation. The current source uses named GET, not pod listing. The decision annotation records pod name, UID and host binding. Two pending approved maintenance grants for one target can conservatively wait on one another until timeout under the existing reservation rule; this is an availability limitation, not overlapping execution permission.

The CLI/MCP surface is S3. Named Secrets are S5. Source tests use HTTP launch, token classification, child request, decision, broker activation, scope refusals and revocation. Authentication and controller pod materialization are test fixtures; this establishes no installed or usable runtime acceptance. Runtime flags, policies, templates, grants, credentials and access activation remain held.
