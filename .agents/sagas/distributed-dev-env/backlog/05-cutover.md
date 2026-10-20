# 05: cutover from v1

**Scope correction, 2026-10-10:** [ADR-003](../adrs/003-session-coordination-private-repositories.md)
separates a testable coordination pilot from retiring v1. Required owner workflows
need guarded usable replacements; the historical twenty-row inventory is migration
planning, not a universal pilot gate. Before retiring old homes, preserve and verify
readable transcripts, memory and handoffs as well as Git work. Approval of this
scope does not approve a cutover or remove v1 access.

**Migration constraint, 2026-10-13:**
[ADR-007](../adrs/007-preserve-v1-archive-trust-boundary.md) records Q-35's
relayed owner direction: v1's behavior, configuration and current access stay
unchanged until decommissioning after accepted MVP. This plan still needs
Tom's explicit decommissioning approval. It authorizes no migration-time
permission reduction, automatic retirement or new privilege.

**Status, 2026-10-20 (story CR-1020):** every prerequisite an agent can verify
is verified (table below), within the W3 limits the table names. The cutover is planned for **2026-10-21**, after the
v1 sessions wind down and write handoffs for new agents (decision 80). It runs
only with Tom's written approval and his go at that moment. Nothing in CR-1020
scaled, restarted or changed v1.
**Depends on:** 03, 04, 07 (verified effective v1 parity, including accepted
Headlamp-equivalent tasks, before retiring v1; restricted break-glass alone is not
a replacement), Q-08's LimitRange live in haynes-ops (no BestEffort household pods), and Tom's
explicit approval. *(2026-10-20: Headlamp stays as it is through the cutover;
guarded Headlamp-equivalent tasks are not proven. See the Headlamp row below.)*
**Parallel with:** nothing

## Goal

v2 carries all agent work. v1 is retired without losing anything on its volume.

## Prerequisites on 2026-10-20

The CR-1020 result files sit in the coordinator's order directory,
`/home/dev/work/orders/dev-env-reset-1018/`, on v1's volume and outside git. Step
4 of the runbook copies them to `~/.shared/v1-home/work-orders/`.

| Prerequisite | State | Evidence |
|---|---|---|
| Plan 03: Claude on the phone | Accepted by Tom | He steered the standby on Wi-Fi (about 12:09 UTC) and on cellular (about 12:11 UTC) |
| Plan 04: Codex on the phone | Accepted by Tom | PEAR in the ChatGPT app over cellular, about 12:14 UTC: the same computer, a new turn, history recalled |
| Shared logins | Accepted | Keeper status on the phone (generation 4, the Claude login lapses 2026-11-16 17:51 UTC); automatic refresh seen (generations 2, 3, 4); lapse notices on 2.39.0, test notice on Tom's phone (story 13.1). The full renewal was skipped at Tom's direction, so it is not a gap |
| Plan 07: grants, Proxmox | Proven from a session | Standing grants for the LAN, Proxmox API and hw-ssh; the Proxmox token for reads (2.38.1) and for a write (W3a, 22:40 UTC: `pve --yes vm 113 onboot 1` at `tier=operator`, token gone 20 s after release) |
| Workbench has v1's OPERATOR tier | Merged and proven from a session | haynes-ops #4141 (merged 22:41 UTC); W1 review: the same 10 write rules as v1, none extra, none missing; W3b: `can-i` matrix, `flux reconcile`, Job create and delete, pod delete, shelf `rollout restart`, exec, guard refusals, a draft PR opened and closed from v2. **Not proven live:** a PR merge from v2 (W3b closed draft dev-env #498 without merging; the first post-cutover merge is the proof, runbook step 7), the `restartedAt` restart of an ordinary app in `media` (server dry run only), and a Job from a CronJob (server dry run only; no safe CronJob named) |
| Restarts, messaging | Proven from a session | Keeper, operator and shelf restarts (NB-02; the keeper again at about 12:20 UTC at Tom's request; the shelf again in W3b); `agent-run msg` delivered and answered |
| Repos outside the catalog | Proven for Claude | W2: `--repo ytdrivarr -p` task Succeeded. Headless Codex tasks still need the catalog |
| Q-08 LimitRange | Live | `default-cpu-request` in every namespace, `dev-agents` and `dev-env-system` included |
| Headlamp | Not touched by the cutover; guarded replacement not proven | It runs on its own in `frontend` and does not depend on v1, so the cutover removes nothing it provides. The workbench has the exec and Job rights v1 has (#4141). Accepted guarded Headlamp-equivalent tasks (D-71), such as owner-directed Secret reads, are not proven; the workbench cannot read Secrets. The CR-1020 coordinator classed that as later work, not a cutover blocker (progress file, 22:20 UTC). It is not Tom's ruling; his approval in step 5 covers it |
| Cluster objects that need v1 | None block scale-to-zero | W2, read-only: nothing else mounts `dev-env-home`; no v1-only network policy blocks a session; dev-env-ops loses only its view of v1's old declaration files (read from its script; a live run is step 8) |
| Copying v1's notes to v2 | Trial passed | W3a: one file copied into `~/.shared` and checked by checksum |
| Tom's written approval | Open | Runbook step 5 |

## Scope

- **Headlamp retirement (D-71):** prove that guarded replacement access supports
  its accepted v1 tasks and existing owner directives, migrate callers, then
  remove the Headlamp app and its privileged identity through GitOps. Retain it
  only as a migration fallback until that proof; a restricted break-glass role
  that omits existing tasks is not enough. Not part of the 2026-10-21 cutover.
- **Workbench: no code-server** (Tom, 2026-10-20, decision 78). The workbench is
  a session profile, not a Deployment: the standby and any session started with
  `--profile workbench`. A proper front end is later work (plan 13). Until then
  Tom uses a terminal in a v2 session pod: `kubectl -n dev-agents exec -it
  <session> -c agent -- bash` for a shell, or `agent-run attach <session>` for the
  agent's own TUI. v1's `dev-env.haynesops.com` route shows an error page once v1
  is at zero, and goes with the v1 app.
- **Cursor workers go offline** (Tom, 2026-10-20, decision 79). The two Cursor
  "My Machines" workers in v1 (`cursor-worker-v1`, `cursor-worker-dev-env`) stop
  with v1 and are not moved to v2.
- **dev-env-ops** already reads v2 `Activity` objects (W2, from its script and
  the script's `jq` filter on a synthetic object; no live run yet). With v1 at
  zero its `dev-activity-check.sh` should print `WARN: no running dev-env pod
  found in ns/dev` and list no tmux sessions, and keep listing v2 declarations.
  Runbook step 8 checks this live. Removing the v1 block and listing
  `AgentSession`s instead is later polish in haynes-ops. Its summoning lanes move
  in plan 10, not here.
- **v1 drain.** A final `agent-run sweep` is **not** a drain. It keeps every
  worktree with activity in the last 3 days, which is all live work. The worktrees
  it does reap end up on local `rescue/*` branches in the canonical clones on the
  v1 volume, never pushed. Once v1 is at zero nothing can run it. Instead:
  - each v1 session finishes or checkpoints its story, pushes its branches to
    `origin/agent/*` (never main) and writes a handoff for a new v2 agent;
  - the coordinator checks that no live worktree has unpushed or uncommitted
    work (runbook step 3);
  - local branches that origin lacks (old scratch, squash-merged branches, the
    `rescue/*` branches) go to the shared volume as one Git bundle per repo
    (runbook step 4).
- **v1's notes move to v2.** v1 does not mount `~/.shared`. Its `~/work/orders`
  and each repo's Claude auto-memory are copied into `~/.shared/v1-home/` and
  checked by checksum (runbook step 4). Then the memory is merged into
  `~/.shared/memory/<name>`, where v2 sessions load it: new files are copied,
  `MEMORY.md` lines that are missing are appended, and nothing is overwritten.
  `~/.shared` is readable by every non-canary session; never copy a secret there.
- **Never delete the `dev-env-home` PVC object.** Its PV has reclaim policy
  `Delete` (StorageClass `ceph-block`), and nothing snapshots or backs it up
  (decision 7 of v1's saga, haynes-ops `.agents/sagas/dev-env/`). Deleting the PVC object destroys v1's history. Flux will not prune
  it: the PVC carries `kustomize.toolkit.fluxcd.io/prune: disabled`. Scaling to
  zero removes only the pod. The PVC stays at least 30 days.
- **v1's Max login is retired with the pod.** Tom stops renewing it; it lapses about
  30 days after its last `/login` (W2 read 2026-11-15 01:38 UTC). Nothing is copied
  to the keeper, which has had its own login since plan 03. The `claude-login-check`
  page and the v1 renewal runbook give way to `agent-run keeper-login` in a
  workbench session and the keeper's lapse notices (quick start section 6).
- **v1's Codex login is retired with the pod too.** Before scale-to-zero, and only
  after Tom's written approval (runbook steps 5 and 6), stop v1's daemon from a
  shell inside the v1 pod, with v1's own `agent-run codex-remote stop`.
  (In v2 the same command stops the codex hub, DESIGN-001 3.5.) Nothing refreshes that
  login again, and its `auth.json` goes with the PVC. Nothing is copied to the keeper or the codex hub, which have had their own
  logins since plan 04 (DESIGN-001 D-12). The phone's v1 computer entry goes offline.
- **haynes-ops cleanup** after the 30 days: delete `apps/dev/dev-env`, the v1
  Dockerfile and build workflow, the v1 Renovate carve-outs and holds, and the
  Kyverno attestor for haynes-ops-built `dev-env*` images. These are held drafts,
  merged at a natural break, because they touch the v1 pod. W2 found these
  dependents, which must move before `apps/dev/dev-env` is deleted (none blocks
  scale-to-zero):
  - the ClusterRole `dev-env-operator` is defined in the v1 app and also bound by
    dev-env-ops (`dev-env-ops-operator`), whose Kustomization `dependsOn` the v1
    app: move the role and drop the dependency;
  - the `openproject-agent-credentials` Deployment and its ExternalSecret live in
    the v1 app, and v2's `opwork` execs into it by that name: relocate it;
  - dev-env-ops and vexa's scribe-notes Job run the v1 image `dev-env:0.6.8`: keep
    the v1 image, its build workflow and the Kyverno `dev-env:0.*` rule until plan
    10 moves them;
  - the operator's `--client-service-accounts` argument and the private canary
    entries name `dev/dev-env`, as do the operator CNP rule, CNP
    `openproject-agent-api` and the v1 ServiceAccount's RoleBindings in
    `database` and `observability`: remove them with the v1 app;
  - the `dev-env-omni-operator` ExternalSecret, used by the `egpu-test-node`
    runbook: relocate it or retire the runbook;
  - the `cursor-*` scripts, `externalsecret-cursor.yaml`, the IngressRoute and the
    Authentik blueprint `50-dev-env.yaml` with its outpost: remove with the v1 app.
- Move v1's runbook knowledge that still applies (hw-ssh, pve, declare-activity)
  into this repo's docs and the pod CLAUDE.md in haynes-ops. The Max login renewal
  is now `agent-run keeper-login` (quick start section 6); the console page
  (DESIGN-001 3.8) is not built.

## Cutover runbook (2026-10-21)

**What authorizes it.** Tomorrow's date alone authorizes nothing. Steps 1 to 5
prepare and check; they stop, restart or end nothing in v1. (Step 3's `git
fetch` updates remote-tracking refs, and step 4 writes its manifests and
bundles under `~/work/migrate-manifests`.) Nothing that
stops, restarts, retires or disrupts v1 or a v1 session (v1's Codex daemon
included) runs before all four of these hold:

- every v1 session has reported its wind-down and handoff done (step 2);
- no live worktree has unpushed or uncommitted work (step 3);
- v1's orders and memory are copied to v2 and verified (step 4);
- Tom's written approval is recorded in the decision log (step 5).

If Tom does not approve, nothing is stopped: v1 and its sessions keep running
as they are, and the copy in `~/.shared/v1-home/` simply stays.

Run by a new cutover coordinator in a v2 workbench session (the standby will do),
under its own story record. Merging step 7 ends every v1 session, so the
coordinator must not run in v1. Steps 3, 4 and 6 need a shell in the v1 pod; run
them from a v1 session before step 7.

1. **Announce.** Tell Tom the plan and the expected end time. List v1's sessions
   in a v1 shell with `tmux ls`. Send each agent session the wind-down notice
   (the text is `CR-1020-WINDDOWN-NOTICE.md` in the coordinator's order
   directory), on one line:

   ```bash
   tmux send-keys -t task-<id> -l "<the notice, one line>"; tmux send-keys -t task-<id> Enter
   ```

   Record the list of sessions in the progress file.
2. **Owners push and write handoffs.** Each session finishes or checkpoints its
   story, pushes every branch it owns to `origin` (`agent/*`, never main), writes a
   handoff for a new v2 agent in its repo (backlog or issue) and in
   `~/work/orders`, and replies, in its session and with one line in
   `~/work/orders/V1-WINDDOWN-REPLIES.md`. Go on only when every session has
   replied done. A session that cannot finish replies "partial" with a reason;
   name it in the step 5 question.
3. **Check that nothing is left unpushed.** In a v1 shell. It changes nothing
   except the remote-tracking refs that `git fetch` updates, and takes no index
   lock, so running agents are not disturbed:

   ```bash
   since=$(date -d '7 days ago' +%s)
   for r in "$HOME"/repos/*/; do
     n=$(basename "$r"); nice -n 19 git -C "$r" fetch -q --prune origin || echo "FETCH FAILED $n"
     # live worktrees: uncommitted paths, or commits that origin lacks
     git -C "$r" worktree list --porcelain | awk '/^worktree /{print $2}' | while read -r wt; do
       [ "$wt" = "${r%/}" ] && continue
       d=$(nice -n 19 git --no-optional-locks -C "$wt" status --porcelain 2>/dev/null | wc -l)
       u=$(git -C "$wt" rev-list --count HEAD --not --remotes 2>/dev/null)
       [ "${d:-0}" -gt 0 ] || [ "${u:-0}" -gt 0 ] && echo "WORKTREE $wt: $d uncommitted, $u unpushed"
     done
     # other local branches changed in the last 7 days that origin lacks
     git -C "$r" for-each-ref --format='%(refname:short) %(committerdate:unix)' refs/heads |
     while read -r b t; do
       [ "$t" -ge "$since" ] || continue
       c=$(git -C "$r" rev-list --count "$b" --not --remotes)
       [ "$c" -gt 0 ] && echo "BRANCH $n $b: $c commits origin lacks"
     done
   done
   ```

   Every `WORKTREE` line of a live session's worktree is resolved by its owner
   (push or commit) and the check rerun until none is left. `BRANCH` lines are
   older local branches: a squash-merged branch shows up too, because its commits
   are not on origin, so note its PR and move on. Whatever stays local goes into
   the bundles in step 4, and the step 5 question says how many there are. On
   2026-10-20, without a fetch, the `BRANCH` check found 109 such branches in
   dev-env and haynes-ops alone, so expect a long list.
4. **Copy orders, memory and local branches to v2, and verify.** From a v1 shell,
   after the handoffs, so nothing changes underneath. It follows W3a's tested
   sequence, with three changes:
   - only text files of `~/work/orders` are copied. Non-text files (compiled test
     binaries, tarballs, gzip files: about 1.6 GiB in 901 files on 2026-10-20)
     are build output a new agent does not need, and a scan cannot see inside
     them;
   - every text file that will be copied is scanned first for token and
     private-key patterns, Proxmox API tokens included; a match is left out. On
     2026-10-20, 23 of 8,182 text files matched (names only were listed; nobody
     read them). A match in the memory stops the run;
   - Git bundles of local-only branches are added.

   The files left out stay on v1's volume, listed in the manifests. On
   2026-10-20 that left about 140 MiB to copy.

   First the writer session:

   ```bash
   export DEV_ENV_API_CA_FILE=~/.cache/dev-env-api-ca.crt     # quick start, "Which agent-run"
   v2=~/.local/bin/agent-run-v2.35.0                            # or the newest agent-run-v2.* there
   $v2 --repo dev-env --local --model claude-haiku-5-5 --effort low   # prints the session's name, does not attach
   S=<that session's name>
   ```

   Then write the copy script and run it. A script, so that a refusal really
   stops it:

   ```bash
   cat > ~/work/migrate-v1-notes.sh <<'EOF'
   #!/bin/bash
   # Usage: bash ~/work/migrate-v1-notes.sh <writer-session>
   set -u; export LC_ALL=C
   S=$1; DST=/home/dev/.shared/v1-home
   W=$HOME/work/migrate-manifests; mkdir -p "$W"; fail=0
   kx() { KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false timeout 1800 kubectl exec -i -n dev-agents "$S" -c agent -- "$@"; }
   PAT='gh[pousr]_[A-Za-z0-9]{30,}|github_pat_|sk-ant-|sk-proj-|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY'
   PAT="$PAT"'|"refresh_token"|"access_token"|PVEAPIToken'
   PAT="$PAT"'|[A-Za-z0-9._-]+@[A-Za-z0-9._-]+![A-Za-z0-9._-]+[=:][0-9a-fA-F]{8}-[0-9a-fA-F]{4}-'

   # 0. Scan. Memory: every file, read as text; a match stops the run (fix it by hand).
   if nice -n 19 grep -rlaE --exclude='*.jsonl' "$PAT" "$HOME"/.claude/projects/*/memory; then
     echo "STOP: the memory files above match a secret pattern"; exit 1
   fi
   #    Orders: the copy list is the text files that do not match.
   cd "$HOME/work" || exit 1
   nice -n 19 grep -rIl --exclude='*.jsonl' '' orders | sort > "$W/orders-text.txt"
   tr '\n' '\0' < "$W/orders-text.txt" | nice -n 19 xargs -0 grep -alE "$PAT" | sort > "$W/orders-secret-scan.txt"
   find orders -type f ! -name '*.jsonl' | sort | comm -23 - "$W/orders-text.txt" > "$W/orders-not-text.txt"
   comm -23 "$W/orders-text.txt" "$W/orders-secret-scan.txt" > "$W/orders-copy.txt"
   echo "copying $(wc -l < "$W/orders-copy.txt") files; left out $(wc -l < "$W/orders-not-text.txt") non-text and empty, $(wc -l < "$W/orders-secret-scan.txt") scan matches"

   # a. The target. Refuse to run over an existing copy.
   kx sh -c "test ! -e $DST && mkdir -p $DST/work-orders $DST/memory $DST/bundles" \
     || { echo "STOP: $DST exists or mkdir failed"; exit 1; }

   # b. The orders copy list.
   tar c -T "$W/orders-copy.txt" | kx tar x -C "$DST/work-orders"
   rc="${PIPESTATUS[*]}"; echo "orders rc: $rc"; [ "$rc" = "0 0" ] || fail=1

   # c. Each repo's auto-memory.
   for m in "$HOME"/.claude/projects/*/memory; do
     p=$(basename "$(dirname "$m")")
     kx mkdir -p "$DST/memory/$p" || { fail=1; continue; }
     tar c -C "$m" --exclude='*.jsonl' . | kx tar x -C "$DST/memory/$p"
     rc="${PIPESTATUS[*]}"; echo "memory $p rc: $rc"; [ "$rc" = "0 0" ] || fail=1
   done

   # d. Local branches that origin lacks: one bundle per canonical clone (none: skipped).
   for r in "$HOME"/repos/*/; do
     n=$(basename "$r")
     out=$(nice -n 19 git -C "$r" bundle create "$W/$n.bundle" --branches --not --remotes 2>&1) || {
       case $out in *"empty bundle"*) continue;; *) echo "BUNDLE FAIL $n: $out"; fail=1; continue;; esac; }
     kx sh -c "cat > $DST/bundles/$n.bundle" < "$W/$n.bundle" && echo "bundle $n copied" || fail=1
   done

   # e. Verify: file count and checksums, checked on the v2 side over stdin.
   tr '\n' '\0' < "$W/orders-copy.txt" | xargs -0 nice -n 19 sha256sum > "$W/orders.sha"
   src=$(wc -l < "$W/orders.sha"); dst=$(kx sh -c "find $DST/work-orders/orders -type f | wc -l")
   [ "$src" = "$dst" ] || { echo "COUNT MISMATCH orders src=$src dst=$dst"; fail=1; }
   kx sh -c "cd $DST/work-orders && sha256sum -c --quiet" < "$W/orders.sha" || { echo "CHECKSUM FAIL orders"; fail=1; }
   for m in "$HOME"/.claude/projects/*/memory; do
     p=$(basename "$(dirname "$m")")
     ( cd "$m" && find . -type f ! -name '*.jsonl' -print0 | sort -z | xargs -0 sha256sum ) > "$W/mem-$p.sha"
     kx sh -c "cd $DST/memory/$p && sha256sum -c --quiet" < "$W/mem-$p.sha" || { echo "CHECKSUM FAIL $p"; fail=1; }
   done
   if ls "$W"/*.bundle >/dev/null 2>&1; then
     ( cd "$W" && sha256sum ./*.bundle ) > "$W/bundles.sha"
     kx sh -c "cd $DST/bundles && sha256sum -c --quiet" < "$W/bundles.sha" || { echo "CHECKSUM FAIL bundles"; fail=1; }
   fi

   # f. Keep the manifests (paths and hashes only) next to the copy.
   tar c -C "$W" --exclude='*.bundle' . | kx sh -c "mkdir -p $DST/manifests && tar x -C $DST/manifests" || fail=1
   [ "$fail" = 0 ] && echo "ALL OK" || { echo "SOMETHING FAILED: read the lines above"; exit 1; }
   EOF
   bash ~/work/migrate-v1-notes.sh "$S" && $v2 reap "$S"
   ```

   `ALL OK` means it worked; then the writer session is reaped. Nothing is deleted
   on either side. `manifests/orders-secret-scan.txt` names the files to read
   before anyone copies them by hand. If a copy is cut off by the timeout, remove
   the exact `$DST` path (no wildcard) and run the script again.

   Then merge the memory, in any running v2 session (the coordinator's will do).
   Only the `-home-dev-repos-<repo>` folders are merged; v2 uses the same names.
   The others (`-home-dev`, `-tmp*`) stay in `v1-home`:

   ```bash
   SH=$HOME/.shared
   for src in "$SH"/v1-home/memory/-home-dev-repos-*; do
     p=$(basename "$src"); dst="$SH/memory/$p"; mkdir -p "$dst"
     ( cd "$src" && find . -type f ! -name MEMORY.md ) | while read -r f; do
       if [ ! -e "$dst/$f" ]; then mkdir -p "$(dirname "$dst/$f")" && cp -p "$src/$f" "$dst/$f"
       elif ! cmp -s "$src/$f" "$dst/$f"; then echo "KEPT v2 copy: $p/$f (v1 copy stays in v1-home)"; fi
     done
     if [ -f "$src/MEMORY.md" ]; then
       touch "$dst/MEMORY.md"
       grep -vxF -f "$dst/MEMORY.md" "$src/MEMORY.md" >> "$dst/MEMORY.md" || true
     fi
     echo "merged $p"
   done
   ```

   It never overwrites a v2 file, and a second run adds nothing.
5. **Record Tom's written approval.** Only after steps 2 to 4 are complete, ask
   Tom through AskUserQuestion. Name what is left: any session that replied
   "partial", the count of local-only branches that went into the bundles, and
   the files the copy left out. Put his words in the saga decision log as a dated
   ruling, in a dev-env docs PR merged before step 6. If he does not approve, or
   does not answer, stop here: nothing has been stopped, and nothing is.
6. **Stop v1's Codex daemon**, only after step 5's ruling is merged, from a shell
   inside v1, with v1's own launcher: `agent-run codex-remote stop`. Then
   `tmux has-session -t codex-remote` must fail. The phone's v1 computer entry
   goes offline. PEAR, the v2 host, is not affected.

   Declare the disruptive part from the v2 coordinator first, so the remediation
   agent takes the alerts as planned:

   ```bash
   declare-activity start "v1 cutover: scale dev-env v1 to zero" \
     --scope dev,dev-env,dev-agents,upgrade-agent --ttl 4h
   ```
7. **Scale v1 to zero by GitOps.** One haynes-ops PR that changes only
   `kubernetes/main/apps/dev/dev-env/app/helmrelease.yaml`:
   `values.controllers.dev-env.replicas: 1` to `0`. It is under
   `apps/dev/dev-env/**` and merging it ends every v1 session, so open it as a
   held draft. Tom merges it, or approves it and the v2 coordinator merges it.
   Then:

   ```bash
   flux reconcile source git haynes-ops -n flux-system
   flux reconcile kustomization dev-env -n dev --with-source
   flux reconcile helmrelease dev-env -n dev
   ```

   Never `kubectl scale`: it is outside the OPERATOR tier, and Flux would undo it.
   If the v2 coordinator merges this PR, that is the first PR merge from v2:
   CR-1020 did not exercise one (W3b opened and closed draft dev-env #498 only).
   The first post-cutover PR merge from v2 is the live merge proof; record it.
8. **Verify v2.**
   - `kubectl -n dev get deploy dev-env` shows 0 replicas and
     `kubectl -n dev get pods -l app.kubernetes.io/controller=dev-env` no pod;
     `kubectl -n dev get pvc dev-env-home` is still Bound; `flux get helmreleases
     -n dev dev-env` is Ready. `openproject-agent-credentials`, blender and audio
     in `dev` keep running.
   - `agent-run list` shows the same v2 sessions as before; `agent-run auth status`
     exits 0.
   - Tom: the standby answers on the phone, and PEAR answers in the ChatGPT app.
   - dev-env-ops' next `dev-activity-check.sh` run prints the expected "no
     running dev-env pod" warning and still lists step 6's v2 declaration. This is
     the first live run of that path. If the declaration is missing, record it as
     a defect; `kubectl get activities.dev-env.haynesops.com -n dev-env-system`
     still shows it.
   - End the declaration. Record the end state in the handoff and the progress
     file.
9. **Rollback.** Revert the step 7 PR (a held-draft revert PR in haynes-ops) and
   reconcile as in step 7. v1 comes back on the same PVC: its files are there, but
   its tmux sessions and agents are not restored, and its Codex daemon starts again
   only by `agent-run codex-remote up` inside it. Its Max login works only until
   it lapses (2026-11-15). The PVC is kept for at least 30 days, so a rollback
   stays possible that long.

After step 9's window: one week at zero, then the acceptance below, then the
held-draft cleanup PRs after 30 days.

## Acceptance

- Tom approves the cutover in writing (a dated ruling in the decision log).
- One week after scale-to-zero, nobody has needed v1.
- v1's PVC is deleted only after its rescue branches are bundled to the shared
  volume (runbook step 4 does this), and never before the 30 days.
