# Handoff: land the held dev-env v1 PRs in one pod restart (2026-10-06)

**For:** an agent Tom runs in the morning on his own machine, with `gh` and git, and
probably no kubectl for the cluster.
**Repo it acts on:** [thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops) (public).
**Audited:** 2026-10-06 03:40Z against haynes-ops `main` at `313e508c`. Everything
below was read-only. Re-check the facts in step 0 before acting, because PRs move overnight.

## 1. Goal, and why you run outside the cluster

Seven haynes-ops PRs change the v1 dev-env pod (namespace `dev`) or the image it runs.
They are held because merging any of them restarts that pod. The pod is
`replicas: 1`, `strategy: Recreate`, and it carries `reloader.stakater.com/auto: "true"`,
so a restart kills every agent session in it, including the one that merged.
You are outside the cluster, so you survive the restart and can check the result.

The goal is to land all of them with **one** dev-env restart and **one** dev-env-ops
restart, then hand the in-pod checks to the first session that comes up afterwards.

## 2. State at audit time

- Live dev-env and dev-env-ops run `ghcr.io/thaynes43/dev-env:0.6.5@sha256:57d001f4…`
  (claude-code 2.1.284, codex 0.158.0). Dev-env ReplicaSet revision 61, created
  2026-09-29T02:20:34Z. No CPU limit (`limits.memory: 64Gi` only).
- On GHCR: `0.6.5` → `sha256:57d001f4…`, `0.6.6` → `sha256:bded3e13…` (never deployed),
  `0.6.7` and `latest` → `sha256:92e206a0…` (claude-code 2.1.288, codex 0.160.0).
  `0.6.8` does not exist (404).
- The image is pinned in **7 places**: `dev-env` HelmRelease ×3 (`app`, `gh-refresher`,
  `auth-watch`), `dev-env-ops` HelmRelease ×3, and vexa
  `kubernetes/main/apps/ai/vexa/app/resources/scribe-notes-job.json` ×1.
- The `#3414` wiring PR is already merged: haynes-ops #3422, at 2026-10-06T03:34:52Z.
  dev-env-ops restarted at 03:35:44Z and logged `what=no-max-login`. See section 10.

## 3. The PRs

All seven merge cleanly onto `main` 313e508c, and onto each other in any order:
I trial-merged them in memory with `git merge-tree`. #3241 and #3330 make the same
image hunks, which merge as no-ops. CI is green on every head listed below. The
advisory `Claude Review (advisory)` check **skipped or never ran on all seven**, because
it skips drafts and Renovate PRs. None of them has an unresolved review finding or a human
comment; the only comments are the flux-local diff bots. So none of this code has had
an advisory review yet. The combined PR in step 3 is where that review happens.

| PR | Head at audit | State | What it changes | What merging restarts | Blockers and notes |
|---|---|---|---|---|---|
| [#3381](https://github.com/thaynes43/haynes-ops/pull/3381) | `2df708f3` | draft, owned by the **haynesnetwork-1003** session | `dev-env` HelmRelease: `limits.cpu: "8"` on the `app` container. `rbac.yaml`: drops the stale `apps.emqx.io` read rule (EMQX retired in #3417). Claude `CLAUDE.md`: the `declare-activity` example says `mosquitto`, not `emqx`. Absorbs issue #3418 (`Closes #3418`). | dev-env pod (HelmRelease and Reloader). The ClusterRole edit restarts nothing. | Ownership: ask Tom (section 7). No ExternalSecret prerequisite. |
| [#3342](https://github.com/thaynes43/haynes-ops/pull/3342) | `63d7ca9e` | Renovate, ready | `scripts/dev-env/Dockerfile`: claude-code 2.1.288 → 2.1.290, codex 0.160.0 → 0.160.1. | **Nothing.** It only triggers an image build on `main`. | **Trap:** `.github/workflows/dev-env-build.yml` hard-codes the tag `0.6.7` (4 lines). Merged as is, it rebuilds and **re-pushes `0.6.7` with a new digest** (the "toolchain trap"). Push a tag bump to `0.6.8` onto this PR first, as #3178, #3245 and #3256 did. |
| [#3241](https://github.com/thaynes43/haynes-ops/pull/3241) | `0297beb4` | Renovate, ready | Re-pins dev-env ×3 and dev-env-ops ×3 to `0.6.7@sha256:92e206a0…`. Misses the vexa pin. | dev-env and dev-env-ops. | **Do not merge it, and do not close it.** #3330 carries the same hunks plus the vexa pin. Renovate auto-closes it once `main` has a newer pin (section 6, step 6). |
| [#3330](https://github.com/thaynes43/haynes-ops/pull/3330) | `fae3deff` | draft | Re-pins all 7 places to `0.6.7@sha256:92e206a0…`. Adds `gpt-6.1-sol` to agent-run's codex fallback rows. Moves the Codex subagent default from `gpt-6-sol` to `gpt-6.1-sol` in Claude `CLAUDE.md`, codex `AGENTS.header.md`, home `README.md` and `.agents/runbooks/audio-authoring.md`. | dev-env (HelmRelease and Reloader) and dev-env-ops (image). vexa: none, because it only changes a Job template. | **Its docs must land with an image that has codex ≥0.159.1.** Live 0.6.5 has codex 0.158.0, which never lists `gpt-6.1-sol`. The PR body says a hand-installed codex 0.160.0 on the PVC runs the phone daemon today, and the next roll without this PR puts the pod back on 0.158.0. |
| [#3336](https://github.com/thaynes43/haynes-ops/pull/3336) | `66708d1d` | draft | `hw-ssh.sh` adds host `pikvm` (alias `kvm`) as `root@pikvm.haynesnetwork`. CLAUDE.md hw-ssh row and the `proxmox-access.md` runbook gain PiKVM rules and a deploy recipe. `kustomization.yaml`: comment only. | dev-env (Reloader). | None. Egress (#3335) and the key on the PiKVM are already live, so no ExternalSecret or 1Password prerequisite. |
| [#3294](https://github.com/thaynes43/haynes-ops/pull/3294) | `6bf569d3` | draft | One Ground-rules bullet in Claude `CLAUDE.md`: every repo gets the Claude Code PR reviewer, with the secret set by the new-repo runbook's one-off Job. | dev-env (Reloader). | None. `.agents/runbooks/new-repo-setup.md` exists on `main`. |
| [#3274](https://github.com/thaynes43/haynes-ops/pull/3274) | `f821a589` | draft | One note in Claude `CLAUDE.md`: a `kubectl` CronJob suspend lasts only until Flux's next reconcile. | dev-env (Reloader). | None. |

**Restart mechanics, so you can predict what happens.** The dev-env Kustomization
`dev/dev-env` reconciles as soon as Flux fetches the new commit. ConfigMap edits
under `app/resources/**` roll the pod through Reloader. HelmRelease edits roll it
through helm-controller. One commit that does both has produced **one** rollout
before: #3238 changed four ConfigMaps and the image in one squash. It merged at
2026-09-29T02:19:30Z, Flux posted `kustomization/cluster-apps` success at
02:20:04Z, and exactly one new ReplicaSet (revision 61) appeared at 02:20:34Z.
`dev-env-ops` has `dependsOn: dev-env`, so it rolls a minute or so later.

## 4. The image build and tag chain

1. `scripts/dev-env/Dockerfile` pins every CLI as an `ARG` with a `# renovate:`
   annotation. Renovate groups them into one manual-merge PR, "dev-env image
   toolchain" (`.renovate/groups.json5`). `.renovate/autoMerge.json5` keeps every
   `thaynes43/dev-env` image bump manual too.
2. A push to `main` that touches `scripts/dev-env/**` or `scripts/github-app-token.sh`
   runs `.github/workflows/dev-env-build.yml`. It builds, smoke-tests, pushes the tag
   **written in the workflow** plus `latest`, and cosign-signs the digest. It does
   **not** list its own file as a trigger, so a workflow-only edit builds nothing.
   The last four `main` builds took 5 to 6.5 minutes.
3. Nothing rolls until a second change re-pins `tag: <ver>@sha256:<digest>` in the 7
   places. Renovate opens that re-pin as a separate PR (like #3241), but it misses vexa.

So if #3342 merges unchanged, `0.6.7` is overwritten with a new digest. The pins that
#3241 and #3330 carry (`0.6.7@sha256:92e206a0…`) would still pull, because a digest
pin resolves by digest. But tag `0.6.7` would then name two different images, and
Renovate would open a digest-drift PR. Bump the workflow tag to `0.6.8` in the same PR.
That produces `0.6.8` and `latest`, and leaves `0.6.7` as it is.

## 5. Model-id floors

| Id | Needs | 0.6.5 (live) | 0.6.7 | 0.6.8 (#3342) |
|---|---|---|---|---|
| `claude-fable-5-1` | claude-code ≥2.1.255 | 2.1.284 ✓ | 2.1.288 ✓ | 2.1.290 ✓ |
| `claude-opus-5-5` | claude-code ≥2.1.280 | ✓ | ✓ | ✓ |
| `claude-sonnet-5-5` | claude-code ≥2.1.284 | ✓ | ✓ | ✓ |
| `gpt-6-sol` | codex ≥0.156.1 | 0.158.0 ✓ | 0.160.0 ✓ | 0.160.1 ✓ |
| `gpt-6.1-sol` | codex ≥0.159.1 | **✗** | ✓ | ✓ |

If Renovate has rebased #3342 overnight onto newer versions, re-read its diff. Newer
versions only raise these floors, so they stay satisfied, but look at the release
notes for anything that changes `codex remote-control` or approvals.

## 6. Recommended landing order

**Recommendation: two merges, one restart.** First merge the image-source PR (#3342
plus the workflow tag bump). It restarts nothing; it only builds `0.6.8`. Wait for the
build. Then fold everything that restarts the pod into **one** new branch that pins
`0.6.8`, open it as a ready PR, handle its review, and squash-merge it once.

Why this order:

- The new image's digest only exists after its Dockerfile is on `main`. If #3342 rode
  in the bounce PR, you would either pin a digest that does not exist yet or need a
  second re-pin later, which is a second restart.
- One squash is one Flux reconcile and, as #3238 showed, one rollout. Merging the
  drafts one by one gives a restart per merge. Each restart starts the post-ready
  standby again, and 3 launches in 30 minutes trip post-ready's circuit breaker
  ("loop suspected"), after which it launches nothing.
- The drafts have never had an advisory review. A ready combined PR gets one review of
  the whole diff before anything restarts.
- One squash commit is one clean revert if the new pod fails.

**No `flux suspend` here.** You have no cluster access, so `flux suspend kustomization
dev-env` is not available to you. Do not ask an in-pod session to suspend Flux as a way
to batch separate merges either: the restart kills that session before it can run
`flux resume`, and dev-env would stay out of GitOps until someone notices. The single
squash is the batching.

### Step 0: refresh the state (read-only)

```bash
R=thaynes43/haynes-ops
# Every open PR that touches dev-env, dev-env-ops, the image source or a pin:
gh pr list -R $R --state open --limit 100 --json number,title,isDraft,files --jq '.[]
  | select(any(.files[].path; test("^kubernetes/main/apps/(dev/dev-env|upgrade-agent/dev-env-ops)/|^scripts/dev-env/|^\\.github/workflows/dev-env-build\\.yml$|scribe-notes-job\\.json$")))
  | "\(.number) draft=\(.isDraft) \(.title)"'
# Heads: compare with the table in section 3. A new commit means re-read that PR.
for n in 3381 3342 3241 3330 3336 3294 3274; do
  gh pr view $n -R $R --json number,state,headRefOid --jq '"\(.number) \(.state) \(.headRefOid[0:8])"'; done
```

A new PR in that list joins the bounce if it restarts dev-env or dev-env-ops; give it
the same treatment as the drafts. If one of the seven has been merged or closed,
drop it.

### Step 1: questions for Tom, one at a time (AskUserQuestion or chat)

1. "#3381 (CPU limit 8, EMQX leftovers) belongs to the haynesnetwork-1003 session.
   Can I fold it into the one dev-env restart?" Recommended: yes. If no, leave #3381
   alone. It then restarts the pod again whenever its owner merges it.
2. "Include the toolchain bump (#3342: claude-code 2.1.290, codex 0.160.1)? It adds
   about 10 minutes for an image build and restarts nothing by itself." Recommended:
   yes. If no, skip step 2 and use the 0.6.7 path in step 3.
3. "The bot's token down-scope has no `secrets` permission, so new-repo secrets need
   the runbook Job. Do you want `secrets` added in this restart?" Recommended: no.
   The runbook's reason is that every agent shell would hold a secrets-writing token.
   Only if he says yes: add `"secrets":"write"` to `GITHUB_BOT_TOKEN_PERMISSIONS` in
   the `gh-refresher` env of `kubernetes/main/apps/dev/dev-env/app/helmrelease.yaml`,
   in the combined branch. The haynes-dev-bot App already holds the permission.

### Step 2: build the new tag from #3342 (no restart)

Run the blocks of steps 2 and 3 in **one shell**, in order. Later blocks use the
variables (`R`, `ACCEPT`, `CUR`, `V`, `NEW`) and the `ghcr` helper set here. `ghcr` mints
a fresh anonymous pull token on every call, because the token expires within minutes.

```bash
R=thaynes43/haynes-ops
ACCEPT='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'
ghcr() {  # ghcr <tag-or-digest>: prints the HTTP status and, when it exists, the digest
  local t; t=$(curl -s "https://ghcr.io/token?scope=repository:thaynes43/dev-env:pull&service=ghcr.io" \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")
  curl -sI -H "Authorization: Bearer $t" -H "Accept: $ACCEPT" \
    "https://ghcr.io/v2/thaynes43/dev-env/manifests/$1" | grep -i -E '^HTTP|docker-content-digest'
}

gh pr checkout 3342 -R $R
export CUR=$(grep -m1 -o 'dev-env:[0-9][0-9.]*' .github/workflows/dev-env-build.yml | cut -d: -f2)
echo "$CUR"                       # 0.6.7 at audit time: the tag main's workflow builds today
export V=0.6.8                    # CUR's next patch
ghcr "$V"                         # must be 404. If it is 200, raise V to the next patch that 404s.
grep -c -F "dev-env:$CUR" .github/workflows/dev-env-build.yml   # expect 2
grep -c -F "$CUR" .github/workflows/dev-env-build.yml           # expect 4
perl -pi -e 's/\Q$ENV{CUR}\E/$ENV{V}/g' .github/workflows/dev-env-build.yml
git diff --stat                   # 1 file, 4 lines changed
git commit -am "dev-env image $V: bump the build tag so the toolchain group builds a new tag"
git push      # pushing a workflow file needs the `workflow` scope: `gh auth refresh -s workflow`
gh pr checks 3342 -R $R --watch   # build-and-push (PR build, no publish), Flux Local - Success, Diff Scope - Success
gh pr merge 3342 -R $R --squash --delete-branch
```

Then wait for the `main` build and read the new digest:

```bash
SHA=$(gh pr view 3342 -R $R --json mergeCommit --jq .mergeCommit.oid)
RUN=$(gh run list -R $R --workflow dev-env-build.yml --branch main --event push --limit 5 \
      --json databaseId,headSha --jq ".[] | select(.headSha==\"$SHA\") | .databaseId" | head -1)
echo "$RUN"                       # empty = the run has not started yet; wait 30 s and repeat the line above
gh run watch "$RUN" -R $R --exit-status                    # about 6 minutes
ghcr "$V"                                                  # 200 and a docker-content-digest
# Cross-check: the digest the run pushed and signed must be the same one.
gh run view "$RUN" -R $R --log | grep -E "$V: digest: sha256|DIGEST: sha256"
export NEW="$V@$(ghcr "$V" | grep -i docker-content-digest | awk '{print $2}' | tr -d '\r')"
echo "$NEW"                       # e.g. 0.6.8@sha256:<64 hex>, the same digest the log shows
```

The `Accept` header must include the single-image manifest types. This image is
single-arch, so an index-only `Accept` returns 404 for a tag that exists. If the run
fails, rerun it once (`gh run rerun "$RUN" -R $R --failed`). If it still fails, use
the 0.6.7 path in step 3. It is safe because 0.6.7 has codex 0.160.0.

### Step 3: one combined branch

Three blocks. Do not paste them as one: block A can stop on a merge conflict, and
blocks B and C must not run until that conflict is resolved.

**Block A: merge the drafts.** The branch names come from the PRs themselves. All five
heads were in thaynes43/haynes-ops at audit time, including Tom's #3330
(`agent/codex-gpt-6.1-sol`). Leave 3381 out of the list if Tom kept it with its owner.

```bash
git fetch origin && git switch -c agent/dev-env-v1-bounce origin/main
for n in 3381 3336 3330 3294 3274; do
  read -r b x < <(gh pr view $n -R $R --json headRefName,isCrossRepository --jq '"\(.headRefName) \(.isCrossRepository)"')
  [ "$x" = false ] || { echo "#$n comes from a fork: stop and ask Tom"; break; }
  git merge --no-edit "origin/$b" || { echo "CONFLICT merging #$n ($b): resolve and commit it before block B"; break; }
done
git log --oneline --merges origin/main..HEAD   # one merge per PR you folded
```

**Block B: re-pin all 7 places to the new image.** Skip it on the 0.6.7 path. It refuses
to run while a merge is still open.

```bash
if ! printf '%s' "$NEW" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$'; then
  echo "NEW='$NEW' is not a <ver>@sha256:<64 hex> pin: set it as at the end of step 2"
elif git rev-parse -q --verify MERGE_HEAD >/dev/null || [ -n "$(git diff --name-only --diff-filter=U)" ]; then
  echo "a merge is still open or has conflicts: finish block A first"
else
  export OLD=$(git grep -h -o -E '[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}' -- \
      kubernetes/main/apps/ai/vexa/app/resources/scribe-notes-job.json)
  echo "OLD=$OLD  NEW=$NEW"   # OLD is #3330's pin (0.6.7@sha256:92e206a0... at audit time)
  git grep -c -F "$OLD" -- kubernetes        # 3 + 3 + 1 before the change
  perl -pi -e 's/\Q$ENV{OLD}\E/$ENV{NEW}/g' \
    kubernetes/main/apps/dev/dev-env/app/helmrelease.yaml \
    kubernetes/main/apps/upgrade-agent/dev-env-ops/app/helmrelease.yaml \
    kubernetes/main/apps/ai/vexa/app/resources/scribe-notes-job.json
  git grep -c -F "$NEW" -- kubernetes        # 3 + 3 + 1 after
  git grep -n -F -e "$OLD" -e '57d001f4' -- kubernetes   # expect nothing
fi
```

**Block C: commit and push**, only after block B's counts are right (on the 0.6.7 path,
after `git grep -c 92e206a0 -- kubernetes` shows 3 + 3 + 1 and there is nothing to commit):

```bash
git status --short                # only the 3 pinned files, no conflict markers
git commit -am "dev-env v1 bounce: pin image $V in dev-env, dev-env-ops and vexa scribe-notes"
git push -u origin agent/dev-env-v1-bounce
```

Open it **ready**, not draft, so the advisory reviewer runs. Put "HELD" in the title so
no in-pod agent mistakes it for routine work. You merge it yourself in step 5.
Suggested title:
`HELD: dev-env v1 bounce: image 0.6.8 (or the $V step 2 built), CPU limit 8, PiKVM hw-ssh, GPT-6.1 Sol, two rules [restarts dev-env + dev-env-ops]`.
The body must:

- list each folded PR (#3381, #3336, #3330, #3294, #3274) with one line on what it
  brings, and say it supersedes #3241. Closing keywords do not close PRs, so you
  close the folded PRs by hand in step 6;
- include `Closes #3418` if #3381 is folded;
- say that the external agent merges it at Tom's window, so a reviewer note that
  dev-env PRs should stay draft has its answer;
- contain no secrets. The repo is public.

```bash
gh pr create -R $R --base main --head agent/dev-env-v1-bounce --title "<title>" --body-file <file>
gh pr checks <N> -R $R --watch          # required: Flux Local - Success, Diff Scope - Success
gh pr view <N> -R $R --comments          # the Claude Review summary and the flux-local diffs
gh api repos/$R/pulls/<N>/comments --jq '.[] | "\(.path):\(.line) \(.body)"'
```

Read the flux-local diff comments. Expect changes to Deployment `dev/dev-env` (image
×3, `cpu: '8'`, ConfigMap contents), Deployment `upgrade-agent/dev-env-ops` (image),
ClusterRole `dev-env-operator` (one apiGroup fewer) and ConfigMap
`ai/scribe-notes-jobtemplate` (image). Anything else needs an explanation first. Fix
each review finding, or answer it on its thread with a concrete reason. A push
re-runs the review.

### Step 4: pre-flight with Tom (right before the merge)

You cannot see inside the pod, and you cannot `declare-activity` from outside it. So
tell Tom, in one message:

- "Merging #<N> restarts the dev-env pod and dev-env-ops. Every claude and codex
  session in dev-env ends, including your phone and Remote Control sessions. Is any
  in-pod session mid-task?"
- If he wants an in-pod session to prepare first (optional), it can run:
  `agent-run list` and `tmux ls` (who is active);
  snapshot each `task-*` pane with
  `tmux capture-pane -p -t <session>` into `~/work/pane-snapshots-<ts>/` (PVC, survives);
  check that dev-env-ops holds no claimed order (#3415: a restart orphans it):
  `kubectl get cm -n upgrade-agent upgrade-work-orders -o json | jq -r '.data | to_entries[] | select((.value|fromjson?|.status)=="claimed") | .key'`
  (empty output means none);
  and declare the window, which lives on the PVC and outlasts the restart:
  `declare-activity start "dev-env v1 bounce (haynes-ops #<N>)" --scope dev,dev-env,upgrade-agent,dev-env-ops --ttl 45m`.
- Wait for his go.

### Step 5: merge, once

```bash
gh pr merge <N> -R $R --squash --delete-branch
M=$(gh pr view <N> -R $R --json mergeCommit --jq .mergeCommit.oid)
gh api repos/$R/commits/$M/statuses --jq '.[] | "\(.created_at) \(.context) \(.state)"'
# kustomization/cluster-apps/... success = Flux fetched the commit (about 30 s after the merge in #3238).
```

Expect the new dev-env pod about a minute after the merge and dev-env-ops shortly
after. The post-ready standby comes up a few minutes after the pod is Ready.

### Step 6: tidy the PRs

```bash
for n in 3381 3336 3330 3294 3274; do
  gh pr close $n -R $R --comment "Folded into #<N> (squash $M) so dev-env restarted once."
done
```

- Leave out #3381 if Tom kept it with its owner. Do not delete the folded branches
  unless Tom says so, because #3381's owner may still have its branch checked out.
- **#3241**: leave it. Renovate auto-closes a PR whose update `main` already carries or
  passes. Mend Renovate's branch schedule is 10pm to 6am America/New_York, so it may
  stay open until that night. Look the next day. If it is still open, tick its rebase
  box rather than closing it.
- **#3342** was merged in step 2.

### Fallbacks

- **Build failed, or Tom said no to #3342:** build the combined branch without the
  re-pin. It then carries #3330's `0.6.7@sha256:92e206a0…` in all 7 places. If #3342
  was not merged, it stays open for a later restart. When it merges, the workflow tag
  bump still applies.
- **The new pod does not come up** (Tom sees no code-server at
  `https://dev-env.haynesops.com` and no standby on his session list after about 10
  minutes): revert with a PR (`git revert $M` on a new branch), squash-merge it, and
  tell Tom. That is a second restart, back to the old image. The old digest
  `0.6.5@sha256:57d001f4…` still resolves on GHCR (checked 2026-10-06).

## 7. Coordination

- **#3381 belongs to the haynesnetwork-1003 session** (`session_018WeCz8…`). Its last
  push was 2026-10-06 03:20Z, when it absorbed #3418. Tom decides whether you take it
  over (step 1, question 1). If you fold it, close #3381 with a pointer, which tells its
  owner where the work went.
- **#3330** was opened under Tom's account. The other drafts and #3381 are from the
  haynes-dev-bot. None of their authors is waiting on a review from you.
- **Do not edit the drafts' branches.** You fold them by merging into your own branch.
- This audit was written by an in-pod agent, read-only. It did not touch any of these PRs.

## 8. Post-restart verification

### What you can check from outside

```bash
# Uses ghcr, ACCEPT and V from step 2. In a new shell, define them again first.
gh pr view <N> -R $R --json state,mergedAt,mergeCommit
gh api repos/$R/commits/$M/statuses --jq '.[].context'      # cluster + cluster-apps success
# The pin on main equals what GHCR serves for the new tag:
git fetch origin && git show origin/main:kubernetes/main/apps/dev/dev-env/app/helmrelease.yaml | grep -m1 "tag: $V@"
ghcr "$V"
# Rollback targets still resolve (HTTP 200):
ghcr sha256:57d001f4c868c04c932c6e7299eea78dcea9597f8e99f2123a06ce754dbde01a    # 0.6.5, live before
ghcr sha256:92e206a02148ac766195d48acdf6c339486cff00473c800434927b54e9c37908    # 0.6.7
for n in 3381 3336 3330 3294 3274 3241 3342 3418; do
  gh api repos/$R/issues/$n --jq '"\(.number) \(.state)"'; done
```

Ask Tom for two things he can see: code-server loads at `https://dev-env.haynesops.com`,
and a new standby session (`task-haynes-ops-*`) appears on his claude.ai and phone list
a few minutes later. The Codex phone entry keeps its old name
(`dev-env-574bdc9844-jhvfs`) and comes back online.

### What needs an in-pod session afterwards

The post-ready standby is the natural one, and Tom drives it from his phone. Hand it
this list:

```bash
kubectl get pod -n dev -l app.kubernetes.io/name=dev-env -o wide        # Running 3/3, new pod name, a talosm* node
kubectl get deploy dev-env -n dev -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}{.spec.template.spec.containers[0].resources}{"\n"}'
                                                                         # 0.6.8@<digest>; limits cpu "8", memory 64Gi
kubectl get rs -n dev -l app.kubernetes.io/name=dev-env --sort-by=.metadata.creationTimestamp | tail -3   # one new RS
claude --version; codex --version                                        # 2.1.290 / 0.160.1 (0.6.7 path: 2.1.288 / 0.160.0)
claude-login-check                                                       # exit 0 (next lapse about 2026-10-21)
claude mcp list                                                          # every server connected
tmux ls; pgrep -af app-server | head -3                                  # codex-remote supervisor + daemon
tail -40 /tmp/post-ready.log                                             # standby launched, no "loop suspected"
agent-run list                                                           # the standby task-haynes-ops-* is listed
stat -c '%y' /creds/gh_token; gh api rate_limit --jq .rate.remaining    # token refreshed within 40 min, and it works
kubectl logs -n dev deploy/dev-env -c gh-refresher --tail=5
hw-ssh list                                                              # includes pikvm (alias kvm)
hw-ssh pikvm 'hostname; pacman -Q kvmd'                                  # read-only
grep -c gpt-6.1-sol ~/.codex/models_cache.json                           # > 0
grep -c 'Every repo gets the Claude Code PR reviewer' ~/.claude/CLAUDE.md  # 1 (#3294)
grep -c 'mosquitto (broker migration test)' ~/.claude/CLAUDE.md          # 1 (#3381)
grep -c 'kustomize-controller takes over fields' ~/.claude/CLAUDE.md     # 1 (#3274)
kubectl get clusterrole dev-env-operator -o yaml | grep -c emqx          # 0 (#3381)
kubectl -n upgrade-agent rollout status deploy/dev-env-ops --timeout=5m
kubectl -n upgrade-agent get deploy dev-env-ops -o jsonpath='{.spec.template.spec.containers[*].image}{"\n"}'
kubectl -n upgrade-agent logs deploy/dev-env-ops -c app --since=15m | grep -E 'watcher-up|max-login'
kubectl get cm -n ai scribe-notes-jobtemplate -o yaml | grep -o 'dev-env:[^"]*'
declare-activity end <id>                                                # if one was declared in step 4
```

Two things look alarming and are not:

- The Deployment annotation `kyverno.io/verify-images: … "fail"` is the known cosign-v3
  audit gap (#3092). The policy is Audit, so it does not block anything.
- `no-max-login` in dev-env-ops is expected until the ceremony in section 10 is done.

If agent-run's codex model picker prints "codex fallback rows are stale vs
models_cache.json", codex 0.160.1's catalog differs from #3330's snapshot. That is
cosmetic. Fixing it means a held dev-env PR, so park it in the saga backlog rather than
restarting again.

## 9. Not part of this restart

| PR | Why not |
|---|---|
| #3276 upgrade-agent image toolchains | A different image (`scripts/upgrade-shepherd`, `scripts/upgrade-agent`). It restarts shepherd and alert-responder, not dev-env. |
| #3316 mcp 2.3.0 | `scripts/audio-authoring/requirements.txt`, a separate service that is upgraded without touching dev-env. |
| #3311, #3306, #3303, #3300, #3285 | Unrelated apps. |

## 10. Follow-ups that must not get lost

1. **dev-env-ops Max login ceremony (pending; issue #3414 stays open until it
   passes).** The wiring is merged as #3422 and deployed (dev-env-ops restarted
   2026-10-06 03:35:44Z). Since then dev-env-ops has **no Max login**, so `wo-*` and
   `esc-*` sessions run on the setup token without Remote Control, and the pages say so.
   The ceremony is in haynes-ops `.agents/runbooks/agentic-remediation.md`, section
   "dev-env-ops Max login". It runs by `kubectl exec` into namespace `upgrade-agent`,
   so it needs an in-pod (or kubectl-capable) session, plus Tom awake to open the link
   and relay the code. Do it **after** the bounce, from the new standby: a session that
   starts it before the merge dies mid-ceremony. Done means
   `/opt/dev-env-ops/login-check.sh` exits 0 and `/opt/dev-env-ops/rc-selftest.sh`
   prints `SELFTEST: REGISTERED`; then close #3414. The login lives on PVC
   `dev-env-ops-home` and survives every later dev-env-ops restart, including this
   bounce's image change. The OAuth URL and the code are secrets: chat and the tmux
   pane only, never git, a PR or an issue.
2. **#3415 dev-env-ops executor fragility (needs Tom's decision).** Every config or
   image merge restarts dev-env-ops and orphans a claimed order. A busy `esc` lane also
   holds new escalations without paging. This bounce is one such restart, which is why
   step 4 checks for claimed orders.
3. **#3392 v1 OPERATOR-tier escalation (needs Tom's decision: accept or mitigate).**
   A mitigation edits `kubernetes/main/apps/dev/dev-env/app/rbac.yaml` and similar
   files. ClusterRole edits do not restart the pod, so this does not need to ride a
   bounce.
4. **#3405 Kyverno `kyverno.io/v1` ClusterPolicy → CEL policy types.** Unrelated to
   dev-env restarts. It is listed here so it stays visible.
5. **`GITHUB_BOT_TOKEN_PERMISSIONS` has no `secrets`.** The dev-env `gh-refresher`
   down-scope is `contents, pull_requests, workflows, issues, checks, actions`, so the
   pod's token gets 403 on `actions/secrets`. The workaround is the one-off Job in
   haynes-ops `.agents/runbooks/new-repo-setup.md`
   (`.agents/templates/k8s/gh-secret-set-job.yaml`). No issue tracks it. Adding the
   permission is a dev-env HelmRelease change, so it can only ride a restart (step 1,
   question 3).
6. **The toolchain trap recurs.** Every future "dev-env image toolchain" Renovate PR
   needs the workflow tag bump pushed onto it before merge (`0.6.8` → `0.6.9` next).
