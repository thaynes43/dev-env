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

## Task source and reference fixes

Added 2026-10-12 for review findings roots-2 to roots-5 (#173).

- **Task source record.** A new private project task pins its source once.
  After the identity proof and the fresh fetch, and before `worktree add`,
  agentd writes `~/.agentd/task-source.json`. It is private (mode 0600) and
  written once: an exclusive atomic create that never replaces a file already
  there. It holds the session, repository alias, GitHub identity, declared
  branch, immutable commit, fetch time and the snapshot's catalog and rules
  revisions. A retry of a task whose worktree creation failed reuses the
  recorded commit, so the record and the worktree never disagree. The fresh
  fetch is still required before that retry. A record that binds another
  session or source, or cannot be read, refuses the new task and is kept.
  Resume never rewrites the record.
  `agentd ctl status` and the heartbeat report it as `workspace.source`;
  `workspace.head` stays the live HEAD. A task created before this record
  existed reports no source rather than a guessed one. Resume and status
  report the record only when it binds this session: its name and UID, the
  repository alias, GitHub identity and declared branch, and the accepted
  snapshot's catalog and rules revisions. An unreadable record, or one that
  binds another session or source, is a boot warning and a `task-source`
  problem. It reports no source and is kept as it is. AgentSession status and
  the CRD do not change: the heartbeat decoder ignores the new field.
- **Resume fetch notes.** On resume the selected-branch fetch only refreshes
  `origin/<branch>`. Its boot note says so and names the recorded source,
  instead of reading like the task's source.
- **Explicit refspec only.** The selected-branch fetch passes `--refmap=`, so
  only `refs/remotes/origin/<branch>` is written. A configured
  `remote.origin.fetch` can no longer move other remote-tracking refs or force
  local branches. This applies to bootstrap, project roots, sync and tasks,
  which all share the one fetch helper. The whole-origin fetches do the same:
  the boot fetch of a reused clone for a session without a project, and both
  rescue fetches (private and shared). Each passes `--refmap=` with
  `+refs/heads/*:refs/remotes/origin/*` and `--no-prune-tags`. Their
  `--prune` then removes only stale `refs/remotes/origin/*` refs. Before this,
  a configured mapping into `refs/heads` let a rescue's `--prune` delete the
  session branch and every local branch origin lacked, and `fetch.pruneTags`
  every such tag, before the rescue reported or bundled them.
- **Resume after identity drift.** The Git storage proof still gates every
  boot. A mismatch in the origin fetch or push URLs refuses a new task, as
  before. When the session's worktree, `agent/<name>` branch or first launch
  record already exists, the mismatch is a warning instead: agentd skips the
  fetch and reuses or recreates the worktree without moving it. It never
  creates a new branch from an unfetched base, so a launched task that lost
  both its worktree and its branch is still refused.
- **Staged task clone.** A missing selected clone uses the same staged
  reference clone as bootstrap: it checks out the declared branch, proves
  identity before publication, and never deletes staging. Preserved staging
  refuses the clone and stays in place for inspection. When bootstrap is
  enabled, the legacy clone used by sessions without a project also preserves
  existing staging instead of deleting it.
- **Recovery from preserved staging.** Staging at
  `~/repos/.<repo>.agentd-clone` is either bootstrap's preserved clone or a
  first clone cut short, for example when the pod was killed or drained. agentd
  cannot tell which, so it refuses the repo step on every boot and the agent
  never starts. Earlier, a private task deleted that staging and cloned again.
  The boot note names the staging path. A human recovers in one of two ways:
  - Inspect the staging from the session pod (`kubectl exec`) and remove it.
    The next boot clones again. Suspend and resume start that boot when the
    session's lifecycle allows a new pod.
  - Reap the session and create the task again. A task refused here never got
    a worktree or started its agent, so it has no work to lose.

The contract is provider-neutral: Claude Code, Codex and a future local-model
adapter use private repositories and the same source/rules provenance. This
slice adds no local inference or GPU allocation. Subscription-backed execution
continues through its existing providers.

## Tracked symlinks in the clean-checkout proof

Added 2026-10-12 after the Q-30 private-repo canary. That bootstrap ended
`Partial`: one catalog repository tracks a symlink (Git mode `120000`), and the
clean-checkout proof accepted only regular files (`100644`, `100755`). The
reference was preserved as holding "special tracked files", and the project
root that names it failed when it proved that repository's anchor.

One proof, `projectIndexProof` in `internal/agentd/projects.go`, decides that a
checkout is clean. It has four callers: the private reference refresh (this
design), the private root's anchor proof (DESIGN-007), and the shared-workspace
reference and anchor sync. It now accepts a tracked symlink when all of these
hold:

- The index entry is stage 0 with mode `120000`.
- No parent component of the path is a symlink, and the leaf itself is a
  symlink by `lstat`.
- agentd reads the leaf with `readlink` and never follows it. It does not stat,
  open or hash the path through Git, because `git hash-object` opens the path
  and would follow the link. Symlinks are left out of that batch.
- agentd computes the blob id in Go: the repository's object hash of
  `blob <length>\0<target>`. The object format comes from
  `git rev-parse --show-object-format`; `sha1` and `sha256` are supported, and
  any other answer refuses. The id must equal the index entry's. This is the id
  Git itself records for a symlink, which no clean or smudge filter touches.
- The executable-bit check does not apply to a symlink.

A relative, absolute, directory-valued or dangling target is accepted: agentd
never resolves it. An agent working in the checkout may follow the link, as in
any clone.

Still refused, so the checkout is preserved: a symlink whose target differs from
the index (retargeted), a regular file or directory where the index has a
symlink (for example a `core.symlinks=false` checkout, or a file holding the
target text), a symlink where the index has a regular file, a submodule
(`160000`), a conflict (any nonzero stage), and every other mode. Regular files
keep the existing proof through Git's filters. The 10,000-entry bound counts
symlinks too. The final index-equals-HEAD check (read-only callers) and
`write-tree` (shared callers) are unchanged.

A tracked `.gitattributes` that is a symlink is now accepted like any other
symlink. It cannot hide an edit to a regular file, because Git 2.32 and later
does not read an in-tree `.gitattributes` through a symlink, so a linked file
cannot add a normalizing attribute such as `* text`. The proof assumes that Git
version. The agent image installs Git from Debian (2.39.5 on the current
bookworm base); an image with an older Git would need this proof revisited.

Recovery from the earlier refusal needs no operator step. The preserved
reference is unchanged and advances on the next refresh. The project root keeps
its pending record, and a new anchor's worktree may exist without a recorded
identity. The next refresh resumes it through DESIGN-007's recoverable advance:
the worktree is adopted only as the self-consistent detached worktree at its
recorded `to` target that passes the full post-create proof, which now includes
its symlinks. A changed target, dirty or staged files, or a moved HEAD stay
preserved, and the project reports `Failed`.

## Reason codes: diagnosis without exec

Added 2026-10-12 after the Q-30 canary. That bootstrap ended `Partial`, and the
only signals outside the pod were the boot problem
`project-bootstrap: fail: catalog bootstrap outcome Partial` and "inspect its
private report". The report sits in the session's private home, so finding the
cause took an exec into the pod or a reading of the code. This section makes a
failed or partial bootstrap diagnosable from the AgentSession and
`agent-run show`, without exec and without publishing private data.

**What is published.** Only catalog names (repository and project names, which
the accepted catalog already holds), states, step names and reason codes from a
closed set. Never a path, a commit or object id, a URL, an error message or any
other detail text. The private report keeps its detail text as before and
gains the same codes.

**Repository reasons.** Each `PrivateProjectSource` gains `step` and `reason`.
`state` stays `Ready` or `Preserved`. `step` is where the refresh stopped:
`Clone`, `Inspect` (layout, identity and Git operation proof before the fetch),
`Fetch` or `Advance` (the clean-view proof and the fast-forward). A Ready
repository has neither. The reason codes:

| Reason | The refresh stopped because |
|---|---|
| `CloneFailed` | the bounded clone attempts failed; nothing was staged |
| `StagingPreserved` | a clone staging directory exists, from this or an earlier attempt, and is kept for inspection |
| `ReferenceUncertain` | the clone path, its `.git`, ref storage, common Git directory or root does not prove, or the fetch destination is symbolic |
| `IdentityMismatch` | an origin fetch or push URL is not the catalog repository |
| `GitOperationPresent` | a merge, rebase, cherry-pick, revert, bisect, sequencer or Git lock is present |
| `CatalogUnconfirmed` | the accepted catalog changed or could not be confirmed before a mutation |
| `FetchFailed` | the fetch of the default branch failed, or its commit is unavailable |
| `UntrackedFiles` | untracked or ignored files are present |
| `UnsupportedTrackedEntry` | a tracked entry is a conflict, a submodule or another unsupported mode, has nonordinary flags (assume-unchanged, skip-worktree), or its name or object format cannot be proved |
| `IndexBoundExceeded` | the index has more than 10,000 entries |
| `WorkingTreeDiffers` | a tracked file is missing, or its mode or content differs from the index |
| `StagedChanges` | the index differs from HEAD |
| `NonDefaultBranch` | the checkout is detached or on another branch |
| `BranchInOtherWorktree` | the default branch is checked out in another worktree |
| `LocalOnlyCommits` | HEAD has commits outside the fetched default branch |
| `ChangedDuringProof` | the checkout changed between the two clean-view proofs |
| `AdvanceFailed` | the fast-forward itself failed |
| `TimedOut` | the two-minute repository budget ended first |
| `Uncertain` | a Git command failed or answered something the proof cannot read |

**Project root reasons.** Each `PrivateProjectRootReport` (DESIGN-007) gains
`reason`, and, when one member repository is the cause, `repository` (its
catalog name) and `cause` (a repository reason from the table above):

| Reason | The root refresh stopped because |
|---|---|
| `StateUncertain` | the private root state or `$HOME/codex` directory is unavailable |
| `ReceiptUnconfirmed` | the operation record, pending record or receipt could not be written, read back or removed |
| `CatalogUnconfirmed` | the accepted catalog changed or could not be confirmed |
| `RootUnproven` | the root, its receipt or its pending record does not prove (DESIGN-007 ownership and clean state) |
| `AnchorRemoved` | the root holds an anchor the catalog no longer declares; it is preserved |
| `RepositoryNotReady` | a member reference does not prove (identity, layout or Git operation) before its fetch |
| `FetchFailed` | a member fetch, or the prefetch of an anchor's fast-forward, failed |
| `AnchorNotFastForward` | an anchor's recorded commit is not an ancestor of the new target |
| `RootPrepareFailed` | the generated rules commit, the unpublished root's staging or its publication failed |
| `AnchorAddFailed` | `git worktree add` of a new anchor failed |
| `AnchorUnproven` | an anchor does not prove clean, detached and owned; `cause` says why when the clean-checkout proof refused |
| `AnchorAdvanceFailed` | an anchor's fast-forward failed |
| `RootAdvanceFailed` | the root's fast-forward, wrapper modes or exclusion write failed |
| `BudgetReserve` | less than the step reserve was left before a mutating step (DESIGN-007) |
| `TimedOut` | the root's two-minute budget ended first |

A root whose member reference is `Preserved` is still refreshed. Its anchors
are detached worktrees at the fetched target, so an owner's work in the
reference checkout does not make the root unsafe, and skipping it would cut
today's capability. The anchor proof decides; the Q-30 canary root reports
`AnchorUnproven` with the symlinked repository and `UnsupportedTrackedEntry`.

**Catalog reasons.** The report gains `reason` when its state is `Failed`:
`ConfigInvalid`, `StateUncertain`, `LockUnavailable`, `ReportUnretained`,
`CatalogUnconfirmed`, `CatalogInvalid`, `OwnerMismatch` or `TimedOut` (the
ten-minute operation budget). A `Partial` or `Ready` report has none; its
items carry the reasons.

**Boot step.** The `project-bootstrap` step's first note becomes a compact
summary of the items that are not Ready, which the existing
`status.agent.problems` line already carries:
`catalog bootstrap outcome Partial: sigoalumni-org=UnsupportedTrackedEntry; project sigo-alumni=AnchorUnproven@sigoalumni-org(UnsupportedTrackedEntry)`.
A Failed report reads `catalog bootstrap outcome Failed: CatalogUnconfirmed`.
The summary is at most 224 bytes and ends `+N more` when items do not fit.
It contains no `/`, no hexadecimal object id and no detail text.

**Status and AgentSession.** agentd keeps the summary in its boot record.
`protocol.Status` gains `projectBootstrap`: `state`, `reason`,
`catalogRevision` (the catalog content digest that task sources already
report), `repositories[]` of `{name, state, step, reason}` and `projects[]` of
`{name, state, reason, repository, cause}`. The heartbeat copies it to
`status.agent.projectBootstrap`. The CRD validates every state, step and reason
as an enum, every name by the catalog name pattern and a 63-byte limit, the
revision as 64 lowercase hex, and the lists at the catalog limits (128
repositories, 64 projects). The API server copies only known values: an
unknown code is dropped rather than failing the heartbeat, so a newer agentd
never stalls an older operator's status. `agent-run show` prints one
`project bootstrap` row and one row per item that is not Ready.

The CRD changes, so haynes-ops must get the new `config/crd/` copy before the
operator pin that needs it; a stale copy prunes the field silently.

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
