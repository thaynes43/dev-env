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

`internal/homeretention` uses the standard library, existing agentd protocol
validation helpers, and the shared API annotation constants for read-only legacy
hold projection. It adds no module dependency. The collector's optional
`ExecGit` uses the standard-library process API. The dry-run command does not
call it.
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

Source now contains the read-only collector, the caller-supplied decision
store, and the exact-UID preflight described below. Those components do not
activate collection, notification, or cleanup. Existing records still lack live
evidence. No production attestation keys or authorities are provisioned here.
No cleanup is enabled.

## Source collector, decision records, and preflight

`CollectPreservation` adapts the read side of `listWorktrees` and
`unpushedRefs`: porcelain worktrees, every branch and tag, annotated tag
objects, every `stash@{n}`, detached `HEAD:<path>`, staged, unstaged,
untracked, nested, in-progress merge/rebase, and initialized submodule state.
It reads each repository twice and again after receipt queries. A read error,
unsupported repository config, symlink, unbound submodule identity, prunable or
bare worktree, or any ref/HEAD/HOME revision change returns an error and no
inventory. The command allowlist refuses fetch, checkout, reset, stash writes,
update-ref, and remote-tracking comparison. `FetchSucceeded` stays false. A
remote-tracking ref is not publication proof, and neither is that boolean.

Publication and durable-copy proof come only from injected
`PublicationSource` and `DurableCopySource` values, checked with the existing
`EvidenceVerifier`. A nil source attaches no receipt. A receipt that does not
verify for the exact binding, repository, and object fails the collection.
Requirements and artifact digests stay caller-supplied. The collector does not
scan a HOME directory or provider history. `ExecGit` uses a private empty HOME,
clears the caller environment, and passes `protocol.allow=never`, disabled
hooks, fsmonitor, helpers, recurse, and external diff both as `-c` and as
inherited `GIT_CONFIG_COUNT` pairs. Each Git command is limited to the shorter
of the caller's remaining deadline and 30 seconds.

Git does not let an alias replace a builtin, so `alias.status` does not run
instead of `git status` and `alias.config` does not hide the config scan. The
collector still refuses `alias.*` because it is not non-executing repository
data. Before `status`, `ls-files`, or `submodule` in a worktree or submodule, it
lists local config and worktree config when `extensions.worktreeConfig` is
true. Any file or blob key outside the small non-executing set (core data,
user name and email, remote URL and fetch, branch upstream, submodule path, URL,
and active) fails closed. The scan does not follow symlinks. A nested `.git`
that status omits because it is ignored is recorded as a nested repository,
including under an ignored directory. A symlink stops that walk. `BISECT_START`
is an in-progress operation. The CLI is not wired to the collector.

`ProjectDecisions` writes warning and decision rows through a caller-supplied
`DecisionStore`. The clock is the caller's, including a test clock. Retention
is 168 hours and the default warning lead is 24 hours, using `Evaluate`. The
same binding and projection does not write again. `RecordOwnerDecision` accepts
only `retain`, `approve_exact_cleanup`, and `withdraw` for the open row of that
exact binding. A new Session, PVC, Pod, attempt, writer generation, or HOME
revision supersedes the row and starts at `pending`. The old disposition stays
on the superseded row and is not copied. There is no service, notifier, or
enabled loop.

`Preflight` compares one approved UID set with fresh caller-supplied uncached
fences: Session, PVC, and Pod UIDs, resourceVersions, writer revision, mounts,
proof, required artifacts, and `approve_exact_cleanup`. It refuses a replaced
PVC or Pod, a late write, a new mount, stale or mismatched proof, a replayed
approval, a future timestamp, and any other approved set. It also reuses
`homeSafety` and the preservation inventory check: state must be stopped, hold
empty, and disposition empty or normal; worktree, ref, stash, and unpushed
arrays must be present; each worktree head ref must exist; the signed
observation must be at or after proof capture; an artifact whose requirement is
not `required` is inconsistent. Publication remains the per-ref authenticated
receipt. `FetchSucceeded` is not consulted. `DeleteEnabled` and
`ExecutionAuthority` stay false. It does not delete, strip finalizers, or call
Kubernetes. Passing preflight is not permission to delete. Controller guards
are unchanged.

`Evaluate` uses the same inventory check. A false `FetchSucceeded` with current
authenticated push or query receipts can be preserved. A true `FetchSucceeded`
without those receipts cannot. The boolean remains on the wire and never
establishes remote publication. Unknown artifact requirements, unknown
dispositions, and stale or forged proofs stay refused.

Residuals, not delivered here: uncached Session, PVC, Pod, and mount reads stay
caller-supplied. Decision persistence stays behind `DecisionStore`; no production
adapter is supplied or deployed, so durable persistence remains integration work. No operator,
agentd, or dry-run CLI path calls these functions. The layout sentence for this
package was not edited. A repository config key this collector does not
recognize fails closed rather than being executed. An ordinary symlink in a
worktree stops the nested walk instead of being followed.

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
