# DESIGN-015: Retain common instruction inputs for private managed tasks

**Scope:** next bounded #173 source unit, after the one-attempt canary guard.
Default off. ADR-003 controls rules, independent homes and preserved WIP.
This source unit runs no provider and chooses no native fleet UX.

## Evidenced gap

Project snapshots bind catalog bytes and project.rules. Template revision binds
mount names/paths, not mounted ConfigMap contents. Claude's global instruction
file is a live mounted-file symlink; Codex global instructions are regenerated
at boot from the current header and common Claude file. Resume retains the project
wrapper but can reread changed configured developer/fallback inputs. No current
common source revision proves those effective inputs. Existing project/root
proofs and model-free acceptance must not be presented as that missing proof.

## Contract

Add an explicit default-off common-rule snapshot route for new managed private
tasks/canaries. Require accepted catalog/project authority, retained private
history and owned private provider homes. Keep global private task admission
off. When enabled for a canary, its trusted fixed policy must name the expected
CommonRevision; the caller cannot select a source, revision, text or path.
Actual canary activation waits for this contract in addition to DESIGN-011.

Resolve common sources with uncached named API GET of the existing fixed
`dev-agents/dev-env-config-claude` key `CLAUDE.md` and
`dev-agents/dev-env-config-codex` key `AGENTS.header.md`. Only instruction data
belongs in the snapshot. No credential, enrollment, whole provider configuration,
MCP authentication or native database copy is permitted. Validate bounded exact
UTF-8 input, known keys, canonical identities and serialized snapshot size.
Use at most 64KiB common text and 16KiB header. The source parser must refuse
oversize/malformed/ambiguous inputs before allocation or mutation.

Retain exact common bytes and source namespace/name/key/UID/resourceVersion/hash,
the project snapshot digest, and one canonical CommonRevision. The revision is a
deterministic digest of fixed source identities and content hashes; observation
times and resourceVersions are retained provenance, not hidden policy inputs
that GitOps cannot declare. Bind the immutable accepted snapshot to the original
Session and trusted record. Independently recheck original source authority and
content before the first executor. Lost acknowledgements and exact retries
retain the original accepted snapshot; changed inputs cannot create a replacement
Session or silently recapture history.

On this route, create owned private regular global instruction files from the
accepted bytes once: Claude common text, and Codex header + newline + common text.
Suppress mutable legacy global rendering for that accepted snapshot. Do not
adopt foreign files or replace unexplained symlinks/state. Use the existing
private ownership, bounded-file, durable intent and write-fence helpers. Preserve
provider credentials, unrelated configuration, repositories, task worktrees,
local commits, index/dirty state and operations in progress.

Seal only the instruction-relevant effective configured developer/fallback/guard
projection in the private retained launch receipt before the first provider
launch. Record its derivation and digest separately from CommonRevision; never
label locally configured inputs as server-authoritative source bytes. Reuse
existing TOML/rule composition and immutable project snapshot helpers. Preserve
native repository instruction discovery and compose the saved common/project
inputs with the separately pinned repository. Do not copy whole configuration
or silently drop configured instructions.

Projection creation has a durable original digest and an explicit Fresh,
Preparing or Confirmed latch in the private ownership receipt. Persist and
verify Preparing before exclusive creation; only full Confirmed original inputs
may be reused. Missing, partial or unknown historical projection state refuses,
including a repeated first-launch call before launch.json exists. Configuration
drift cannot reconstruct lost inputs. The unreleased receipt format does not
migrate uncertain older receipts; preserve their bytes and WIP.

Resume uses the original retained global bytes and effective projection.
Changed source ConfigMaps or current configuration cannot rewrite them. Foreign,
partial, ambiguous or mismatched files/receipts refuse while preserving WIP;
do not reconstruct missing historical bytes from current configuration. Legacy
sessions/snapshots and native hosts keep their supported behavior with common
provenance explicitly Unknown. This unit grants no native routing, restoration,
refresh-owner, paid API or cleanup authority.

## Paths and acceptance

- A small common-rule source/snapshot package with deterministic bounded identity.
- `internal/apiserver`, `internal/projectcanary`, operator settings and controller:
  uncached named resolver, fixed expected revision, immutable binding and
  independent pre-executor validation.
- Typed Session/schema and `internal/protocol` transport, retaining backwards
  compatible old snapshots without fabricating common provenance.
- `internal/agentd` rendering, launch/resume and retained instruction helpers:
  owned private globals, exact effective projection and preservation fences.
- A later GitOps PR adds only named nonsecret ConfigMap GET to the operator's
  existing namespace Role before signed activation. No new worker grant.

Focused tests must prove exact common/header/source/project binding, malformed
and byte-bound refusal, two-provider global bytes and actual argv composition,
source drift before Pod creation with zero executor, lost-ACK/original retry,
immutable schema, clean new ownership, foreign/symlink/partial-state refusal,
and resume after source/config changes without rewriting globals, effective
inputs or WIP. Preserve legacy byte fixtures, dirty/index/local-only commits,
worktrees and operations. File presence and synthetic argv tests are source
proof only; actual loaded common and distinct repository markers must later be
proved through provider output/events, with marker values absent from the prompt.

Use sequential focused nice19/GOMAXPROCS2/p2 checks, cached schema/codegen tools,
affected lint and native independent review before required CI. No burners,
stress, wide/looped tests, local images, real credentials or runtime trial.
Claude reviews are optional/down; read available findings and retain unavailable
outcomes without retries. Stop the logical blocker at three failures or sixty
minutes without evidenced progress, preserving receipts and one native question.

## Remaining acceptance

Signed source/schema/named GET promotion and snapshot-enabled canary profiles
precede the actual managed-provider rule trial. Native project entrypoints,
complete-chat routing, directory/history, messaging/claims, budgets/recovery,
required-access guards, truthful usage and the owner pilot remain open. Q-24
stays pending. Local-model rule/provenance contracts remain provider neutral;
Qwen inference waits for GPU readiness and household reservations. Preserve v1,
all current sessions, failed fixtures and ledgers. Keep #173/#91 open and report
built, deployed and accepted separately.


Transport ruling, 2026-10-11: keep the accepted snapshot in immutable Session
spec and render the canonical launch document through a fixed controller-owned
Pod annotation and DownwardAPI file at `/opt/dev-env/session/session.json`. Only
the new route sets `AGENTD_SESSION_FILE`; legacy environment transport is
unchanged. Reject documents above 192 KiB before admitting a Session and total
Pod annotations above Kubernetes' 256 KiB limit before executor creation. Raw
64 KiB common/16 KiB header ceilings and the serialized ceiling both apply;
heavily escaped input can fail the serialized ceiling. Test exact and oversized
limits and worst-case escaping.

This projection needs no per-session ConfigMap, create permission or admission
policy. The existing original Session/retention/one-executor fences govern the
retained input and Pod identity. Only named GET access to the two trusted common
source ConfigMaps is added; the caller cannot override annotation, file or mount.


The typed common source revision and retained launch-input contract also applies
to the local-model provider. This increment activates only the two existing
subscription CLI adapters. The local adapter remains disabled until its private
instruction projection, exact model identity and household GPU reservation gates
are implemented and accepted; it must not infer readiness or substitute a paid
API. No local inference is launched by this slice.


Privacy prerequisite from the finite source audit: explicit canaries currently
mount the entire shared scratch PVC at `/home/dev/.shared` without a subpath.
The same UID can reach peer memories, task-log copies and rescue Git bundles;
source documentation warns those bundles can contain private untracked work or
secrets. Actual contents remain Unknown and were never inspected. No direct
mount of another private HOME or enrollment is established.

Omit that shared volume and mount only for explicit project canaries, and refuse
any rendered canary PVC other than its exact own HOME. Assert both Claude and
Codex shapes have exactly one own PVC, with no shared/workspace/alias mount.
Preserve all existing legacy/native/noncanary and hold rendering. This narrow
source correction removes peer artifact access before any rule-loading trial;
it is not a storage investigation, migration, cleanup or running-Pod change.
Coordination stays on the protected API.
