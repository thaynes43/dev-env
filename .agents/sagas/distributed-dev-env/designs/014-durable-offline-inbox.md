# DESIGN-014: Attributed durable offline inbox

**Scope:** next bounded #175 source unit after atomic task claims (#187).
**Decision:** ADR-003. Default off; no provider prompt delivery or fleet UX follows.

## Contract

Use immutable `InboxMessage` and `InboxReadAck` resources in the protected
`coordination.dev-env.haynesops.com` group. The operator gets namespaced
get/create/list only, using uncached reads. Workers get no direct access to
these resources. Do not use ConfigMaps, provider homes, shared repositories or
the existing broadly readable API group as the message store. No owner
references, expiry, garbage collection, replacement or background consumer
is part of this unit.
*Amended 2026-10-12 ([ADR-004](../adrs/004-coordination-record-retention.md), Q-29): when the sender's UID is absent and
the recipient has acknowledged or is absent too, the operator's retention
runnable archives the message with its ack, waits 30 days from the first
observation, then deletes both. It alone holds a namespaced delete
([DESIGN-024](024-coordination-record-retention.md)).*

An authenticated original live sender Session with a currently controlled,
non-deleting executor Pod may publish one bounded UTF-8 text message, at most
16 KiB. The request specifies an explicit project, recipient namespace/name/UID,
logical idempotency key and text. Derive sender Session UID, authenticated actor,
provider/requested model and saved catalog/rules provenance on the server.
Do not accept caller-supplied provenance or infer actual model, initial source
pin, recipient readiness or provider delivery from it. The contracts cover the
future local-model provider without starting inference or reserving a GPU.

Sender UID and logical key determine one full-digest identity. Recipient, text
and source/rules revisions are immutable intent, not uniqueness inputs. Atomic
Create and uncached confirmation return 201 for a new confirmed message and
200 for an identical confirmed retry. A changed intent returns 409; an
unconfirmed acknowledgement returns 503/Unknown, with the same key retained.
Do not allocate a suffix, retarget a message or inject a prompt after uncertainty.

For a new message, uncached recipient lookup must prove the original UID and
saved project identity. A stopped or deleting recipient is allowed; a missing,
replaced or ambiguous recipient is refused. For an already accepted identical
retry, confirm the stored immutable intent even if that recipient has since
disappeared. The existing object must be checked before requiring new-recipient
existence. Operator reconstruction and source Session deletion leave accepted
messages intact.

## Authorization and acknowledgements

Routes have an independent explicit project content scope. Claim metadata scope
and legacy broad Session read/reap rights grant no inbox text access. Only the
original recipient UID, verified against its current controlled, non-deleting
Pod, can read its inbox and acknowledge a message. A deleting Session can still
read/ack while that proof remains valid; do not loosen the existing claim caller
helper to implement this. A replacement Session with the same name cannot
adopt the old inbox. An absent Session/Pod cannot authenticate as that recipient.

A configured coordinator may read authorized project content, including messages
whose original sender or recipient is stopped, deleting or absent. It may not
publish or acknowledge on another Session's behalf. Unauthorized or foreign
reads return uniform 404 without exposing message metadata or text. Session-less
native hosts are not assigned a synthetic Session UID; their publication and
recipient identity remain separate work.
*Amended 2026-10-12 (Q-29): after a release, the same readers read a message
and its ack from the archive, with `archived: true`; without a shelf the answer
is 503. A recipient's identical ack retry after the release answers 200 from
the archive and writes nothing (DESIGN-024 section 6). One accepted residual
departs from the uniform 404 above: while the shelf is down, a scoped caller
asking for a name that is not live gets 503, where a live message outside its
scope gets 404. Names are full digests of identities the caller must already
know, so this reveals only that the name is not live.*

An immutable acknowledgement binds message UID and original recipient UID,
with the authenticated actor and API creation time derived by the server.
Atomic creation and uncached confirmation make an identical retry safe. It
proves an API read acknowledgement only; it does not prove provider consumption,
phone notification, human reading or prompt delivery. GET never creates an ack.
Lists return bounded metadata, and individual authorized GET returns text. No
provider runner, model call, automatic prompt injection or polling loop runs.

## Bounds and fences (2026-10-12)

These rules come from the #175 coordination review.

- **Bounds.** A new message is refused with 429 `limit_exceeded` when its
  sender Session has already sent 512 messages, or its project holds 4096.
  Nothing is created. A retry of an existing message (same sender and key) is
  never refused. Text is at most 16 KiB and nothing is deleted, so one
  project's retained text stays near 64 MiB at most. New messages carry a
  source label (a digest of the sender identity) beside the project and
  recipient labels. Messages from before the label count toward the project
  bound only. Read acknowledgements are not counted, because there is at most
  one per message and recipient. Concurrent sends can pass the same count by
  the number of racing sends.
  *Amended 2026-10-12 (Q-29): messages are now deleted after an archive, so
  the 64 MiB applies to a project's live text; the archive holds the rest
  (DESIGN-024).*
- **At the bound.** The bounds are lifetime bounds, because messages are never
  deleted. DESIGN-010 "At the bound, recovery and rollback" has the behaviour,
  the record-count metrics, the manual recovery with its preservation duty, and
  the rollback floor. The inbox CLI prints the API's `limit_exceeded` error.
  Recovery keeps every message whose recipient still exists and has not
  acknowledged it, and deletes a message's acknowledgement with it.
  *Amended 2026-10-12 (Q-29): the per-Session bound stays a lifetime bound; the
  project bound counts live messages, which retention frees.*
- **Final fences.** The sender fence (on the new-message and retry paths) and
  the recipient fence compare each Session's binding: UID, generation and spec,
  deletion, labels, annotations, owner references and finalizers. They do not
  compare the whole resourceVersion. A heartbeat or status write during the
  catalog recheck no longer turns a valid send into the hidden 404 that a
  sender would read as "no such recipient". Any identity, project-snapshot or
  spec change still refuses.

## Paths and acceptance

- `api/coordination/v1alpha1/`: immutable bounded message/ack types, deepcopy
  and generated CRDs under `config/crd/`.
- `internal/inbox/`: strict identity/intent, atomic create and uncached
  confirmation/read/list; no controller or informer.
- `internal/apiserver/inbox.go`, `apiv1` wire types and dedicated routes:
  publish/list/read and explicit ack/read-ack under `/v1/inbox`.
- `cmd/operator`: default-off `enable-durable-inbox`, explicit enabled projects
  and a separate coordinator content-read scope; scheme/store wiring.
- `internal/agentd/inbox.go`, `cmd/agentd/inbox.go`: explicit bounded stdin JSON
  publish plus list/read/ack commands. Use projected-token rotation and verified
  TLS, refuse redirects and response overflow. Do not open provider files.
- A later GitOps PR installs both CRDs and narrow operator grants before a
  signed operator enables the route. No worker grant or elevated access follows.

Focused tests must prove two API instances, retries/lost acknowledgements,
operator reconstruction, immutable changed-intent refusal, stopped/deleting
recipient publication, identical retry after recipient deletion, replacement
UID refusal, original-recipient read/ack, scoped coordinator read without
publish/ack, legacy/project text denial and no provider invocation. Validate
ASCII and multibyte UTF-8 byte bounds; schema character limits alone do not prove
the byte contract. Verify schema immutability and CLI token rotation/TLS/redirect
refusal with meaningful fixtures.

Use sequential focused checks at `nice -n 19`, `GOMAXPROCS=2`, `go test -p 2` and
cached envtest tools. No burners, stress, wide/looped tests, local image build,
runtime credential copy or fixture cleanup. Required CI and native independent
review gate merge. Claude reviews are optional and currently unavailable; read
any available findings and record failed/skipped outcomes without a pass claim.

## Remaining acceptance

This unit provides durable attributed API publication/read/ack, not automatic
provider inbox consumption. After signed deployment, run one reviewed disposable
two-parent offline-message trial, with effective unauthorized text denial and
preservation across an operator upgrade. Actual one-time provider delivery,
native host identity, task-budget linkage and the owner pilot remain unaccepted.
Keep #175/#91 open. Preserve current Sessions, failed native/roots fixtures and
ledgers. Q-24's native routing question remains pending; it does not block this
provider-neutral source contract or authorize a fleet UX choice.

## agent-run source client (2026-10-27, OpenProject #215)

The provider-neutral `agent-run inbox` client exposes explicit send, list, read
and ack against these existing routes. Send requires a caller-supplied stable
key and bounded UTF-8 stdin; it does not generate a replacement key or replay a
mutation. A 503 or lost response is unconfirmed: the caller may explicitly retry
only the identical intent with the same key, as the server contract permits.
Reads do not acknowledge. An acknowledgement is an API record, not proof of
provider consumption. The server remains authoritative for identity and scope;
a configured coordinator can read its authorized project but cannot publish or
ack on behalf of a Session. Source tests do not establish installation or live
usability.
