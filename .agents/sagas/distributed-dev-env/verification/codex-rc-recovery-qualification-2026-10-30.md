# Codex RC source gates and retained-host recovery proposal

2026-10-30 UTC. Successor coordinator `dev-env-1030-032005`,
[OP272](https://work.haynesnetwork.com/work_packages/272) and existing
[OP252](https://work.haynesnetwork.com/work_packages/252). Requested dispatch:
`gpt-6.1-sol`, medium; actual provider-served model **Unknown**. A driving
model's self-description is not a served-model receipt. OP272 journals
2955/2956 transfer the stopped source author's delivery work; journal 2971
records the separate offline-host failure. OP252 journal 2508 preserved the
old HOME; the current user reopens investigation, not runtime action.

## Source qualification

The four supplied exact heads were verified, including required `CI - Success`,
native review records, and the actual advisory comments. The release freeze
was lifted in OP272 journal 2922. Source merge authorizes neither installation
nor operation on any paired host.

- PR690 `118c2552`: required CI green, independent exact-head review clean,
  advisory no findings. Merged as `e325d0c1` at 03:24:08Z.
- PR695 `7bc0397`: required CI green, independent review clean. The advisory
  retry-status ambiguity was answered in its review thread: PR696's implemented
  contract returns 200 only for an identical acknowledged receipt, 409 for an
  identical unresolved receipt or a conflicting intent, and never redispatches.
  The older proposal's conditional wording does not promise 200 for uncertainty.
  Merged as `57938cc7` at 03:27:27Z.
- PR691 `6b1149a`: required CI green and prior native review clean; later
  advisory found a disconnected duplicate candidate could disappear from
  uniqueness counting. Correct this before source merge; do not infer unique
  inventory from a transient connection state.
- PR696 `fb478917`: required CI green and prior native exact-head review clean;
  later advisory identified startup expiry and final receipt context defects.
  The correction separates structural target validation from request-time
  freshness and gives only final receipt persistence a detached five-second
  context. Execution remains deadline-bound; failures retain the consumed fence.
  Independent review of the correction found no findings. New tests/checks and
  final head receipts belong in the PR and canonical OP record.
  Fresh review after rebase also identified maximum-handoff JSON expansion,
  definite refusal attribution, and a missing exact design contract. The
  correction disables HTML expansion on the wire while preserving canonical
  hashes, retains only the fixed verified pre-mutation refusal without
  releasing its fence, and records the implemented shape in DESIGN-038 3/3b.

PR696 must go to **main after PR690**, with a retarget/rebase and fresh checks
on the resulting head. Never merge it into PR690's dependency branch. Discovery
and the older contract remain separate source changes. Prior broad local
agentd failures are retained in the original PRs; no baseline equivalence or
new full-suite pass is asserted.

The default-off route is `POST /v1/sessions/{name}/codex-thread`, enabled only
by `--enable-codex-rc-thread` plus `--codex-rc-target-file`, the existing API
and an accepted catalog binding. It uses fixed
`agentd ctl codex-thread preflight|start` against the existing native daemon.
No live target/root/process/supported-client-pairing tuple is qualified here.

**Product limit:** the implementation permanently retains one launch fence per
AgentSession UID. It does not meet repeatable fresh conversations on a stable
paired host. The smallest follow-up is to retain the existing CAS annotation
and local fsynced fence, permit a successor request only after the preceding
request is durably confirmed in both stores, and bind that successor to the
preceding receipt digest. Identical requests only read receipts; any reserved,
unconfirmed, mismatched or uncertain state blocks succession. Keep bounded
history without silently evicting unresolved intents. Test two sequential
confirmed launches, competing successors, stale requests, lost ACKs and crash
boundaries. This is a proposed source increment, not an implemented or
authorized runtime workaround; recreating a host to reset the fence is unsuitable.

## The exact offline target

Read-only Kubernetes metadata confirms:

| Object | Identity and state |
| --- | --- |
| AgentSession `dev-agents/dev-env-1019-205125` | UID `e9a76314-3064-4dce-b4a6-5bb783105bf3`; remote Codex; spec Running; phase Failed; no deletion, workspace, canary or private-history binding |
| Retained Pod | UID `c77255fd-99c4-4c7c-8420-74c43bac0417`; Failed/Evicted; `/tmp` EmptyDir exceeded 8Gi |
| Container evidence | Exit 137, `ContainerStatusUnknown`, no start/finish timestamps; this is not a stronger terminal receipt |
| HOME claim | `home-dev-env-1019-205125`, UID `03912a1c-df4d-4b72-811f-2c34dd3abe25`; Bound; 40Gi; gasha01-rbd |
| Bound PV | `pvc-03912a1c-df4d-4b72-811f-2c34dd3abe25`, UID `b9455c08-72e0-43d2-9a88-04c389c27a74`; Bound, reclaimPolicy Delete, no deletion; recheck before operation, no retention change authorized |
| Installed agent | 2.45.0, commit `86cef5bb619bbde29610e3436299a3a4362b7a88` |
| Installed operator | image `sha-3e3e93d`, digest `8cf8a2ba541033280563cb483aa891f49e990358d2a9f347648140f827afdd10` |
| Current template agent | 2.45.0, digest `9578576cfef3dcf87f28189508ad28154deb109c8dbeee6bee308e1406d496eb`; archiveAfter 168h |

Node talosw02 reports Ready and no pressure. No VolumeAttachment for the HOME
PV was listed at this observation. Neither fact proves filesystem integrity,
provider enrollment, pairing, a stopped executor, or available capacity.

The still-active `dev-env-1027-010042` is a different Session, UID
`e454b212-fa54-4e67-b0de-cccc4d7f4959`, Running/Ready on talosw02. Its
`pair-code-issued` status is not phone-pairing evidence. Do not restart or
re-pair it as part of this recovery.

## Supported recovery candidate and approval boundary

The installed ordinary remote supervisor is **not** the guarded `codex-host`
described in [the coordinator-host guide](../../../../docs/codex-coordinator-hosts.md).
At installed agent commit `86cef5bb`, `internal/agentd/codex_remote.go:19-33`
explicitly has no one-start receipt and retains enrollment under the session
HOME across resume. Do not invent a reset of `codex-host-start-intent.json` or
apply that different host's reset prohibition to this supervisor.

Installed operator `3e3e93d` supports the following candidate through existing
API/CLI operations. Independent native source QA checked its lifecycle,
controller and HOME recreation path. **No step below has been executed.**

1. After explicit approval, re-read exact Session/Pod/PVC/PV UIDs, ownership,
   deletion/archive state and effective template image. Refuse changes, a
   missing HOME, a new private-history/workspace binding or uncertain state.
   Coordinate a short exclusive lifecycle window for this target; no retention,
   templates, image, credentials, grants or other sessions change in the window.
2. Through an existing authorized lifecycle caller, invoke exactly
   `agent-run suspend dev-env-1019-205125`. Plain resume now would be a 200
   no-op because spec is already Running (`internal/apiserver/lifecycle.go`).
   The normal controller guard handles the old Pod; do not delete it directly.
3. Wait for authoritative Suspended and the old Pod's disappearance; re-read
   the identical non-deleting Bound HOME/PV. The legacy guard treats PodFailed
   as ended and skips rescue. Record that precise guard, not a fabricated
   clean shutdown, current rescue or private executor-terminal proof. The
   October20 rescue is superseded and belongs to another Pod UID.
4. Recheck all identities and the approved template, then invoke exactly
   `agent-run resume dev-env-1019-205125`. Observe a new Pod UID on the same
   AgentSession UID and original HOME/PV. Do not shorten archiveAfter, create
   a new host, copy enrollment, clear native files, mount a rescue pod or pair.
5. Read normal authenticated lifecycle/RC status and ask for supported-client
   confirmation that the **previously paired identity** returns online with its
   history. Do not guess pairing IDs. Native connected or code-issued status
   alone cannot satisfy this step. If identity/pairing is missing or unknown,
   stop and preserve the result; no re-pair or repeated lifecycle cycle.

Approval must explicitly cover **one suspend/resume lifecycle cycle with the
existing supervisor's automatic retry/backoff behavior**. It is not one native
start: the supervisor can retry indefinitely with capped five-minute backoff.
An approval limited to one native-start attempt requires a source change first.
No host install, daemon restart, old failed-session restart or live RPC is
authorized by the present investigation order.

Risks: recreation uses current templates; ephemeral `/tmp` contents are lost;
existing project bootstrap may remain Partial; backoff can continue native
starts; filesystem/enrollment integrity and old client pairing remain unknown.
Most significantly, the legacy controller recreates a missing HOME claim and
mounts by name. Pre/post checks do not atomically enforce its original UID.
If fail-closed original-HOME enforcement is required before approval, the
smallest source increment is a default-off, exact Session-UID/HOME-UID guard
at both API resume and controller recreation using uncached reads. It must
refuse absent, replaced, foreign or deleting HOME and never create a replacement
claim, release finalizers or manufacture terminal evidence. Test disappearance
between acceptance and recreation as well as unchanged-HOME success. This
targeted guard needs no reset framework, roles or new platform.

**Q-252-01, pending:** choose the original-HOME-UID source guard first
(recommended), explicitly approve one existing lifecycle cycle with the stated
limits, or keep paused. The native question tool accepted emission; no phone
delivery or owner answer is established by that receipt. Runtime stays held.

## Legacy screenshot provenance and the exact blocked read

The screenshot prefix `dev-env-574bdc9844-jh...` has no matching current
AgentSession, Pod, ReplicaSet or PVC. Current v1 `dev/dev-env` Deployment UID
`6959868c-24ba-476a-9a6c-5ab72e6545f9` dates to July13 and mounts the Bound
`dev-env-home` PVC UID `9ce371f5-1c22-42ac-8782-e4f9acf0163c` at `/home/dev`.
Its current Pod is `dev-env-66f455c8bf-rcbxm`. Public GitOps `agent-run.sh`
documents that the display name comes from first enrollment and persists
across Pod rolls, with private state under HOME/.codex. Thus an old Pod-shaped
display name is plausible on the current v1 HOME; this does **not** prove the
screenshot's exact enrollment or lifecycle and does not prove retirement.

A fixed read-only v1 supervisor/socket/process-count check was rejected before
execution by `vpol.validate.kyverno.svc-fail-finegrained-dev-env-exec-guard`:
`dev-env-exec-guard: a dev-env agent may not exec into
dev-env-66f455c8bf-rcbxm, which runs as the privileged ServiceAccount
dev/dev-env (haynes-ops #3392, DESIGN-001 D-19)`.

That read is stopped. No alternate identity or Mac route is used around the
denial. No private app DB/UI, enrollment secrets or credential contents were
read. Preserve the entry and v1 HOME; deletion, unpairing, restarting and
re-enrollment are excluded. A supported nonsecret client-side identity receipt
that already exists could settle provenance later; none is claimed here.

Source, tested, merged, published, installed, online and accepted are distinct.
This packet records source qualification and an unexecuted recovery proposal.
Old paired hosts are not repaired or accepted by publishing a fresh-thread API.
