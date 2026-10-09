# Workflow guide and session freshness, 2026-10-09 America/New_York

Tom resumed from the 2026-10-08 reset work order and requested a detailed
high-level Markdown quick start and feature guide before project finalization,
plus discussion of Codex sessions, `/work/codex` project folders, and stale
shared repositories.

## Delivered guide and contract

[PR #124](https://github.com/thaynes43/dev-env/pull/124) merged as
`759f084f52099cda258b5cd9f1a0eebecce5d284` after current-head required CI and
actual Claude advisory review. Both initial findings were fixed: the question
table renders correctly, and verified existing worktrees preserve detached HEAD,
changed branches and in-progress Git operations with a warning.

The initial [workflow guide](../../docs/workflow-guide.md) shipped with five
Mermaid diagrams; the current revision has two rendered workflow images and
four Mermaid diagrams,
working v1/v2 quick-start commands, a feature-status inventory, task/review/deploy
workflow, Codex session recommendations, access/approval boundaries, all twenty
parity closures, and the remaining cutover gates. Front-door documents link it.
It distinguishes accepted goals, working paths, built-but-disabled features,
and remaining implementation.

[Plan 11](../sagas/distributed-dev-env/backlog/11-project-workspaces.md) now
tracks Tom's supplied R1–R6 and additional R7. Both providers use one project;
Q-20 is superseded, with no answer to its old options needed. The stable-root
baseline is `~/codex`, not the earlier `/work/codex` shorthand. Multiple Codex
remote links must span pods and share workspace files; the older single-hub
and per-session private Git choices need explicit revision under D-74.

The [verbatim owner requirements](../sagas/distributed-dev-env/requirements/2026-10-09-project-roots.md)
are preserved with the audit correction that #3633 was still open/unmerged.
Manual anchors exist on v1's RWO home; catalog/commands, joint trust/scope,
project/task rule propagation, cross-pod locks/ownership and shared workspace
implementation remain missing. The historical stale-index symptoms in the two
named repos were not reproduced at audit; no canonical repair was performed.

The operator API/controller is orchestration; a coordinator agent is a requester
role, not a separate deployed requester service. D-37's console remains planned;
the primary management surface/storage backend still needs workflow review.
Questions must arrive as native phone prompts with an answer round-trip, one at
a time. Neither commentary text nor a DESIGN record substitutes for delivery.
No new architecture implementation, auth migration or v1 rollout followed these
requirements; this update draws and records them before further design.

The implemented D-73 correction applies to **v2 agentd**, not all v1/Codex
entry points. New remote-based task branches require a successful clone/fetch
and use the resolved commit SHA. Existing verified branch/worktree recovery
preserves local work even offline. Branch changes or detached HEAD warn;
foreign repository/path and invalid HEAD fail. A saved launch that has lost both
branch and worktree requires restore. An intentional offline rescue base must
match a ref actually verified and imported from its bundle, not its manifest
alone or the non-imported bundle HEAD line.

## Release

[PR #123](https://github.com/thaynes43/dev-env/pull/123) released **2.9.1** at
`b6c0f3c950da09a532e8ca533c06d1aa6f383326`, after current-head CI and an actual
`@claude` review of the release PR. The automatic review skips release-App PRs;
the explicit review found no required fixes. The compare from v2.9.0 confirms
#121 and #124 plus documentation/chore commits.

[Publish-agent run 37944933337](https://github.com/thaynes43/dev-env/actions/runs/37944933337)
passed its image smoke test and exact-tag cosign verification. The immutable pin
is:

```text
ghcr.io/thaynes43/dev-env:2.9.1@sha256:a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf
```

The verified identity is
`https://github.com/thaynes43/dev-env/.github/workflows/publish-agent.yml@refs/tags/v2.9.1`.
Controller pins need no change for this agentd-only fix. Admission policy remains
in Audit; successful publication verification is not a claim of admission
enforcement. Existing haynes-ops tracking of that policy remains separate.

## GitOps deployment

[haynes-ops PR #3636](https://github.com/thaynes43/haynes-ops/pull/3636)
merged as `b6e858b5bf2c8f683244dec09505eb6d4ec3aa5e` after both required
aggregates (`Flux Local - Success`, `Diff Scope - Success`) passed and the
actual Claude advisory review found no issues. Root also checked the diff: only
the agent template and shelf image pins/comments changed.

The audit declared activity `act-144420-750331` before merge, scoped to
`dev-agents`, `dev-env-shelf`, and `dev-env-templates`, with a twenty-minute TTL.
It reconciled only those template/shelf Kustomizations. The live template and
Ready shelf use the exact signed 2.9.1 pin above. The new shelf UID is
`572bd232-c8b4-4caa-8a68-8bdc8b0891eb`, with zero restarts. No controller or v1
image pin changed.

## Deployed-image acceptance and cleanup

The final replacement Job completed in **five seconds**, exit 0, on a worker
with CPU request/limit 100m/500m and the exact 2.9.1 image ID. It executed the
published `/usr/local/bin/agentd run` for four serial cases:

1. Failed fresh fetch rejected branch/worktree creation with zero launch attempts.
2. Successful fetch passed the pinned SHA to real Git while a shim moved the
   mutable base ref at the worktree-add boundary.
3. Offline resume preserved staged/unstaged/untracked WIP and the conversation.
4. Offline stopped-rebase resume preserved detached HEAD, Git operation state
   and the conversation.

Only the model-launch boundary was stubbed. The final pod had no init containers,
no Secret/PVC/ServiceAccount token, only emptyDir home/tmp, and no account
credentials, heartbeats, external Git remote or model calls. The first run was
provisional because admission added an unlimited timezone init; it was removed
and the documented opt-out was reviewed before one replacement. These are actual
installed-binary Git/startup/resume checks, not proof of live model operation,
project rule loading, multi-host enrollment or new shared workspace acceptance.

Both trial Jobs/pods were removed and both scoped activities ended. Final runtime
verification at `2026-10-09T14:52:53Z` preserved v1 UID
`cf7abc47-0363-491a-9420-12db9731ea8d` and all five controller UIDs/image IDs,
Ready with zero restarts. The new shelf UID above was Ready with zero restarts.
Sessions remained zero; credential job/policy/ended grant counts were unchanged.
The template payload hash was
`94f6785a83910ed937e0acc01e788eda29741bca45597899e729106ec3271f71`.

Private safe metadata evidence is under `/home/dev/work/state-snapshots/`:
`fresh-start-pin-shipped-evidence.json`, `fresh-start-acceptance-final.json`, and
`fresh-start-pin-final-cleanup.json`. No credential values or private targets
were read or published.

## Preserved scope and next work

All twenty parity closures remain open. Plans 03/04 still need keeper-owned
login and the Claude/Codex phone paths. Plan 11 still needs the complete R1–R7 joint-project/cross-pod contract;
a stable folder or periodic fetch is insufficient.

Q-19's CA storage confirmation survives: do not generate another CA or item.
Keeper CA projection, owner node trust, private targets/host pins, egress,
standing policies and real Proxmox provider acceptance remain to be completed
in the reset work order's sequence. Minting stays off. The node trust step
belongs to Tom unless explicitly delegated. No credential values were read.

Q-16 remains; the earlier Q-18 question was withdrawn. No approval route or
blanket standing admin policy was selected. Headlamp stays as the migration
fallback until the guarded replacement is proven and callers are migrated.
No laptop kubeconfig ceremony blocks the accepted `agent-run`/agent-started
workflow. No v1 or active session restart is authorized by this guide.
