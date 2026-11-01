# DESIGN-044: HOME preservation verifier and read-only retention classification

Source-only slice OP350, under Epic 99, linked to OP102 and issue174/H7-H10.
[ADR-018](../adrs/018-two-tier-home-retention.md) records the newer owner policy.
Older archive delivery gates remain historical and do not add owner intent to
this slice. Source, tests, merge, installed state and usable acceptance must be
reported separately. No physical storage saving is inferred from filesystem
measurements or a candidate count.

## Policy

The classifier accepts an explicit evaluation time and finite synthetic/offline
records. It produces two UID-ordered lists: immediate safe-preserved candidates,
and a stopped-work queue with clock source, warning, deadline, decision status
and named exceptions. Active/held/named special-disposition records remain
visible with protected status; unsafe identities remain explicit queue
exceptions. Seven days is exactly 168 hours; equality
is expired. The configurable warning lead defaults to 24 hours. A clockless or
future-dated record enters decision status now, with no invented deadline.
Unknown work with a valid old completion clock remains in the expired decision
batch even when terminal or preservation proof is absent. No per-HOME model
review, sends, scheduler, deletion API or enable switch is part of this package.

Prefer a recorded provider-task `finishedAt` for the same immutable binding. If
there is no task-completion record, a durably recorded actual executor terminal
observation may supply `observedAt`; the output names this fallback. An invalid
present task timestamp cannot silently fall back. Pod absence, Session phase,
PVC birth, filesystem mtime and classifier run time are not completion clocks.
A valid completion clock does not by itself prove terminal state or no mounts.

The current authenticated observation also binds lifecycle state, owner hold and
special disposition. Raw metadata cannot clear protection while reusing an old
signature. An unverified protective marker prevents immediate candidacy and
keeps protected status. Stopped holds and special fixtures still enter the owner
disposition batch at expiry or when their clock is unknown, with
`dispositionDecisionRequired: true`; protection never silently exempts stopped
work from a decision. Verified active work remains protected without expiry actions. Unverified
reported activity stays protected but retains any expired/clockless owner
verification decision; it does not acquire deletion or disposition authority.

## Verifiable evidence and trust

The proof is a versioned attestation, not an input `CleanAndPushed` flag.
`EvidenceVerifier` is an explicit trusted caller boundary: an existing agentd/
operator integration can authenticate exact payloads and receipt scopes through
its supported authenticated channels. It cannot trust a snapshot's own boolean
or claimed identity. No new production signing service or public-key provision
is a compulsory cleanup prerequisite. Remote publication receipts must bind the
exact repository identity, local ref/commit and authenticated remote ref/commit.
The collector must obtain that evidence via an authenticated push acknowledgement
or authenticated remote query, not stale remote-tracking refs or a PR link.
For portable offline input, the optional `TrustStore` verifier uses separately
configured purpose-scoped Ed25519 public keys; inputs cannot introduce trusted
signers. This prevents an untrusted file from asserting its own authentication.
Signatures authenticate collector assertions; they do not query Git/Kubernetes
or turn a self-signed fixture into real preservation evidence. The offline CLI
has no flag that promotes arbitrary JSON into an authenticated caller boundary.

Each binding includes SessionUID, HOME/PVCUID, PodUID, executor attempt,
writer generation and current HOME revision. Inventory must account for every
repository, all worktrees/HEADs, local branches, tags and every stash entry.
No staged, unstaged, untracked, unpushed or stashed work may qualify for the
immediate clean-and-published route. A complete empty inventory is explicit,
never inferred from an absent array. Each local ref and detached HEAD needs a
receipt for its exact commit; annotated tag objects must be preserved too.
Required handoff/result/recovery/history artifacts have exact content digests
and verified durable-copy receipts. Known no-history-required is explicit;
unknown requirements block eligibility with a named exception. No credential
or provider-cache archive is implicitly required.

Requirements carry authenticated `source` and immutable `revision` references to an
existing authoritative decision. Missing references are a named exception.
Resolving those references and proving that the collector applied the actual
owner requirements is part of collector integration, not new decision authority
for a signer. Repository identity must resolve to the authenticated remote's
exact repository, using its immutable repository identifier where available;
an arbitrary matching display name is not collector evidence. Initialized
submodules need their own complete repository inventory and receipts as well as
the parent worktree's clean-submodule check.

The existing deterministic rescue checks in `internal/agentd/rescue.go`
(`listWorktrees`, `unpushedRefs`, submodule checks and fetch refusal) are the
collector foundation. They enumerate all branch/tag candidates and every stash,
and check dirt and unpushed commits. Rescue can create a local rescue branch or
bundle and is therefore not invoked by this read-only classifier. Its fetched
remote refs and `RescueReport.CleanAndPushed` do not supply the new authenticated
publication receipt or a complete authenticated HOME inventory. This slice reuses the
check semantics; the exact remaining collector integration is described below.

## Freshness and races

Immediate classification needs a fresh trusted observation of the exact current
Session/PVC/executor/revision, a confirmed terminal executor and complete active
mount enumeration with none present. Unknown, active, replacement or newly
resumed PodUIDs refuse candidacy. The observation must be at or after proof
capture, within a short configured freshness bound, and carry the latest proof
identifier. Late writes, changes to refs/worktrees/stashes/artifacts, proof
supersession, a resume, writer-generation increment, new PodUID or PVCUID
invalidate old evidence. They do not silently restart an old retention clock.
A new completed attempt has its own explicitly bound completion record. Old
records lacking bindings/evidence remain visible unknown/decision records;
there is no backfill that labels them preserved based on success or age.

The proof and authenticated publication/durable-copy receipts also need fresh
verification within the configured observation age bound. A remote ref or copied
artifact can disappear without changing the HOME revision; a fresh mount check
cannot refresh an old remote receipt. An old push acknowledgement may therefore
need a new authenticated remote query before it supports immediate candidacy.

A dry-run observation is only a point-in-time claim. Any later approved
destructive executor would need uncached Session/PVC/Pod/mount reads, renewed
write fencing, the same exact approved UID manifest and UID/resourceVersion
preconditions immediately before action. This package cannot close that race
and never authorizes action. Owner holds and fixture dispositions survive all
proofs. It never weakens current finalizers or coordination retention.

## Bounded interface and remaining integration

`internal/homeretention` uses the standard library and existing agentd protocol
validation helpers; it adds no external dependency.
`cmd/home-retention-dry-run` reads a bounded strict JSON snapshot and optional
separate trusted public-key configuration, and writes JSON to stdout. Its
explicit time and policy settings make output reproducible; it has no network,
subprocess, Kubernetes client, filesystem mutation or delete mode. Unknown
versions/fields and oversized or duplicate records fail closed. Missing legacy
proofs classify as exceptions rather than becoming unsigned truth. A bounded
`legacy-snapshot` adapter consumes existing sanitized AgentSession/PVC/Pod audit
metadata, preserving clock paths, recorded associations and snapshot hashes.
Its UID-specific fixture dispositions can add protection but cannot remove it.
It never promotes old rescue booleans into preservation proof and cannot use
`--trust`. Unused provider metadata is ignored within strict depth/byte/count
and duplicate-member bounds. Known unbound timestamps stay in provenance;
missing bindings remain unknown decisions, never invented identities. Original
provider attempt identity comes only from recorded `task.exitSource` (or matching
retained `taskTerminal.task`), never Session generation/later configuration.
Legacy raw clock age remains visible under `recordedWarningAt`,
`recordedDeadline` and `recordedAgeEligible` without becoming an exact bound task
clock. Conflicting task sources or timestamps before Session creation remain
explicit exceptions. Null task/no recorded timestamp may use a valid matching
actual terminal record; a present invalid timestamp never silently falls back.
A valid unsigned bound clock carries `completion_clock_unverified`; offline
queue arithmetic trusts its recorded value for planning, not as authenticated
cleanup evidence or permission to reset the durable source record.

Remaining integration is a separately owned collector: (1) adapt the existing
rescue Git checks into non-mutating complete HOME inventory collection with
read/write fencing; (2) capture authenticated exact-remote publication receipts;
(3) enumerate and preserve explicitly required artifacts; (4) authenticate exact
proof provenance through existing authenticated agentd/operator identity (with
optional offline signatures when needed); (5) collect uncached
Session/executor/PVC identity and all current mounts, validate terminal
provenance, record revisions/supersession and authenticate fresh observations. Existing
records do not yet have this evidence. No production attestation keys, collectors
or authorities are provisioned here. Notification delivery, owner decision UX,
approved batch execution and rollout remain separate. No cleanup is enabled.

## Validation

Use synthetic fixtures only. Inspect tests before execution; serialize suites
with `/tmp/dev-env-go-tests.lock`, `nice -n 19`, `GOMAXPROCS=2`, `-p 1` and bounded
timeouts. Cover immediate publication, dirty/untracked/unpushed/stash refusal,
missing/untrusted/foreign receipts, required history/recovery, 24-hour warnings,
exact 168-hour equality, missing/future clocks, late writes, active/unknown/new
PodUID and PVCUID races, superseded proof, legacy records, deterministic ordered
UID manifests, strict input bounds and absence of deletion/default activation.
Independent native review and normal required CI apply. Automated Claude review
is advisory; handle findings when it is available. No live tests or broad suites.
