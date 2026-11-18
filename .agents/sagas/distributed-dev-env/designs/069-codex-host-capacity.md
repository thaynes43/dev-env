# DESIGN-069: Bounded capacity for ordinary Codex hosts (OP922)

Status: proposed source contract, 2026-11-18. Nothing installed or accepted.
Owner: story 922's coordinator; this branch is its worker's delivery.
Companion decision: ADR-032. This supplements DESIGN-063 and preserves
DESIGN-067 section 6.2's ordinary/workbench isolation.

## Investigation at pinned main adce1019344a62ebdb65ade85927410e241776c2

1. `internal/agentrun/create.go:createSession` calls
   `codexBeforeCreate` for a non-restore Codex interactive request.
   `internal/agentrun/codexhost.go` sends a read-only
   `POST /v1/codex-hosts/select?check=true` with repository, profile and launch ID.
   It sends neither requested size nor model. `selectCodexHost` resolves the
   account/profile, collects host facts and calls `codexhostpool.Select`.
   `Select` picks any suitable running host before resume/create. An older pod
   revision sets `outdated=true` but is usable with reported agentd >=2.102.0.
   The CLI prints that `use` object and never calls session create. This is the
   code path explaining the size L request answered with the existing size S
   host. It does not prove a new create was submitted to the API.
2. `at_capacity` counts Running, undeleted, unarchived, unprotected ordinary
   Codex remote hosts, suitable or not and pool-labelled or not. Failed rescue,
   hands-off, reserved, workbench and unresolved accounts are excluded. Defaults
   are one per resolved profile and two fleet-wide. Busy leases count hosts;
   neither memory, thread count nor lease count is a capacity input. A running
   unsuitable host filling the profile is normally `report` with its reason.
3. There is no CLI fresh-host flag or select API fresh field. The supported
   `POST /v1/sessions` Codex remote create accepts human, trusted client and
   workbench-session callers (the latter only on the default profile). It
   validates in `newSession`, admits via `admitCreateDirect`, and writes via
   `createWithCallerCap`; it does **not** call the pool selector or enforce its
   caps. It can therefore create a second same-profile host directly. A named
   resume can also bring an eligible suspended host back; neither route is an
   automatic capacity-aware fallback. Ordinary sessions/coordinators cannot
   create Codex remote hosts. CLI rescue restore skips the selection preflight,
   but restore validation and ordinary remote creation authority still apply.
   Existing operator flags `--codex-host-max-running-per-cell` and
   `--codex-host-max-running` can raise count caps after a reviewed deployment;
   they never override reuse of a suitable running host. With higher caps and
   no suitable/busy host the old selector can already return `create`.
   Disabling selection skips CLI preflight too. None of these is a client
   fresh request, and no setting is changed here.
4. `protocol.Status`/`AgentStatus` report agent state, attached tmux clients,
   heartbeat, reporting pod UID, version and repository bootstrap; remote status
   reports pairing/daemon state. There is no native live-thread count, memory
   usage or memory headroom. Attached tmux clients are not phone threads. The
   single annotation lease serializes launches for at most 15 minutes and is
   not a workload measurement. Session size is requested/default size; the owned
   pod's agent-container resource limit is the actual memory budget (including
   any approved raise). Fleet resources describe requests/limits, not usage.
5. OP695 supplies automatic folder trust. OP696 records Tom's 2026-11-10 reuse
   ruling: keep a few reusable hosts so he does not pair constantly. OP709
   permits a usable older image and exposes `outdated`, without restarting it.
   This work does not change those rules or touch any running host.

## Proposed rule and boundaries

Agentd on an ordinary Codex remote host passively reads its own cgroup v2
`memory.current`, `memory.stat` and `memory.max`. It reports the working set (`memory.current` minus `inactive_file` from
`memory.stat`, floored at zero) and the limit, with sample time;
no process list, native RPC, path or transcript is sent. Missing/unreadable,
unlimited, malformed, stale or old-pod reports are unknown and preserve reuse.
A host is full when a fresh current-pod sample leaves less than 1 GiB OR less
than 20% of its finite container memory limit. This reserves one modest turn's
headroom; it is a conservative admission estimate, not an OOM guarantee. Memory
can grow after the sample and native phone starts bypass operator selection.
Use another suitable host first; preserve leases and all safety exclusions.
Only after no host has room may capacity yield `create` with `host_full`.

An explicit `freshHost` request is also needed: the existing old host cannot
report the new observation and must not be restarted to add it. Only the
existing human or trusted-client caller classes may ask; no v2 session pod,
including a workbench session, and no coordinator may do so. The trusted-client
class includes the v1 service account and retains its existing create authority. It does not grant creation
rights. CLI spelling is `--fresh-host`, only for new Codex interactive hosts.
Record the reason and authenticated requester as operator-owned annotations on
the new session. Do not expose new fields in legacy Session/list/fleet views.

Reuse stays the default. Normal profile cap remains one; `host_full` or an
explicit fresh request may use a second slot, with a fixed upper bound of two
ordinary retained hosts per profile and the existing fleet running cap. Pending
creates count. GitOps ships an empty `dev-env-codex-host-capacity` ConfigMap
in the configured policy namespace (`--policy-namespace`, default `dev-env-system`),
with no `pending` key, `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent` and
`kustomize.toolkit.fluxcd.io/prune: disabled`. Reconciliation must never reset
operator reservations. The operator only reads and updates this named object.
Compare-and-swap appends one counted reservation per create, capped by profile
and fleet; uncertain creates occupy one slot, rather than block the whole fleet.
A snapshot of reservations is read **before** the live session list: a create
that commits and releases during that list is still counted. Hosts and their
reservation are deduplicated only by the exact attempt annotation, not name.
No operator/session mutex or TTL is the authority for capacity.

The create API rechecks selection and bounds, so bypassing CLI cannot exceed
them. With selection disabled it still enforces two retained hosts per profile
and the default running fleet bound; explicit fresh intent requires selection.
Rescue restore skips reuse selection, since using an existing host cannot restore
the rescued state, but still observes the fixed profile and fleet bounds. Portal
callers already lack Codex remote creation authority; this does not expand it.

A pending session counts before it becomes Running. A reservation is reclaimed
only when its exact attempt annotation is observed on the created session;
absence or another attempt keeps that one slot for operator-owner review. No
automatic expiry or unproved absence releases an uncertain create. At the bound,
creation stops; use of existing suitable hosts continues. A safe reset for an
absent unknown attempt requires independent fencing of the old creator and is
outside this story. No unsupported reset or manual ConfigMap edit is prescribed.
Definite refusals, including the API's own typed admission and stale-schema
refusals, release their own reservation. CAS prevents a stale release from
clearing another attempt. Same-key in-flight creates are fenced by a parent/key
hash in the reservation; committed same-key creates are rechecked before write.
No host is stopped, resumed, resized, reimaged or paired by this work.

### Installation prerequisites (not performed here)

The coordinator must arrange the haynes-ops CRD copy before the operator pin,
and confirm `get` and `update` ConfigMap permissions on this one name only in
the configured policy namespace. The operator never creates the ledger.
No RBAC prerequisite PR has been supplied or opened by this worker: haynes-ops
changes are expressly outside this work order. Installation remains unfulfilled
until that GitOps prerequisite is linked and verified. New agentd is needed for
memory samples; an authorized fresh request works with an old reporting host.

## Coordinator rulings, 2026-11-18

These are rulings by coordinator `dev-env-fable-coordinator-1109` as story 922's
owner, not rulings by Tom. OP922 comment **9217** records R1–R4; comment **9228**
records the independent review and dispatch of fix-round rulings G1–G9.
Comment **9222** is the previous source/test handoff, superseded by this round.

- **R1 / Q-069-01 answered:** retain less than 1 GiB OR less than 20% remaining;
  measure after install and tune only in a later PR. **G5** requires the working
  set in this round, overriding the review's suggestion to defer M4.
- **R2 / Q-069-02 answered:** unknown creates retain their slot, with no expiry,
  manual reset or extra slot. **G2** names session and attempt in refusals and
  logs, uses `create_unconfirmed`, and lists a phantom only as skipped. A
  supported reset must fence the old creator. Follow-up story **925** exists
  (comment 9228); ADR-032 remains **Proposed**, with no installation or acceptance.
- **R3 / G1:** the operator never creates the ledger. Missing, forbidden or
  malformed ledger is HTTP 503 `capacity_ledger_unavailable`, naming the
  ConfigMap and state. It blocks creates (including the first host and Codex
  rescue restore) and selection-chosen resumes, while `use` and `report` keep
  working. Malformed data is never overwritten. GitOps and named get/update
  rights are installation prerequisites, outside this worker's scope.
- **G3:** Kubernetes 403, 422, 429, 404, 405, 406, 413 and 415 are definite
  refusals and release the reservation, as do existing definite API refusals.
  Create ignores client cancellation but has its own 30-second timeout. After
  an unknown outcome, read its name once; the exact attempt releases because
  the committed session now counts itself. Everything else keeps its slot.
- **G4:** rescue-blocked and unresolved-account ordinary hosts retain a profile
  slot until archive or removal. Only reserved, workbench, hands-off and
  protected hosts stay excluded. The existing running-cap exclusions remain.
- **G6:** reconstruct P1–P6 and prove the fresh guard, malformed-ledger and
  Forbidden/Invalid guards by killing review mutants X1–X3.
- **R4 / G7:** use ADR-032 (ADR-031 already belongs to stop recovery); DESIGN-069
  keeps its number. The decision stays Proposed.
- **G8:** refusals must not print empty action/host fields. After a committed
  create, retry release a bounded number of times before logging exhaustion.
  A pod can steer its own memory report, which grants no create authority.
  The no-session-fresh rule covers v2 session pods; trusted-client callers,
  including the v1 service account, keep their existing create authority.
  A future-dated memory sample is unknown. A resume takes no reservation, as
  before this change; concurrent resume/create can still exceed the running
  fleet cap, without changing the retained-host bound.
- **G9:** record these coordinator rulings and provenance here; do not attribute
  them to Tom or mark the decision Accepted.

## Evidence and delivery

Source, tests, installation and acceptance are separate. This PR will carry the
focused cases, all apiserver tests, lint/layout/generation and CI results, plus a
table of decisions and actual mutation failures. Installation requires a later
CRD/operator/agent image delivery. An owner obtaining and pairing a second host
is acceptance and is outside this story's worker scope.

## Wire compatibility

There are **no new response fields**, even when this feature is used. Reasons
`host_full` and `fresh_host` use existing keys. New request `freshHost` is omitted
unless true. `status.agent.codexHostMemory` stays internal to the CRD; public
Session detail/list and fleet mappings deliberately omit it and the intent
annotations. At tags v2.104.0 and v2.111.0, checked `showOnce` →
`strictSessionJSON` / `strictWireJSON` plus `DisallowUnknownFields` in
`internal/agentrun/show_once.go`; directory list and budget reads use the same
strict walker, and canary receipts validate Session too. Ordinary `callWith`
and `chooseCodexHost` use tolerant `json.Unmarshal`, but cannot justify exposing
a new unconditional Session key. The new fields do not enter any of these
response DTOs. Tests cover populated telemetry through show/list/fleet and
normal versus fresh CLI request serialization.

## Initial worker validation checkpoint (superseded by fix round 1)

The full apiserver suite passed with envtest 1.35.0 installed after correcting
the new admission path to reuse the already pinned per-request template/account
snapshot (`TestCodexWorkbenchAPIResolvesOnce`). Full agentd, protocol, CLI,
selector and CRD suites passed. The final PR records lint, generated-code,
layout and CI results separately.

[Mutation evidence](../evidence/069-capacity-mutations.md) records all capacity
independent removals, the exact edits and the assertion failures. Originals were
restored byte for byte. Existing hands-off, protected/reserved/workbench and
lease cases remain part of the full suites. No native independent agent was
started: this work order explicitly forbids other agents/sessions. The advisory
Claude Opus PR review and coordinator's pre-merge Opus read remain the review
route authorized by this story.

## Advisory review corrections

The first Claude Opus review found a definite-refusal cleanup bug and a
fleet-wide unknown-create hold. Both were corrected: typed API refusals release
their slot, and counted reservations leave other slots usable. A read-order
regression test commits/releases another create during the live list. Restore
has an explicit preserved path with bound tests. The ConfigMap follows the
configured policy namespace and names the outstanding GitOps RBAC prerequisite.
All 28 final mutation runs are recorded in the linked evidence.

Final local verification on code head `67165313736c6922ca4d4ee90c234e7684af62f4`:
all apiserver tests passed in 98.945s; selector, operator command and API wire
suites passed. `make generate lint check-layout` passed with zero lint issues
and no generation diff. Full agentd (245.090s), protocol, CLI and CRD suites
passed during this delivery. All 28 final mutation runs failed their acceptance
assertion and restored the original files. The second Claude Opus advisory
review said “Looks good” with no new findings; R2 answers Q-069-02 with its stated limit.
CI must still finish on the delivered head; the PR/handoff records that result.

## Fix round 1 compatibility and evidence

G2 changes two answer shapes, with no added keys:

- `POST /v1/sessions` errors and ledger-dependent selection errors retain
  `ErrorResponse.error.{code,message,reason,fields}`. Names and attempts go only
  in the existing `message` string. An unconfirmed bound from selection is
  HTTP 409 in this existing error envelope: released CLI code collapses a
  200 refusal choice to its reason sentence and would hide the attempt.
  `TestUnconfirmedSelectionNamesAttempt` checks both interactive preflight
  and `codex-host select` preserve the name and attempt. `create_unconfirmed` /
  `capacity_ledger_unavailable` use `reason`. Released v2.111.0 already declares
  that reason field. v2.104.0 lacks it but decodes refusals with tolerant
  `json.Unmarshal` (`internal/agentrun/client.go:280–282` at that tag).
- Successful `POST /v1/codex-hosts/select` retains `host`, `action`, `ready`,
  `outdated`, `reason`, and `skipped` rows with only `name` and `reason`.
  A phantom appears only in skipped, as `create_unconfirmed`. v2.104.0 reads
  selection with tolerant `json.Unmarshal` (`internal/agentrun/codexhost.go:103–104`);
  v2.111.0's DTO already has the full shape. No attempt/requester field is added.

The strict released paths (`internal/agentrun/show_once.go:34–35,46–55` at
v2.111.0, directory/budget/canary readers) validate Session and other successful
DTOs, which are unchanged. `TestMemoryHeartbeatAndLegacyViews` still checks
populated telemetry through show/list/fleet. `TestCreateUnconfirmedIsNamed`
strictly decodes both changed answers against frozen v2.111.0 field sets.

[Fix-round mutation evidence](../evidence/069-capacity-fix1-mutations.md)
records each isolated broken guard, its failing named test, and restoration.
Tests use fake clients or local envtest, sanitized child environments, two CPUs,
one package at a time and no real sleeps in the new cases. Source/tested results
are recorded on the replacement PR and tracker handoff at the delivered head;
this worker performs no install, host action or acceptance.
