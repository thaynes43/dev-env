# Handoff — 2026-10-18 MVP source pass

This page is the pickup for the Cursor work that landed on `main` from
2026-10-17 through 2026-10-18. The front door is still
[HANDOFF.md](HANDOFF.md). That page's 2026-10-17 installed pins, rendezvous
rules, and held owner gates stay in force. This page does not replace them.

`main` before this handoff is `203413614f93c655abc9a00835e2f36f29e64990`
([#415](https://github.com/thaynes43/dev-env/pull/415)). Nothing in the
373–415 chain is in a release, and nothing in it is pinned in haynes-ops.

v1 stays up. Do not restart pod `dev-env-68b4b9f6cf-lt6j7`. Do not open a
live rendezvous cohort. A dev-env agent test uses `gpt-6-luna` or
`claude-haiku-4-5`.

## Installed pins, checked 2026-10-18

Checked on `thaynes43/haynes-ops` `main`, not by rolling a pod.

- Operator image `ghcr.io/thaynes43/dev-env-operator:sha-f315635@sha256:60f66558bd178543c64ff4b5554fbd204fd9c023f7f9d8c57b1b9016b7a4099a`
  from release `f315635ad6fc30129da4e0310295634e57b78422` (v2.29.4,
  [haynes-ops #3992](https://github.com/thaynes43/haynes-ops/pull/3992),
  merge `08cfff93f0ab3b286673df4e0116fe8c30819daf`).
- Task image `ghcr.io/thaynes43/dev-env:2.29.5@sha256:1887be46224bfacd4a6e9c20e443c03f3b2d1516ca0f9305b939011f6a484c61`
  from release `56f1b8c127eae4864b241d729afae71cd7e23f2d`.
- The operator args set `--enable-private-project-tasks=false`. The Helm
  comment says general private admission remains disabled. Executor stop,
  HOME archive, history reads, and HOME deletion stay at their H7/H8 hold.
- The installed canaries are the consumed pair
  `rule-canary-claude-sigo-1012b` and `rule-canary-codex-1011`. There is no
  new #173 canary.

[Release pull request #369](https://github.com/thaynes43/dev-env/pull/369)
is still open. Its changelog lists only
[#370](https://github.com/thaynes43/dev-env/pull/370). Release Please last
ran after #415 and left the pull request unchanged, because the later squash
subjects are not conventional commits. Merging #369 would tag the whole tree
as v2.29.6 and publish an agent image. It was left open.

## This Cursor session

This is the coordinator that started on 2026-10-17 around 09:33 EDT with a
stand-by, then took the v2 MVP adoption goal after 13:44 EDT and kept merging
through 2026-10-18. The workers were child agents of that chat. The table is
`gh pr list --state merged` on 2026-10-18, merge commit SHAs included. #401
is absent because it was closed, not merged.

The chain is source and offline proof. It does not admit a pilot, pin an
image, or accept Chapter 7.

### Merged pull requests

| PR | Merge SHA | Title |
| --- | --- | --- |
| [#415](https://github.com/thaynes43/dev-env/pull/415) | `203413614f93c655abc9a00835e2f36f29e64990` | Record the worst-case coordination archive get memory |
| [#414](https://github.com/thaynes43/dev-env/pull/414) | `39033e5d8227993858a0c277372c6b595e968f51` | Leave a claim in place when retention has no delete right |
| [#413](https://github.com/thaynes43/dev-env/pull/413) | `5eaed461441fa774fb066b865058c942d3e2758a` | Refuse agent exec with the identity guard |
| [#412](https://github.com/thaynes43/dev-env/pull/412) | `f45101eaeddc744158ba1bce54f2c39c1d5423ba` | Archive and delete a worst-case publication |
| [#411](https://github.com/thaynes43/dev-env/pull/411) | `56ff54809732f8d89e85c9f167231d9189471b33` | Delete a replaced owner's claim after the grace |
| [#410](https://github.com/thaynes43/dev-env/pull/410) | `2bfad7dc2eaa167dd6b6330ab74fa329a8ebc480` | Delete an acknowledgement on the pass after its delete conflicts |
| [#409](https://github.com/thaynes43/dev-env/pull/409) | `8f75442e963eddf4dbc60d506c033c351ddbbbdb` | Stop a retention pass when the archive is not a volume |
| [#408](https://github.com/thaynes43/dev-env/pull/408) | `794024816739d8fdb31a12a4cdc32c307640f6a0` | Leave a claim unmarked when no shelf pod is ready |
| [#407](https://github.com/thaynes43/dev-env/pull/407) | `b9eef894872e5b840bd636aaf804650cc6d0a8c8` | Retry a claim after a stale retention delete conflicts |
| [#406](https://github.com/thaynes43/dev-env/pull/406) | `be1244bda73de1c9abc4b07bf1d76b4f23b95ded` | Leave a claim in place when its archived bytes change |
| [#405](https://github.com/thaynes43/dev-env/pull/405) | `66f1f7f4d544f381cb85b95d8d2ed039b2aad2fe` | Reset a missing archive revision and open a new grace |
| [#404](https://github.com/thaynes43/dev-env/pull/404) | `f9a0dec135bed6591ad15bb586d47abef941adeb` | Answer unavailable when an archived read has no volume |
| [#403](https://github.com/thaynes43/dev-env/pull/403) | `6b31f83e7993586215d92bb91de8ab57fc4ff1ed` | Read archived context, message, and acknowledgement |
| [#402](https://github.com/thaynes43/dev-env/pull/402) | `6229aeb7ba0a09688dc308fa70cff59c8f2469ae` | Run the shelf archive commands through agentd |
| [#400](https://github.com/thaynes43/dev-env/pull/400) | `2480f9246f248f2f1e704cbfb54edc5eced4e024` | Read a removed task claim from the real archive |
| [#399](https://github.com/thaynes43/dev-env/pull/399) | `a515ba668f551b6fc00abb618a2a4f0fe712e133` | Archive an inbox acknowledgement with its message |
| [#398](https://github.com/thaynes43/dev-env/pull/398) | `4b13930f41fa9f45883129eb9fea3aa623063114` | Archive an inbox message before deleting it |
| [#397](https://github.com/thaynes43/dev-env/pull/397) | `1a2c4e380d07190046189f920a42ae392d1a94f3` | Archive a published context before deleting it |
| [#396](https://github.com/thaynes43/dev-env/pull/396) | `8302ba50d9beac3624c8dd172c342469ff78794b` | Cross the coord-archive JSON boundary |
| [#395](https://github.com/thaynes43/dev-env/pull/395) | `af31371e152015f8286f0907c4597a70ac3295d4` | Prove two retention passes delete a claim once |
| [#394](https://github.com/thaynes43/dev-env/pull/394) | `5bc25c0bdbf564e0964e2f21fe1c60aa5f5ca2da` | Match the archived claim to the live object |
| [#393](https://github.com/thaynes43/dev-env/pull/393) | `cfea5d1e138bf7af81594222d603adca8bf93320` | Archive one claim before retention deletes it |
| [#392](https://github.com/thaynes43/dev-env/pull/392) | `0950f53fd230647cd83c19b468b3b25c9f9811e9` | Leave an oversize record unmarked |
| [#391](https://github.com/thaynes43/dev-env/pull/391) | `a0707e336ed9ba26a0dd18a4a20ed806fada98f6` | Leave an unavailable shelf unmarked |
| [#390](https://github.com/thaynes43/dev-env/pull/390) | `898368ebad18a0fd16a4dced21eca00eb4e36381` | Refuse a delete when the archive revision is missing |
| [#389](https://github.com/thaynes43/dev-env/pull/389) | `0f078f01ce953312480695812aabfa8637d5b0ba` | Refuse a delete when archived bytes change |
| [#388](https://github.com/thaynes43/dev-env/pull/388) | `986efe059a7feaf4ebf7b61edf655d305e462111` | Prove the retention delete right |
| [#387](https://github.com/thaynes43/dev-env/pull/387) | `5a372348042fe5df06a661cfd9f7363361ae9a3d` | Keep a second retention pass from deleting again |
| [#386](https://github.com/thaynes43/dev-env/pull/386) | `dcd72918338d27bede817384f070d361d971dd51` | Release a claim whose owner took a new UID |
| [#385](https://github.com/thaynes43/dev-env/pull/385) | `bd3b56a87e9941757c7b8ded7d40f4e5868298da` | Prove the inbox keep decisions on a real API server |
| [#384](https://github.com/thaynes43/dev-env/pull/384) | `84ea141178a9ab2f5d93449224121b18bc4edb30` | Release an inbox acknowledgement on the next pass |
| [#383](https://github.com/thaynes43/dev-env/pull/383) | `dc33e66adf674abdb6a75b68970470302c95d16d` | Delete a released inbox message before its acknowledgement |
| [#382](https://github.com/thaynes43/dev-env/pull/382) | `1c91acf67cbed1d2adc3b5d7ff4d51cdd7ac9043` | Prove a released claim frees only the project bound |
| [#381](https://github.com/thaynes43/dev-env/pull/381) | `a7323fc0dc8ceb4110d5dc6bb31d2bcac4a402d1` | Wait for a grant request answer |
| [#380](https://github.com/thaynes43/dev-env/pull/380) | `a9fcc7790c4afd52706cfddc8309ff18626d75f9` | Add agent-run breakglass |
| [#379](https://github.com/thaynes43/dev-env/pull/379) | `b214e2070ebbcf8c7cea45bee73f78b59a797eb4` | Add grant use and the in-pod access tools |
| [#378](https://github.com/thaynes43/dev-env/pull/378) | `55eca76d28cd735c028ebc340e343c524a291dfe` | Add agent-run grant list, show, request and release |
| [#377](https://github.com/thaynes43/dev-env/pull/377) | `c5cbc8f3b1e4da62f79546dbb982f4760afd3242` | Refuse pilot prompts that repeat the r10 and r11 command failures |
| [#376](https://github.com/thaynes43/dev-env/pull/376) | `be6581fc1d00fcb4ce5d789556091160bf4ce14c` | Add the default-off coordination retention runnable |
| [#375](https://github.com/thaynes43/dev-env/pull/375) | `9cecee76019ab18a2037bd994d60e9cb73044498` | Add idle drain for outdated sessions |
| [#374](https://github.com/thaynes43/dev-env/pull/374) | `38e65e894ac8eee49f7ab0ec8470efc4e2e8f133` | Prove broker egress revoke rechecks a replaced policy |
| [#373](https://github.com/thaynes43/dev-env/pull/373) | `6f332cbc0e04e247f23f5dc2ce3d3a02e05545d8` | Specify never-started reap release |

[#401](https://github.com/thaynes43/dev-env/pull/401) was closed on
2026-10-18 as a duplicate of #402. The close comment names merge
`6229aeb7ba0a09688dc308fa70cff59c8f2469ae`. Claude's review on #401 had one
medium documentation note. The pull request was closed rather than updated.

### What that chain is

- **Plan 04 idle drain, source only (#375).** The controller can drain an
  outdated session after the launch record is stored and the agent has
  stopped. Plan 04's Codex hosts, image pre-pull, and client acceptance are
  still backlog. The installed operator is still v2.29.4, so this drain is
  not running in the cluster.
- **Coordination retention, default off (#376, then #382–#415).**
  `--enable-coordination-retention` defaults to false and is refused unless
  archive reads are on. The later pull requests are envtest and offline
  proofs: archive before delete for a claim, context, message, and
  acknowledgement; mismatch, missing revision, oversize, and unavailable
  shelf leave the live object unmarked; a second pass does not delete twice;
  a replaced owner and a stale delete are handled; reads of removed objects
  go through the real archive command; a worst-case publication is archived
  and deleted in test; #415 records one-get memory. DESIGN-024 R5 still has
  concurrent gets and a prune to judge. Retention is not turned on in
  haynes-ops.
- **Grant CLI, source only (#378–#381).** `agent-run` can list, show,
  request, release, and use a grant, and can send a break-glass ask. #381
  fails a request that is refused or still pending. This is not a proof
  against a live grant.
- **Guarded access, offline (#374, #413).** #374 proves the broker's egress
  revoke rechecks a replaced policy. #413 proves an identity-guard refusal
  of an agent exec on a real API server in envtest. Neither is a live grant.
- **Pilot command contract (#377).** The harness refuses the r10 bare
  inbox name and the r11 `task-claim-create stdin` shape. No cohort was
  opened.
- **Never-started reap (#373).** A never-started reap releases only when
  its Pod and HOME are absent.

A local untracked `scripts/trials/pilotdigest/main.go` (`//go:build ignore`)
was in the coordinator checkout. It was not committed and it is not on
`main`.

## The other Grok sessions

Two Cursor threads are distinct from this coordinator. The Friday-night thread
was opened to take over after Tom told the Friday-afternoon thread to stand
down at pull request #360. Both are Grok chats from the transcripts. The split
below is what those chats claim, checked against `gh pr list --state merged`
and the haynes-ops pull requests named here. Where a chat and the git history
do not line up, the git history wins.

### Friday 2026-10-16, stood down at #360

Tom asked this thread, starting 16:27 EDT, to get the repo up to date and to
burn through the Cursor handoff with subagents. It merged the heartbeat gate
[#358](https://github.com/thaynes43/dev-env/pull/358) as
`a7c8e6096433d5464aa20ddb50b4ad45e2b091c6`. It opened and closed two
rendezvous windows in haynes-ops: r2 seal
[#3984](https://github.com/thaynes43/haynes-ops/pull/3984)
(`4bf46e127e6bfe5be54014528fac442a45ca7019`) and admission
[#3985](https://github.com/thaynes43/haynes-ops/pull/3985)
(`b607e1b0f486e95f7195147b49c0a85f8f5c279e`); r3 seal
[#3988](https://github.com/thaynes43/haynes-ops/pull/3988)
(`69c085fa797bc9cdfd5fb3503d6efa07374f45a5`) and admission
[#3989](https://github.com/thaynes43/haynes-ops/pull/3989)
(`46c2b455ccf422bf1cc3686debd06c5458c114e2`); then closed admission in
[#3990](https://github.com/thaynes43/haynes-ops/pull/3990)
(`906f23d123ae93046c008112b3a29d67702f95c0`). r2 expired before a Session
existed. r3 created only side A, which exited 125 before a claim. The thread
opened [#360](https://github.com/thaynes43/dev-env/pull/360) to switch the
sealed pilot to `gpt-6-luna` and stood down while CI was still running. Its
last note says #360 was open and unmerged.

### Friday night 2026-10-16 through Saturday morning

Tom started this thread at 21:03 EDT to take over from the thread above. It
merged #360 as `e4350bc5eb7bf474a2e6673441fe33675532be69`, then release
[#359](https://github.com/thaynes43/dev-env/pull/359) as
`f315635ad6fc30129da4e0310295634e57b78422` (v2.29.4), and pinned it with
haynes-ops #3992. It recorded that pin in
[#361](https://github.com/thaynes43/dev-env/pull/361)
(`110213c2878e49924902092ef98a3ab9584dd381`).

It then fixed the r3 exit 125 path and the harness, and released v2.29.5:

| PR | Merge SHA | Title |
| --- | --- | --- |
| [#370](https://github.com/thaynes43/dev-env/pull/370) | `60c4e38c507c21d5fce64c51d0decc4b56362996` | fix(trials): retain pilot log capture after executor stop |
| [#368](https://github.com/thaynes43/dev-env/pull/368) | `56f1b8c127eae4864b241d729afae71cd7e23f2d` | chore(main): release 2.29.5 |
| [#367](https://github.com/thaynes43/dev-env/pull/367) | `5d44d12a55e37211f29d094370efc03911ba5e29` | Expose configured model and effort in agentd status |
| [#366](https://github.com/thaynes43/dev-env/pull/366) | `b8a0873343d3e7d88666bba6d2133ee222522f48` | Accept direct task-claim stdout in pilot rendezvous gate |
| [#362](https://github.com/thaynes43/dev-env/pull/362) | `e2be49403871d742c82cf6404e2e139b9728e6d2` | Fix pilot rendezvous claim receipt exit 125 |
| [#361](https://github.com/thaynes43/dev-env/pull/361) | `110213c2878e49924902092ef98a3ab9584dd381` | docs: refresh HANDOFF for v2.29.4 pilot state |
| [#360](https://github.com/thaynes43/dev-env/pull/360) | `e4350bc5eb7bf474a2e6673441fe33675532be69` | fix(pilot): run sealed coordination tests on gpt-6-luna |
| [#359](https://github.com/thaynes43/dev-env/pull/359) | `f315635ad6fc30129da4e0310295634e57b78422` | chore(main): release 2.29.4 |

The same thread drove cohorts r4 through r13. The current
[HANDOFF.md](HANDOFF.md) cohort history is that record: r10 is the furthest
run (both sides claimed, B sent one inbox message, A's `inbox-read` used a
bare name and exited 2); r11 died on `task-claim-create stdin`; r12 returned
`claim_stale`; r13 was admitted by
[haynes-ops #4031](https://github.com/thaynes43/haynes-ops/pull/4031)
(merge `97e897faae32fb8e3bf72148535b8156d9e1aa74`) and never started. One
checked close from that night is haynes-ops
[#4018](https://github.com/thaynes43/haynes-ops/pull/4018)
(`24a947f6c76c7d54574f8e65e099ab9d04a6e767`), the r8 admission close. Do not
reuse those seals or windows.

[#371](https://github.com/thaynes43/dev-env/pull/371)
(`0d3e9247ac7b02cb4f31fae530d941caa3cd3fc3`) and
[#372](https://github.com/thaynes43/dev-env/pull/372)
(`25f2fc0349bcb51b8dd6aa161dc4e5dfdb8a973b`) merged at 13:40Z and 13:48Z on
2026-10-17. That is 09:40 and 09:48 EDT, while this coordinator had only been
told to stand by and before its MVP goal. A handoff worker in the Friday-night
thread was updating `.agents/HANDOFF.md` at 09:25 EDT. Those two doc merges
are attributed to that thread. The overlap is a few minutes, so the
attribution is the best fit, not a commit trailer.

Renovate [#363](https://github.com/thaynes43/dev-env/pull/363) also merged in
that window. It is a dependency bump, not either session's product work.

## Still open

Checked against [HANDOFF.md](HANDOFF.md), the 2026-10-13 pickup brief, and
`main` at `203413614f93c655abc9a00835e2f36f29e64990`. The 373–415 chain does
not close any of these.

- **Fresh rendezvous seal, window, and bound ConfigMap UID.** Chapter 7 is
  not accepted. Done still means A and B both claim, an inbox ACK exists, A
  publishes context, and B reads it after A is terminal. The r13 window in
  the 2026-10-17 handoff is expired. The next attempt needs a fresh seal, a
  fresh window, and a bound live ConfigMap UID. Do not start one from this
  page.
- **H7 reader held under Q-36.** The 2026-10-17 handoff still holds the
  reader, and Q-37's PV Retain step stays consumed. No later ruling in this
  pass reopened either. Cold measurements and the shelf mount were not run.
- **H8 session choice.** H8 is the installed record of an archived history
  read (#174). It needs a reaped, non-canary private Session. Global private
  admission is off and canaries do not expire, so Tom still chooses the
  session source. Deletion stays off until that record exists. Ask when H7's
  flags are on, not before.
- **Q-24 Codex native routing.**
  [DESIGN-006](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing)
  still says the 2026-10-11 question has no owner ruling. Phone delivery of
  that prompt was never verified. Do not ask it again. Build nothing that
  assumes an answer.
- **A new #173 canary.** Issue
  [#173](https://github.com/thaynes43/dev-env/issues/173) is open. The
  2026-10-13 handoff accepts the Sigo rerun for private-repo evidence and
  leaves these items open: two bounded pods compared for independent Git
  directories and worktrees, missing-repo bootstrap in an existing HOME, a
  failed fetch blocking new work, and preserved dirty, WIP, and reference
  state. The installed canaries are still the consumed 1012b and 1011
  records. A new canary is an owner gate.
- **Private admission off, ownership inspector unwired.** Live operator args
  set `--enable-private-project-tasks=false`. The source default of that flag
  is false. The 2026-10-12 handoff and the private-budget checkpoint still
  say the ownership inspector is unwired and always refuses reservation. No
  pull request in this pass wires it.
- **Plan 03 is not built.**
  [Plan 03](sagas/distributed-dev-env/backlog/03-remote-control.md) is still
  backlog: keeper-owned Max login, fenced refresh, and Remote Control core.
  An API status field named `remoteControl` is not that plan.
- **Truthful cost is not accepted against a real provider.** The #176 source
  unit is on main. The handoff still says signed promotion and
  actual-provider acceptance remain gates. This pass did not add a provider
  run.
- **Guarded access is not proved against a live grant.** #374 and #413 are
  offline. The grant CLI is source. Minting and the history flags stay off
  in the installed operator comment. Plan 07's real-provider acceptance is
  still open in the handoff.
- **v1 is not retired.** Pod `dev-env-68b4b9f6cf-lt6j7` was not restarted.
  Plan 05 cutover was not started.

Also still open, found while checking the tree:

- The 373–415 source is not released and not installed. #369 must not be
  merged as the changelog stands.
- Plan 04 beyond the idle-drain source: Codex hosts, pre-pull, and acceptance.
- DESIGN-024 retention R5's concurrent gets and prune, then any haynes-ops
  enablement. The runnable stays default off.
- #154 budgets and #160 recovery, past the unwired inspector.
- #175 consumer acceptance of claims and inbox with real consumers. The
  archive proofs are envtest.
- #174's installed archived-history read, which is H8.
- Codex phone delivery of a native question is still unverified. That remains
  a plan 11 acceptance requirement.
- Drafts left open on purpose:
  [#292](https://github.com/thaynes43/dev-env/pull/292) (ungroomed multi-client
  viewer; the pull request says not to merge without owner grooming) and
  [#231](https://github.com/thaynes43/dev-env/pull/231) (maintenance-aware
  escalation proposal; draft for scope review, owner decisions pending).
- Open Renovate pull requests were not merged. They are bot dependency
  updates, and several are red: #365 (lint failing), #344 (`check-generated`
  failing), #252 (git-wait image failing). The green ones left open are
  #364, #343, #295, #293, #284, #280, #278, #277, #255, and #100.

## Codex review prompt

Paste this to Codex. The merge SHA below is the squash merge of this handoff.
If it still says `pending-squash-merge`, the recording commit has not landed.

---

Adversarially review the repository thaynes43/dev-env. The handoff is
`.agents/HANDOFF-2026-10-18-mvp-pass.md` on main. The handoff merge SHA is
`pending-squash-merge`.

That handoff is the source of what was done and what is still open. Verify
every claim against the code, `git log`, and the open pull requests. Where
the summary and the tree disagree, the tree wins.

Advise the next steps. Keep the owner gates in the handoff. Do not retire
v1. Do not open a live rendezvous cohort. Do not restart pod
`dev-env-68b4b9f6cf-lt6j7`. Dev-env agent tests, if any, use only
`gpt-6-luna` or `claude-haiku-4-5`.

---
