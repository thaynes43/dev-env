# DESIGN-006: Private catalog bootstrap and refresh

**Scope:** the next bounded source slice of [#173](https://github.com/thaynes43/dev-env/issues/173).
**Decision:** [ADR-003](../adrs/003-session-coordination-private-repositories.md).
**Checkpoint:** publish the contract within 30 minutes, then ship one tested,
independently reviewed PR. The logical task retains the 60-minute stall and
three-failure limits across agents, methods and resumes.

## Contract

A configured session can bootstrap every repository declared by the trusted
project catalog into its own repository directory. Startup and an explicit
refresh use the same preparation path. Configure
`AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP=true`,
`AGENTD_PROJECT_CATALOG=dev-env-system/dev-env-project-catalog` and
`AGENTD_PROJECT_CLONE_OWNER` together. Bootstrap defaults off. It needs neither shared-workspace
preflight nor a shared writer lock.

`agentd project-refresh` takes no catalog file or caller digest. It uses those
same pod settings and the standard in-cluster ServiceAccount token and CA.
Disabled bootstrap performs no catalog or credential read. Startup records a
separate `project-bootstrap` step and a private
`.agentd/project-bootstrap.json` receipt before selected-task preparation.
This slice covers managed-session daemon startup and explicit refresh. Native
host/client entrypoints and permanent project roots remain the next unit.
Concurrent refresh in the same private home is refused through a nonblocking
private lock; it creates no shared Git lock or cross-pod dependency.

Read the fixed `dev-env-project-catalog` ConfigMap with a named, uncached GET.
Use the existing catalog parser, owner validation and preservation helpers.
A caller-provided file or digest is not catalog authority. Record the catalog
UID, resource version and content revision in a private receipt, together with
each repository's observed source commit and preparation outcome. Recheck
catalog authority before publication; a changed catalog ends the operation
without resetting previously preserved state.

Clone a missing repository through the existing staged private-clone path.
Before fetching an existing clone, prove the root, common Git directory and all
origin fetch/push URLs match the accepted repository identity. Fetch the
declared default branch explicitly. A reference checkout may advance only when
it is an ordinary clean default-branch checkout and the change is a safe
fast-forward. Otherwise report the preservation reason and keep its files,
index, local commits, branches, linked worktrees and unfinished Git operation.
Ignored files also need preservation. Ambiguous identity refuses mutation.

Prepare repositories sequentially within a ten-minute operation deadline and
two-minute per-repository deadline; trusted catalog reads have a five-second
timeout. Retain per-repository failures. A receipt
reports what happened; it does not authorize a task to skip its freshness check.
Immediately before each new task, the existing selected-repository path still
fetches and pins an immutable commit. A failed selected fetch blocks that new
task. Resume retains its worktree, source receipt and saved project rules.

The contract is provider-neutral: Claude Code, Codex and a future local-model
adapter use private repositories and the same source/rules provenance. This
slice adds no local inference or GPU allocation. Subscription-backed execution
continues through its existing providers.

## Affected paths

- `internal/agentd/projects.go` and private bootstrap implementation: catalog
  iteration, preservation, revision receipts and reusable refresh.
- `internal/agentd/project_authority.go`: trusted named catalog reads and authority
  checks independent of shared workspace state.
- `internal/agentd/settings.go`, `daemon.go` and `cmd/agentd`: explicit configuration,
  startup integration and refresh command.
- Focused agentd/catalog/command tests, this design, plan 11, workflow guide and
  HANDOFF. Existing catalog, clone and task-source helpers remain the foundation.

## Acceptance for this PR

1. Missing repositories throughout the accepted catalog are cloned into private
   storage with shared features off; no shared lock or marker is required.
2. Clean default references refresh safely. Dirty files, staged index changes,
   ignored files, local-only commits, linked worktrees and unfinished operations
   remain preserved. Wrong owner/origin and ambiguous roots refuse mutation.
3. Receipts identify catalog/source revisions and partial failure without
   publishing credentials, provider homes or transcripts.
4. A moving remote is fetched again at new-task start and its immutable commit
   is pinned. Fetch refusal and resume/WIP regressions still pass.
5. Meaningful focused checks, required CI, native independent review and the
   available Claude advisory findings are handled on the reviewed PR head.

## Delivery boundary

Publish the agent release and promote the new-session template through GitOps.
Verify exact image/config revisions and protected pod identities. Do not replace
existing sessions to demonstrate startup. Private-project admission remains
disabled until its separate prerequisites and acceptance are met.
The existing worker ConfigMap read authority covers the catalog; promotion adds
only new-session settings. A signed-binary disposable bootstrap Job uses its own
ServiceAccount and a Role/RoleBinding limited to that one ConfigMap. It can prove this
preparation path without provider credentials or model turns; it cannot prove
actual provider loading.

#173 remains open for permanent project roots, client integration and actual
Claude/Codex rule loading on independent bounded pods. File or argument tests
are not real-provider loading proof. #174 history, #175 claims/messages, #154
budgets, #160 recovery and #176 provenance retain separate bounded contracts.
The Codex native routing gate must be resolved before committing the fleet UX.

## Q-24: Codex MVP native routing

**Asked through the native question tool on 2026-10-11; owner ruling pending.**
Emission is not a phone-delivery receipt. The finite capability audit did not
establish full native-chat routing from one paired computer to independent
app-server pods. Pinned 0.160.1 owns threads in-process. Separate command/build
executors do not isolate the native chat harness.

- **Separate paired session hosts (recommended):** each independent native
  session has its own bounded pod/app-server and private enrollment. More visible
  computer entries; live pairing and complete lifecycle still need acceptance.
- **One coordinator with isolated managed workers:** task execution has private
  bounded pods, while native chats share the coordinator's failure boundary.
  Choosing this requires an explicit narrowing of native-session isolation for
  that workflow. It is not full native-chat federation.

No option permits enrollment copying, concurrent rotating-token ownership or
silent paid-API substitution. No live pairing or daemon change follows from
the question emission. Bootstrap delivery proceeds independently.
