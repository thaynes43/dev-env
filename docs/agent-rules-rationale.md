# Agent rules: rationale and history

This file holds the reasons, dated history and detail cut from this repository's
`CLAUDE.md` and `AGENTS.md` when OpenProject story #499 shortened them on 2026-11-09.
`CLAUDE.md` links here once; agents look things up here when a rule's reason
matters. The complete earlier text of both files is in git at commit
`4633b162759539d64d5fcfb6b0d989782d69615a`. The common rules every session loads have
their own rationale file in haynes-ops, `.agents/reference/dev-env-agent-rules-rationale.md`.

## Owner ruling, 2026-11-09

Tom chose "Merge after your review (Recommended)" by phone prompt for two changes.

- **Docs first.** The earlier rule: "A change in behaviour starts in the saga: ADR for
  the decision, design for the how, a backlog plan for the steps, then code. Docs
  change in the same PR as the behaviour they describe. An Accepted ADR is never
  edited; a new ADR supersedes it." Now a design document is required only for
  changes to access or auth, data lifecycle or retention, and API or CRD contracts.
  Other behaviour changes are described in the PR body. An Accepted ADR is still
  never edited.
- **Stop boundary** (in the common rules): an owner session that finishes a story may
  take the next ready story in the same epic, within its budget, once that story has
  its own tracker record. An explicit pause, hold, pending question or budget
  threshold still stops it.

## What was removed as stale on 2026-11-09

- **The "current takeover" pointer** to the 2026-10-13 pickup brief, and the
  paragraph saying the 2.28.0 shared-history correction was installed (operator
  `sha-cbf0700`, haynes-ops #3906 and #3907). On 2026-11-09 the live agent image was
  2.95.0. Current state now comes from OpenProject (epic #91), `agent-run fleet` and
  the haynes-ops manifests.
- **Instructions to read `.agents/HANDOFF.md` and the pickup briefs in full.**
  HANDOFF.md was 256 KB and the two briefs 72 KB together. They are references now,
  with a banner at the top of each that says to search them.
- **"Claude Code reviews are optional and are currently unavailable (Tom,
  2026-10-11)."** Tom confirmed the review service was down on 2026-10-11. It ran on
  most PRs on 2026-10-12 and 2026-10-13, and on current PRs on 2026-11-09 (for
  example dev-env #1005 and haynes-ops #4725). The rule that survives: the review is
  advisory, may be missing, never gates a merge, and its findings get answers.
- **The Layout block** moved from `CLAUDE.md` to `docs/repo-layout.md`, because the
  block alone was 21 KB. `make check-layout` now reads the new file. Its HANDOFF line
  ("the front door") changed to describe the file as a log to search.

## Detail cut from CLAUDE.md

### Releases and publication

- The release token is the GitHub App `thaynes43-dev-env-release`, from the repo
  variable `RELEASE_APP_ID` and secret `RELEASE_APP_PRIVATE_KEY`, so release PRs run
  CI like any PR. The automatic advisory review skips the release App's PRs; an
  explicit `@claude` request through `claude.yml` can still review one.
- The first release was 2.0.0, because the agent image line is 2.x; it shipped on
  2026-10-07 (#52). Tom, 2026-10-07: "Merge, and let agents merge releases."
- `publish.yml` push paths include the release-version manifest and the workflow
  itself, so reviewed release bumps and publication fixes publish automatically.
  Manifest-changing commits take their version from that checked-in release record,
  so publication cannot race tag creation; other commits keep the Git-derived
  version. Cosign signing is pinned like haynes-ops v1 (haynes-ops #3092).
- `publish-agent.yml` pushes `dev-env:2.x.y` from a `v2.x.y` tag, never `latest` or an
  existing tag, and signs it keyless. A tag made with `GITHUB_TOKEN` starts no
  workflow, hence the manual `gh workflow run publish-agent.yml --ref v2.x.y`.
- Renovate auto-merge has been on since the Protect Main ruleset began requiring
  `CI - Success` (2026-10-07).

### Build

- The agent image (`images/agent/Dockerfile`, D-53) is v1's Dockerfile plus `tini`,
  agentd, `agent-run`, Codex and `kubectl-cnpg`, with `pve` and `hw-ssh` in
  `images/agent/tools/`. `ci.yml` smoke-tests it with `images/agent/smoke-test.sh`.
  Since release 2.97.0 (OP504, 2026-11-09) the image also carries the Go toolchain,
  copied from the build stage into `/usr/local/go`, with `GOTOOLCHAIN=local`. Under
  that setting `go` ignores a newer `toolchain` line in `go.mod` and refuses a newer
  `go` line. Before that release Go appeared only in the build stage, so a session pod
  had no Go toolchain.
- The envtest API server version follows the main cluster's Kubernetes minor, not
  `go.mod`'s client libraries.
- CEL rules without `MaxLength` or `MaxItems` on what they read exceed the API
  server's cost budget, and the CRD install fails in envtest.

### Coordination

- The routine-coordination ruling is Tom's, 2026-10-30 (OP272/3416, OP252/3435).
- Codex must use an available native user-question tool once phone delivery is
  verified; that delivery was unverified on 2026-11-09 and is a plan 11 acceptance
  requirement.
- The outcome-file guidance is source guidance: installation needs the matching
  agent, operator and AgentSession CRD rollout.

### Hard-rule history

- **2026-10-05, CPU starvation.** A flake-reproduction run in the v1 pod, which had no
  CPU limit, pushed control-plane node talosm02 to load 222 on 20 cores from 23:42 to
  00:12Z. EMQX, traefik, authentik and cloudnative-pg went into liveness-kill loops.
  The kubelet was not starved (it peaked at 0.18 cores): BestEffort pods, with no CPU
  request and so CPU weight 1, got no CPU on the saturated node.
- **2026-08-29, token family revoked.** Two processes refreshing the same Claude Max
  login revoked the whole token family.
- The repository became public on 2026-10-07 (DESIGN-001 Q-12).
