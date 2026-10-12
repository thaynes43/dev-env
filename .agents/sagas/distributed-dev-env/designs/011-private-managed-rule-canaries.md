# DESIGN-011: Two guarded private managed-provider canaries

**Scope:** the next bounded #173 client/admission source unit, after private
retention protection. Default off. This authorizes no trial, pairing or native
fleet UX; Q-24 remains pending. Claude/Codex use their existing subscription
CLIs. Local-model contracts stay present; inference waits for GPU readiness and
household reservations.

## Contract

Keep global private-project admission disabled. Add an independent strict
canary policy with at most two explicit records, one per subscription provider.
Only the existing client identity can request a fixed canary ID; requests cannot
choose its project, repo, profile, model, effort, size, source, rules or limits.
The trusted record fixes the client namespace/ServiceAccount and exact accepted
project/repo/provider/profile/full-model/effort/size-S tuple, catalog/rules
revision, exact template revision, verified immutable image, fixed bounded
prompt/timeout/turns and idempotency key. The request contains the canary ID only;
the fixed prompt does not contain validation marker values. No wildcard, unknown field, duplicate or caller
override is accepted. API disabled or missing prerequisites means no creation.
The operator enforces that on the reviewed record (2026-10-12, #91 rules-003;
this replaces the same day's "tests check the rendered values instead", which
left the record itself unchecked). `projectcanary.CheckPrompt` refuses a prompt
that contains a trimmed line of at least 16 bytes of the bound text, or one of
its marker-shaped tokens even mid-sentence (at least 12 characters joined by `-`
or `_`, with a letter, and with a digit or no lowercase letter). `ResolveCatalog`
applies it to the project rules, so the create route, the controller before an
executor and the result reader all refuse such a record; `Record.CheckCommon`
applies it to the retained common text and Codex header in those same three
places. A refusal is fail-closed: it only stops a record that already breaks this
contract, and the record is reworded. The live records' prompt passes against
their bound revisions. Separately, the operator's pod
(`TestProjectCanaryRenderedPromptAndArgsCarryNoRuleLine`) and agentd's launch
(`TestProjectRulesReachTheAgentOnlyAsRuleInputs`), each with and without a
retained common snapshot, carry no line of the bound rules, common text or header
outside the rule inputs.

Require trusted named catalog/clone authority, private-history retention and
the reviewed private profile/template contract. Codex also requires the existing
managed Codex gate. Only finite task mode is supported: refuse base overrides,
restoration, shared workspace, tools override, session/coordinator callers and
budget-assigned callers. Whole-task private budget dispatch remains #154's
separate acceptance. The original new-task fetch/immutable pin and saved
instruction composition are reused, never replaced by bootstrap refresh.

Derive one deterministic retained Session identity from caller and canary ID.
Exact retries return that original record even when terminal. Concurrent API
replicas, lost create acknowledgements and uncertain state cannot generate a
second name or executor. Persist an immutable canary binding in the Session.
Attach a distinct permanent canary-retention finalizer at the initial atomic
Create. Early deletion before controller reconciliation burns that original
identity without allowing a replacement; normal history-retention initialization
cannot remove this finalizer. Never release or rearm it in this unit.
The controller independently validates the policy/binding and confirms the
retention latch before creating its one permitted Pod.
Pod absence never re-arms that latch. The one exception, since 2026-10-12, is
a recorded known non-attempt: no Create was called for the latched intent, so
admission may resume it once per mark
([DESIGN-019](019-private-pod-retention-guard.md#a-known-non-attempt-after-the-executor-latch--2026-10-12-history-01-174)).
An authenticated canary Session cannot create other Sessions through legacy
dispatch routes. Heartbeat and observation remain available. Disabling child
decision rendering alone does not establish the one-executor boundary.

The execution is one container, no init/sidecar/ephemeral container,
RestartNever, 600-second active deadline, five-second termination grace and
k8tz injection disabled. Keep size-S CPU/memory limits and worker placement.
Provider execution timeout is at most 180 seconds; Claude is at most three
turns, Codex keeps its supported max-turns value zero. Preserve terminal
Session/HOME and original executor attempt. Pod absence, deadline or stale
heartbeat does not establish StopConfirmed, authorize rearm or prove a native
chat boundary. Operator upgrades preserve existing agent Pods.

Codex uses only the existing keeper access-only delivery and five-minute
startup validity guard. No enrollment, rotating credential or private provider
history is cloned. Expired/short-lived access refuses provider launch. Claude
uses the existing subscription credential path. No paid API substitution,
new refresh owner or new high-power access follows.

## Paths and acceptance

- Operator flags and strict bounded policy parser; shared policy validation.
- API request canary ID, deterministic create/retry path and precise refusals.
- Typed immutable Session canary binding and generated schema.
- Controller independent admission proof and finite Pod rendering; existing
  private-history intent guard protects execution.
- Focused parser/API/controller/schema tests and durable saga/HANDOFF receipts.

Prove disabled/missing/malformed/unknown/duplicate/wildcard policies fail closed;
wrong caller or tuple creates nothing while the global gate stays false;
concurrent and uncertain retries retain one original Session/Pod UID; terminal
retry does not launch again; retention uncertainty creates no executor;
template drift/sidecars/absence of bounded resources are rejected; and source,
rules, subscription access and original executor identities remain bound.
Use sequential focused tests under nice19/GOMAXPROCS2/p2, necessary real schema
checks and affected lint/codegen. No stress, wide loops, local image builds or
provider/model/cluster invocation belongs in this source unit. Required CI and
native independent review govern merge; Claude reviews remain optional/down.

## Separate GitOps and installed acceptance

After signed publication, review exact canary profiles, policy and schema in
GitOps. Use the existing Codex read-only access-only projection; add no refresh
credential or enrollment copy. Supply nonempty versioned common/repo rule
markers through the accepted catalog, with no marker supplied in the task
prompt. Review effective canary egress under additive policies; unrelated
MCP/web permissions must not silently broaden these one-shot tasks.

Run each reviewed canary once, only with usable existing subscription access.
Prove actual placement/resources/private HOME and Git admin/worktree boundaries,
fresh selected source/pin, exact injected rule revisions and actual provider
output/events confirming both markers. File presence alone is insufficient.
Retain terminal history and failed receipts; track logical attempts across
pods/methods and stop at three failures or 60 minutes without evidence.
Managed-task success does not establish native nested rule discovery or
complete-chat routing. New native project start/injection remains separate.
Keep #173/#91 open for remaining client/pilot acceptance; no v1 restart/cutover,
elevation, destructive cleanup or retired storage retry.

## Private-repo canary on the Sigo alumni project — 2026-10-12 (Q-30, #173)

**Why.** Both providers have now loaded the common and the repository rules in
private v2 pods: the Codex original and Claude canary 1012. Every canary so far
ran on `thaynes43/dev-env`, which is public. A public clone works without a
token, so no run has proved private GitHub authorization. #173 still needs that
proof and a fresh task pin.

**Q-30.** The coordinator asked Tom through the Claude native prompt, free-text
"Other" allowed:

> To close #173 I need one more Claude canary on a private repo, proving private
> GitHub access and a fresh pinned task worktree. The dev bot can't create repos
> on your personal account. Could you create an empty private repo
> thaynes43/dev-env-private-probe (a README on main is enough)? Global private
> admission stays off; the canary reuses the spent 1012 slot.

Options: "I'll create it (Recommended)", "Use an existing private repo", "Not
now". Tom answered in free text: "All the Sigo alumni repos are private and all
are needed for dev-env, we use them now. Why can't you use those?"

The coordinator recorded this interpretation: use the existing private
repositories `thaynes43/sigo-alumni` and `thaynes43/sigoalumni-org` instead of a
new probe repository. Declare them as a v2 catalog project. Run the one approved
private-repo Claude canary against it: one create, one named read and one result
read, with no retries. Global private admission stays off. Repository visibility
does not change. The owner answer receipt is
`root-q30-owner-answer-r1.json` (1332 bytes, sha256
`1490331885be14f5939fe85c81d91f186274dac089286e2343ca373bdd0b3722`), recorded at
18:43:31 UTC.

This grants no retry, no second canary, no general private project task, no
directory read and no cleanup of any original. Running the canary is the
coordinator's step after the GitOps change deploys.

### The project

| Catalog key | GitHub identity | Default branch | Visibility |
|---|---|---|---|
| `sigo-alumni` | `thaynes43/sigo-alumni` | `main` | private |
| `sigoalumni-org` | `thaynes43/sigoalumni-org` | `main` | private |

These two are the full set Tom's "all the Sigo alumni repos" names: the
haynes-dev-bot installation lists exactly these two Sigo alumni repositories
(checked 2026-10-12), and the v1 pod keeps clones of the same two.

Project `sigo-alumni` selects both repositories. Its rules are short and
generic. They carry no private content, only the marker
`sigo-alumni-project-rules-20261012-9b122cd5`. They say to report that
revision as the repository marker in an acceptance probe, because neither
repository's own `CLAUDE.md` or `AGENTS.md` carries a marker (checked
2026-10-12). The `dev-env` project entry is byte-identical, and so is its rules
revision. The catalog revision covers the whole file, so it moves for both
projects: `9c5cc110…` becomes
`5fe34bd347f85c4b41773fbe48f815f8655945af9d73503a1ef0e89ee89aad90`. The
`sigo-alumni` rules revision is
`d070e7a6d7e10359ed9fa4de897d6e24be7c49ba7df2f8af39ef4317b83ceae0`.

The haynes-dev-bot installation covers both repositories. The keeper mints
installation tokens with a permission set and no repository list
(`internal/keeper/github.go`), so the `dev-env-gh-token` Secret that the
`rule-canary-claude` profile mounts at `/creds` reaches them. Neither repository
uses Git LFS or submodules, so `github.com:443`, already in the canary's egress,
is the only host involved.

### The record

The policy holds at most two records, one per provider (`Parse`). The new
Claude record therefore replaces the consumed `rule-canary-claude-1012`.
`rule-canary-codex-1011` stays byte-identical. The new record is the 1012
record with six fields changed:

| Field | Value |
|---|---|
| `id` | `rule-canary-claude-sigo-1012` |
| `idempotencyKey` | `rule-canary-claude-sigo-1012-once` |
| `project`, `repository` | `sigo-alumni`, `sigo-alumni` |
| `catalogRevision` | `5fe34bd3…` (the new catalog) |
| `rulesRevision` | `d070e7a6…` (the `sigo-alumni` rules) |

The rest is unchanged: client `dev/dev-env`, provider `claude`, profile
`rule-canary-claude`, model `claude-opus-5-5`, effort `xhigh`, size S, timeout
180s, one turn, `captureOutput`, template `2.24.1-7e04fb00dc`, image
`dev-env:2.24.1@sha256:f2713217…`, common revision `3b9b829c…` and the 227-byte
prompt (sha256 `5a3da07a…`). The derived policy digest is
`154c3a12ecf2cb3e7f8861737c7875052b41a0bc49ab7e20b5e75fcf874bea27`. The Session
is `dev-agents/private-canary-a1444f4131ec7281b5d1e3130c3b5ada`.

A harness at tag v2.24.1 ran `Parse`, `ValidateCommon`, `ValidateClients`,
`Validate`, `ValidateTemplate`, `ResolveCatalog` (which includes the
prompt-repeats-rules guard) and `CheckCommon` on the full policy. All pass. The
prompt carries no line or marker-shaped token of the new rules. The operator's
own `parseFlags` accepts the exact HelmRelease args.

### Scope flags

Only `--context-payload-read-projects` gains `sigo-alumni`. Managed output
publication and the fixed result read both require the record's project in
that list. Directory enrollment at Create is optional, and only listed projects
get it. Neither capture nor the result read needs enrollment, discovery,
claims or the inbox, so those flags stay `dev-env`.

### What else the catalog change does

- **codex-1011.** Its `catalogRevision` no longer matches, so its Create, its
  result read and the controller's executor admission refuse it. Its template
  revision already refused it after 2.24.1. All three are consumed, and its pod
  is gone. Operator startup runs only `Parse`, `ValidateCommon` and
  `ValidateClients`, so the operator still starts.
- **Bootstrap reach.** Every profile in `dev-env-templates` enables private
  catalog bootstrap. Bootstrap clones every catalog repository into the pod's
  private HOME. It runs at agentd boot, at a private Codex host start and on an
  explicit `agentd project-refresh`, and never on a timer. So only pods that
  boot after the change receive the two private repositories. Nothing running
  restarts. This reaches further than the one canary, and it is parity, not new
  access. Tom's answer says the repositories "are needed for dev-env, we use
  them now". The v1 pod already keeps fetch-only clones of both in `~/repos`,
  read with the same haynes-dev-bot token. A v2 pod gets its clones in its own
  private HOME. To withdraw the reach, remove the project and its two
  repositories from the catalog after the canary's result read. New pods then
  stop cloning them. Bootstrap never deletes a clone, so HOMEs that already
  hold one keep it.
- **Admission.** General private project tasks stay off. An ordinary task that
  names a project still refuses. The fixed canary route is the only way in.
- **Freeze.** The result read (`canaryResultAuthority` in
  `internal/apiserver/canary_result.go`) checks the bound state against live
  state again. Between the Create and the result read, none of these may change:
  - **The catalog** (`apps/dev-env-system/projects`). The Session binds its UID,
    resourceVersion and snapshot at Create, and the read compares all three. A
    change to another project denies the read too.
  - **The templates** (`apps/dev-env-system/templates`). Their revision must
    still be `2.24.1-7e04fb00dc`, so any template or image bump there denies the
    read.
  - **The common rules** (`apps/dev-env-system/session-config`). The text of
    `dev-env-config-claude` `CLAUDE.md` and `dev-env-config-codex`
    `AGENTS.header.md` must still match the bound bytes.
  - **The operator args the read checks** (`apps/dev-env-system/operator`): this
    record in `--private-project-canaries`, `sigo-alumni` in
    `--context-payload-read-projects`, `--enable-private-project-tasks` still
    false, and `--enable-private-history-retention` and
    `--enable-retained-common-rules` still true.

  The canary has no retries, so a denied result read wastes it. Before the
  Create, the coordinator lists open haynes-ops PRs that touch those four
  paths, Renovate's included, and holds any with auto-merge until the result
  read.

### Acceptance evidence for #173

The run is accepted for #173 only when all of these hold:

1. **Private repository.** `gh api repos/thaynes43/sigo-alumni` reports
   `private: true` at Create time, and so does `sigoalumni-org`.
2. **Bootstrap and task clone.** The `project-bootstrap` boot step reports
   `catalog bootstrap outcome Ready`. agentd reports `Ready` only when every
   catalog repository and project root is `Ready`; otherwise it reports
   `Partial`. The repo step succeeds, and the Session's `agent.problems` names
   neither step. The note
   `no GitHub token at …; cloning without one (public repos only)` must be
   absent, but that is supporting evidence only. agentd writes the note only
   when the repo step makes a fresh clone (`internal/agentd/clone.go`), and
   bootstrap has already cloned `~/repos/sigo-alumni`, so the repo step reuses
   that clone. Items 1, 3 and 5 prove access: the private fetch succeeds, and
   the pin equals the private tip.
3. **Fetch and record lines.** The boot log read (below) has
   `fetched accepted repository thaynes43/sigo-alumni branch main at <pin> on <fetchedAt>`
   and `recorded the task source: thaynes43/sigo-alumni branch main at <pin>`.
4. **The task-source record.** `~/.agentd/task-source.json` in the private HOME
   is written once, before the worktree exists. It holds version 1, the
   Session's name and UID, repo `sigo-alumni`, GitHub `thaynes43/sigo-alumni`,
   branch `main`, commit `<pin>`, `fetchedAt`, catalog revision `5fe34bd3…` and
   rules revision `d070e7a6…`. The record line carries the commit, the fetch
   line carries `fetchedAt`, and the Session's bound snapshot carries both
   revisions. The file stays in the retained HOME; nothing execs into the
   private pod to read it.
5. **Fresh pin.** The coordinator reads `git ls-remote` of
   `thaynes43/sigo-alumni refs/heads/main` immediately before the Create, and
   records the time of that read and of the Create. `fetchedAt` must be later
   than both, so the fetch happened after the Create and is not an older one.
   `<pin>` must equal the pre-Create `ls-remote`. If `main` moved after that
   read, a second `ls-remote` taken right after the boot log read brackets the
   fetch, and `<pin>` must equal it instead. If it equals neither, the pin stays
   Unknown.
6. **Worktree.** The boot log read has
   `worktree /home/dev/work/<session> on agent/<session> from <pin> at <pin>`.
   The named read reports branch `agent/<session>` and head equal to the pin.
7. **Peers unchanged.** Against the pre-merge baseline, every non-operator pod
   in `dev-env-system`, `dev-agents` and `dev` keeps its UID, restarts and
   images; pods of CronJob runs come and go and are not compared. The three
   retained Sessions keep their UID, generation and spec.

**The boot log read.** Items 2, 3 and 6 rest on agentd's boot-step lines.
agentd writes those notes only to its stdout. The Session view carries Boot,
Problems, Branch and Head, not the notes. The 1012 record could not accept a
fresh pin from an unbound `kubectl logs` extract, so this read is part of the
procedure:

- **When.** Once, after the named read shows the task has finished, and before
  any pod deletion. Take it promptly: a retained Pod is not durable. A node
  reboot removed the two retained 1011 canary Pods (Q-31).
- **How.** A read-only
  `kubectl logs -n dev-agents private-canary-a1444f4131ec7281b5d1e3130c3b5ada`.
  The executor Pod has the Session's name. This is not one of Q-30's three
  operator reads and spends nothing from them.
- **What is kept.** Only the `msg="boot step"` lines whose `step=` is
  `project-bootstrap` or `repo`. They go in a new file in the canary's order
  directory, and the receipt that the Root acceptance binds lists that file
  and its sha256. The unfiltered log is not kept.
- **If it fails.** If the Pod or its log is gone, items 2, 3 and 6 stay
  Unknown. The canary is not run again.

**Markers are secondary here.** Rule loading is already accepted for both
providers. The prompt can expect the common marker
`dev-env-common-rules-20261011-4ca29d70`. The project rules ask for
`sigo-alumni-project-rules-20261012-9b122cd5` as the repository marker, because
the repositories' own instructions carry none. If the output reports that
marker, the project rules reached the agent. `Unknown` proves nothing either
way. The common rule says to report a repository marker only from loaded
repository instructions, and `Unknown` when there is none, so `Unknown` is a
correct reading of the common rule as well. Neither outcome changes items 1 to
7. Removing the overlap would change a rule revision and the record, so this
canary keeps both rules as they are.

**Public records.** The canary loads the private repository's own `CLAUDE.md`,
and its captured output and the boot log come from a private pod. The 1012
record quoted its captured output word for word, but that canary ran on a
public repository. Before anything from this run goes into a public record
(this repository, haynes-ops, an issue or a PR comment), screen it, and quote
only marker values, IDs, revisions, commit SHAs and timestamps. Quote the
captured output word for word only if screening shows it holds nothing else.
The full captured output and the filtered log stay in the order directory,
which is not published.

Served model, usage, cost, full history, physical stop and capture completeness
stay Unknown, as for the earlier canaries. The bootstrap reference clone of
`sigoalumni-org` also proves authorization for that repository, but no task
pin. Q-24 and the owner pilot are unaffected.

**Run, 2026-10-12.** The canary ran once. Root acceptance `dd5dee24` holds items
1 and 3 to 7 and fails item 2: catalog bootstrap ended `Partial` because one
repository tracks a symlink, which #237 fixes in source. The runner read the
boot log after the wait saw the task result and before the named read. The Root
recorded that order as a deviation, `bootlog-before-named-read`, and accepted
it, because both conditions rest on the same Session status. The
[Sigo private-repo canary record](../verification/sigo-private-canary-2026-10-12.md)
has the details.
