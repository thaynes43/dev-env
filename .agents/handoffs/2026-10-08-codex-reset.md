# Codex reset work order — 2026-10-08 America/New_York

Tom requested a safe parking point with 7% Codex usage left and a handoff for
the reset. Pause new implementation after shipping this record. The whole v2
project is unfinished; twenty mandatory capability-parity gaps still block
cutover. No running v1 or agent session is to be restarted for this work.

## Start here after the reset

Read this file, [HANDOFF](../HANDOFF.md), the repository
[CLAUDE.md](../../CLAUDE.md), and the saga
[README](../sagas/distributed-dev-env/README.md). In the pod also read the original
work order `/home/dev/work/orders/codex-handoff-2026-10-08.md` in full. Its priority
order survives, with the owner corrections below. Use latest canonical main as
the base for a fresh task worktree; never implement in the detached coordinator
home `/home/dev/work/dev-env-codex` or a canonical clone.

Codex coordinates on Astra; native work goes to exact `gpt-6.1-sol`, effort
`xhigh`, `fork_turns: none`, with self-contained orders. Ask owner questions here
one at a time, after verifying the premise. Give simple operational instructions
directly in chat; repository housekeeping must not delay that answer.

## Owner state — preserve it

- **Q-19 complete by owner confirmation.** Tom generated and saved the fresh
  keeper CA in existing 1Password vault/item `HaynesKube/dev-env`, fields
  `SSH_CA_PRIVATE_KEY_B64` and `SSH_CA_PUBLIC_KEY`. No values were sent to or read
  by this coordinator. Do not ask him to generate it again or create another
  item. Format/delivery validation is still pending; saved confirmation is not
  runtime acceptance. D-72 supersedes the originally proposed separate item.
- Existing ExternalSecrets select individual fields. The new CA must be mapped
  only into a separate keeper Secret, never v1's consumed Secrets or session
  profiles. Its storage alongside existing fields does not require a v1 change.
- Existing Proxmox hosts, Unix `dev-env` account and sudo access are reused.
  Fresh keeper CA trust is the remaining node change. The original work order
  explicitly assigns that trust setup to Tom, unless he delegates it; existing
  hw-ssh reach does not transfer that owner step to the coordinator.
- Use `agent-run` or agent-started sessions. Laptop kubeconfig setup/acceptance
  is not a prerequisite. D-68 remains an optional, externally unverified path.
  A web session UI was mentioned as an option, not commissioned.
- Effective parity includes accepted Headlamp cluster-admin and self-merged
  GitOps task paths, with the existing owner-directive rules. Preserve every
  accepted task through guarded access, prove parity and guardrails, migrate
  callers, then retire Headlamp via GitOps (D-70/D-71). Its continued use is a
  migration fallback. No blanket standing admin policy is authorized.
- Q-18's abstract additional-powers prompt was withdrawn by the coordinator.
  No approval route was selected; this is not an owner ruling to defer it.
  Q-16's phone/app requirement remains. [PR #90](https://github.com/thaynes43/dev-env/pull/90)
  and [haynes-ops #3550](https://github.com/thaynes43/haynes-ops/pull/3550) stay closed;
  their Pushover/web approval design is superseded. Do not replay those branches.

## What is already shipped

1. Plan 02 CLI workflow/acceptance is complete (#106), incorporating the owner
   correction above. No laptop test holds it open.
2. The docs-only approval spike is complete (#107), with D-70/D-71 corrections
   in #113. R-03 remains candidate research, not an enabled human adapter.
3. R-04 is complete (#109); all twenty backlog 07 parity rows remain open for
   their mandatory closure tests. Presence checks or denial checks alone do not
   close them.
4. Keeper PVE backend, broker credential jobs, UID-fenced agentd store and pve
   helper behavior are built, tested and deployed **disabled** (#111/#110;
   [haynes-ops #3589](https://github.com/thaynes43/haynes-ops/pull/3589)
   schema/RBAC/admission/journal and
   [#3595](https://github.com/thaynes43/haynes-ops/pull/3595) image pins).
5. #113 corrected the Headlamp baseline and retirement target. #114 merged as
   `29bea15d5ad96ce13d5cba15e0c44d213cae3c97`, with green CI and actual Claude
   review, and corrected existing-item storage plus owner-run generation and
   cleanup. This parking record adds the saved confirmation; it needs no rollout.

Runtime pins to verify before touching deployment:

| Component | Deployed pin |
|---|---|
| Operator, broker, keeper | `ghcr.io/thaynes43/dev-env-operator:sha-eeb15e3@sha256:491bfd05e101843d1bb31250841095a23901cf1eeb51566398ddc416ccbcef3c` |
| Agent template and shelf | `ghcr.io/thaynes43/dev-env:2.9.0@sha256:423201bec73fc4dae1bc92d22d444587c3ed89d641674e6a92d3f15208b68c72` |
| Agent revision | `2.9.0-5030ab2483` |

Parking metadata was verified at **2026-10-08 21:37:07 America/New_York**
(`2026-10-09T01:37:07Z` in the snapshot). Operator/broker/keeper are Ready at
2/2, 2/2 and 1/1 on the pins above. All three HelmReleases and seven v2 Flux
Kustomizations are Ready at haynes-ops `04c611e7`. V1 pod UID
`cf7abc47-0363-491a-9420-12db9731ea8d` and shelf UID
`01a49105-473a-42c3-9ad9-db3cc64e189b` are unchanged, with zero restarts.

AgentSessions, CredentialJobs and GrantPolicies are all zero. Three historical
AccessGrants are ended (two Released, one Expired); none is active. Both PVE
enable flags are absent/default false. Keeper has only its GitHub App
ExternalSecret/mount; CA projection and SSH targets/trust configuration remain
absent. GitHub readiness does not prove PVE mint readiness. Fixtures are gone,
owned activity declarations were already ended, and this parking turn made no
runtime writes. The read-only snapshot is
`/home/dev/work/state-snapshots/dev-env-park-20261008.json`.

## Resume sequence

1. Confirm live metadata, runtime pins, feature flags, empty fixtures and the
   latest main/PR state. Check `claude-login-check`; at parking the Max login
   remains valid for about twelve days. Fetch canonical clones only for git
   administration, then create a task worktree under `/home/dev/work` on an
   `agent/` branch. Re-read current rules; do not replay already merged commits.
2. Prepare isolated GitOps CA wiring with minting still off. A keeper-only
   ExternalSecret selects item `dev-env`: Base64-decode
   `SSH_CA_PRIVATE_KEY_B64` once into `private-key`; map `SSH_CA_PUBLIC_KEY`
   unchanged into `public-key`. Destination is
   `dev-env-system/dev-env-keeper-ssh-ca`, read-only mount
   `/etc/dev-env-keeper/ssh-ca`. The keeper parses raw, unencrypted Ed25519
   OpenSSH private-key bytes and the matching public line.
3. Prepare exact, reviewable node trust instructions using the existing account.
   Ask only the next concrete owner question: Tom installs the fresh CA's trust
   and restricted principal configuration, or explicitly delegates that work.
   The minter principal is `dev-env-keeper-proxmox-minter`; the implemented
   two-minute certs force each token operation and have no PTY/forwarding
   extensions. Server-side restrictions and host trust remain to be verified.
   Avoid account-wide changes that alter v1's existing hw-ssh behavior.
4. Supply explicit private target configuration and pinned host keys at
   `/etc/dev-env-keeper/ssh/targets.json` and
   `/etc/dev-env-keeper/ssh/known-hosts`, scoped keeper TCP22 egress, and the
   accepted standing credential policies. Keep private addresses and actual
   trust material out of public docs/source. CA storage alone is not activation.
5. Stage both keeper and broker `--enable-proxmox-grants` only after prerequisites
   are verified. Declare the bounded runtime activity. Prove real mint, UID-fenced
   installation, existing pve CLI/API use, release/expiry/recovery and cleanup;
   then remove fixtures/grants and end the declaration. Compare v1 and existing
   AgentSession pod UIDs/restarts before and after. General hw-ssh leaf issuance
   remains a separate unbuilt gap, pending P-19's connection/revocation contract.
6. Continue the twenty parity restorations and remaining plan 03 login/Remote
   Control and plan 04 Codex core. Only propose an approval choice around a
   concrete workflow after its premise is verified. No cutover or Headlamp
   removal until acceptance and caller migration are complete.

## Sources and operating constraints

- [DESIGN D-69/D-70/D-71/D-72 and Q-19](../sagas/distributed-dev-env/designs/001-dev-env-v2.md)
- [Backlog 07 H5 and P-01 through P-20](../sagas/distributed-dev-env/backlog/07-access-broker.md)
- [R-04 parity evidence](../sagas/distributed-dev-env/research/R-04-v1-capability-parity.md)
- [R-03 approval research](../sagas/distributed-dev-env/research/R-03-claude-code-approvals.md)
- [Issue #91](https://github.com/thaynes43/dev-env/issues/91), including the last
  coordinator parking comment, is the GitHub resume index.
- Pod-local machine contract: `/home/dev/work/orders/keeper-pve-contract-1008.md`.
- Pod-local metadata: `/home/dev/work/state-snapshots/headlamp-effective-parity-20261008.json`,
  `/home/dev/work/state-snapshots/pr3589-prerequisites-20261008T151702Z.json`,
  `/home/dev/work/state-snapshots/pr3595-runtime-20261008T153445Z.json`, and the
  new `/home/dev/work/state-snapshots/dev-env-park-20261008.json`.

All implementation is in merged main; old task branches are references, not
unmerged required work. Preserve other agents' worktrees and rescue branches.
The detached coordinator home is clean and stays read-only. Ordinary ready PRs
are squash-merged after required CI and actual advisory findings are addressed;
changes under haynes-ops dev-env `app/resources/**` remain held drafts because
they bounce this pod. No Secret values, private addresses or auth-flow material
belong in git, chat, public issues or this handoff.

Never run CPU burners, stress tools or wide/looped tests on shared nodes. Use
bounded low-parallelism checks, fake clocks/providers, and CPU-limited worker
Jobs when necessary. Do not broaden tests after relevant checks pass without a
new failure or concern. Read before writes; declare disruptive work and end the
declaration. No v1 restart or automatic session restart on operator upgrades.
