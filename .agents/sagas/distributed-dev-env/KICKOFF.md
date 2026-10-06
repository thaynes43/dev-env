# KICKOFF: the first build session (ratification, spikes, plan 01)

- **For:** the first Claude Code or Codex build session, in the v1 dev-env pod or on
  Tom's own machine. It starts by asking Tom to ratify ADR-001. Read
  [`.agents/HANDOFF.md`](../../HANDOFF.md) first.
- **Source of truth:** backlog [00 (spikes)](backlog/00-spikes.md),
  [01 (foundation)](backlog/01-foundation.md) and
  [DESIGN-001](designs/001-dev-env-v2.md). This file sets the order and adds the repo
  setup that those files leave out. If it disagrees with a plan, the plan wins: fix
  this file in your PR.
- **Written:** 2026-10-06, while ADR-001 was still Proposed.

## Before you start

- **ADR-001 must be Accepted before any code.** If it is still Proposed, ask Tom once
  whether he ratifies it ("Accept as written" first), then do step 0. Track B and
  every code PR wait for Accepted. Track A's spikes do not: they write no code, and
  [00](backlog/00-spikes.md) runs them in parallel with the ratification. So an
  in-pod spike may start while Tom's answer is pending.
- **If you are in the v1 pod,** wait until the v1 bounce is done (HANDOFF, "What
  happens first"). Also run `claude-login-check`, as the pod's CLAUDE.md asks.
- **Read** plans 00 and 01 in full. From DESIGN-001, read sections 3.1 to 3.6, 4.4,
  5.1, 6.4, 6.6, 6.10, 6.11, 7 and 10 to 13.

## Step 0: the ratification PR

Branch `agent/ratify-adr-001`. Put Tom's own words in each dated ruling.

- [ADR-001](adrs/001-distributed-dev-env.md): set Status to `Accepted`. Add a
  `**Ratified:** Tom, <date>: "<his words>"` line. Change the Deciders line from
  "the ADR itself is not yet Accepted" to the date he accepted it. Change nothing
  else: this is the last edit an Accepted ADR gets.
- [DESIGN-001](designs/001-dev-env-v2.md): change its status line to "Accepted
  <date> with ADR-001", and make "Governed by" read Accepted.
- [Saga README](README.md): update the status paragraph. Set decision-log rows 2, 3,
  9, 10 and 16 to `DECIDED <date> (Tom)`.
- [HANDOFF](../../HANDOFF.md): update "State" and "What happens first".
- If Tom changes anything, fold each change in as a dated ruling (D-NN, or a new
  Q-NN with its ruling) in this same PR, before the status flip.

## 1. Objective of phase 1

Plan 01's goal, unchanged: from the v1 pod, `agent-run -p "<task>"` creates a session
pod on a worker node. The task runs on the static Claude token and opens its PR. The
operator suspends, rescues and archives the session, and an operator restart
mid-task does not disturb it. The baseline guard refuses every haynes-ops #3392
path. Phase 1 is task mode only. Interactive sessions (02), Remote Control (03) and
the broker (07) come later.

Plan 01 depends on spikes S-7, S-8 and S-12, and on nothing S-1 decides. So tracks A
and B run side by side: the in-pod spikes go to a pod session, and the repo skeleton
goes ahead here. S-1 still starts first among the spikes. Its answer reshapes plans
03 and 10 (the keeper's login, or a coordinator host), and step 3 can wait up to 8
hours for a token refresh.

## 2. Track A: spikes

Run each spike exactly as [00-spikes.md](backlog/00-spikes.md) writes it, and follow
its safety rules: one CLI call at a time, scratch directories under
`/tmp/spike-<id>/`, never a refresh token copied or printed, and key names only.
Record each result as 00's Acceptance says: a result line in DESIGN-001 section 13, a
dated note in the section the spike decides, and a tick in 00. Use one PR per spike
or per small group.

**Group 1: now, in the v1 pod** (from outside, hand these off as HANDOFF describes)

| Order | Spike | Pass | On a fail |
|---|---|---|---|
| 1 | S-1 and S-1b: Claude on an access-token-only credential. **Passed 2026-10-06** (00 and DESIGN-001 6.2) | 00's pass line holds: steps 2 and 3 work; agentd's re-merge or `CLAUDE_CODE_OAUTH_401_WAIT_MS` covers the 401 window; step 5 fails harmlessly and never writes to the live login. Record whether `.claude.json` seeding was needed, and whether S-1b registers. | D-11 switches to the coordinator host, and plan 03 takes its "If S-1 failed" branch. Record that and tell Tom in one line. |
| 2 | S-6: resume with Remote Control. **Passed 2026-10-06** (00 and DESIGN-001 6.7) | After `--resume`, the same Remote Control entry comes back with its history: the same bridge session id in `~/.claude/sessions/<pid>.json`. | Section 6.7 records that a drain makes a new phone entry. Plan 03's acceptance changes to match. |
| 3 | S-15: archive on reap. **Passed 2026-10-06** (00 and DESIGN-001 6.7) | The archive call returns 200 or 409, and the entry leaves Tom's active list. Ask him to look: one question. Record step 4 either way. | Reaped entries stay offline, the console's archive button covers them, and `agent-run fleet` counts them (00, S-15). |
| 4 | S-16: reading plan usage. **Done 2026-10-06: the read works** (00 and DESIGN-001 7.3) | Either outcome is a result: the usage read works with no side effects (record key names and freshness), or it does not. | The operator counts quota errors instead (7.3). |
| 5 | S-3: Codex on an access token | `codex exec` and the unpaired app-server both run on the access token alone. | D-12 stops at step 1: the codex hub keeps `auth.json`. |
| 6 | S-7: clone time per repo | Wall time is recorded for each of the five repos, cloned one at a time with `pack.threads=2`. | Any repo over 2 minutes gets a shared mirror (D-15). |

Do not poll for S-1 step 3's token refresh. Read `expiresAt` once, do other work, and
come back after that time. S-2 is already answered; re-run its one-line
probe after each Claude Code CLI bump.

**Group 2: phase 1, after the haynes-ops PRs that deploy what they test**

| Spike | When | Pass | On a fail |
|---|---|---|---|
| S-12: the baseline guard | After the RBAC and guard PR, and before any agent pod gets the role. Run it from a pod that uses the agent ServiceAccount: the first task pod, or a test Job the PR deploys with a CPU limit. An in-pod session reads the output. | Every #3392 path is refused and every runbook action is allowed. Record whether the ValidatingAdmissionPolicy sees `CONNECT` for exec. The break-glass half of S-12 (its exclusions refused, the `kube-system` eviction allowed) needs `dev-env-grant-breakglass`, which plan 07 ships, so it runs there. | Fix the guard before going further. D-19 records the change. |
| S-8: `gasha01-rbd` against `ceph-block` | Once the first task pod runs at size M. Do two runs, never at the same time. | The times for each step are recorded. | If gasha01 is more than twice as slow overall, size L defaults to `ceph-block` (D-22). |

**Later, not phase 1:** S-10 before plan 08, S-5 in phase 3, S-4 in phase 4, and S-9,
S-13, S-11 and S-14 before plan 09. S-14 needs Tom at each machine.

## 3. Track B: the first PRs in this repo

Each step is one ready PR in the order below. Once B2 exists, merge each PR when
`CI - Success` is green and every review finding is handled.

**B1: the Go skeleton.** Done in #14 (2026-10-06).

- **Module:** `github.com/thaynes43/dev-env`, with a `go` and `toolchain` line that
  Renovate bumps.
- **Tooling:** controller-runtime plus controller-gen. A kubebuilder scaffold is fine
  if you trim it to this layout. *As built:* B1 pins controller-gen in the Makefile.
  controller-runtime is not in `go.mod` yet: its one helper for API packages
  (`pkg/scheme.Builder`) is deprecated in v0.25 because API packages should depend on
  k8s.io/apimachinery only, so `api/v1alpha1` uses apimachinery's `SchemeBuilder`, and
  nothing else in B1 imports controller-runtime. It arrives with the first code that
  does: plan 01 step 1's envtest suite and the operator's manager. *Plan 01 step 1
  added it* (v0.25.2), for `pkg/envtest` and the test client in `internal/testenv`;
  `api/v1alpha1` itself still imports apimachinery only.
- **CLAUDE.md:** record the layout in its "Layout" section in the same PR.
- **A first test:** ship one real command (`agent-run version`) with its test, so CI
  has something to run.

```
api/v1alpha1/          CRD types, group dev-env.haynesops.com (AgentSession first)
cmd/dev-env-operator/  the operator; the broker is a second mode of it (DESIGN-001 3.1)
cmd/dev-env-keeper/    the keeper, its own binary in the operator image (D-38)
cmd/agentd/            the in-pod supervisor
cmd/agent-run/         the CLI, static (CGO_ENABLED=0)
internal/              controllers, the /v1 API server, shared clients
config/crd/            generated CRDs; haynes-ops gets copies by PR
images/operator/       Dockerfile: ghcr.io/thaynes43/dev-env-operator (operator + keeper) (B2)
images/agent/          Dockerfile: ghcr.io/thaynes43/dev-env, tag line 2.x (B5)
Makefile               generate, lint, test, build; test parallelism capped
```

**B2: CI, `.github/workflows/ci.yml`.**

- **Jobs:** `lint` (golangci-lint), `test` (`go test -p 2 ./...`; add envtest once
  controllers exist, one suite at a time) and `build` (`agent-run` for linux/amd64
  and darwin/arm64, and the rest for linux/amd64).
- **Image builds on PRs** live in this workflow too: the operator image always, and
  the agent image only when `images/agent/**` changes. They build and never push.
  B2 adds `images/operator/Dockerfile`, which B1 left out because nothing in B1
  builds an image. It ships both `dev-env-operator` and `dev-env-keeper` (D-38), built
  with `make build` (or the same flags). The build has no `.git`, so neither
  `git describe` nor the toolchain's VCS stamp is there: pass `COMMIT=<sha>`
  always, and `VERSION=<release tag>` on release builds. Without `VERSION` the
  binaries report `dev`.
- **The aggregate job.** `CI - Success` needs every job above, runs `if: always()`,
  and fails if any of them failed or was cancelled. It is the only check to make
  required. Do not put `paths:` filters on the workflow trigger: a docs-only PR must
  still get `CI - Success`, or it waits forever. Filter inside the jobs instead.
- **Settings:** `permissions: contents: read`, a `timeout-minutes` on every job, and a
  concurrency group per ref that cancels superseded PR runs. Pin actions to a SHA
  with a version comment (the haynes-ops style), so Renovate bumps them.
- **Keep it light.** The runners are GitHub-hosted, but Actions minutes on a private
  repo are a budget. The same `make test` must also be safe to run in the pod. So:
  no wide matrices, no repeated full-suite runs, and keep `-p 2`.
- **Leave the reviewer alone.** Keep `claude-code-review.yml` and `claude.yml` as they
  are. They are advisory and never required.

**B3: publish the operator image, `.github/workflows/publish.yml`.**

- **On push to main only:** build `images/operator`, push
  `ghcr.io/thaynes43/dev-env-operator:sha-<short>`, and sign the digest with cosign
  keyless.
- **Permissions:** give `packages: write` and `id-token: write` to the publish job
  only.
- **Tags:** never push `latest`. Never re-push an existing tag; a workflow-only change
  must not rebuild and re-tag, because that is how v1 got Renovate digest-drift PRs.
- **Signing:** match the cosign pin in haynes-ops `dev-env-build.yml`, so v2 images
  verify (or fail to) exactly as v1's do. Follow haynes-ops #3092 when it changes how
  Kyverno reads signatures.
- **After the first publish,** the package must be made public (Q-13, ruled A on
  2026-10-06, because the cluster pulls GHCR images anonymously). GitHub has no API
  for visibility, so a laptop agent flips it in the browser: part 2 of
  [the laptop handoff](../../handoffs/2026-10-06-tom-laptop-settings.md). Tell Tom
  when the package exists, then check that an anonymous pull works.

**B4: Renovate and release-please.** Done in #18 (2026-10-06).

- **Renovate** (`.github/renovate.json5`, one file):
  - the Dockerfile `customManagers` are copied from haynes-ops
    `.renovate/customManagers.json5`, as plan 01 says, and a second regex manager
    reads the `# renovate:` pins in the Makefile (controller-gen, golangci-lint);
  - gomod, GitHub Actions (`helpers:pinGitHubActionDigests`, SHA plus version
    comment) and Dockerfile digests (`docker:pinDigests`);
  - haynes-ops' nightly schedule (after 10pm, before 6am, America/New_York);
  - **auto-merge is off.** Two rules cover it (minor and patch for Go modules
    and for Actions; Actions pin and digest refreshes count with them) and set `automerge: false` until the Protect Main ruleset requires
    `CI - Success`. With no required check, GitHub auto-merge has nothing to wait
    for, so it would merge before CI reports. Turning it on is a flip of both rules in the
    PR that records laptop handoff part 3 as done (HANDOFF checklist).
- **release-please:** one version for the repo; both images are tagged with it
  (DESIGN-001 section 10). Manifest mode, `go` release type, and
  `initial-version: 2.0.0`: section 10 puts the agent image on the `2.x` line and B5
  publishes `2.x.y` tags only, so the one repo version starts at 2.0.0 and the first
  release is 2.0.0. Tags are `vX.Y.Z` (the Makefile's `git describe` matches
  `v[0-9]*`); image tags drop the `v`. The Go module path has no `/v2` suffix, so
  `v2.0.0` is not a version `go get` accepts for the module. That is fine: everything
  here ships as built binaries and images (D-06), and nothing imports the module as
  a library. If that changes, move the module to `/v2` first.
- **The release-please trap.** A PR opened with the workflow's `GITHUB_TOKEN` starts
  no workflows, so `CI - Success` never reports on release PRs. Tom ruled the fix on
  2026-10-06 (DESIGN-001 Q-14, A): a GitHub App key secret. The workflow
  (`.github/workflows/release-please.yml`) reads the repo variable `RELEASE_APP_ID`
  and the repo secret `RELEASE_APP_PRIVATE_KEY` with `actions/create-github-app-token`.
  The App needs Issues read and write too, because release-please creates its
  `autorelease:` labels. Only Tom can add them (part 1 of
  [the laptop handoff](../../handoffs/2026-10-06-tom-laptop-settings.md)).
- **Until the secret exists** the workflow falls back to `GITHUB_TOKEN` and prints a
  `::warning::`. Then close and reopen each release PR from the pod as
  haynes-dev-bot (App tokens do start workflows):
  `gh pr close <n> && gh pr reopen <n>`. Repeat it after every update: release-please
  force-pushes the same PR branch after each push to main, and a push made with
  `GITHUB_TOKEN` starts no run, so `CI - Success` stays on the old head. Do not merge a release PR whose
  `CI - Success` has not reported. The fallback also needs the repo setting "Allow
  GitHub Actions to create and approve pull requests" (the App path does not); without
  it the first run on main failed at the PR-create call on 2026-10-06 (laptop handoff
  part 1, step 5).

**B5: agent image `2.0`.** Plan 01 lists the contents: a copy of haynes-ops
`scripts/dev-env/Dockerfile` with `tini`, agentd, Codex and `kubectl-cnpg` baked
in, plus `pve` and `hw-ssh`, and no code-server. Build it in CI only; it is about
1 GB, so never build it in the pod. Port v1's smoke test. Publish `2.x.y` tags only,
never `latest` or v1's `0.6.x`.

B5 has two preconditions:

1. Tom has granted this repo write access to the existing GHCR package.
2. The haynes-ops Renovate rule that holds the v1 HelmRelease below `2.0.0` is
   merged. Without it, the first `2.x` tag invites a v1 "upgrade" PR. Done
   2026-10-06: haynes-ops #3458 (`.renovate/holds.json5`).

Two haynes-ops edits follow from the Kyverno rule that checks v2 signatures
(`verify-thaynes43-images`, rule `verify-dev-env-v2`, added in haynes-ops #3462,
Audit only). The first is B5's; the second is done:

- **The tag ref.** The rule trusts `thaynes43/dev-env` workflows on
  `refs/heads/main` only. If B5 signs the agent image from a tag-triggered run (a
  release-please tag), the certificate ends in `@refs/tags/v2.x.y` and fails the
  rule. Either sign from main, or widen the subject to `@refs/tags/v2.*` in the
  same change that adds the workflow.
- **The v1 glob.** v1's `dev-env*` image glob also matches `dev-env:2.*` and, already
  today, `dev-env-operator:sha-*`, which `publish.yml` signs from this repo. So v1's
  rule failed the operator image. Done 2026-10-06: haynes-ops #3463 narrowed it to
  `dev-env:0.*`, ahead of the operator HelmRelease (section 4, item 8.9).

**What only Tom can click** (the bot has no Administration permission):

1. **A "Protect Main" ruleset** on the default branch, modelled on hass-sandbox's. It
   blocks deletion and force-push, keeps linear history, and requires a PR with 0
   approvals and the status check `CI - Success` from GitHub Actions. Leave "require
   up to date" off, so parallel agent PRs do not re-run CI after every merge. Add it
   once `CI - Success` has reported at least once. If GitHub says the ruleset will
   not be enforced on a private repo on his plan, ask him DESIGN-001 Q-12 then, not
   before: the repo has no ruleset yet, and that warning is the question's premise.
2. **Settings:** allow auto-merge (Renovate's `platformAutomerge` needs it), and
   optionally auto-delete head branches.
3. **GHCR access:** on the `dev-env` package, "Manage Actions access", give
   `thaynes43/dev-env` Write. This comes before B5.
4. **Visibility:** make the `dev-env-operator` package public after its first publish
   (B3), as Tom ruled on Q-13 (A, 2026-10-06). Part 2 of
   [the laptop handoff](../../handoffs/2026-10-06-tom-laptop-settings.md).
5. **Renovate:** the Mend Renovate app must cover this repo.
6. **The App key secret** for release-please (B4), as Tom ruled on Q-14 (A,
   2026-10-06). Part 1, step 4 of the same handoff.

Items 2, 3 and 5 are plain settings; HANDOFF lists them as a checklist. Items 2 to 6
are in [the laptop handoff](../../handoffs/2026-10-06-tom-laptop-settings.md), which
Tom can give to an agent on his own machine. Q-12 is the only question left, asked
when item 1 raises it.

An agent working outside under Tom's own gh login could make changes 1 and 2 with
`gh api`, but only after Tom says yes to that exact change.

## 4. Then plan 01 itself

After B1 to B4, work through plan 01's "In this repo" and "In haynes-ops" lists. One
PR per piece, in this order:

1. `AgentSession` types and the generated CRD, with an envtest suite. Done in #24
   (2026-10-06): D-39 records what the schema enforces.
2. Pods and volumes from the size class and the `dev-env-templates` ConfigMap,
   placed per DESIGN-001 section 7, with tests that enforce 5.1: no owner reference
   to the Deployment, and no delete of a Running session's pod outside drain and
   suspend.
3. The `/v1` API (`sessions`, `fleet`) with TokenReview auth.
4. agentd: config rendering (a port of `dev-init.sh`), partial clone and worktree,
   tmux start, heartbeat, and `ctl status|rescue`.
5. Rescue to a bundle on the shared volume (D-10), then suspend and archive.
6. The minimal keeper: mint the gh token every 40 minutes.
7. `agent-run` v2: `-p`, `list`, `reap` and `fleet`.
8. The haynes-ops PRs, smallest first:
   1. the Renovate v1 hold;
   2. the Kyverno identity for `thaynes43/dev-env`;
   3. the namespaces, and the CRDs in their own Kustomization with `prune: disabled`;
   4. RBAC with the guard (S-12 runs here);
   5. the network policies;
   6. the PriorityClass and the Kyverno CPU-limit policy;
   7. the templates and the shared volume (`prune: disabled`);
   8. the ExternalSecrets;
   9. the operator and keeper HelmReleases;
   10. the MCP network policy admits for `dev-agents`.

   Write new Kyverno policies on the `policies.kyverno.io` CEL types where they can
   express them (haynes-ops #3405). An outside agent may author these PRs; an
   in-pod session merges and verifies them, because they are cluster-scoped.
9. The first end-to-end run: from the v1 pod, `agent-run -p` a small real task, such
   as a docs fix in this repo, then plan 01's acceptance checks.

## 5. What "done" means for plan 01

- Every item in plan 01's Acceptance has passed, and the completing PR names the
  evidence for each: pod UIDs, node names, the PR the task opened, the bundle path,
  and the guard's refusals.
- S-7, S-8 and S-12 are recorded as section 2 says.
- Plan 01 is marked `Status: done` with the completing PR, the saga README's backlog
  row agrees, and HANDOFF's "State" is updated.
- The deployed haynes-ops apps are Ready on the merged revision. The v1 pod and
  `dev-env-ops` are untouched: same pods, no restarts caused by this work.
- CLAUDE.md names every code directory, and README's status reads "phase 1 built".

## 6. Safety while building

- **No CPU burners anywhere shared** (HANDOFF, "Hard constraint"). Every Job, test
  pod or spike pod you put in the cluster has a CPU limit.
- **Never edit `kubernetes/main/apps/dev/dev-env/app/resources/**`** in haynes-ops.
- **Declare activity first.** Before cluster work that could trip an alert, run
  `declare-activity` from the pod.
- **No secret leaves its owner.** Spikes read key names and expiry times only.
  Nothing secret goes into git, PRs, issues or logs.
- **Running sessions are sacred**, v1's included. Nothing in phase 1 restarts them.
