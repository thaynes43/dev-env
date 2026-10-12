# DESIGN-007: Permanent private project roots

**Scope:** the next #173 source slice after bootstrap [#179](https://github.com/thaynes43/dev-env/pull/179).
**Decision:** ADR-003. **Activation:** default off; no native-host or fleet UX
change while Q-24 is pending. Publish the contract within 30 minutes, then ship
one independently reviewed PR under the existing stall/attempt limits.

## Contract

`AGENTD_ENABLE_PRIVATE_PROJECT_ROOTS=true` requires trusted private catalog
bootstrap. Managed startup and `agentd project-refresh` prepare permanent
`$HOME/codex/<project>` roots inside that session's home. Use the same private
serialization, trusted catalog identity checks and finite operation budget.
Keep provider homes and enrollment separate from these project directories.

Each project root is a managed Git repository tracking only its generated
AGENTS.md and CLAUDE.md, with no remote. A private receipt owns the root path,
Git identity, branch, exact generated HEAD/tree, wrapper/exclusion bytes and
catalog/rule revision. Declared repository folders are detached worktrees
against that home's verified reference clones, pinned after fetching their
effective catalog branches, including project overrides.

This root Git marker satisfies ordinary repository discovery without adding
`skip-git-repo-check` or a provider permission bypass. Nested repositories keep
their native instructions and Git identities. Their `.git` boundary can stop
parent-rule discovery, so existing managed task snapshot injection remains
required. File presence and Git recognition are not actual model loading proof.

Create a missing root through private staging and confirm accepted catalog
authority before publication. Preserve an existing plain, foreign or ambiguous
root rather than adopting it. Existing managed roots may advance only from the
exact saved synthetic commit, after verifying branch, index, generated files,
operation state, remotes, Git directory and ownership receipt. Build generated
objects separately from the live index and advance by a non-force fast-forward.
No reset, clean, deletion or attachment to an unknown local commit is allowed.
Private Git operations clear ambient repository/index/object/config routing
variables while retaining the credential-helper environment; otherwise an
owned-path check could read or mutate a peer index/object store. Prove Git
write-path components, including owned refs/reflogs and object fanout
directories, have no peer aliases before mutation. Apply the same bounded
protection to reused private preparation helpers.

Only receipt-owned declared repository directories are allowed alongside the
generated root files. Bind their `.git/info/exclude` entries to the receipt, and
inspect direct root entries independently of Git ignore behavior. Preserve
unknown files, ignored entries, undeclared anchors, local commits, attached or
dirty anchors, modified rules and operations in progress. Bind each anchor to
its exact worktree-specific Git directory, regular `.git` file bytes and
administrative backpointer/commondir before any advance. A same-common-dir peer
alias is ambiguous authority and must leave both worktrees untouched. An existing reference
checkout can remain dirty when a successful fresh fetch supplies an immutable
anchor target; preparing the anchor must not change that reference's index/WIP.

Keep a private operation outcome and per-project source/rule provenance under
`.agentd/project-roots`. Interrupted or partial publication cannot leave an old
Ready receipt describing the current invocation. Unconfirmed ownership is
preserved and reported; it is not silently repaired. agentd's own recorded
partial advance is resumed instead (next section). A project is Ready only
when all required anchors and both wrappers match confirmed authority.

## Reason codes

Added 2026-10-12. Every exit of a root refresh that is not Ready records a
closed-set `reason` in `PrivateProjectRootReport`, and the member repository
and its clean-checkout `cause` when one repository is why. The codes, what is
published from them and where they appear (boot step note, `status.agent.projectBootstrap`,
`agent-run show`) are in
[DESIGN-006, reason codes](006-private-catalog-bootstrap.md#reason-codes-diagnosis-without-exec).
No path, commit or detail text leaves the private report.

## Recoverable advance

Added 2026-10-12 for review finding roots-1. A refresh can fail part-way: a
transient catalog read, the time budget, or a refused fast-forward after one
anchor already moved. Such a failure must not strand an owned root.

- Before its first change, agentd proves every existing anchor clean at its
  proven commit, and each fresh target a descendant of that commit. It also
  fetches, in one batch, every blob each anchor's fast-forward will check out
  (a `git diff --shortstat` between the two commits). A refusal or a failed
  fetch here changes nothing in the root.
- It then writes a private pending record, `<project>.pending.json`, beside the
  receipt (fsync and atomic rename). The record holds the root's private ID and
  config digest, the proven state it starts from (`from`) and the state it
  intends (`to`): root HEAD and tree, wrapper digest, exclusion bytes, and each
  anchor's repository, target and exact identity. A new anchor's identity is
  added as soon as its worktree exists.
- A later refresh that finds the record proves the live root component by
  component: root HEAD, tree and wrappers at `from` or `to`, exclusion bytes at
  `from` or `to`, and each anchor at its `from` or `to` commit with its recorded
  identity. An anchor that `to` adds may be absent, or present as the
  self-consistent detached worktree at its `to` target that agentd's own
  post-create proof accepts. That proof accepts tracked symlinks under
  DESIGN-006's clean-checkout rules (added 2026-10-12); an anchor left by an
  image that refused them resumes this way. Wrappers at `to` may still carry the umask mode;
  agentd re-applies 0644 (DESIGN-012) before going on. Every other check stays
  exact. Dirty, staged, ignored or unknown files, local commits, attached
  anchors, peer aliases, operations in progress and foreign identity are
  preserved and refused, as before.
- The proven reached state is the base of a normal advance to the current
  catalog and fresh targets. The receipt is written only after the exact final
  proof, and the pending record is removed after that. If the remove fails the
  outcome is Failed, and the next refresh finishes it.
- The time budget ends a refresh between Git steps, not inside a mutating one.
  Reference clones are `blob:none`, and a checkout fetches missing blobs only
  after it has removed the paths the target deletes, so a fast-forward stopped
  in that fetch would be neither at `from` nor at `to`. With the blobs already
  fetched, each anchor fast-forward and the root fast-forward are local work. A
  mutating step in a published root starts only with at least 30 seconds of the
  budget left. A new anchor's `git worktree add` may still fetch: Git removes
  its half-made worktree when it is stopped.
- When a time limit does stop a command, agentd's runner sends SIGTERM to its
  process group and SIGKILL 3 seconds later. On SIGTERM Git removes its lock
  files and an unfinished `worktree add`. A hard kill (that SIGKILL, the OOM
  killer, a pod or node lost) inside a mutating step can still leave a lock or
  a half-written checkout. That root is preserved and refused, and the operator
  repairs it (next section).
- A new root records its pending ID before it creates staging, which is named
  `.<project>.agentd-root-<id>`. Recovery may delete only that staging, and only
  while the root is unpublished and the staging holds nothing but what agentd
  generates: wrappers at the recorded digest, and a `.git` whose every path is
  one that Git's empty-template init, object writes and checkout, or agentd's
  marker and exclusion writes, create, each a directory or single-link regular
  file that agentd owns. Any other staging content is preserved and refused. A
  published root is never deleted. Staging under any other name,
  including the older fixed `.<project>.agentd-root`, is left untouched and no
  longer blocks creation.
- A root stranded by an earlier image, with no pending record, is not adopted.
  It stays preserved for the repair below.

## Repair of a preserved root

agentd never repairs a root it cannot prove. The operator does, from a shell in
the session pod, as the session's user. `TestPrivateRootsRepairAPreservedRoot`
runs steps 3 to 5 against real Git.

1. Hold the bootstrap lock, so no refresh runs while you work:
   `exec 9<>~/.agentd/project-bootstrap.lock && flock -n 9`. It fails while a
   refresh runs; wait and try again.
2. Read `~/.agentd/project-roots/<project>.operation.json`, and look at the
   root and each anchor (`git -C ~/codex/<project>/<repo> status`) for owner
   work.
3. **A leftover Git lock only.** `git -C ~/codex/<project>/<repo> rev-parse
   --path-format=absolute --git-path index.lock` names an anchor's index lock.
   If no Git process runs in that anchor, delete the lock and go to step 5.
4. **Anything else: rebuild the root and keep the old one.**

   ```sh
   old=~/codex/<project>.preserved-$(date -u +%Y%m%dT%H%M%SZ)
   mv ~/codex/<project> "$old"
   # For each repository in the project's catalog entry:
   git -C ~/repos/<repo> worktree repair "$old/<repo>"
   git -C ~/repos/<repo> worktree prune
   rm -f ~/.agentd/project-roots/<project>.json ~/.agentd/project-roots/<project>.pending.json
   ```

   `git worktree add` refuses a path that is "missing but already registered",
   so the moved anchors must not stay registered at the old path. `worktree
   repair` points each one's registration at its new path, and the old anchor
   stays a working worktree for inspection. It reports an error for an anchor
   that never became a worktree; `worktree prune` then drops any registration
   left at the old path.
5. Release the lock (`exec 9>&-`) and run the refresh (`agentd project-refresh`,
   or let managed startup run it). It proves everything again. After step 3 it
   resumes the recorded advance if the anchor is otherwise clean, and refuses
   again if it is not. After step 4 it builds a new root with a new ID.
6. Copy what you need from `$old`. Then remove each old anchor with
   `git -C ~/repos/<repo> worktree remove --force "$old/<repo>"` and delete
   `$old`. Staging named `~/codex/.<project>.agentd-root-<id>` for the old ID
   can be deleted once inspected.

New task admission still fetches and pins selected source independently. Resume
keeps its saved worktree, snapshot and effective instructions. This contract
applies to Claude, Codex and a future local provider; it starts no inference.

## Paths and acceptance

- `internal/agentd` private root implementation, existing project/preservation
  helpers, bootstrap integration and settings; command configuration tests.
- Correct the known `agent-run` help claim that project tasks use shared worktrees.
- DESIGN-007, plan 11, workflow guide and HANDOFF record the delivery boundary.

Prove two independent homes have distinct root/repository Git directories;
single/multiple repository roots, override fetch/pins and generated instruction
bytes; safe generated-root and detached-anchor advances; preservation of dirty,
staged, ignored, local-only, linked-peer, operation and foreign state; refusal of
changed catalog authority, edited/undeclared roots and partial provenance;
recovery of an interrupted multi-anchor advance, of a single-repository advance
whose authority confirmation failed after the anchor moved, of a generated-root
fast-forward left at the umask mode, and of a new root interrupted before or
after publication; no anchor moved before every fast-forward is proved and its
blobs fetched; no mutating step started without the budget reserve; Git stopped
by a time limit leaves no lock or half-made worktree; the documented repair of
a leftover lock and of a half-written anchor; and unchanged selected-task
freshness/resume protections. Default-off roots write
nothing. Shared-path regressions remain guarded.

Required CI and native independent review govern merge. Claude Code reviews
are optional and currently unavailable (Tom, 2026-10-11); resolve available
findings and record unavailable outcomes without a review-pass claim. Release/promotion and actual provider acceptance remain separate.
Private-project admission remains disabled in this source promotion.

## Next acceptance

Native host startup currently requires legacy shared preflight and projected
catalog configuration. Its private conversion is a separate lifecycle slice:
one independent app-server per native-session boundary, trusted configuration,
private enrollment and existing absence/owned-stop proofs. No busy-host refresh
or pairing follows from root creation.

Actual Claude/Codex rule acceptance needs nonempty project and native repository
canaries, reviewed admission/auth prerequisites and independent bounded pods.
Keep #173 open. Retention #174, claims/messages #175, budgets #154, recovery #160
and provenance #176 retain separate contracts; no retired storage/fixture retry.
