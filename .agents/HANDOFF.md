# HANDOFF: start here

## Current state — 2026-10-20 UTC (v2 release 2.38.1)

This block replaces the 2.37.0 block below, which stays as history (its rulings
Q-110-03 to Q-110-08 still stand). It records batch NEXT-BATCH-1020 (Saga85 /
Epic107, after Story110), run overnight from 03:27 UTC by coordinator
task-dev-env-1019-232515. The dated record is that batch's progress
file and per-story result files, kept by the coordinator outside git. The
user-facing result is the [v2 quick start](../docs/v2-quickstart.md).

Proof levels: **proven on the phone** (Tom, end to end); **proven from a
session** (an agent in a v2 workbench session or the v1 coordinator, live, not
Tom's phone); **installed, not exercised**; **not built** (no code, or off).
Installed is not accepted.

**Installed, operator and keeper:** release 2.38.1 (dev-env #484, tag `v2.38.1`,
commit `fdf2542`). haynes-ops #4133 (`73e08fd3`, about 06:53Z) pins operator
`sha-fdf2542` (`sha256:c1680f80…`), the keeper's `app` `sha-fdf2542`,
`claude-login` `2.38.1` (`sha256:8db9ce94…`) and `codex-login` `sha-fdf2542`
(`sha256:eea8b0ab…`). Both publish runs (39382009894, 39382031869) passed
`cosign verify`. No CRD change. haynes-ops #4132 (the exec guard allows
`agentd claude-login-abandon`) merged before the pin. The templates and the
task-budget worker image deliberately stay 2.38.0, so no session drained
overnight; the next release moves them. 2.38.1 fixes the three defects found on
2.38.0 (below): #480 standby duplicate, #481 Proxmox mint, #483 Claude cancel.

**Installed, sessions:** release 2.38.0 (dev-env #476, tag `v2.38.0`, commit `702a57f`).
haynes-ops #4131 (`5ddbea0a`, about 05:31Z) pins, together: operator
`sha-702a57f` (`sha256:981075c0…`); agent, task-budget worker and templates image
`2.38.0` (`sha256:bb67ea9c…`); the keeper's `app` and `claude-login` at 2.38.0;
`codex-login` `sha-702a57f` (`sha256:3b622b24…`); 2.38.1 replaced the operator
and keeper images above. Both publish runs passed `cosign verify`. No CRD change.
The shelf stays on 2.9.1 and the broker on `sha-eeb15e3`.

2.38.0 adds: keeper login from a workbench session (#475); workbench sessions
on the coordination routes (#472); the Claude 401 "continue" matching the real
CLI error (#473); the keeper's SSH offering only the pinned host-key algorithms
(#474); a stalled clone abandoned and retried (#478).

haynes-ops changes installed tonight besides the pin: #4122 (five more catalog
projects), #4123 and #4129 (workbench restarts of the operator, keeper, broker
and shelf, guarded to the restart annotation), #4127 folded into #4131
(workbench cluster read without Secrets, Flux read, Omni, GCP, MCP credentials,
the project catalog read), #4128 (operator `--enable-private-executor-stop`),
#4130 (the operator's exec into the keeper's login helpers only, pinned by
`dev-env-operator-exec-guard`).

At the install, MANGO (`dev-env-1019-190936`) and the standby
(`haynes-ops-1020-002115`) moved to the new template revision at 05:31:38Z by
the designed drain and are Running. T08 (`dev-env-1019-141220`) and the Codex
host PEAR (`dev-env-1019-205125`, still 2.37.0) were not touched.

**Proven tonight:**

| Item | Proof level | Evidence |
|---|---|---|
| Restart the keeper, shelf and operator with `kubectl rollout restart` | Proven from a session (workbench) | NB-02, 03:45 to 04:01Z; every session pod kept its name, start time and restart count; the logins' generation and expiry unchanged. Broker: dry run only |
| The workbench restart Roles allow and refuse what they should | Proven from a session (workbench) | NB-02 part A: `can-i` matrix; the guard refused a label, another annotation, an image and a replica change |
| Codex host moved to a new image by suspend and resume | Proven from a session (v1 coordinator) | NB-03: PEAR 2.35.0 to 2.37.0, same `installation_id` and enrolment, daemon reconnected, thread history kept |
| A damaged access token comes back from the keeper | Proven from a session | NB-03: Claude and Codex, about 4 s, next turn worked |
| Codex and Claude tasks on a repo that was outside the catalog | Proven from a session | NB-08: both tasks on `libretto` Succeeded |
| Reap, rescue and restore; suspend and resume (`--local` sessions) | Proven from a session | NB-06: restored ref `refs/rescued/heads/rescue/<session>-<stamp>` held the same files by checksum; a code word recalled after resume |
| A reaped task's pod stops | Proven from a session | #4128 live: seven reaped private tasks' pods went Failed (DeadlineExceeded); pods, volumes and Sessions kept. Two more on 2.38.0 (`dev-env-1020-054000`, `-054012`), seen with `kubectl get pods` |
| Inbox, task claims, published contexts; `agent-run msg` | Proven from a session (default-profile sessions) | NB-05: send, read, ack, idempotent retry, claim conflict naming the owner, context publish and read; `msg` answered by a live session, 409 for an exited task |
| Release 2.38.0 published, signed and installed | Verified in CI and the cluster | REL-238: publish runs 39374328506 and 39374348374, `cosign verify` passed; #4131 merged |
| Release 2.38.1 published, signed and installed for the operator and keeper | Verified in CI and the cluster | REL-2381: publish runs 39382009894 and 39382031869, `cosign verify` passed; #4133 merged; operator rolled, keeper restarted, every preserved session unchanged, one standby |
| Workbench reads on 2.38.0: cluster without Secrets, Flux, Omni, `talosctl`, GCP, all ten MCP servers | Proven from a session (workbench) | LIVE-238b: NB-04 rows 4 to 7, 11, 14 and 15 pass; `can-i` says no to pod delete, Deployment patch and Job create |
| Coordination routes from a workbench session (#472) | Proven from a session (workbench project task) | LIVE-238b: inbox both ways, ack and idempotent retry, sender read 404, claim conflict, context publish and read |
| Project bootstrap Ready, 8 of 8 repos (#478); the standby's `CatalogUnconfirmed` gone | Proven from a session | LIVE-238b: five new sessions and the standby. No clone stalled, so #478's retry was not exercised |
| The Claude 401 "continue" is sent after a failed turn (#473) | Proven from a session (new generation simulated) | LIVE-238b: receipt `sent`, the failed prompt completed. A real keeper refresh triggering it is not yet seen; a revoked token's wording is unobserved |
| Keeper login from a workbench session (#475): `status` for both logins, `url` for both, ChatGPT `url`, `cancel`, `url` again, a v1-pod caller refused (other callers by unit test), logins unchanged after abandoned tries | Proven from a session (workbench) | LIVE-238a. No full ceremony with Tom's code yet |
| Claude keeper-login `cancel` ends the helper (2.38.1, #483) | Proven from a session (workbench) | LIVE-2381, 06:53 to 06:56Z: no helper left after `cancel`, the next `url` not Busy, a no-ceremony `cancel` answers "no keeper login ceremony is open", `status` unchanged (gen 3, same access and login expiry) |
| Proxmox operator token end to end, reads only (2.38.1, #481) | Proven from a session (workbench) | LIVE-2381: credential grant (15 min) Active in 5 s, CredentialJob `Installed`, `credential-available` exit 0; with an egress grant for `pvedash.haynesnetwork:443`, `pve ha` showed `tier=operator` and `pve nodes` five nodes; release revoked the job in 19 s, PVE's token list back to `operator` only, `tier=read`. No write tried |
| No ERROR log after a ChatGPT keeper-login `cancel` (2.38.1, #483) | Proven from a session | LIVE-2381: no "needs attention" line; PendingPublication lasted over 35 s this time (about 25 s before), then Ready, same login |
| Standby counts a draining standby as present (2.38.1, #480) | Installed, not exercised | No template move since the 2.38.1 pin, so no drain has run |
| Claude keeper refresh, generation 3 | Proven from a session (natural refresh observed) | LIVE-238a: refreshed at 05:50:05Z, access to 13:50:05Z, read by sessions. `keeper-login claude status` gives the login lapse as 2026-11-16T17:51:35Z before the refresh and 17:51:36Z after (a 1 s drift) |
| The keeper's certificate SSH to a PVE node (#474) | Proven from a session | LIVE-238a: the node logged the certificate login and the `pvesh create`; the two stuck CredentialJobs from 00:49Z went Revoked |

**Corrected tonight:**

- The Proxmox operator token grant never made a token before 2.38.0. The grant
  reached Active, but the keeper's SSH client was offered each node's ECDSA host
  key while the pin holds only ED25519, so it left before logging in (NB-09).
  NB-04's first reading (the nodes lack CA trust) was wrong: the CA line is in
  `dev-env`'s `authorized_keys` on all five nodes. The 2.37.0 block below says the
  token grant was "exercised"; that meant Active, not a token. With 2.38.0 the
  login worked but the mint failed one step later; 2.38.1 fixes that (below).
- Reap of a task did not stop its pod before #4128 (task sessions are private
  under ADR-008); suspend still does not (ADR-006). The quick start's section 4
  was wrong for tasks and now says so.
- `kubectl get` on the `coordination.dev-env.haynesops.com` records is refused
  for every agent, v1 and the workbench by design (DESIGN-010, 013 and 014: the
  message text is in them). The read path is `agentd` inside a session.

**Defects found tonight on 2.38.0, fixed in 2.38.1:**

- **Claude keeper-login `cancel` did not end the helper** (LIVE-238a D1). The
  next `url` was 409 "held elsewhere" for up to 15 minutes, because the
  operator's exec delivered no hangup. Fixed by #483 with haynes-ops #4132: the
  cancel runs a fixed `agentd claude-login-abandon` exec in the keeper's
  `claude-login` container, which the exec guard allows. Proven from a session
  (table above). A slot held elsewhere (an earlier operator or the v1 route) is
  now freed by `cancel` too, by design; that case was not tried live.
- **The Proxmox token was minted and then deleted** (LIVE-238a D2). PVE's `pvesh
  create` answer carries `expire` and `privsep` as strings; the keeper expected
  numbers, recorded `AmbiguousMint` and deleted the token. Fixed by #481 (both
  shapes accepted; one log line names a refused mint's reason). Proven from a
  session for reads (table above). A Proxmox write with the token is untested.
- **A second standby** (`haynes-ops-1020-053140`) appeared at 05:31:40Z, two
  seconds after the standby drained onto 2.38.0 (LIVE-238b). The coordinator
  reaped the unused duplicate. Fixed by #480 (a draining, pending or suspended
  standby, a stale report or a missing pod counts as present). Installed, not
  exercised: the next template move is its live test, and the standby
  ConfigMap's `creations` count should not change.
- The ERROR line after a ChatGPT `cancel` (LIVE-238a O3) came from the keeper's
  30-second tick seeing the paused refresh during a ceremony. #483 logs it at
  debug level; proven gone live. The PendingPublication after a cancel (about
  25 s once, over 35 s the next time) is expected and harmless.

**Open, not accepted:**

- Tom's morning checks ([plan 13](sagas/distributed-dev-env/backlog/13-phone-browser-management.md#morning-checklist)):
  the standby off home Wi-Fi; PEAR in the ChatGPT app after its image move; a
  keeper login ceremony from v2; a restart from the phone; a message between
  two sessions.
- Only a project task (`agent-run --project <p> -p`) has a project, so only it
  can use the inbox, claims and contexts. An interactive session, Tom's
  included, gets 404 (LIVE-238b). This bears on held decision 5 below.
- A reaped task stays Pending with its volume until the history archive is
  switched on (H7: the shelf needs an H7-capable image, today 2.9.1, a second
  volume and `--history-archive`; then the operator's archive and read flags) and
  the guarded delete after it (H9). Owner gates are in DESIGN-025 section 12.
  `agent-run history` answers 404 until then. Each coordination test leaves two
  such tasks with 20Gi volumes. Left tonight: `dev-env-1020-033058`, `-033158`,
  `-033206`, `-033558`, `-054000`, `-054012` and `libretto-1020-034202`,
  `-034214`, `-035511`.
- Suspended private tasks keep their pods running (ADR-006, plan 11 F3; NB-06
  D3). Five were running at 03:45Z.
- `dev-env-1020-002245` (a reaped Codex host probe that never cloned) retries a
  rescue every 15 minutes and fails `NotProven`. It needs a dev-env fix that
  accepts a provably clone-less volume, or a reviewed cleanup. Recorded as a
  story, not started, because it changes the volume-delete guard.
- The session directory is off (`--enable-session-directory=false`).
- `dev-env-agent-guard` has the same Kyverno `verify-images` annotation gap that
  #4129 fixed in the workbench guard.
- Two `libretto` task volumes keep preserved clone staging from before #478.
- `dev-env-h7-metadata-audit` Kustomization is not Ready (a stalled Failed Job,
  from before tonight).
- `TestInboxArchiveListImmutableAttributionWithoutSenderJoin` fails when `TZ` is
  not UTC (in the pod only; CI runs UTC).

**Held for Tom (one question each, at the moment it is needed):**

1. **Write tier for the workbench (NB-04 G-6).** The workbench now reads the
   cluster but cannot delete pods, reconcile Flux or create Jobs. It is exempt
   from the identity and exec guards, so granting writes needs narrower
   exemptions and must keep exec into the keeper impossible. The alternative is
   to keep the workbench read-only and do cluster writes from a default (`full`
   profile) session.
2. **Suspended private tasks (NB-06 D3).** Whether a stop at the suspend deadline is
   wanted (ADR-006 left it as its own owner question).
3. **The SSH user on gasha01.** `hw-ssh` has no entry because no guest user is
   verified; name one and the alias is one line.
4. **Catalog repos left out (NB-08):** haynes-quest (about 870 MB, would slow
   every session's start), the private sigmaphiomicron-com, demo-console and
   haynes-swarm, and the dormant hass-immich-addon, todos-for-dues, ytdrivarr
   and ytdl-sub-config-manager.
5. **A project-wide inbox and claims view for a human coordinator.** Today a
   session reads only its own inbox; a project-wide view exists only for
   configured coordinator hosts. Giving one to a workbench or v1 coordinator is a
   change to DESIGN-014's scope, not RBAC.
6. **Story 13.1 next** (recommended): the keeper login's lapse date in
   `agent-run auth status`, keeper metrics, a 7-day phone warning and a critical
   NeedsLogin alert. The keeper reports the Claude login lapses on
   2026-11-16T17:51Z, and nothing warns yet.

**In flight at the time of writing:** story NB-11, the two #443 review nits (a
`login url` race in the pod-owned login mode; renewal status). Source only, no
live sessions.

**Next safe step:** Tom's morning checks above, then his pick of the next
story (13.1 recommended). Nothing else is queued beyond NB-11. The next
release moves the templates and task worker to it, so idle sessions drain then
(the first live test of #480). A new story needs a saved record first (see
`~/.claude/CLAUDE.md`, "Define work before pickup"). Preserve every v1,
rendezvous, H7, grant and security hold and every consumed attempt; the older
blocks below are history and do not reopen them.

## State at release 2.37.0 — 2026-10-20 UTC (historical)

*Historical. The 2.38.1 block above supersedes its installed state, its "still
on v1" list and its next step. Its rulings Q-110-03 to Q-110-08 still stand.*

This block replaces the 2026-10-19 Story110 checkpoint and the "next steps" that
older blocks below still carry. The dated record is the Story110 progress file
kept by the coordinator; the user-facing result is the
[v2 quick start](../docs/v2-quickstart.md). "Proven" means done end to end,
on Tom's phone where a phone is involved. Installed is not the same as accepted.

**Installed:** release 2.37.0 (#468). Operator `sha-9539413`, agent image `2.37.0`,
deployed through haynes-ops pins #4121 (`f79e4cbe`) and #4120 (`84f4adbf`, standby).
The keeper holds one Claude Max login and one ChatGPT login (sections below).

**Proven end to end on v2, 2026-10-19 to 2026-10-20:**

- **Claude on the phone, on the shared keeper login.** Connect, steer, completed
  response with the served model recorded, suspend and resume as the same phone
  entry, and the earlier answer recalled. No per-pod login; no pod holds a refresh
  token.
- **Codex on the phone, on the shared keeper login.** A Codex host session paired
  once (`agent-run pair`); prompt and answer; suspend and resume without pairing
  again; thread history kept.
- **Headless tasks.** A Claude task and a Codex project task (served model
  reported for Codex since 2.37.0).
- **Operator restart.** Session pods stayed unchanged and the phone kept working.
- **Automatic agent-image moves.** Idle Claude sessions moved onto a new image
  twice (same phone entry, same transcript, history recalled from the phone). This
  needed the AgentSession CRD sync, haynes-ops #4113.
- **Workbench identity.** A session in the `workbench` profile (service account
  `dev-env-workbench`) runs `agent-run pair|attach|detach|login|auth status`
  against v2 sessions and creates Codex hosts, with no v1 pod.
- **Standby.** The operator makes and keeps one Opus 5.5 `haynes-ops` session on
  the phone (default off in source; enabled by haynes-ops #4120). It registered
  Remote Control by itself on the shared login.
- **Standing access grants** (haynes-ops #4117, `308f4ab3`): internal ingress,
  Proxmox API, hw-ssh and a short-lived Proxmox operator token are auto-approved
  by policy for workbench sessions, with no phone step. All four were exercised
  from a fresh workbench session.
- **Keeper Claude refresh.** The keeper refreshed the Claude access token on its
  own at 00:29:48Z (generation 2); sessions stayed registered.

**Rulings Q-110-03 to Q-110-08 (Tom, 2026-10-19; text in the Story110 progress file):**

- Q-110-03: for the Claude v2 MVP, acceptance checks are ordinary repeatable tests; reruns are allowed, and new credentials, RBAC or trust scope still need approval.
- Q-110-04: only task sessions get private-history retention; interactive sessions can suspend again (ADR-008, #453).
- Q-110-05: activate the shared keeper-owned Claude login as bundled.
- Q-110-06: one fresh keeper-owned ChatGPT login for Codex.
- Q-110-07: build the v2 workbench identity.
- Q-110-08: approve the access-grants bundle (standing egress policies, Proxmox grants for workbench, the v1 `hw-ssh` key mounted only in workbench; reverses Q-07).

Q-110-01's provenance caveat applies to Q-110-01 only. Q-110-02 (one isolated
login for the T08 checkpoint) was used and is superseded by the shared login.

**Not yet exercised:**

- The 401-window continue: no turn has needed it.
- Codex keeper refresh: the Codex token is valid to 2026-10-29, so its first
  refresh is not due.
- A Codex host moving to a new agent image by suspend then resume. Suspend and
  resume of a host are proven; using them as the upgrade path is not.
- A Remote Control session past 13 hours, and merge latency after a refresh: not measured.

**Still on v1 (none is a blocker for daily work):**

- The keeper login ceremonies. The Claude login lapses about 30 days after the
  last sign-in, so the next one is due around 2026-11-18. The ChatGPT login is
  renewed only if the keeper reports NeedsLogin. Run them from the v1 pod or
  Tom's admin laptop; a workbench session reaches session pods only.
- Restarting the v2 operator, keeper or shelf. A v2 session is refused.
- Headless Codex tasks on a repo outside the catalog (the catalog is `dev-env`
  and `sigo-alumni`), such as haynes-ops.
- Retiring v1. That is Tom's call, after he accepts the replacements.

**Backlog (none blocking):** the console page and its login-renewal page; archive
on reap through the keeper; messaging tier 2 (all [plan 03](sagas/distributed-dev-env/backlog/03-remote-control.md));
the Codex host image-move exercise above; the two #443 follow-ups (a `login url`
call within one poll of a finished ceremony; renewal status).

**Next safe step:** none is queued. A new story needs a saved record first (see
`~/.claude/CLAUDE.md`, "Define work before pickup"). Preserve every actual
v1/rendezvous/H7/grant/security hold and consumed attempt; the older blocks
below are history and do not reopen them. Same canonical Saga85/Epic107/Story110
and sole owner.

**Historical, 2026-10-18 implementation update:** Q-24 is answered: Tom approved small
separate native hosts; see [DESIGN-006](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing).
The campaign CLI source increment is documented in
[DESIGN-003](sagas/distributed-dev-env/designs/003-task-budget-api.md#campaign-cli-entry-point--2026-10-18-source-increment).
It adds assigned campaign create/show, without executor admission. Private launch,
native readiness and installed acceptance remain open. Release #369 and all
runtime/access holds below remain in force. Older Q-24 pending entries are historical.

**Historical (2026-10-18):** [MVP source-pass handoff](HANDOFF-2026-10-18-mvp-pass.md).
It records what landed on `main` from 2026-10-17 through 2026-10-18, what the
other Grok sessions did, and which owner gates are still open. The 2026-10-17
installed-state rules, rendezvous rules, and held work below stay in force.

**Historical (2026-10-17):** **installed state and next work** are the
[2026-10-17 blocks](#installed-state--2026-10-17-utc) below. The older
[Cursor wind-down handoff](DEV-ENV-CURSOR-HANDOFF-2026-10-16.md) is a stopped-session
snapshot only: partial windows, consumed seals and shutdown evidence to preserve, not
the current pickup. Do not repeat those attempts.

The front door for any agent that opens this repo, in the cluster's dev-env pod or on
Tom's own machine. Read this page, then [CLAUDE.md](../CLAUDE.md) (the rules), then
the saga. The [2026-10-13 pickup brief](CLAUDE-RESUME-2026-10-13.md) holds owner
gates; the [2026-10-12 brief](CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap)
holds requirement history and the six-step roadmap. The 2026-10-13 enumerated
next-work list is stale where this page's 2026-10-17 sections disagree.
[KICKOFF.md](sagas/distributed-dev-env/KICKOFF.md) is the historical first-build order.

## Installed state — 2026-10-17 UTC

*Historical. The installed release is in the 2026-10-20 block at the top.*

**Released and installed v2.29.4**, release commit
`f315635ad6fc30129da4e0310295634e57b78422`, including heartbeat freshness
[#358](https://github.com/thaynes43/dev-env/pull/358) and Luna pilot
[#360](https://github.com/thaynes43/dev-env/pull/360). The sealed pilot model is
`gpt-6-luna`; effort is `high`.

- Task image `ghcr.io/thaynes43/dev-env:2.29.4@sha256:9f1213cce94cc0ad33fc01eb124136ec82a6cc47f1c9d7a997775ae3a5117bd2`.
- Operator `ghcr.io/thaynes43/dev-env-operator:sha-f315635@sha256:60f66558bd178543c64ff4b5554fbd204fd9c023f7f9d8c57b1b9016b7a4099a`.
- [haynes-ops #3992](https://github.com/thaynes43/haynes-ops/pull/3992) merged
  `08cfff93`. Flux applied `dev-env-crds`, `dev-env-templates` and
  `dev-env-operator`. The live CRD requires `gpt-6-luna` for the sealed pilot.

**Later task image v2.29.5**, release commit
`56f1b8c127eae4864b241d729afae71cd7e23f2d`:

- Task image `ghcr.io/thaynes43/dev-env:2.29.5@sha256:1887be46224bfacd4a6e9c20e443c03f3b2d1516ca0f9305b939011f6a484c61`.
- [#367](https://github.com/thaynes43/dev-env/pull/367) (`5d44d12`): `ctl status` shows
  model and effort.
- [#366](https://github.com/thaynes43/dev-env/pull/366) (`b8a0873`): harness accepts
  direct claim JSON.
- [#362](https://github.com/thaynes43/dev-env/pull/362) (`e2be494`):
  `agentd task-claim-create --timeout`.
- [#370](https://github.com/thaynes43/dev-env/pull/370) (`60c4e38`): retains the last
  good log after executor stop in the source harness. It merged after v2.29.5;
  it is not part of that release or its task image.

**dev-env `origin/main` seal (last fetch):** `60c4e38c507c21d5fce64c51d0decc4b56362996`.
Session pods clone `repo: dev-env`, `base: main`, so the worktree HEAD is
`origin/main`, not the release tag. Write that SHA verbatim into harness
`config.json` `head`. It must match `^[0-9a-f]{40}$`. Do not parse HEAD from prompt
prose.

**Chapter 7 rendezvous is not accepted.** Codex checks use Luna; Claude Code checks use
Haiku (`claude-haiku-5-5` for new launches, Tom's 2026-10-19 latest-family
preference; record requested and served model). Luna quota is tighter.

**Held without reopening:** H7 reader work under Q-36 and the consumed Q-37 PV Retain
step stay held unless Tom explicitly reopens them in a new ruling.

## Next work — 2026-10-17 UTC

*Historical. The 2026-10-20 block at the top supersedes this list; the rendezvous rules and holds below still stand.*

**Do not start a live cohort from this chat.** A takeover agent reads this page and
the saga, then runs the next attempt only with a fresh seal, window and bound live
ConfigMap UID.

### Rendezvous acceptance (not done)

Done means A and B both claim, an inbox ACK exists, A publishes context, and B reads
it after A is terminal. `B_CREATE_ACCEPTED_NOT_PILOT_ACCEPTANCE` is not done.

### Cohort history (retain; do not reuse)

- **r3 exit 125** was GNU `timeout` wrapping `agentd`. Do not wrap `agentd` in GNU
  `timeout`. On 2.29.5, `--timeout=10s` exists. The working claim shape is
  `printf '%s' 'JSON' | /usr/local/bin/agentd task-claim-create` with no extra argv.
  The word `stdin` must not appear.
- **r4** A claimed exit 0 with taskKey `task:c7-private-pilot-1016-r4-a`. The seal
  required `task:c7-private-pilot-c7-rendezvous-1016-r4-a`. B was not created.
  Session `private-pilot-c7-rendezvous-1016-r4-a` /
  `a8a696d3-cccb-4df4-8b82-80896953f1a4`. haynes-ops #4001–#4003.
- **r5** sealed the v2.29.4 tag `f315635` while the worktree was origin/main
  `394c6b19`. A never called `task-claim-create`. Session
  `private-pilot-c7-rendezvous-1016-r5-a` /
  `c2edcfdf-c0ff-4e90-83e9-483c94fc2a3e`. haynes-ops #4004–#4006.
- **r6** sealed `394c6b19` and A claimed
  `task:c7-private-pilot-c7-rendezvous-1016-r6-a` (`d863a69f`, `04:09:40Z`). The
  harness log had raw claim JSON and no `operation` wrapper, so it polled until
  `task_ended_or_head_changed` (37 rounds). B was not created. haynes-ops
  #4007–#4010. [#366](https://github.com/thaynes43/dev-env/pull/366) parses that
  JSON.
- **r7** dispatched B. A `60253bec-ee21-420a-91af-be7ca7465da4` claimed
  `4fcc5466`. B `9d51d406-1b40-4850-a08a-5aebc60ee312` stopped before its own
  claim because `ctl status` omitted model and effort. No inbox. haynes-ops
  #4011–#4013. [#367](https://github.com/thaynes43/dev-env/pull/367) is in
  v2.29.5.
- **r8** ran on 2.29.5. A `4e2c50c6-7f06-49b6-93f6-f97ad1a3259d` exited after one
  turn. The log poll used `--tail 8000` and the v1 `agent-run` CLI. No B claim.
  haynes-ops #4014–#4016 admit, #4018 close.
- **r9** A `0262fe96-37e3-46f5-90be-dabf43c5b3aa` claimed `dfac26e6` for
  `task:c7-private-pilot-c7-rendezvous-1016-r9-a`. `config.json` `head` was the
  words `and private worktree`, and the worktree had moved to `60c4e38` after
  #370. B was not created. haynes-ops #4019–#4021.
- **r10** (`60c4e38`) is the furthest run. A claimed
  `f01d99af-6d4e-455d-8208-e12f7bfa32fa` for
  `task:c7-private-pilot-c7-rendezvous-1016-r10-a`. B
  `d191bd6f-c704-4084-8f47-c74956719088` claimed
  `6c423430-56f5-43a2-8220-28f279e85933` and sent InboxMessage
  `message.ce4a9f8a21da2153afba4e115e643770.555066cd2a8dc46506572a4da482f962`. A's
  `inbox-read` passed the name as a positional and exited 2 with empty stderr
  (`cmd/agentd/inbox.go` usage). The working command, proven on that pod, is
  `/usr/local/bin/agentd inbox-read --project dev-env --message EXACT_NAME`.
  `inbox-ack` needs the same flags. No ACK, no PublishedContext.
- **r11** B ran `task-claim-create stdin` and stopped. Inbox flags were never tested.
- **r12** harness started ~2 minutes after A (`bdc0c114-a1b8-43a7-9b1b-0d855a89cc92`)
  and returned `claim_stale`. The gate refuses a claim older than 30 seconds. Execute
  must start within 20 seconds of A existing. No separate nonexec. No missing
  `run-harness.sh`.
- **r13 admit** merged as [haynes-ops #4031](https://github.com/thaynes43/haynes-ops/pull/4031),
  digest `c1967f6c62ba443ac28fc187cffe3ba7fd15260f95c8d192523734f91f08ca95`, window
  `2026-10-17T08:35:00Z`–`09:00:00Z`, placeholder sealUID
  `3cfa283f-dc49-491b-a570-21cc156c2023`. It never ran: Mac polls used the human
  OIDC kubeconfig in the next bullet. That window is expired. Do not reuse it.
  The next cohort needs a fresh seal, a fresh window, and a bound live
  ConfigMap UID.
- **Mac kubectl uses Codex's Omni service account** `haynes-m5max-local-agent`
  (issuer `omni-haynes-service-account-issuer`). File
  `/Users/thaynes/.kube/haynes-ops-machine.yaml`, context
  `haynes-haynes-ops-machine`, bearer token, no exec plugin. Every Mac
  `kubectl` passes `--kubeconfig=` that file. Mac Flux prefixes the command
  with `KUBECONFIG=` that file. A bounded read succeeded: namespace `dev`.
  `~/.kube/config` and `haynes-ops/kubeconfig` are the human OIDC login
  (`kubectl oidc-login get-token` as `manofoz@gmail.com`). The repo `.envrc`
  selects the repo file. Polling that file opens a browser login before
  `kubectl exec` reaches the pod. Tom refreshed that OIDC login on 2026-10-17.
  Agents still use the service-account file. Do not refresh Omni login. A poll
  that fails token auth stops. Do not retry it. The Mac path does not exist
  inside a pod. In-pod commands use the pod's own kube setup. Do not change
  `.envrc` or install credentials. Do not create another service account.

### Next attempt rules

1. Admission is a single `--private-project-pilot` arg. Close it after the harness
   returns. Keep the seal ConfigMap.
2. Do not restart the v1 pod `dev-env-68b4b9f6cf-lt6j7` (UID
   `51cbe97d-f88b-46f2-a95d-046fbdf102f8`) or existing session pods.
3. Pin task image **2.29.5** (or newer if Tom approves a pin). Do not re-open shipped
   [#358](https://github.com/thaynes43/dev-env/pull/358) /
   [#360](https://github.com/thaynes43/dev-env/pull/360) unless unrelated work needs a
   new deploy.
4. **Log command** on a running executor:
   `/home/dev/.local/bin/agent-run-f315635 log SESSION --tail 5000 --once -o json` plus
   the CA file under
   `/home/dev/work/orders/dev-env-chapter7-pilot-runtime-1016-0907/public-operator-ca.pem`.
   `--tail` max is 5000. A Failed pod returns 409. v1 `agent-run log` is the wrong CLI.
5. For everything outside the sealed Luna pilot, continue the
   [2026-10-13 next-work order](CLAUDE-RESUME-2026-10-13.md#next-work-in-order) where
   it does not conflict with this page. H7 reader (Q-36) and Q-37 Retain remain out of
   scope until Tom reopens them.
6. Before creating either actor, both sealed prompts must pass
   `PilotPolicy.ValidateRendezvousCommands`. The harness `validate` and `run`
   refuse otherwise, before observation or B's create. Require the 2.29.5 claim
   pipeline with no extra argv, `task-claim-list` and `inbox-list` with
   `--project dev-env`, and `inbox-read` / `inbox-ack` with
   `--project dev-env --message NAME`. Every read and ack must be that exact
   flag form, including a second line beside a good one. Refuse any extra
   `task-claim-create` argv and the word `stdin` in either prompt.
   Operator admission is unchanged. This check does not open a cohort, bind a
   seal, or accept rendezvous. Context publication commands remain unproved.
   The next live attempt still needs a fresh seal, a fresh window, and a bound
   live ConfigMap UID.

**R2 get measurement, 2026-10-18.** One worst-case `agentd ctl coord-archive get`
of a 1 MiB object peaked at 41140 KiB on arm64 and 84688 KiB as an amd64 binary
on VirtualApple, in an offline container limited to 0.25 CPU and 256 MiB, with
no OOM. Neither figure is the installed amd64 shelf. The
[record](sagas/distributed-dev-env/verification/coordination-archive-get-memory-2026-10-18.md)
does not mount either archive, start R5, raise the shelf limit, open a cohort,
or accept retention.

**Concurrent get and prune measurement, 2026-10-18.** Four worst-case gets and
one `ctl prune` ran together in that same offline limit. Native arm64 peaked
at 143355904 bytes with no OOM. An amd64 binary on VirtualApple reached the
limit and the kernel killed one get, on both runs. Neither figure is the
installed shelf. The
[record](sagas/distributed-dev-env/verification/coordination-archive-concurrent-get-memory-2026-10-18.md)
does not raise the 256 MiB limit, start R5, mount either archive, or open a
cohort.

## General archive get selectors: recorded-time source portion — 2026-10-14 UTC

The [selector slice](sagas/distributed-dev-env/verification/coordination-archive-selectors-2026-10-14.md)
accepts optional UID and paired revision/hash pins. Defaults use the UID's first
recorded publication time with its immutable birth index, then that UID's recorded
revision archivedAt. Late recaptures do not reorder older UID births. Only the
selected object is fully verified, with no fallback from corrupt latest data.
Cross-project ambiguity, damaged/incomplete or linked selection metadata and
equal greatest timestamps refuse without an object. Traversal uses 128-name
batches and constant candidate memory. Explicit pins bypass default ordering.

All 49 focused coordination tests (129 passing nodes with finite subcases) passed.
Recorded timestamps do not establish strict arrival chronology after rollback;
that interpretation remains unestablished. No opaque/lexical tie breaker or new
clock/sequence policy was introduced, and full selector acceptance is not waived.
Next source: paged list, old incoming cleanup and shelf startup wiring. Installed
consumers, H7/H8, full parity and pilot acceptance remain separate. All live,
release/deployment, archive activation/deletion, v1 and cutover holds remain.
Chapter6 stays sole implementation owner; Chapter5/workers remain idle.

## Append-only coordination journal and grace — 2026-10-14 UTC

The [journal slice](sagas/distributed-dev-env/verification/coordination-archive-journal-2026-10-14.md)
adds `put --mark`, batched present/reset `mark`, and truthful marked lookup.
Absent starts grace after verified revision/index publication. Repeated marked
puts retain the original start; present/reset clears it; re-mark starts anew.
Malformed/unknown events, gaps, duplicate sequences, links and backward times
refuse state reconstruction. Append-only publication never replaces an event;
retry repairs sync after a visible event link without duplicating it.

All 41 focused coordination methods and finite subcases passed after preserving
the writer's existing no_archive refusal for linked archive parents. The local
main-dispatcher fixture demonstrates these transitions and corruption refusal.
These recorded observations do not authenticate absence or authorize release.
No live request, permission, release/deployment, archive mount/activation/deletion,
v1 change or cutover occurred. Chapter6 remains sole implementation owner;
Chapter5 and its workers stay idle. Shared budget/stall history is preserved.

The next source milestone is deterministic latest-revision/UID selection for get,
then paged list, old incoming cleanup and shelf startup wiring. R2 and installed
consumers remain incomplete; H7/H8, full parity, the real two-provider pilot and
separately approved v1 retirement remain independent acceptance gates.

## Local read-only coordination archive discovery — 2026-10-14 UTC

The [lookup slice](sagas/distributed-dev-env/verification/coordination-archive-lookup-2026-10-14.md)
adds stdin batch discovery for up to 1024 exact record identities. It returns
stored resourceVersion/hash selectors, then exact get verifies their bytes.
Lookup reads only manifests under each requested UID and does not verify objects.
An absent or empty journal yields null grace; nonempty journals fail closed.
The complete metadata response is bounded at 4 MiB, with no partial batch on error.

All 34 focused coordination methods and their finite subcases passed, including
put -> lookup -> exact get with identical digest and corrupt get refusal.
This source/offline slice adds no release eligibility or live execution.
General selectors, event journal/grace transitions, list, old incoming cleanup
and shelf startup wiring remain R2 work. The next useful source milestone is
the append-only event journal and grace transitions needed by marked lookup.
R2, installed consumers and pilot acceptance remain incomplete. All live,
deployment, archive activation/deletion, v1 and cutover holds remain.

## Local coordination archive CLI round-trip — 2026-10-14 UTC

The [minimal CLI slice](sagas/distributed-dev-env/verification/coordination-archive-cli-2026-10-14.md)
adds `agentd ctl coord-archive put` for an unmarked manifest/object request and
`get --resource R --name N --uid U --resource-version V --sha256 H`.
The existing command dispatcher and real storage were exercised with synthetic
temporary files: put succeeded, get returned identical bytes/digest, and a
corrupted object returned `corrupt` without an object. The production filesystem
check remained enabled; no mount or public bypass option was added.

All 28 focused coordination/CLI methods passed. Nonempty journals refuse
unmarked put, preserving unknown grace state. General selectors, `--mark`,
events, marked lookup/list and old incoming cleanup remain source work;
R2 and installed acceptance are incomplete. Do not execute this demonstration
against live archives under the current hold. Deployment, permissions, v1,
archive/deletion activation and cutover remain unchanged.

## Coordination archive index publication and repair — 2026-10-14 UTC

The [R2 index slice](sagas/distributed-dev-env/verification/coordination-archive-index-2026-10-14.md)
adds immutable all-record indices and recipient indices for InboxMessage only.
Entries hold identity and the UID's first publication time, with no object text.
Puts repair missing indices and retry directory syncs after interrupted revision
or index publication. Later revisions preserve the first index time even when
the clock moves back. Damaged entries are refused without replacement.

The focused suite passed all 21 coordination methods and finite subtests.
Commands, append-only absence events, bounded lookup/list, latest-revision
resolution and old-incoming cleanup remain R2 work. The next source unit is the
absence-event journal and grace-state transitions. R2, installed consumers and
pilot acceptance remain incomplete. Deployment, live execution, archive/deletion
activation, v1 changes and cutover stay held.

## Coordination archive immutable storage source checkpoint — 2026-10-14 UTC

The [R2 storage slice](sagas/distributed-dev-env/verification/coordination-archive-storage-2026-10-14.md)
adds atomic immutable revision puts and exact verified reads under the shelf lock.
All four record kinds round-trip with unknown fields and number precision intact.
Identical puts retain the first manifest. Conflicts, damaged records and linked
files return no object and never replace the stored revision. Interrupted staging
stays invisible. The focused coordination suite passed with two test workers.

This is source storage behavior, with no CLI/API or deployment. R2 still needs
indices and their crash repair, absence events, bounded lookup/list and command
wiring; R3–R7 and installed consumer/pilot acceptance remain. Keep archive/deletion
activation, deployment, live guard and cutover gates held. The next useful source
unit is immutable index publication and repair after interrupted puts.

## Coordination archive ingestion source checkpoint — 2026-10-14 UTC

The [DESIGN-024 R2 ingestion slice](sagas/distributed-dev-env/verification/coordination-archive-ingestion-2026-10-14.md)
verifies full canonical coordination objects against their manifest identities,
project, recipient/message bindings, size and SHA256. Unknown fields and numeric
encodings survive. The focused protocol suite passed. No archive command,
filesystem write, API route, retention/delete flag or deployment is added; R2's
atomic shelf storage/commands and R3–R7 remain.

Separately, the [six-query authority acceptance](sagas/distributed-dev-env/verification/h7-six-query-authority-acceptance-2026-10-14.md)
records the consumed Mac authorization window and current coordinator ownership.
Keep live guard, deployment, v1 and cutover gates held. Next source milestone:
atomic immutable shelf put/get; pilot/consumer/history acceptance is separate.

## Shared-history correction deployed — 2026-10-13 EDT

The [D-92 delivery record](sagas/distributed-dev-env/verification/d92-shared-history-delivery-2026-10-13.md)
records source #273, release 2.28.0 (#263), the publication fix (#274) and the
separate operator/template deployments (haynes-ops #3906 and #3907). Both signed
images were verified. The operator is Ready 2/2 at `sha-cbf0700` (actual binary
version `v2.28.0-1-gcbf0700`); the task template pins agent 2.28.0. Retained
sessions and their Pods, the shelf, failed reader/Jobs and v1 are preserved.

The history routes now have the shared-read correction, with runtime reads still
off. No archive mount, privilege, trust membership or feature flag changed.
Next: finish H7's installed guard acceptance and remaining prerequisites in the
pickup brief's order. Keep the personal-identity check separate from phone-path
proof. F4, both cold measurements, persistence/restore and H8 remain; an approved
non-canary source and positive peer retrieval with matching digest are still
needed. No Accepted ADR or owner question ID changed.

## Shared history requirement and fresh metadata acceptance — 2026-10-13 EDT

**D-92 / README decision 73:** all Tom's trusted dev-env agents may read shared
history. [The owner clarification and bounded plan](sagas/distributed-dev-env/requirements/2026-10-13-shared-agent-history.md)
replace per-agent denied-read isolation as a parity gate. Use existing
authenticated access; preserve credential exclusion, integrity, write/delete
protections, external/anonymous refusal and unchanged v1. The Mac relayed this
requirement; Root did not directly observe the original owner turn. It consumes
no question ID and authorizes no silent RBAC or trust-membership expansion.

Separately, Root accepts the [fresh existing-Mac capture-r2](sagas/distributed-dev-env/verification/h7-mac-capture-r2-2026-10-13.md)
for the bounded installed-configuration/pre-mount metadata window at
20:18:15–20:18:25 EDT (00:18:15–00:18:25 UTC October 14). Independent review
found no issues: 26 GETs, all 200, nine complete lists, 13 objects, five stable
protected objects and zero archive references under the reviewed predicates.
Actual admission/lookup behavior remains separate. The held reader and previous
failures are preserved; no PV request, mutation, retry or runtime change occurred.

The D-92 source correction is limited to the history routes: existing exact
configured API clients, live Sessions and configured Coordinators may read
shared history across projects, including projectless histories. Authentication,
live Session checks and publication/write/delete helpers are unchanged. The CLI
can list the existing projectless bucket by omitting `--project`. This is source
behavior; it enables no runtime flag and is not installed-read acceptance.

The release and image installation are recorded above. After the remaining H7
gates, use one approved private sample and positive peer retrieval with matching
digest; retain F4, bounded resource, persistence/restore and H8 requirements. No new isolation project or per-agent denial matrix is needed.
Reads and deletion remain off. Next IDs: Q-38, D-93, README 74, ADR-008,
DESIGN-026.

## H7 PV Retain verified; Q-37/D-91 consumed — 2026-10-13

The [Retain acceptance](sagas/distributed-dev-env/verification/h7-retain-acceptance-2026-10-13.md)
records Tom's "Yes" through the source Mac conversation, the preserved
source-corroborated approval relay, and the sole verified execution. Root did
not observe the original user turn directly. The reviewed #269 executable
made GET/PATCH/GET once, all 200, then verified the same bound history PV and
claim with Retain at 19:40:32 EDT (23:40:32.254177 UTC). Its output and exit 0
were independently audited from saved evidence; Root made no extra PV request.

The PV Retain gate is accepted. The one-time exception is consumed: no second
launch or PATCH is authorized. General PV rights remain owner-only. No v1 or
reader configuration, permissions, archive mount or flag changed in this
operation. The fresh bounded existing-Mac guard and baseline capture is now
accepted above; installed behavior acceptance before mounting remains separate.
[DESIGN-025 section 22](sagas/distributed-dev-env/designs/025-private-home-expiry.md#22-one-time-pv-retain-q-37-d-91)
records Q-37/D-91 and README decision 72. Other H7 gates remain separate.

The [personal-identity mapping checkpoint](sagas/distributed-dev-env/verification/h7-personal-identity-mapping-2026-10-13.md)
establishes a strong conditional expected username from current Omni metadata
and pinned source. It does not establish a current personally authenticated
request or phone path. The private username remains outside this repository.

## H7 reader failed; existing Mac evidence and hold — 2026-10-13

The [failed-reader and existing-access checkpoint](sagas/distributed-dev-env/verification/h7-reader-failure-and-existing-access-2026-10-13.md)
records haynes-ops #3904, merge `85d1dce3`: the reader foundation and read grants
are installed, but its single automatic Job failed before any workload inventory.
Its first policy read reported `InstalledPolicySourceMismatch`; the actual Pod
also had unreviewed timezone injection with an unlimited init container. Preserve
the failed Job; no retry, additional access or undo is proceeding.

Q-36/D-90 records Tom's relayed "Go for it" for holding the new reader and using
existing authorized Mac access for bounded evidence. The relay arrived after the
merge/deployment and expressly required reporting existing state without undo.
The Mac route reached the same source mismatch without denial. Independent
review supports a historical nine-kind baseline (13 objects, zero archive
references), matching five-object protected lineage and stable named rereads.
It found transport-limit and checker-assumption defects; full audit and real
guard acceptance remain pending. The source-only correction merged in
[dev-env #267](https://github.com/thaynes43/dev-env/pull/267), `dad78a39`, at
22:14:26 UTC. Required CI and both reviews passed; Python 3.9.25 and 3.12.15
each passed all 20 methods, including real CEL checks. Verified Mac binaries
and the reviewed invocation later produced the accepted capture-r2 above.
The held reader remains unchanged. No
workstation commands are requested from the owner. Already-installed access
has no withdrawal ruling.

## H7 archive guard registered; installed acceptance pending — 2026-10-13

[haynes-ops #3902](https://github.com/thaynes43/haynes-ops/pull/3902), merge
`650f3a04`, installs the compatible archive guard. The
[source and hook checkpoint](sagas/distributed-dev-env/verification/h7-compatible-guard-source-and-hook-2026-10-13.md)
records 1,593 passing hosted tests/subtests, no remaining native/advisory finding,
the deployed Kyverno lookup permissions and Flux Ready/Healthy at 20:23 UTC.
The generated Fail webhook matches the intended namespace/resources/operations.
Actual installed request outcomes remain pending; the later capture-r2 above
accepts the bounded pre-mount baseline. The dedicated audit's failure and existing-access hold are recorded
above. v1's inaccessible reads are not success or empty state. The owner is
currently on a phone; do not request workstation commands.

[haynes-ops #3898](https://github.com/thaynes43/haynes-ops/pull/3898), merge
`a287a4f6`, created the 20Gi RWO history archive PVC. The
[installed checkpoint](sagas/distributed-dev-env/verification/h7-archive-pvc-2026-10-13.md)
records Flux Ready/Healthy, the Bound claim and no Pod references at 17:24 UTC.
The shelf, retained sessions and failed Jobs match their baseline. Expiry flags
remain off except terminal recording in that snapshot. PV Retain was later
verified under Q-37/D-91 (the acceptance above). Finish the guard's installed and
baseline evidence before the shelf mount, preserving v1 under Q-35 and
[ADR-007](sagas/distributed-dev-env/adrs/007-preserve-v1-archive-trust-boundary.md).
F4 and both cold measurements still gate activation.

**Guard prerequisite:** the read-only review found operator-account borrowing
paths available to v1's existing administrator powers. A shelf-only guard does
not close them. [Q-35](sagas/distributed-dev-env/designs/025-private-home-expiry.md#20-h7-archive-trust-boundary-q-35)
asked whether v1 remains trusted during migration or H7 must isolate archives
from it before use. An oversight relay quotes Tom's answer: "v1 must remain the
same until it’s decommissioned after the MVP has been accepted". The coordinator
verified the relay, not the original owner turn or native phone delivery.
At 21:33 UTC, Tom directly answered the original asynchronous Q-35 prompt:
"Keep v1 trusted during migration (Recommended)". This confirms the existing
boundary; it grants no new rights or activation and leaves Q-36 intact. Receipt
`q35-direct-owner-confirmation-r1.json`, SHA-256
`d7728c571d7373417c1ca1d9dc52fd8670468483dc6140535cf447b21f3789e9`.
ADR-007 records the narrow constraint: preserve v1 behavior, configuration and
current access; add no rights, archive activation or automatic decommissioning.
[Issue #261](https://github.com/thaynes43/dev-env/issues/261) preserves the
evidence and validation limits. That initial read-only checkpoint performed no
bypass probe. The compatible guard protects v2 paths without claiming isolation from v1.
The [compatible guard contract](sagas/distributed-dev-env/verification/h7-compatible-guard-contract-2026-10-13.md)
is ratified for source implementation: preserve the named current administrator
and infrastructure routes, prove controller parent chains and a clean activation
baseline, and verify real webhook delivery separately. Source acceptance and
registered hook configuration now have the checkpoint above; complete installed
guard acceptance and archive mounting remain pending.

The [resource setup checkpoint](sagas/distributed-dev-env/verification/h7-measurement-setup-2026-10-13.md)
records the corrected synthetic fixture accounting, measurement-only isolation
and small focused checks. These are source prerequisites, not either cold
resource acceptance. Hosted artifacts, cache evidence and separately limited
measurement phases remain required.

## H7 owner identity and read-only prerequisites — 2026-10-13

The Codex takeover refreshed the startup and H7 read-only checks. The
[prerequisite checkpoint](sagas/distributed-dev-env/verification/h7-prerequisites-2026-10-13.md)
records every direct RBAC subject, Headlamp's shared ServiceAccount backend and
the scoped v1 workflow scan. No haynes-ops change was made.

[Q-33](sagas/distributed-dev-env/designs/025-private-home-expiry.md#18-h7-owner-identity-q-33)
is answered directly in this chat: Tom always needs his personal Kubernetes
account; Headlamp retires after full v2 adoption. Preserve that account for H7
owner controls. The shared Headlamp backend does not establish his personal
username. The original native prompt never reached him; his direct chat answer
settles this choice without proving native phone-prompt delivery.

[Q-34](sagas/distributed-dev-env/designs/025-private-home-expiry.md#19-personal-kubernetes-username-q-34)
asked for a workstation check. Tom cannot run it from his phone and directed the
coordinator to keep working. That request is withdrawn; discover the identity
from accessible read-only evidence. Keep only the identity-dependent policy
pending if evidence is insufficient. A bounded read-only audit found the exact
historical personal username in a 2026-07-29 cluster inspection, with the same
account still present as an Omni Admin (last active 2026-10-10). Current personal
Kubernetes authentication remains unobserved. Keep the account email outside this
public repo; guard preparation needs a reviewed private identity parameter. The
first H7 GitOps unit, the unmounted history PVC, does not depend on that username.

The F4 code audit found that `remote-control/fork-seeds` can contain conversation
history, but none of the 34 names was present in the observed v1 config directory.
All 34 remain unclassified; preserve `unknown_entry`. F4 and both cold resource
measurements remain prerequisites before archive activation. Source, deployment
and installed acceptance remain separate.

## Claude coordinator session 2026-10-12 to 2026-10-13: summary and pickup brief — 2026-10-13 UTC

The next Claude coordinator starts from the
[2026-10-13 pickup brief](CLAUDE-RESUME-2026-10-13.md). It has the deployed
state, counters, rulings with receipt paths, next work with owner gates,
boundaries, lessons and the questions still to ask Tom. The
[2026-10-12 brief](CLAUDE-RESUME-2026-10-12.md) is superseded for current
state; its requirement history and stopped routes still hold.

What the session (`task-dev-env-1012-105043`) did:

- **Reviewed Codex's source** in six units: 29 findings, 27 confirmed, 2 high
  (#220, an interrupted project-root advance; #221, the ledger's confirm
  read-back). Fixed in #219 to #225 and #229, released as 2.24.1.
- **Asked Q-27 to Q-32 and a pacing question**, one at a time through the
  Claude native prompt; each has a receipt. Rule loading is accepted for both
  providers (Q-27, Q-28). The Sigo rerun under Q-32 holds all seven DESIGN-011
  items. Q-29 and Q-31 produced ADR-004, ADR-005 and ADR-006 with DESIGN-024
  and DESIGN-025. Pacing (2026-10-13): "Slow down", one Opus subagent unit
  (`claude-opus-5-5`) at a time (README decision 69). Q-24 still has no answer.
- **Built DESIGN-025 H1, H3, H4, H5 and H6** (#236, #244, #246, #251, #254),
  bootstrap fixes S1 and S2 (#237, #239) and provider-reported usage for #176
  (#245).
- **Released and deployed 2.24.1, 2.25.0, 2.26.0 and 2.27.0.** The 2.26.0 and
  2.27.0 deploys have no entry of their own; this is their record.
  - 2.26.0: haynes-ops #3877 (CRD generation 11 to 12), #3878 (operator
    `sha-20c710f@sha256:23aae8a8…`), #3880 (template `2.26.0-3d5ddc3cf0`,
    agent `2.26.0@sha256:bb4b5d1d…`). Post-check PASS under `act-035219-4042498`.
  - 2.27.0: haynes-ops #3885 (CRD generation 12 to 13), #3886 (operator
    `sha-2221b83@sha256:fd8bdeca…`), #3887 (template `2.27.0-e83cacfe3e`,
    agent `2.27.0@sha256:b635dd81…`). Post-check PASS under
    `act-112939-38525`.
  - In both, the operator's args stayed byte-identical, and every
    non-operator, non-Job pod in `dev-env-system`, `dev-agents`, `dev` and
    `upgrade-agent` kept its UID, node and restarts. The three `Failed`
    Sessions changed only their `Outdated` condition; the two `Pending` ones
    did not change. Of the DESIGN-025 flags only the terminal record is on;
    stop, archive, reads and delete are off. Post-check evidence stays in the
    coordinator's deploy orders, outside this repo.
- **Housekeeping.** haynes-ops #3881 makes dev-env v2 image bumps manual-only,
  so Renovate #3710 (the shelf image) waits for H7. #243 tracks the Codex bump
  (#100 must not merge as is). #248 parks usage after reap. #231 got a scope
  review.

Next is H7 in haynes-ops (DESIGN-025 section 12), under the slow-down pacing.

## DESIGN-025 H6 in source: archived-history reads and the guarded HOME delete, both off — 2026-10-13 UTC

#254 adds three read routes behind `--enable-history-archive-reads` (default
off; off, they answer 404): `GET /v1/history?project=P` (a project's archived
histories), `GET /v1/history/{sessionUID}` (an entry's manifest, in pages) and
`GET /v1/history/{sessionUID}/file?path=&offset=` (64 KiB pages). The scope is
the trusted shared-history scope under D-92 in the corrected source; the initial
2.27.0 implementation used DESIGN-013's project boundaries. Untrusted callers
get 404 on single reads before the shelf, and the shelf's failures are 503. Each read
takes one of the replica's two shelf exec slots, shared with the H5 relay. The
client is `agent-run history list|show|read`. agentd gains `history-archive
manifest` (one page of an entry's manifest), so the shelf needs the agent image
of this release before the reads are turned on (H7 pins it).

It also adds the delete and the release behind `--enable-home-expiry-delete`
(default off), which the operator refuses at start unless the archive and the
reads are both on. Every attempt verifies the archive under `.shelf.lock`,
re-reads uncached and checks that no Pod uses the HOME before it records
`Deleting` (with `archivedAt` for a suspended Session) or deletes the claim with
its UID and resourceVersion; then `Expired`, and a reaped Session is released.
`TestExpiryDeleteNeedsVerify` pins that order. Deletion stays off until H8
records the archived read on the installed system (#174). The templates check
now refuses `dev-env-history-archive` and `dev-env-coordination-archive` as a
session claim. No CRD change (DESIGN-025 6.7, 7, 7.1, 8.1, 11).

Its review added tests for an owner hold and a resume written during the
delete's verify and for a verify answer that names another digest or entry,
made `TestExpiryDeleteNeedsVerify` check the two `if` conditions, corrected
6.7 on refusals recorded at `Archived` (each clears only through a verify, so
it needs the shelf), and made `agent-run history show|read` refuse a Session
UID that is not a Kubernetes UID before any request.

## DESIGN-025 H5 in source: the operator's HOME expiry up to Archived — 2026-10-13 UTC

#251 adds `status.privateHistory.expiry` (and `expiryClearedAt`) with its CEL
rules, and the operator's expiry behind `--enable-home-history-archive`
(default off; needs `--enable-private-terminal-record`; `--home-expiry-grace`
720h or longer). After the grace, or release-now, it removes the ended executor
Pod, makes a hold pod bound to the HOME UID with its intent recorded first,
rescues in it, pipes H4's export into the shelf's `history-archive put`,
verifies the entry and records `Archived`. A resume, a moved deadline or a new
Pod cancels; an owner hold stops every step; canary originals never enter it.
It deletes no HOME: `Deleting`, `Expired`, the reads and the delete flag are H6.
The reference copy of `dev-env-home-expiry-guard` that H7 ships is in
`internal/controller/testdata/haynes-ops/`, with its envtest. A new refusal
reason, `export_invalid`, joins `DevEnvHomeExpiryNeedsOwner` (DESIGN-025 5.4,
section 10). The CRD changed: haynes-ops needs the CRD copy before an operator
pin with this source.

Its review (7 findings) changed four things an installer must know. The
operator takes a history plan of at most 16 MiB and refuses a longer one as
`plan_too_large` (a new reason, in `DevEnvHomeExpiryNeedsOwner`); H7 sets `GOMEMLIMIT` near 200 MiB on the operator and runs
`TestHistoryRelayMeasure` in its container (DESIGN-025 5.4, sections 12 and 15).
`protocol.CanonicalJSON` streams now, byte-identical to 2.26's, so agentd and
operator of either release agree on digests. A mistyped override value holds
the expiry (fail closed), with a new warning alert,
`DevEnvHomeExpiryOverrideInvalid` (section 9, 10). Two CEL rules keep
`expiryClearedAt` (never removed or moved back; a cancel needs a newer one), so
an operator rollback below this release freezes those Sessions' status instead
of dropping the cancel (section 14). An export refusal that needs a source
change gets a hold pod of the new agent image at its next re-check (5.4).
The reference `dev-env-home-expiry-guard` now refuses every `system:` identity,
not only ServiceAccounts; human users stay outside it, so H7 lists who can
update AgentSessions and matches on Tom's identity if anyone else can (section 9).

## DESIGN-025 H4 in source: agentd history export and archive — 2026-10-13 UTC

#246 adds `agentd ctl history-export plan|stream` (hold or session pod, private
HOME, read-only), `agentd ctl history-archive put|verify|get|list|read` (shelf)
and `agentd shelf --history-archive`. Nothing calls them until H5 and H6, and
the archive volume arrives in H7. The layout tables were checked against the
pinned Claude Code 2.1.296 and Codex 0.160.1 (DESIGN-025 6.3, H4's
confirmation). Plan 11 follow-up F4 holds 34 Claude config names that still have
no class. The in-cluster resource measurement moves to H7 (DESIGN-025 15).
Before merge the review's findings were fixed: put refuses a manifest that
proves less than a whole export (rescue verdict, its own bundle, all four
roots); links whose targets HOME's delete would remove refuse; agentd's own
crash leftovers are classified; names that are not UTF-8 refuse; the plan
refuses a manifest put could not take; every read walks from its base with
`O_NOFOLLOW` on each component (DESIGN-025 6.3 and 7.1).

## Sigo rerun canary under Q-32: all seven items hold; #173's private-repo evidence accepted — 2026-10-13 UTC

Tom answered Q-32, "Yes, after deploy (Recommended)": one more Claude canary on
the Sigo alumni project after the 2.25 deploy, to prove catalog bootstrap
reaches `Ready` and the new status codes work. haynes-ops
[#3855](https://github.com/thaynes43/haynes-ops/pull/3855) (`156bd3a6`) replaced
the consumed Sigo record with `rule-canary-claude-sigo-1012b` (policy digest
`5b11e61c…`; `codex-1011` byte-identical); its post-check passed, with every
earlier peer pod move attributed to declared maintenance (`act-000159`). A
corrected runner (r4, 100 of 100 offline tests, independent review APPROVE; r3
never ran) waited until that maintenance and its successor `act-005526`, which
scoped Ceph and `gasha01`, had ended. Then it made one Create, one boot log
read, one named read and one result read, with no retries and no directory
read. Session `dev-agents/private-canary-eb9d486a612e27fd2b52d27f675512be`, UID
`454d12a2-60bc-4c4e-95c5-bb4b26428a65`, created at 01:20:09 UTC; the task
exited 0 after one turn.

**Root acceptance `a3aa0b7d`: `ACCEPTED_PRIVATE_REPO_CANARY_ALL_ITEMS`.** It
binds 215 files: every other file in the order directory (179), the Q-32
answer, the 2.25 deploy evidence and the earlier inputs. All seven DESIGN-011
items hold, so under the all-items gate the run is accepted for #173's
private-repository evidence.

- Catalog bootstrap reached `Ready`: the boot step says so, and
  `status.agent.projectBootstrap` reports `dev-env`, `sigo-alumni` and
  `sigoalumni-org` and both project roots `Ready`, with no reason code, in a
  HOME that was new at Create. The #239 field was reported and well formed,
  but with everything `Ready` no non-`Ready` reason code was exercised.
- agentd fetched the private `thaynes43/sigo-alumni` `main` at `c5da46c2` 49
  seconds after `createdAt` (01:20:09Z), equal to the `ls-remote` tip read
  just before Create, recorded the task source and made the task worktree from
  it; the task finished 62 seconds after `createdAt`.
- Peers unchanged: the 10 pods in `dev-env-system`, `dev-agents` and `dev` that
  are neither operator nor Job pods, and the four retained Sessions (item 7).
  The `upgrade-agent` pods in the snapshots are outside item 7.
- Markers (secondary): common `dev-env-common-rules-20261011-4ca29d70` present,
  project `sigo-alumni-project-rules-20261012-9b122cd5` reported.
- The boot log was again read after the wait and before the named read; the
  Root records and accepts `bootlog-before-named-read`, as for Q-30.

**#173 status.** Accepted: rule loading for both providers; private GitHub
authorization, a `Ready` private multi-repository catalog bootstrap and a fresh
pinned task worktree (this run, Claude only). Partly shown: missing-repo
bootstrap, for a new HOME only. Open: two bounded pods compared for independent
Git directories and worktrees, a failed fetch blocking new work, and preserved
dirty, WIP and reference state in an installed run. #173 stays open. Served
model, usage, cost, full history, physical stop beyond the terminal record,
capture completeness and tool-use compliance stay Unknown.

Counters now: Directory 6 (no directory read this run); Claude Create 4 (1011,
1012, sigo-1012, sigo-1012b), Codex Create 1; Claude named/result 4 each; Codex
named/result 1 each. All five permanent IDs are consumed, the last being
`rule-canary-claude-sigo-1012b`. No further read, retry, Create, second rerun or
cleanup is authorized. The
[Sigo rerun record](sagas/distributed-dev-env/verification/sigo-rerun-2026-10-13.md)
has the details.

## dev-env 2.25.0 deployed: terminal record on (DESIGN-025 H2), R1 alert — 2026-10-12 UTC

haynes-ops #3852 (`217dbede`, the AgentSession CRD, generation 10 to 11) merged
first, then #3854 (`1815eab0`). The operator runs
`sha-388444d@sha256:75c41e04…` (code identical to v2.25.0; its binary reports
`v2.24.1-9-g388444d`), 2/2 Ready, 0 restarts, with one new arg,
`--enable-private-terminal-record=true`. It has `get` on Nodes and on Leases in
`kube-node-lease`, bound to its own ServiceAccount only. The task template runs
agent image `2.25.0@sha256:25b20148…`, revision `2.25.0-b7972dd7b5`, so #237
(tracked symlinks) and #239 (bootstrap reason codes) reach new pods. The
canary policy is byte-identical; both records are consumed and the operator
started with them. No other pod in `dev-env-system`, `dev-agents`, `dev` or
`upgrade-agent` changed.

**The terminal record is live.** Within a second of taking the lease the leader
stored four marks: `executorTerminal` on the two 1012 canaries
(`private-canary-6f090716…` on talosw03, `private-canary-a1444f41…` on
talosw02; phase `Failed`, `DeadlineExceeded`, container `agent` exit 0) and
`executorMissed` on the two 1011 originals (`private-canary-3bfe2875…`,
`private-canary-fc5935c1…`), whose Pods were lost in the talosw02 reboot.
Counter on the leader: `recorded` 2, `missed` 2, `refused` 0. Spec, phase and
task result did not change. `DevEnvPrivateExecutorEndedUnrecorded` is an H7
alert and never sees these two misses (DESIGN-025 section 10, corrected here).

**R1 is live.** `DevEnvCoordinationRecordsNearLimit` (DESIGN-024 section 7) is
loaded in `dev-env-operator.rules`, health ok, inactive; the largest ratio is
about 0.003. Next for DESIGN-025 was H3 (source), now in source in #244 (below).
Post-check evidence stays with the coordinator's deploy order, outside this repo.

## Sigo private-repo canary recorded: not accepted for #173; private authorization and fresh pin accepted separately — 2026-10-12 UTC

The one Q-30 canary, `rule-canary-claude-sigo-1012`, ran on the Sigo alumni
catalog project after haynes-ops #3845 deployed. A corrected runner (r2, 60 of 60
offline tests; r1 never ran) made one Create, one boot log read, one named read
and one result read, with no retries and no directory read. Session
`dev-agents/private-canary-a1444f4131ec7281b5d1e3130c3b5ada`, UID
`430297d9-aa29-4332-9b3b-37095c1b5963`, created at 21:00:11 UTC; the task
exited 0 after one turn.

**Root acceptance `dd5dee24`: `PARTIAL_ACCEPTED_PRIVATE_AUTH_AND_FRESH_PIN`.**
It binds every other file in the canary's order directory (102 files).
DESIGN-011 accepts the run for #173 only when items 1 to 7 all hold. Item 2
fails, so the run is not accepted for #173, and the runner's verdict candidate
says so (`UNACCEPTED_CANDIDATE`). The Root accepts two properties separately, on
their own items; the record names that departure from the gate as the deviation
`partial-acceptance-by-property`.

- Items 1 and 3 to 7 hold. In its private pod, agentd fetched the private
  `thaynes43/sigo-alumni` `main` at `c5da46c2` 53 seconds after Create. That
  equals the `ls-remote` tip read just before Create. It recorded the task
  source and made the task worktree from it. That shows private GitHub
  authorization (items 1, 3, 5) and a fresh task pin (items 3 to 6). Peers were
  unchanged: pods that are neither operator nor Job pods, and the three retained
  Sessions (item 7).
- Item 2 fails: catalog bootstrap ended `Partial`. The diagnosed cause, from
  read-only analysis that no receipt binds: `thaynes43/sigoalumni-org` tracks
  one symlink, which agentd's clean-checkout proof refused.
  [#237](https://github.com/thaynes43/dev-env/pull/237) fixes it in source; it
  ships with release PR #238 and then needs a haynes-ops template pin. Until then, every v2 pod that boots with this catalog reports the same
  `project-bootstrap` problem (in this canary the task still ran).
- Markers (secondary): common marker `dev-env-common-rules-20261011-4ca29d70`
  present; project marker `sigo-alumni-project-rules-20261012-9b122cd5`
  reported.
- The boot log was read after the wait saw the task result and before the named
  read, not after it as DESIGN-011 says. The Root names this a recorded
  deviation, `bootlog-before-named-read`, and accepts it: both rest on the same
  Session status.

**#173 status.** Not accepted for #173 under DESIGN-011's gate. Accepted
separately, as a named deviation: private GitHub authorization and a fresh task
pin. Shown earlier: rule loading for both providers. Failed: catalog bootstrap
of a symlink-carrying repository (fixed in source by #237, deploy pending). Pending:
acceptance of a `Ready` bootstrap on a private multi-repository project. That
needs the fix deployed and a new canary, which needs Tom's approval and its own
record. #173's other items (two bounded pods, missing-repo bootstrap, a failed
fetch blocking new work, preserved local state) stay open. Served model, usage,
cost, full history, physical stop, capture completeness and tool-use compliance
stay Unknown.

Counters now: Directory 6 (the Sigo run made no directory read); Claude Create
3 (1011, 1012, sigo-1012), Codex Create 1; Claude named/result 3 each; Codex
named/result 1 each. All four permanent IDs are consumed, the last being
`rule-canary-claude-sigo-1012`. No further read, retry, Create or cleanup is
authorized. The
[Sigo private-repo canary record](sagas/distributed-dev-env/verification/sigo-private-canary-2026-10-12.md)
has the details.

## Q-30: private-repo canary on the Sigo alumni project, run and not accepted — 2026-10-12

Tom answered Q-30 in free text: use the existing private Sigo alumni repositories
instead of a new probe repository. They become catalog project `sigo-alumni`
(`thaynes43/sigo-alumni` and `thaynes43/sigoalumni-org`). One Claude canary,
`rule-canary-claude-sigo-1012`, will prove #173's private GitHub authorization and
a fresh task pin: one create, one named read and one result read, with no retries.
The [rulings table](#rulings) has the full question and answer.

**Prepared.** haynes-ops
[#3845](https://github.com/thaynes43/haynes-ops/pull/3845) adds the project. It
replaces the consumed `claude-1012` policy record with the new one, and
`codex-1011` stays byte-identical. It adds `sigo-alumni` to
`--context-payload-read-projects` and moves the Claude canary network selector.
Policy digest `154c3a12…`, Session `private-canary-a1444f4131ec7281b5d1e3130c3b5ada`,
catalog revision `5fe34bd3…`, rules revision `d070e7a6…`. The coordinator runs
the canary after #3845 deploys.
[DESIGN-011](sagas/distributed-dev-env/designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173)
lists the acceptance evidence.

**Run: bootstrap `Partial`, not accepted.** The canary Session was created
at 21:00:11 UTC on template `2.24.1-7e04fb00dc`. Its boot reported
`project-bootstrap: fail: catalog bootstrap outcome Partial`, so DESIGN-011's
acceptance item 2 fails, and the Session ended `Failed` (its pod reached its
deadline; the HOME is kept). The canary has no retries, so it does not count for
#173. **Cause:** one of the two repositories tracks a symlink (Git mode
`120000`), and agentd's clean-checkout proof accepted only regular files. The
reference was preserved and the project root failed on that repository's anchor.
**Fix:** [#237](https://github.com/thaynes43/dev-env/pull/237) accepts a tracked
symlink without following it
([DESIGN-006](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md#tracked-symlinks-in-the-clean-checkout-proof)).
A HOME left by 2.24.1 resumes on its next refresh with no operator step. It
ships in the next release and needs a haynes-ops template pin; a new canary
needs its own record after that deploys.
*Update, Root acceptance, 2026-10-12: items 1 and 3 to 7 hold and item 2 fails
as above, so under DESIGN-011's gate the run still does not count for #173. Root
receipt `dd5dee24` accepts private GitHub authorization and a fresh task pin
separately, a departure from that gate the record names
(`partial-acceptance-by-property`). See the entry above and the
[Sigo private-repo canary record](sagas/distributed-dev-env/verification/sigo-private-canary-2026-10-12.md).*
*Update, Q-32, 2026-10-13: after 2.25.0 deployed, the one rerun canary
`rule-canary-claude-sigo-1012b` reached a `Ready` bootstrap and all seven items
hold (Root `a3aa0b7d`); see the
[Sigo rerun record](sagas/distributed-dev-env/verification/sigo-rerun-2026-10-13.md).*

**Diagnosis without exec:** [#239](https://github.com/thaynes43/dev-env/pull/239)
(D-88) gives every repository and project root a closed-set reason code. The
boot step's note then reads, for example,
`catalog bootstrap outcome Partial: <repo>=UnsupportedTrackedEntry; project <p>=AnchorUnproven@<repo>(UnsupportedTrackedEntry)`,
`status.agent.projectBootstrap` holds the codes, and `agent-run show` prints
them. Its CRD change goes to haynes-ops before the operator pin. The next
canary's named read through `show --once` needs the runner's `agent-run` built
from the deployed operator's release tag or newer: an older one refuses the new
`projectBootstrap` field (DESIGN-006, reason codes).

**Effects to know.** Every v2 pod that boots after #3845 clones both private
repositories into its private HOME, because bootstrap is catalog-wide. Nothing
running restarts. Between the canary's Create and its result read, no change may
land in haynes-ops `dev-env-system` `projects`, `templates` or `session-config`, or
in the operator args the result read checks (DESIGN-011's freeze list): with no
retries, a denied result read wastes the canary. Global private admission stays
off. The run must also take the boot log read DESIGN-011 describes, and public
records quote only markers, IDs, revisions, SHAs and timestamps from it.

## Q-31 ruling: private HOME expiry, docs only — 2026-10-12

Tom answered Q-31 through the Claude native prompt: "Archive, then delete
(Recommended)". [ADR-005](sagas/distributed-dev-env/adrs/005-private-home-expiry.md)
records it and [DESIGN-025](sagas/distributed-dev-env/designs/025-private-home-expiry.md)
specifies it. It closes ADR-004's known gap (plan 11 follow-up F1).

- **The stop proof.** The node talosw02 rebooted, undeclared, from about 18:14 to
  18:19 UTC and removed the two retained, already-ended 1011 canary Pods. The
  operator correctly refused to recreate them and marked preservation Unknown. So
  a retained Pod is not a durable stop proof. The operator must record the
  executor Pod UID's termination in the Session's status before the Pod can
  vanish. Absence alone never proves stop.
- **The policy.** A private Session (no shared workspace, not a canary) that is
  Reaping, with no owner hold, is stopped; a Suspended one is never stopped and
  expires only if its executor ended on its own (ADR-006 below). 30 days after
  its recorded stop: a Git rescue in a hold pod bound to the HOME UID; the
  provider history (an allowlist, refused if a credential sits in the tree) and
  the Git bundle into a second archive volume on the shelf with a `Retain` PV,
  readable under DESIGN-013's payload scope; then the operator's leader deletes
  the HOME and releases the Session, and ADR-004 releases its records. Canary
  originals never expire. Tom alone can hold an expiry or release it now.

**Nothing runs yet.** Plan 11 lists units H0 (these docs) to H10, all default
off. H1 and H2, the terminal record, should ship first: each day without it can
lose a proof. H1 is in source in #236 (DESIGN-025 3.6): the schema, the
uncached checks, the confirmed write, the missed marker, `ExecutorEnded`, the
counter and `--enable-private-terminal-record`. It records nothing until H2
gives the operator `get` on Nodes and on Leases in `kube-node-lease`, copies the
CRD, pins a release with it and turns the flag on. Deletion (H9) waits for the installed archived-history read (H8,
#174). H7's one-time history-PV Retain step is now complete under Q-37/D-91;
that delegated attempt is consumed. Before H7 writes the override policy, check which
identity Tom's phone path uses (DESIGN-025 section 9).

**H3 is in source in #244 (DESIGN-025 4.1), default off.** It adds
`status.privateHistory.stopRequest` and `--enable-private-executor-stop`. The
operator refuses to start with that flag unless the terminal record flag is on
too. For a Reaping private Session with no owner hold, the operator writes the
stop intent and confirms it. Then it sends one JSON patch that sets the
recorded executor's `activeDeadlineSeconds`. Any override value except
`release-now` blocks the stop. A guard test scans the module for Pod writes. The
CRD gains a field, so haynes-ops needs the CRD copy before the operator pin that
carries it. H7 turns the flag on, after a read-only check for Reaping private
Sessions whose executor still runs: the flag stops each at once (DESIGN-025
section 12).

**Corrected after review, the same day (ADR-006, D-87).** An independent review of
#233 against the source and haynes-ops RBAC found 13 defects; the follow-up PR
fixes all of them in DESIGN-025 and, where ADR-005's own text was wrong, in
[ADR-006](sagas/distributed-dev-env/adrs/006-private-home-expiry-corrections.md).
The ones that change behaviour:

- **No stop at the suspended deadline.** ADR-005 stopped a private executor at the
  168-hour archive deadline too. With the 72-hour idle timer, that would have
  killed a live interactive Session for good after about ten days alone, with no
  human asking, and Q-31 never asked about stopping executors. Now only a reap
  stops one, an owner hold blocks it, and a confirmed stop intent serializes the
  two. Plan 11 F3 records the deadline stop as not planned without a new owner
  question.
- **The archives are not private against v1.** The v2 guards match only
  `dev-agents` identities. v1's OPERATOR tier (`dev/dev-env`,
  `upgrade-agent/dev-env-ops`) can exec into the shelf or mount an archive claim
  in a Job; the broker and keeper hold `pods/exec` in `dev-agents`. An admission
  guard for both archives ships with H7 or DESIGN-024's R5, whichever is first,
  after a check that no v1 workflow execs into the shelf (none did on
  2026-10-12). This historical v1 restriction is superseded by
  [ADR-007/Q-35](sagas/distributed-dev-env/adrs/007-preserve-v1-archive-trust-boundary.md):
  preserve v1 unchanged through accepted MVP and separately approved
  decommissioning. The current guard protects untrusted v2 paths without
  claiming privacy from v1.
- **Smaller fixes:** the terminal record sits beside `lastExecutor`; a once-only
  missed marker; a fifth guard exception for the expiry's hold pod; every HOME
  delete attempt re-verifies the archive; edit snapshots, pasted-text caches and
  attachments are not exported; agentd's entries are classified by name;
  provider directories must be set literally in the executor's `env`.

## Claude rule canary 1012 under Q-28 — 2026-10-12 UTC

Tom answered Q-28 through the Claude native prompt: "Yes, after fixes deploy
(Recommended)" (owner answer receipt `daadc6b5`). It allows one new Claude rule
canary, `rule-canary-claude-1012`, after the review fixes deploy: one create,
one named read and one result read, with no retries.

**Deploy first.** dev-env v2.24.1 (#228) shipped the fixes. The agent image is
`2.24.1@sha256:f2713217…` and the operator is `sha-538c4f6@sha256:d706ee40…`.
haynes-ops #3841 updated the AgentSession CRD. #3842 pinned the operator and
template `2.24.1-7e04fb00dc` and replaced the `claude-1011` canary policy record
with `claude-1012`; `codex-1011` stayed byte-identical. The post-check passed
with every peer preserved.

**The run.** One local `agent-run` built from tag v2.24.1. The directory read
passed (Directory 5 to 6) and Create returned HTTP 201: Session
`dev-agents/private-canary-6f090716f22b84cb55321a14338ccf30`, UID
`de61708b-3dc5-45de-8a16-885c2bdc83a3`, at 18:13:54 UTC. The runner's wait then
held after 40 seconds: it passed `--request-timeout` to kubectl, which in this pod
skips the in-cluster config and dials `localhost:8080`. The wait makes no operator
API call, so it spent nothing from Q-28. One reviewed correction (r2, 88 of 88
offline tests) dropped the flag and replaced only the wait step. The new wait
passed on its first poll: task result `Succeeded`, one turn. Then exactly one
named read and one result read ran, with no retries.

**The result.** The retained output is two lines,
`Common marker: dev-env-common-rules-20261011-4ca29d70` and
`Repository marker: dev-env-repository-rules-20261011-6d807bf2`. The runner's
exact-text check is false only because Claude labelled each line; both marker
values are exact. The prompt held neither marker, so Claude got both from the
rules in its private pod. The prompt also said not to use tools; while full
history is Unknown, compliance with that line is unproven, so a tool read of the
rule files cannot be excluded. Root acceptance `88909059`
binds 115 receipt files. The
[Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md)
has the details.

**#173 status.** Rule loading is accepted for both providers: the Codex original
and Claude canary 1012 both loaded the common and repository rules in private v2
pods. Genuine private GitHub authorization and worktrees, and a fresh task pin,
remain open for #173, with its other acceptance items (two bounded pods,
missing-repo bootstrap, a failed fetch blocking new work, preserved local
state). Served model, usage, cost, full history, physical stop, capture
completeness, private capture acknowledgement and tool-use compliance stay
Unknown.

Counters now: Directory 6; Claude Create 2 (1011, 1012), Codex Create 1; Claude
named/result 2 each; Codex named/result 1 each. All three permanent IDs are
consumed: `rule-canary-claude-1011`, `rule-canary-codex-1011` and
`rule-canary-claude-1012`. No further read, retry, Create or cleanup is
authorized. Q-24 is still pending. Older entries keep their original text.

## Q-29 ruling: coordination record retention, docs only — 2026-10-12

Tom answered Q-29 through the Claude native prompt: "Archive, then delete
(Recommended)". [ADR-004](sagas/distributed-dev-env/adrs/004-coordination-record-retention.md)
records it and [DESIGN-024](sagas/distributed-dev-env/designs/024-coordination-record-retention.md)
specifies it. A TaskClaim, PublishedContext or InboxMessage (with its ack) is
released 30 days after the Sessions it depends on are first proved absent. It is
archived first to a new volume that only the shelf pod mounts, then a leader-only
operator runnable verifies the archive and deletes the record. Reads fall back to
the archive. This amends DESIGN-010's no-release rule in the reap case only.

**Nothing runs yet.** Plan 11 lists units R0 (these docs) to R7; all are
default off until the haynes-ops change R6. R1, the 80% alert, needs only
today's metrics and can ship first. R5 needs Tom once: after the archive claim
binds, he sets its PersistentVolume to `Retain`.

**Known gap.** With the installed private-history flag, every Session without a
shared workspace is retained and never absent, so retention will release almost
nothing until a HOME expiry policy exists. That is plan 11 follow-up F1 and needs
its own owner question.
*Update: Q-31 ruled the HOME expiry policy later on 2026-10-12; see the entry
above.*

## Coordination review fixes, source only — 2026-10-12

PR #223 fixes the five #175 coordination review findings and the six findings
from its own review. TaskClaims, published contexts and inbox messages now have
per-Session and per-project bounds (429 `limit_exceeded`). The coordination
fences compare the Session's binding, not its resourceVersion, so a heartbeat
no longer refuses a valid create; status-borne authority is rechecked
separately. A catalog rename of a repository key no longer mints a second
claim owner. A mixed-case GitHub repository can publish. An unconfirmed
directory read is 503, not absence. The rules are in DESIGN-010, DESIGN-013,
DESIGN-014 and DESIGN-017.

**Not in effect yet.** The operator change needs a release, the signed operator
image and a haynes-ops pin. agentd's limit message needs the agent image.

- The bounds are lifetime bounds, because these records are never deleted. The
  operator exports the counts as `dev_env_coordination_records*`. Recovery is a
  manual delete by Tom, after an export, with the preservation duty in
  DESIGN-010. Retention is parked in plan 11 and needs an owner question.
  *Update: Q-29 ruled retention later on 2026-10-12; see the entry above.*
- **Rollback floor.** Once the new operator has written one coordination
  record, do not roll the operator back below the release that carries this
  change. Older operators fail that project's claim, inbox and context lists
  and gets with 503 (DESIGN-010 "At the bound, recovery and rollback").

## Codex original result read under Q-27 — 2026-10-12 UTC

The one Q-27 reader correction passed 23 focused tests and independent native
review of both read plans. Then exactly one named read (finished 15:55:52 UTC)
and one fixed result read (finished 15:56:02 UTC) ran, with no retries. The
Codex original's retained output is exactly two lines: the common marker and the
repository marker. The prompt held neither marker and said not to use tools, so
Codex loaded both rule sets in its private pod. Compliance with the no-tools line
is unproven while full history is Unknown.

Root acceptance `48637874` binds the receipts. Root accepted on its own checks;
no independent review followed the reads until the native review of PR #227,
which supports the acceptance (receipt `6c9a5fe1`). The
[Codex original result record](sagas/distributed-dev-env/verification/codex-original-result-2026-10-12.md)
has the details.

The task exited 0 after one turn, but the Session reads `Failed` because its Pod
later hit the deadline (finding history-04; a fix is proposed in draft PR #222).
Served model, usage, cost, full history, physical stop, capture completeness,
private GitHub authorization and a fresh task pin remain Unknown or unaccepted.
Claude's rule loading is still unproven; its original returned a weekly-limit
response.
*Update: #222 shipped in v2.24.1 and was deployed, and a new Claude canary loaded
both rule sets under Q-28; see the Q-28 entry at the top.*

Counters now: Directory 5; Claude Create 1, Codex Create 1; Claude named/result
1 each; Codex named/result 1 each. Both permanent IDs and both Q-27 reads are
consumed. No further read, retry, Create or cleanup is authorized. Q-24 is still
pending. The Q-27 entry below and every older entry keep their original text.

## Reader availability and one-shot exit codes, source only — 2026-10-12

Source [#219](https://github.com/thaynes43/dev-env/pull/219) fixes four #174 review
findings. In the operator, the fixed result reader now compares the bound common
content (project digest, CommonRevision, and each source's name, key, SHA-256 and
bytes), not the UID or resourceVersion of `dev-agents/dev-env-config-claude` and
`dev-agents/dev-env-config-codex` (rules-001). It also keeps a reaped, retained
original readable: with `deletionTimestamp` set, the publication generation must be
the live generation minus one (history-03). In `agent-run`, `canary create` and
`directory list` fence and strictly walk the whole reply (history-05). A one-shot
request that was sent and got no confirmed answer exits 6 and is not repeated. It
exits 5 only when nothing was sent (history-06, D-86). The amendments are in
DESIGN-022, DESIGN-015, DESIGN-020, DESIGN-021, DESIGN-023 and DESIGN-001 3.5.

**Not in effect yet.** The operator change needs a release, the signed operator
image and a haynes-ops pin. The client change needs that release's `agent-run`
build (and the agent image, for pods). Until the operator pin lands, the deployed
reader still has both lockouts. Both Q-27 reads are already done (entry above) and no further
read is authorized, but a later authorized read needs the results to stay readable:

- Any change to either ConfigMap above (any key, or an identical re-create) holds
  every retained result read. Do not edit them until the pin lands.
- A reap bumps an original's generation and holds its result read. Do not reap a
  canary original until the pin lands (no cleanup is authorized anyway). A suspend
  changes its spec and holds the read even after this fix.

The catalog's UID/resourceVersion check is still strict and cannot recover; it is
parked in [#226](https://github.com/thaynes43/dev-env/issues/226).

## Q-27 ruling and stale-guidance labels — 2026-10-12

Tom answered Q-27 on 2026-10-12 through the Claude native prompt:
"Fix once, then read (Recommended)". The ruling allows one offline correction of
the Codex reader preparation. The correction uses a fixed list of already
accepted evidence, focused tests and independent review. After that, the ruling
allows exactly one named read and one result read of the Codex original. There
are no retries and no counter reset. The
[Codex probe record](sagas/distributed-dev-env/verification/codex-fixed-rule-probe-2026-10-12.md#q-27-one-final-reader-preparation-correction)
keeps the original pending text. Under it, it adds the ruling, the question Claude
sent and the hash of the saved answer receipt. This docs update records the ruling
only; it does not run the correction or either read.

Q-24 is still pending: no owner answer is recorded. Do not ask it again; check for
an actual answer first. The [rulings table](#rulings) now runs through Q-27.
This update labels the superseded blocks it found lower on this page. Each label
is one line in italics or a quoted note and points to the current state. The page
is long, so an unlabelled entry is not thereby current: the newest entries and the
pickup brief control. Original text and receipts are kept. Retitled headings
record their first title, and the front-door paragraph moved to the top.
*Update: the correction and both Codex reads ran later on 2026-10-12; see the
entry above.*

## Claude pickup and controlling roadmap — 2026-10-12, America/New_York

Read the [Claude pickup brief](CLAUDE-RESUME-2026-10-12.md) in full. It records
Tom's current requirements and earlier enumerated lists, the six-step delivery
order, exact built/published/deployed/accepted distinctions, source-review paths
and stopped routes. Claude is to review, fix and improve Codex's implementations,
then ship bounded increments. Native phone questions remain the decision route;
Claude GitHub advisory reviews are optional/down.

The admission/preparation first assignment is completed. Both fixed canary IDs
are consumed; Codex reads remain zero and Q-27 remains stopped after three
preparation failures, with no fourth attempt authorized. Q-24 routing is pending.
This pickup does not reset counters or authorize retries, pairing or cutover.
Older entries below, including “What happens first” and shared-RWX prescriptions,
are historical; later owner requirements and ADR-003 control conflicting guidance.
*Update: Tom answered Q-27 later on 2026-10-12; see the ruling above.*
*Update: both Codex reads ran later on 2026-10-12 and returned both markers; see
the top entry.*

## Original Codex canary and stopped reader preparation — 2026-10-12 UTC

The [Codex probe contract](sagas/distributed-dev-env/verification/codex-fixed-rule-probe-2026-10-12.md)
starts from merged checkpoint #215 (`9eeb066c`). The Codex adapter passed 16
offline checks and independent full review `55e27946`. One reviewed program
completed Directory attempt 5 and original Codex HTTP 201 at 06:47:25 UTC,
binding UID `f0396ab3-1abf-41bf-be4e-93ce0e43608b`. The complete page contained
exactly the retained original Claude record; the Codex fence was fsynced before
Create, which completed within 0.195 seconds of capture finish. Native saved
review `c3bcc13b` and Root `f189f079` accept saved transport and original identity
only. Both fixed IDs are permanently consumed. Current counters are Directory 5,
Claude/Codex Create 1 each, Claude named/result reads 1 each, Codex reads 0.
Executor, output, served model, rule loading and post-Create directory remain
Unknown for Codex; no followup read authority exists.

Reader preparation stopped after three historical-evidence indexing failures;
untested drafts and corrected reason `0b6eeed4` remain preserved. Q-27 asks through
the native tool for one final offline correction using a fixed set of accepted
evidence, followed by tests and independent review; the answer is pending.
No fourth attempt or result read is authorized. The Claude quota result and
uncertain lifecycle receipts remain preserved. Q-24, actual both-provider rules
and the owner pilot remain open; Claude advisory is optional/down. Earlier
entries below are historical and must not restore zero-Create or unstarted IDs.
*Historical: Q-27 was answered later on 2026-10-12; see the ruling at the top.*
*Update: both Codex reads then ran and returned both markers; see the top entry.
The Q-27 ruling is now the second entry.*

## First original Claude canary and released ownership seam — 2026-10-12 UTC

The [current original-canary record](sagas/distributed-dev-env/verification/first-original-canary-2026-10-12.md)
supersedes the zero-Create state below. The reviewed sequencer completed logical
Directory attempt 4 and the first trusted Claude Create at 05:28:22 UTC, within
0.273 seconds of the successful empty page. Independent saved review `fd25b915`
and Root receipt acceptance `2d283d67` bind original HTTP 201 UID
`45a614cb-f537-441e-a5c1-33b66a285521`. The Claude ID is permanently consumed;
Codex's fixed ID remains unstarted *(historical: Codex's original HTTP 201
followed at 06:47:25 UTC; see the entry above)*. A single named read and native
saved review bind retained executor metadata and ordinal-zero context identity.
The Session reports Failed/exit 1; underlying cause, physical stop, history and
pilot acceptance remain open. The one fixed result read retained the 49-byte
weekly-limit response, reporting reset at 12pm UTC; no rule markers appear and
Claude rule loading is unaccepted. Preserve the original and do not retry its
consumed ID or substitute a paid API. Native saved review `c3ad5658` and Root
`54f89e02` accept retained-result identity and source assertions only. Both
named and result reads are consumed; no polling, logs, repost, replacement or
rearm follows. Next Codex preparation must account for the exact existing
original on a fresh Directory page before its first Create.
*(Update: under Q-28 a new Claude canary, `rule-canary-claude-1012`, returned both
markers; this original's ID stays consumed. See the Q-28 entry at the top.)*
*(Historical: that Codex preparation and Create followed at 06:47:25 UTC; see the
entry above.)*

Source #213 merged as `b3aa4d93` with focused tests, required CI and native review
passed. Release #214 merged as `78879c4a`, publishing v2.24.0. The new ownership
inspector remains unwired and refuses reservation; no private dispatch or #160
runtime recovery is accepted, and no deployment is required for that source seam.
Global private admission remains off; task pins remain v2.20. Existing sessions
have not been restarted. Claude advisory reviews are optional/down. Q-24 remains
pending; Q-26's one authorized R4 correction is complete. Earlier entries below
are historical and must not overwrite these counters or original identity.

## Private-budget ownership source and canary gate — 2026-10-12 UTC

*Historical entry, first titled "Private-budget ownership source and current
canary gate". Both fixed canaries ran later; see the entries above.*

The [ownership and runtime checkpoint](sagas/distributed-dev-env/verification/private-budget-ownership-2026-10-12.md)
records the bounded #154 source unit: original Session/HOME/intent/directory/Pod
identity proof with stop Unknown, and an unwired inspector that always refuses
reservation. The two new files pass focused tests and affected lint; native
review and required CI remain source shipping gates. Existing API, schema,
canary, directory, shared-budget and private-Pod retention guards are unchanged.
Runnable private dispatch and #160 recovery remain unaccepted.

The public-CA correction produced two complete empty Directory pages. Both
expired before Create; three logical reads include the retained initial TLS
failure. Final Create review preceded attempt 3's Root ruling expiry; the later
pre-spawn freshness check nevertheless refused execution. Root receipt `45a9f745` preserves that
refusal and artifact-absence evidence. No Create/helper/POST ran, no fixed key
was consumed and no fourth read is authorized by the expired plan. An inert
sequencing correction is under review *(historical: the reviewed sequencer later
created the Claude and Codex originals; see the entries above)*; unchanged
300-second freshness and the permanent once-only key remain required. v2.23 is
signed and published, not deployed; fixed task pins remain v2.20. Actual
both-provider rule loading and the owner pilot remain open. Q-24 is pending;
Q-26's authorized final R4 correction is already satisfied. Claude advisory
remains optional/down.

## Fixed result reader built and released — 2026-10-12 UTC

Source #208 merged as `cd966d17`; release #209 merged as `ca0dc3f1` and
[v2.22.0](https://github.com/thaynes43/dev-env/releases/tag/v2.22.0) is published.
The [current result-reader evidence](sagas/distributed-dev-env/verification/canary-result-2026-10-12.md)
supersedes the earlier source-pending entries. The initiating ordinary client can
read one named retained result for its trusted canary and original Session UID.
The admission preview fix accepts initial capture creation without persisting a
preview UID or weakening runtime UID validation. Focused checks, required CI and
independent native review passed. The bound local client was built and accepted
without invoking it. The exact-source signed operator image is verified.

Image-only delivery is [haynes-ops #3825](https://github.com/thaynes43/haynes-ops/pull/3825).
Its required Diff/Flux checks, native source review and independently reviewed
fresh preservation baseline passed. It merged as `b80cac13` at 02:15:34 UTC.
The one post observation preserved the exact signed image, two owned Ready pods,
all 11 peers, authority and fixtures. Its exact-revision HOLD remains unchanged:
Flux applied the next commit `0167e37a`, whose only changes are two Ollama tags.
Independent source review `a6271339`, saved review `d3e78362` and combined review
`5541773d` support separate root acceptance `f610a2d6` at 03:10:31 UTC. This accepts
#3825 image delivery and preservation in the saved observation window; there was
no extra GET, reconcile or restart of existing sessions. Both permanent canary IDs remain
unstarted, no key is consumed and global private-project admission is disabled.
*(Historical: both fixed IDs were consumed later on 2026-10-12; see the Claude
and Codex original entries above.)*
Actual Directory discovery, both-provider loaded rules and the owner pilot are
unaccepted. Claude advisory is optional during the outage; error-only runs were
read and retained without retries. Q-24 remains pending. Q-26 is satisfied by R4;
the new image delivery is a separate bounded unit, not another correction to R4.
The D23 named `show --json --once` client merged through PR #210 as `4c1e0c37`. One held
candidate's response-size finding was corrected before decoding; its focused
HTTP regression, affected lint and independent full native review passed.
Required CI passed. Release #211 merged as `c2ac2ab7`, and
[v2.23.0](https://github.com/thaynes43/dev-env/releases/tag/v2.23.0) was published at
03:09:41 UTC. Its actual merged-source local client `f136b205` was built once
offline at low parallelism; native saved review `693f1b3e` passed 483 checks and
root accepted the unsigned local build. It has never been invoked or installed
globally. The failed build preparation is preserved; its successor binds the
actual `4c1e0c37` source and all 67 unchanged, four changed and two added files.
This build supplies no runtime authority; the Directory TLS gate below remains.

Q-24 blocks acceptance of fully isolated native Codex sessions, as well as the
fleet UX. Pinned 0.160.1 native chats share one app-server harness; separate tool
executors do not isolate it. The outstanding native question recommends separate
paired session hosts. Coordinator/workers retain a shared native failure boundary
and require an explicit owner choice. [DESIGN-006 Q-24](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing)
and #91 remain the decision record; do not repeat the question or copy enrollment.

The closed installed binding `1061d476` retains current caller and four source
ConfigMap UID/resourceVersion bindings from the saved post observation. First
Directory plan `751813d5` passed independent final data review `b3e1ab06` and its
closed native gate `e3421598`. Root authorized one read. The attempt at 03:31:01
UTC failed certificate validation: the client does not trust the operator CA.
Its original refusal `ac1dfe77` and raw stderr `14ecfe02` are retained; no directory
data was returned, no Create is authorized, both keys are untouched and cardinality
remains Unknown. No retry, alternate API or TLS bypass is authorized. A finite
source/public-CA trust audit proceeds without runtime or credential reads.
No observer retry or original receipt rewrite is authorized.

## Fixed-rule configuration accepted — 2026-10-12 00:31 UTC

[The activation record](sagas/distributed-dev-env/verification/fixed-rule-activation-2026-10-11.md)
supersedes the pending R4/merge state below. Owner Q-26 allowed one final
correction after three failed preparations. Frozen R4 passed independent
preparation review; its one fresh 21-GET baseline passed independent saved review.
Ops #3824 squash-merged as `00bd6403` with required Diff/Flux checks and native
source review passed. Claude review is optional/down; its error-only result was
read and retained.

The one post-roll observer completed with 21 GETs and 29 predicates passed.
Independent saved-response review `d394b19e` passed; root configuration acceptance
is `b74d9a29`. Both replacement operator Pods are Ready and all 11 peers, fixture
Jobs/ledgers, schemas, authority objects, guards, networks and rule/catalog
ConfigMaps are preserved. Activity `act-235107-2570013` ended after acceptance.
Q-26 is satisfied: one final correction, one baseline and one observer, no retry
or fifth candidate. This accepts configuration preservation only.

The first Directory wrapper passed inert preparation review only. Actual
installation/caller/plan bindings remain unset. No new client or provider has
run; Directory contents remain Unknown. The two permanent IDs, signed operator
`sha-a44b5b1`/`d0463c21` and v2.20 task/template pins remain fixed. Global private
admission remains disabled. Capture audit `8093f2b6` found the initiating client
cannot read stored probe text through the general context API. DESIGN-022 is
the separate read-only result slice in flight. Its fake API tests also found
initial capture creation refuses the pre-create empty UID (`17ddd14e`). The
bounded admission-only DeepCopy preview fix must retain real runtime UID checks.
Both fixes require source checks/native review and signed delivery before either
permanent POST. No Job-output or live-log route supplies a retained receipt.
Actual rule acceptance and the owner pilot remain open; Q-24 is pending.

*Historical paragraphs: later on 2026-10-12 the DESIGN-022 reader shipped in
v2.22.0 and Ops #3825 delivered operator `sha-cd966d1`. Complete Directory pages
were read and both fixed canaries were created. Task/template pins are still
v2.20. See the entries above.*

## Client increment — 2026-10-11 UTC

*Historical entry, first titled "Latest client increment". v2.22 to v2.24 and
both fixed canaries came later; see the entries above.*

Source #204 (`c9493db0`) and #206 (`562d7df2`) are merged after focused checks,
required CI and native independent review. Release #205 (`42fb8bf2`) publishes
[v2.21.0](https://github.com/thaynes43/dev-env/releases/tag/v2.21.0) with both
explicit single-request clients. These source/release facts supersede the
historical pending-merge entries below.

[Client evidence and remaining acceptance](sagas/distributed-dev-env/verification/clients-2026-10-11.md)
binds the cached dev client to actual merged source. Its one build and metadata
guards passed without executing it, changing global PATH or using runtime auth.
Native saved-result reviews18502c6e (build) andbb3b7547 (publication) passed.
v2.21 is a verified signed artifact; it is not a runtime rollout.
Neither new client has called the operator API; no fixed provider attempt has
occurred.

*Historical paragraph: Ops #3824 merged, Q-26's one correction is complete and
Ops #3825 is the later accepted delivery; see the entries above.*

The latest accepted deployment evidence is the v2.20 prerequisite observation
from 22:00 UTC, separately evaluated offline. This client release changes no
GitOps pins and requires no session restart. Ops #3824 is still unmerged. Q-26's
owner answer authorizes ONE final focused R4 metadata correction, preserving the
three unsuccessful preparations and their counter. Its complete finite source/
generated metadata predicate remains unimplemented, pending root ratification
and independent preparation review before the same bounded checks. No fifth
candidate, alternate method or automatic retry if R4 fails. No new baseline,
rollout or provider attempt yet. Q-24 remains pending; no fleet UX is chosen.

Before EACH first fixed probe: reviewed configuration installed/preserved, a
bound merged-source client and one fresh complete authorized Directory page.
Keep the permanent IDs, original receipts and one initial attempt per provider.
Global private admission remains disabled in the last verified configuration.
Actual both-provider loaded rules, private GitHub authorization, live/stopped
discovery, full native history, claims/inbox consumer behavior, whole-task
budget/recovery integration and the concurrent owner pilot remain unaccepted.
Local-provider contracts remain in scope; live inference waits for GPU readiness
and household reservations. Claude review is optional while down; required CI
and independent native review remain gates. Parent issues stay open.

Q-25 needs no decision: the local DNS/schema route stopped, and shipping uses
authorized hosted CI plus saved real-API evidence. No DNS investigation opened.

## #173 ID-only canary client prerequisite

*Historical entry: Ops #3824 merged, Q-26 is answered and both fixed canaries
ran on 2026-10-12; see the entries above.*

[DESIGN-021](sagas/distributed-dev-env/designs/021-canary-cli.md) ratifies explicit
`canary create --id <fixedID> --json --once`. The pre-D21 CLI lacks this route;
generic create sends extra fields/retries and is not a safe substitute. Use the
ordinary caller and existing callOnce, ID-only JSON, no redirect/retry/wait/poll,
and a validated original receipt distinguishing HTTP201 from200 without claiming
provider execution. The first integrated Canary/Directory focused suite and
affected lint passed; packet6b8aa213 preserves both freezes and all 571 incoming
paths. Final native review/required CI/merge and a merged-source bound client
remain gates. Before EACH first create, require D20's fresh complete authorized page.
Actual fixed-provider campaign remains UNSTARTED; no global admission/fleet UX.
Source D20 #204 merged at c9493db0 with required CI38180555833 and native309d
passed; its actual Directory read/bound build remain unaccepted. Ops3824 config
activation is unmerged after three unsuccessful verifier-preparation candidates.
Q-26 asks one final focused metadata-check correction versus hold; no further
checker work, baseline/rollout or provider attempt until the owner's answer.

## #174 directory CLI prerequisite for #173 rule probes

*Historical entry: the bound client and Directory reads ran later; the counter is
Directory 5. See the entries above.*

[DESIGN-020](sagas/distributed-dev-env/designs/020-directory-cli.md) ratifies the
explicit `directory list --project dev-env --limit 20 --json --once` route.
The shipped pre-D20 binary has no Directory verb; do not invoke an unknown
command, because the dispatcher can reach creation. Reuse ordinary auth and
existing no-retry callWith, refuse redirects and expose typed continuation.
Corrected focused routing/query/single-request tests and affected lint passed;
packet925a5b93 retains the present-null output regression and original failed
fixture. Source #204 merged at c9493db0 after required CI38180555833 and native
309d passed; the actual bound build/API read remain unaccepted. No schema, runtime
read, provider attempt or authority change is included.
After a bound client build, require a fresh complete authorized page before each
first fixed canary create. Actual directory and both-provider rules remain
unaccepted. Preserve original observer/counters; optional Claude is not a gate.

## Historical installed state and acceptance gates — 2026-10-11 22:00 UTC

*Historical entry, first titled "Latest installed state". Ops #3825 is the later
accepted operator delivery; see "Fixed result reader built and released" above.*

This entry supersedes the historical “pending release/install” paragraphs below.
Source #199/#200/#202 and release #201 are merged; signed v2.20.0 prerequisites
are deployed through haynes-ops #3823 at `29cd8811`. Both new operator replicas
are Ready. Global private admission and common/canary/directory flags remain off;
no provider attempt or managed Session was created. Claude reviews are optional
while unavailable; required CI and native independent review remain gates.

[Installation evidence and corrected observation contract](sagas/distributed-dev-env/verification/installation-2026-10-11.md)
records the original 22-read refusal and separate offline verification. Directory
cardinality is Unknown: the direct list was Forbidden under the intended
operator-only authority. Preserve the failed observer/raw artifacts and counter;
do not retry, switch identity or grant broad reads. The separately reviewed offline adapter passed all 26 preservation/installation
checks without a new runtime read, and independent saved-result review passed.
Installation/preservation is accepted; actual Directory reads are unaccepted.

Next: bounded trusted fixed-record activation, then a fresh authorized
project-scoped Directory API read before EACH first canary creation. Retain
permanent IDs and one attempt per provider. Actual both-provider rule loading and
the concurrent owner pilot remain unaccepted. Q-24 is pending; no fleet UX choice.
Q-25 needs no owner decision: local schema DNS route stopped; use authorized CI
and saved real-API schema evidence without opening a DNS investigation.

## Urgent #174 original private Pod retention source correction

[DESIGN-019](sagas/distributed-dev-env/designs/019-private-pod-retention-guard.md)
ratifies PRIVATE-POD-R1: the private check protects volume removal, while a
suspended/reaped owned terminal/never-started Pod can reach UID/RV-conditioned
deletion before private Unknown conditions are applied. Complete source audit
`05765bfe3f5ce589d8f02f9de2b044361b9b6ee1e2a73cff29cdacfb9899a5a8`
and corrected pilot audit
`92a3997e5c198038d1f8de3fadc0028e08fd156a39763bb4db26ed1b84797bb9`
preserve the original mistaken premise/evidence; no runtime deletion was tried.
Add the existing protected-private-history refusal both in podRemovalAllowed
and before removePod enters legacy rescue/exec/deletion. No API/schema/flag or
cleanup change; retain exact original Pod/HOME/latches and Unknown stop/history.
Meaningful pre-fix regression, corrected controls, native review and CI remain
gates. Ship before provider activation via the pending combined D17/D18 release;
actual original-executor stop and the concurrent owner pilot remain unaccepted.

Source correction `cf0a708c` adds only the two existing private-history refusal
boundaries and two focused test paths. Pre-fix full Reconcile removed the exact
original owned Pod UID; the immutable failed fixture remains preserved. Two
required cached TestMain startups completed; corrected sixteen suspend/reap
state cases, partial/unknown/canary and shared/legacy controls passed. Affected
lint passed after one PATH-preparation correction. Packet
`189bf28fa7733cb85e2aa71d5b38f2011e865252b24605545d1206d82e7d84e7`
preserves 555 incoming paths and 102 earlier artifacts. Integration on Cost200
`a0ba2ee4` is complete at source `3cd61b8d`: all four source/test hashes, 565 main
incoming paths, 102 earlier artifacts and combined schemas remain unchanged.
Packet `92cb9dcf525d4aeeb39ee2a61ec48f8f3c1493ebabcfc557755b7218c860ee12`
records the rebase without a third local TestMain startup. Final native review/CI
remain gates; the current installed operator still predates this source fix.


## Built #174 durable directory source increment

[DESIGN-017](sagas/distributed-dev-env/designs/017-durable-session-directory.md)
ratifies one default-off protected directory record per original namespace/UID,
prospective managed enrollment before first executor, CAS last witnesses and
fresh current state kept separate. Independent paged metadata discovery and
exact-source context references grant no payload/control authority. No provider
HOME/history import, migration, expiry or reaping; full native history and
actual live/stopped discovery remain unaccepted. Local metadata is typed with
runtime inference still GPU/household gated. Native source audit receipt
`b9c92c9120cb5d579beebac08c5983d2108b932d1409f487e594322f49ee3c59`.

Source implementation passed focused API/store/controller/common/capture/schema
checks on released main. Native review found DIRECTORY-R1: whole optional status
removal could erase a last witness. The root schema guard and real API negatives
are corrected at `f610029a`; original HOLD/source/failed fixture remain retained.
Corrective packet `40baa50b91b82cab6fd16e09de82d772112e3b1c5e6564824a014fc0abc040d6`
binds the three-path correction and untouched twenty-path union. Corrected exact
native review also found DIRECTORY-R2: CRD types linked Kubernetes into agent-run
through the wire DTO. Standard-library DTOs and explicit server projection are
corrected at `5ceabbc6`; the unchanged build guard, bounded cached CLI build and
affected API/store checks passed. Packet
`b9bb67ebfac2b923d0eec23a6fdf7d54dcfb3756d9ff291f1dc54ec13732836a`
retains earlier findings/artifacts and unchanged generated/incoming paths.
Final native review and required CI passed; signed promotion and actual
directory/pilot remain gates.
DIRECTORY-R3 present-zero optional report timestamps are corrected at
`b3b3b9a5` with standard-library nullable JSON encoding/decoding. Eleven projection
cases, static CLI module guard and lint passed; corrected packet
`423663ecc3b36ae3ae71fefe256da3b731ca2d4b71f7e6fa9f1f00374fc6a0c8`
preserves all prior evidence and generated/incoming paths. Discovery uses an
explicit global project grant for all existing exact configured trusted human/
client/coordinator identities; it is not a per-SA map. Live Session readers must
also prove their own accepted project. Future pilot identity/project bindings
remain separate and do not select Q-24 fleet UX.

Source #199 squash-merged as `7fa1705e4981632f15580996c303848930913dbd`
at 20:40 UTC. Exact head `0afed31a` passed required CI 38172562777 and final
native review `7447409850fb3d0c6567d4e12b77225704b86a84924f65c93578fa2b6087d045`.
Optional Claude run 38172562788 failed before a verdict; its error-only receipt
and protected log were read without retry. Combine the directory and D18 usage
schemas before signed promotion; neither standalone schema contains both.
Directory activation is prospective and remains disabled until narrow access
and the admission guard are installed. Actual history/discovery is unaccepted.

## Built #176 source unit: truthful initial usage

[DESIGN-018](sagas/distributed-dev-env/designs/018-initial-invocation-usage-provenance.md)
fixes the evidenced missing-cost-to-$0 defect through typed presence/provenance
and one retained initial-invocation snapshot. Legacy raw status stays preserved;
new API responses omit unqualified legacy Usage so old clients cannot invent a
free cost. New clients show Unknown without typed evidence, explicit observed
zero with CLI estimate provenance, requested exact model and actual Unknown.
No parent/child sums, billing inference, API substitution or budget changes.
Native COST-R1 also proves new-agent raw heartbeats could fabricate zero through
an old operator/client. New agents must omit deprecated raw Usage from outgoing
heartbeats while preserving private results, stored legacy evidence and ordinary
liveness/budget processing. The original failed review remains retained.
Finite audit SHA256 `9c96d404956f661f1a09f5361a1ffa6f9e1874f8a5e3e1a7848b00808db197e8`.
Source implementation, native review and required CI passed. Signed promotion
and actual-provider acceptance remain gates. Existing sessions must not restart
for reporting alone.

COST-R1 source correction `c97f800c` removes raw forwarding at collection and
at the final heartbeat Send boundary. Actual old-decoder wire cases, incoming
legacy/typed liveness and stable-counter/deadline budget cases passed; private
raw results and generated schemas stay preserved. Corrected packet
`192b9a2ccc1bc0d022419fe2090f1ebfbe6c0187e94b709c4ba5aff06187c525`
retains the original native HOLD. Final independent review and required CI
passed before source merge; signed installation and actual-provider acceptance
remain separate.

Standalone Cost head `a3d44d5e` passed native corrected review
`063da43dfee9faa4485dc6e435c24dcbee1a2fa0a0047d00e233a99af4229a41`
and required CI 38172643863. Optional Claude run 38172643851 failed before a
verdict; log `77fb4eb7ea5d02d49dd5ab557b28c1368cbdbf8b7993c60f74ae4a089ac05367`
was read. The active #200 rebase on merged Directory7fa preserves all original
source/failed reviews/fixtures. Combined-schema, receipt/CAS validation and
exact integrated-head native review/CI remain required before merge.

Integrated source `35f2f4bf` on merged Directory7fa passed focused receipt/CAS/
replay and combined real-schema negatives, lint, static CLI guard and reproducible
generation. Packet `9aa9a031b96eaa720e69606180dfc760809deb7d568ff2c7ba4a4ae68817ccbe`
preserves 541 main incoming paths and 98 earlier artifacts. Combined AgentSession
schema is `32b2c3e3f40258694e3693a3f5ba3631b71fa729b1708a3cfb6f9e4828155703`.
Source #200 squash-merged as `a0ba2ee413d38e8368e99f6bb95809d85b214aff`
at 21:10 UTC. Exact final `647f114f` passed CI 38174709908 and integrated native
review `e2f980ca4928f1e7f82870c4eefd6827447af07c03b0305478ab41cd669f2196`.
Optional Claude 38174709818 failed before a verdict; error-only/empty findings
and log `63c15af014f0fda76b24ba8740ba8ab19afb1a39746199279bb4a31b4a4e57c9`
were read without retry. Pending release #201 proposes v2.20.0 with directory,
usage and this private Pod guard; publication/install/actual pilot remain gates.

## Finite #154/#160 source audit, runtime unchanged

Audit `949b9d839601a90660389ec27a71ba4d3dc43429b11a59acb07e8c7b88a8f9fa`
preserves 39 source hashes and 24 budget/recovery seams. Current private budget
refusals remain intact. Next bounded candidate is a default-off private direct-
child adapter reusing the same logical-task ledger, reservation/deadline/counters
and original private Session/HOME/executor/directory binding. Exact retained
Pod/all-container/Node/Lease termination must support a distinct private proof;
absence/partition/foreign ownership remains uncertain with effort/WIP retained.
No native routing/rearm/rescue is included. Exceptional lost-native-host recovery
still lacks accepted fence/one-use authorization/history ownership. Original
#170 fixture/ledger and stopped Q-25 schema route remain preserved; Q-24 pending.
Audit performed no tests, runtime GETs/writes, provider turns or private reads.
The #173 rule acceptance and current D17/D18 promotion/pilot remain in flight.

Finite pilot audit `5aff018d28cfd115a8a5704b85e8a2fd200ad358bb0b638b0d75c088261c0fba`
keeps the fixed rule pair unstarted. Public dev-env cloning into private homes/
worktrees cannot prove private GitHub repository authorization. Short no-tools
marker invocations do not guarantee a concurrent interaction/upgrade window.
Discovery alone grants no claims, payload or inbox rights. The original audit
misattributed the private volume-removal guard to Pod removal. Exact source
instead requires a full suspended/reaped removal-path audit before claiming Pod
retention. Preserve the original audit and correction; no runtime deletion was
attempted. Suspend/reap intent and absence are not a termination proof. A bounded
original-executor stop-and-preserve source proof must precede the termination
pilot, preserving original HOME/Pod evidence without archive/delete/rearm.

## Installed common-rule canary prerequisites, admission disabled

[haynes-ops #3822](https://github.com/thaynes43/haynes-ops/pull/3822) merged as
`1a386bd136e13928b5e82f9546c04fab3fd3793b`. Exact signed source24960dc operator,
v2.19.0 templates, AgentSession/PublishedContext schemas, minimal provider
profiles, versioned catalog/common sources and narrow policies installed with
all admission flags unchanged/off and no provider record/task. Native source
and observer reviews and required CI passed; optional Claude failed before a
verdict and was read without retry. One 21-GET observation passed:
`31b6e674f275c544e0710aa97453c64c757a68462c2052e0c66b77d8c0b07e48`.
All six affected Flux Kustomizations reached the exact merge, both operator
replicas are Ready, and all 11 other Pod UIDs/restarts/specs, four Jobs, CRD UIDs/
unrelated schemas, existing authority/guards, source identities and failed-native
ledger remain preserved. The scoped activity ended. This accepts prerequisite
installation only; actual both-provider instruction loading, private task pins,
prospective directory/usage and the disposable owner pilot remain unaccepted.
Install their final source/guards before separate fixed-record activation.

## Built #174 source unit: partial managed output capture

[DESIGN-016](sagas/distributed-dev-env/designs/016-managed-output-context-capture.md)
uses existing assistant-event parsers and protected context storage. Separate
Claude stdout/stderr first; capture only bounded initial managed task output,
with original private invocation intent and truthful publisher-asserted partial
provenance. No raw prompts/tools/provider HOME/database imports or model replay.
Native/TUI/resumed full history and the session directory remain unaccepted.

The corrected source and Common195 integration are frozen after focused parser,
API/store/policy/controller/schema/lint/generation checks. Raw invalid Codex
UTF-8 refuses before JSON substitution, marks Unknown and preserves an accepted
prefix. Original CAPTURE-R1 packet/review and corrected/integration packets
remain retained. Integration validation SHA256
`124166ea8424ff92f5884f8099faa06dddb1b6ea49f7472c0248117542545371`.
Final native review and exact-head required CI passed; #198 squash-merged as
`24960dc128b219c48f4a32ba9f25e778f20e9a66` at 19:30:45 UTC. Native final
receipt `4f5e0415f9d6fb10e369cfcd978423e9169b5b8141fbb82aedfba3f579cf395a`
is retained. Release #197 merged as `fb53eb971da1beec1abc513fb1f251bb0926d0a6`
and signed v2.19.0 published digest
`4cf24d0c0bff7a9174a8a453340e3800d99fd9282b8787b6f7274f3474903210`
is verified. Separate GitOps/actual capture acceptance remain gates. Claude
failed before a verdict and is optional.

Context guard GitOps #3819 installed at `f316532a`; receipt
`cd911ef5e9e474a9cd54f2a951f65d748e6ac9d9ea83395231ddd4ca2641a06f`
proves exact signed operator/CRD/Role and preservation of 11 other Pods, four
retained Jobs and all prior schemas/claims authority/catalog/templates/native
ledger. Zero AgentSessions existed, so actual session upgrade acceptance still
needs the owner pilot. Scoped activity ended; no provider trial occurred.

## Built #173 common instruction snapshot — 2026-10-11 UTC

[DESIGN-015](sagas/distributed-dev-env/designs/015-retained-common-rule-inputs.md)
closes an evidenced gap: catalog/project snapshots do not record global common
configuration content, and resume can reread changed global/developer inputs.
Source #195 merged as `f1bb6ca26937557f6c823fc74b0ba385661f360a` after
exact-head required CI and native review. The default-off new managed route
retains bounded named source bytes,
source UID/RV/hash, CommonRevision and a separately derived effective instruction
projection. Owned private globals and resume use the original accepted inputs;
foreign/partial/changed state refuses while preserving WIP. Actual canary
activation waits for signed release/deployment and reviewed narrow config/
profile/network/required-access changes. COMMON-R1 was fixed with the durable
original projection digest and Fresh/Preparing/Confirmed latch, including
first-launch refusal before launch.json exists. Native correction receipt
`6fca81709bfaf74d6d22aecf580884ccc82c3fbc02d3839221a35f7628e3dc29`
and both original/revised packets are retained; actual provider loading is pending.
Legacy/common provenance remains Unknown; Q-24 is pending.

Required common-source GET access installed through haynes-ops #3821 merge
`519252fc05ca448f37c081a8d6b4ad32bdd593fe`. A separate namespaced Role grants
only the operator GET on two fixed public v2 rule ConfigMaps. The 19:39:12 UTC
receipt `bcaab45c470fcc2ce47c3c5fde0d1253a9406c9cdf2cbd3ed7f5e61c444b6d0f`
proves exact Flux/grant and preservation of all thirteen Pods including both
operators, four Jobs, nine schemas, sixty prior authority records, catalog/
templates and native ledger. No rollout, provider turn or session was created.
Common/canary/capture activation remains separate; global admission stays false.

## Installed #173 owned roots, narrow acceptance — 2026-10-11 UTC

Source #186/release #188 v2.15.1 and haynes-ops #3817 merged at
`a85d0f2ae6324bcd7029efeaa8909ecc94b9ed76`. New templates pin signed digest
`b44f676305bab133b32e9d53d108a43a8da45379829024d3068e8dafc778c04d`.
Attempt 2's separately named Job has UID `e13a226e-7024-4294-9183-58b975a6922a`,
one Pod on talosw01, exit0 and zero restarts. Exact admitted script/image/limits/
private emptyDirs/token scope/egress matched review. Two reports are Ready:
FirstRefresh then Unchanged, with stable owned root/tree/wrappers and exact
catalog provenance. Selected source is pinned to `296c3d7`; build source is
`2da15bf`. Flux targets are Ready at the reviewed merge. All 12 protected Pods,
three prior Jobs, old schema/catalog and original ledger were unchanged;
activity ended. Retained receipt SHA256
`693c2684172315153e6fb80ebb41ee8217b4926c26d37c723d71a284b8447024`.

This accepts installed explicit roots refresh for one public repository. The
first failed roots fixture remains retained; logical count is 2, with no further
trial. Common rules are empty, so private authentication, daemon/native startup,
actual provider loading and MVP acceptance remain open. Global admission stays
false. Task-claim GitOps #3818 (`8e17a394`) installed signed `d95e8fb`, the exact
protected CRD and narrow operator Role. Receipt at 17:57:25 UTC
`69c25fd40a14cc052abac7987feb255782bff20ee4960608c258496eacababba`
proves guard installation and preservation of all 11 other Pods, four Jobs,
old schema/catalog/template identities and native ledger. Runtime claims remain
unaccepted; context source #190 is merged and release #192 shipped v2.17.0; its
GitOps #3819 installed at `f316532ab00d4dd4edbf5f2918f5fa1baff825ca`.
Receipt `cd911ef5e9e474a9cd54f2a951f65d748e6ac9d9ea83395231ddd4ca2641a06f`
proves exact context CRD/narrow Role/signed image/two Ready replicas and
preservation of all 11 other Pods, four retained Jobs, prior schema/claim
authority/catalog/templates/native ledger. Zero AgentSessions existed; actual
publication/stopped-read and live-session upgrade acceptance remain pending.
Scoped activity ended.

## Installed #175 durable offline inbox guards — 2026-10-11 UTC

Source PR #194 merged as `f02d0ad276d007105795dd8611d0725c5176869b` after
required CI and native original/integration reviews passed. Corrected validation
provenance retained the original uncertain packet without source changes. Claude
review failed before a verdict and remains optional; its receipt was read.
Release #193 shipped signed v2.18.0. GitOps #3820 merged as
`271f8efef3085353cb42b0968913e1faf2b3adda` and installed signed f02d0ad
with only the three explicit inbox flags. The 18:59:38 UTC observation
`3654a185c965e6b8a5bcd941883ad9d39ce1c0938051dc8e4bb07854bb43f722`
proves exact Established schemas, narrow operator-only grants and two Ready
replicas, with all eleven other Pods/restart counts, four Jobs, prior schemas/
authority, catalog/templates and failed native ledger preserved. Original
observer findings and baseline were retained; a separately reviewed corrected
helper pins the baseline and complete CRD specs. Scoped activity ended.
Zero AgentSessions existed, so actual offline recipient/API acknowledgement
and live-provider upgrade preservation remain unaccepted.

[DESIGN-014](sagas/distributed-dev-env/designs/014-durable-offline-inbox.md)
defines immutable attributed messages and explicit read acknowledgements in the
protected coordination API group. Original Session UIDs fence publication and
recipient ownership; separate project content scopes permit stopped-context
reads without legacy broad-read access. Accepted retries survive recipient
deletion; acknowledgements prove API receipt only. No provider prompt injection,
native host identity or runtime delivery is claimed. Source implementation,
native independent review, required CI, signed publication and narrow GitOps
guards are delivered; a disposable offline trial remains an acceptance gate.
Claude review stays optional.

#175 atomic task-claim source #187 merged as
`d95e8fbe7b3e1c07eed75959819cd35f90a155c4`; GitOps PR #3818 merged as `8e17a394` and installed the exact protected CRD,
narrow operator-only Role and signed `d95e8fb` image. The 17:57:25 UTC receipt
`69c25fd40a14cc052abac7987feb255782bff20ee4960608c258496eacababba`
proves installation and preservation of all 11 other Pods, four terminal Jobs,
old schema/catalog/template identities and the native ledger. Actual claim
contention and live authorization remain unaccepted. Private admission remains false;
neither source merge closes #175 or establishes the MVP.

## Built #173 one-attempt managed canaries — 2026-10-11 UTC

[DESIGN-011](sagas/distributed-dev-env/designs/011-private-managed-rule-canaries.md)
adds a separate default-off policy with at most two fixed Claude/Codex records,
while global private admission remains false. Existing clients submit only an
ID. Trusted records bind exact project/repo/catalog/rules/template/image/model,
fixed prompt and finite limits. Original retained identity survives concurrency,
lost acknowledgements, early deletion and terminal retries. A permanent finalizer
at initial Create prevents replacement before the first reconcile.

The controller independently proves authority and retained execution intent
before one bounded Pod. Canary Sessions cannot dispatch legacy child Sessions.
One container, no init/sidecars, RestartNever, 600-second deadline, size-S caps,
worker placement and at most 180-second provider execution are enforced. Existing
subscription credential paths remain; Codex uses keeper access-only delivery,
never enrollment/refresh copies or paid API fallback. Focused parser/API/schema/
controller/command checks, affected lint and reproducible generation passed.
Rebase onto released main `296c3d7` retains claims/history wiring. Independent native review and required CI passed on the original candidate;
the integration rebase onto #190 passed native review and exact-head required
CI38162585232. PR #191 merged as `c062e9897d95aa5da7643250a5b9811c4ecc8cae`.
Claude review failed before a verdict and is optional/down; its receipt was read.


DESIGN-015 retained common-rule inputs must land before activation. Signed
publication and separately reviewed GitOps profiles/policy/nonempty rules
must precede any actual canary. No trial, native rule loading, pairing or fleet UX
is accepted here. Q-24 remains pending. Provider output must prove loaded markers
without marker values in the task prompt. Keep #173/#91 and the MVP open.


## Built #174 published context source — 2026-10-11 UTC

[DESIGN-013](sagas/distributed-dev-env/designs/013-published-stopped-context.md)
adds default-off immutable explicit publication and authorized stopped-source
reads in the protected coordination group. Original Session UID/kind/key binds
one publication; atomic confirmation preserves lost-ACK uncertainty and refuses
changed content. The server derives actor, requested provider/model and saved
catalog/rules identity. Actual model/usage/cost/initial pin/stop remain Unknown.
Explicit stdin text is an excerpt, not a verified provider transcript.

Separate project payload scopes authorize own live readers or configured
coordinators; legacy broad read and claim metadata scopes grant no text access.
Lists omit text, and reads do not require a stopped/absent source to be Ready or
invoke exec/resume. The CLI retains a private immutable intent before HTTP,
uses projected-token rotation and verified TLS, and refuses redirects.

Focused store/API/client/command and real cached schema checks passed, affected
lint has zero issues, and generation reproduces exact bytes. Rebase from the
reviewed claim stack onto main `296c3d7ac0fb56600c30508d29ee066bb1baad27` preserves
all 19 frozen source files and eight unaffected claim files. Native independent review and required CI passed at exact head `4cc602f`;
source PR #190 merged as `5cfcf0e` on 2026-10-11. A native finding about
bounded token-file reads was fixed across publication, heartbeat, project-sync
and access minting. Claude review was unavailable and remains optional/down.

Release #192 shipped v2.17.0 with a signed agent image. GitOps #3819 merged
as `f316532ab00d4dd4edbf5f2918f5fa1baff825ca`; installation and operator
preservation passed at 18:20:46 UTC in receipt
`cd911ef5e9e474a9cd54f2a951f65d748e6ac9d9ea83395231ddd4ca2641a06f`.
Exact CRD/narrow Role/signed image/two Ready replicas were verified. All 11 other
Pods, four terminal Jobs, prior schemas/claims authority/catalog/templates and
failed native ledger stayed unchanged; there were zero AgentSessions. The scoped
activity ended. Actual publish-stop-read acceptance remains separate. Provider capture, complete retention, native directory,
genuine stopped history and the owner pilot remain unaccepted; keep #174/#91 open.


**Review ruling (Tom, 2026-10-11):** Claude Code reviews are down and optional,
including releases. Record unavailable/failed/skipped outcomes without a pass
claim. Required CI and native independent review remain merge gates; do not
retry or repair Claude authentication as a prerequisite.

## Built #173 owned wrapper permission correction — 2026-10-11 UTC

[DESIGN-012](sagas/distributed-dev-env/designs/012-owned-root-wrapper-permissions.md)
fixes a bounded source mismatch: Git checkout inherits umask 077, while owned
generated wrappers require 0644. Only newly generated AGENTS.md/CLAUDE.md after
fresh owned checkout or proved-clean rules fast-forward receive that mode.
Exact bytes, UID, inode/link/path identity and current authority are proved
before fd chmod/fsync. Existing mode drift, foreign files and edited roots
remain preserved/refused. Private parents, repo/provider files and peer Git
state keep their permissions and state.

The full focused roots suite passed in 34.975s, refusal regressions in 1.294s;
gofmt, diff checks and native independent review passed. Signed publication
and a separately named reviewed model-free check remain delivery gates.
The first installed roots Job from haynes-ops #3815 failed before verifier
acceptance with zero restarts. Its internal cause remains Unknown; this offline
mismatch does not identify it. Attempt 1, both earlier bootstrap Jobs and the
native fixture/ledger remain retained. Protected runtime comparison passed and
activity ended. Logical roots count stays 1 of 3, with no retry from this source
change. Private admission/provider rule acceptance/MVP remain unaccepted.

## Built #173 private native Codex preflight — 2026-10-11 UTC

[DESIGN-009](sagas/distributed-dev-env/designs/009-private-native-codex-preflight.md)
defines the default-off private host preparation guard. Trusted catalog/root
proof and a writable private HOME replace shared preflight only when explicitly
enabled. A durable reservation precedes preparation; daemon absence, accepted
authority and original configuration are checked again at each write boundary.
Live, Unknown and unconfirmed intents preserve state without refresh or launch.
Original preparation identity survives startup/stop; confirmation follows
durable reservation cleanup. Keeper access-only renewal is unchanged.

Focused source checks and native independent review passed. Review corrected
the final absence fence, same-device mount aliases and concurrent configuration
drift. This does not install a private native API/profile or prove native rule
loading. Q-24 still awaits an owner ruling; no enrollment/pairing or fleet UX
change follows. Managed provider canaries need their own reviewed admission,
one-attempt execution and actual rule-loading acceptance.

The signed v2.14.0 roots image was promoted through
[haynes-ops #3815](https://github.com/thaynes43/haynes-ops/pull/3815), merge
`0be5071ff035059747a82a3e330f48cb445a1d2e`. New-session templates reconciled
with digest `e69e676786db9a0d5c14213730429a73425834a09b95aada4c8c365c70f72177`.
The first installed model-free roots check failed at
`first-project-refresh-command`, before verifier acceptance, with zero
restarts. Its Job/Pod and private receipt are retained; the internal cause is
Unknown. Offline Git proof separately establishes an umask 077/generated-mode
mismatch. Source correction #186 merged as
`9c5cd87fadacee98fd79edc97c6a74def76b198b`; roots count is 1 of 3,
with no retry. All 11 protected Pods, both earlier Jobs and the original native
ledger match the prepromotion baseline. The activity declaration ended.

#174 source [#183](https://github.com/thaynes43/dev-env/pull/183) merged as
`af80cfc8bc2bcb4cb28290ac110eff924308be76`; release
[#184](https://github.com/thaynes43/dev-env/pull/184) tags v2.15.0 at
`04fb03be3325065a27ff6e8894e6610d772fffed`. Operator and agent publications
passed signing/verification. GitOps haynes-ops #3816 merged as
`52d2bbc3ac8b1647e0abaf092a68516934235e80`. At 16:59:24 UTC the exact generated
schema was established and both operator replicas were Ready on signed digest
`def7dac13c94da7a31b943f4b8944c327aa0508e3cbbb9423bfe0c32021dc120`, with
retention=true as the sole flag delta. Ten non-operator Pods, all three retained
Jobs and the original ledger were unchanged; activity ended. Private admission
is false and AgentSessions zero. This accepts guard installation and upgrade
preservation, not actual retention or stopped-context reads.
After latches exist, preserve both the retention schema and a retention-aware
operator across rollback. Flag disable preserves latches; a pre-guard operator
downgrade is not established safe.

## Built #175 atomic explicit task ownership — 2026-10-11 UTC

[DESIGN-010](sagas/distributed-dev-env/designs/010-atomic-task-claims.md) defines
the first default-off ownership API. A separate coordination API group holds
immutable bounded claims, outside existing worker read grants. Explicit project,
repo and logical task key determine one full-digest identity across parents.
Atomic Create and uncached confirmation return 201/200/409 without transferring
ownership on a lost acknowledgement, stale heartbeat or absent/stopped owner.
Original Session UID, authenticated actor and saved catalog/rules are immutable.
Initial fetch pin and executor stop remain Unknown; heartbeat HEAD is reported.

An independent explicit project scope grants bounded claim metadata only.
Own live Session or verified coordinator direct-child identity can create;
legacy broad read/reap does not grant this route. No prompt, rules text,
transcript or authentication body is returned. No expiry, release, transfer,
automatic launch gate or inbox is implemented by this first unit.

Focused store/API/operator checks, necessary real CRD checks, affected lint,
codegen reproducibility and native independent review passed. Rebase preserves
#174 retention wiring; the operator command suite passed again after the two
mechanical wiring-file deltas. Signed publication, protected namespaced Role
and exact generated schema/API GitOps delivery remain separate. Installed
two-parent contention and durable messages/offline acknowledgements are still
required for #175. Keep #175/#91 open and preserve all sessions/fixtures/ledgers.

## Built #174 private-history preservation guard — 2026-10-11 UTC

[DESIGN-008](sagas/distributed-dev-env/designs/008-private-provider-history-retention.md)
defines the first retention source unit. Default-off
`--enable-private-history-retention` protects private HOME PVCs and durable
AgentSession identity on reap and suspended autoarchive. A persisted latch
survives flag disable. Original Session/HOME/executor identity cannot be erased
or rebound; one confirmed causal executor intent precedes creation. A lost
acknowledgement can adopt only that same verified executor. An absent attempted
executor remains PreservationUnknown, with its original record retained, and
cannot authorize replacement. No private stop proof or new retention TTL is added.

Focused checks across six affected packages, affected lint, schema/deepcopy
reproducibility and native independent review passed. Review corrected an
absent-executor status that misleadingly said Creating. Envtest has no GC
controller: tests use real API finalizer/deletion semantics and explicitly
model dependent deletes. Operator/schema publication and installed preservation
remain separate gates. No runtime, keeper lease, credential, failed-fixture or
ledger change follows from source tests.

The existing API adds bounded retention/provenance metadata under its current
ACL. Coordinator direct-child/sibling denial is preserved; ordinary session
callers still have legacy broader read/reap behavior. This is not a new peer
read-only authorization contract. Separate authorized stopped-context routes,
provider history adapters and broader discovery remain required for #174.
Do not close #174 or #91 from the guard alone.

## Built and released #173 permanent roots — 2026-10-11 UTC

[DESIGN-007](sagas/distributed-dev-env/designs/007-private-project-roots.md)
defines the source unit merged in
[#181](https://github.com/thaynes43/dev-env/pull/181) as
`e34ad2c07ab2c93b77e14f882adb058251ff8f90`. It creates owned Git-backed project
roots and detached private repo anchors, retains
catalog/rule/source provenance, and uses safe fast-forwards only after ownership
and preservation proof. Foreign, edited, ignored, divergent and interrupted
state is preserved. The roots flag defaults off; no native-host/UI conversion
or actual provider loading is claimed. Q-24 remains pending through the native
question tool; no phone-delivery receipt or owner ruling is inferred.

Release [#182](https://github.com/thaynes43/dev-env/pull/182) tags v2.14.0 at
`2b6d59682499456998200535caa2df5af74609b2`. Native exact-head review and required
CI passed; the optional Claude release review skipped without findings.
Signed image publication, new-session GitOps promotion and the model-free roots
check remain distinct from that release metadata. A private native Codex
preflight source slice is in progress with no pairing or fleet UX change.

## Built, deployed and bounded refresh accepted — #173 bootstrap — 2026-10-11 UTC

The fresh task branch starts at v2.12.0 (`1a83e9f`), including delivered #178.
[DESIGN-006](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md)
defines the next source PR: trusted catalog-wide private startup/bootstrap and
safe refresh, independent of shared preflight and locks. Preserve dirty/index
state, ignored files, local commits, worktrees, operation markers and ambiguous
authority. Record catalog/source revisions. Every new task still fetches and
pins its selected source; resume preserves WIP and saved rules.

Source [#179](https://github.com/thaynes43/dev-env/pull/179) merged as
`951eeafa43871825e6eb00ac2240fa31970b86ee` after exact-head required CI and
native independent review. The peer-worktree preservation finding was fixed.
The automatic Claude advisory failed before a verdict; its result/findings were
read, with no inferred cause or retry. Release
[#180](https://github.com/thaynes43/dev-env/pull/180) tags v2.13.0 at
`68c76e3c725a7478529ba061eb572994956bd966`. [Agent publication run38150512129](https://github.com/thaynes43/dev-env/actions/runs/38150512129)
passed smoke, keyless signing and exact-tag signature verification. Image digest
is `4ef99b35266064f9af5f1f4b86629001c56e6a47d9948707f09ff0ddee3e743a`.
[GitOps #3807](https://github.com/thaynes43/haynes-ops/pull/3807) merged as
`040101107e6122eb27ffb3d469709fb8ce3ec90b`; network, projects and new-session
templates reconciled Ready at that revision. Native independent bound-artifact
review and required CI passed. The GitOps advisory failed before a verdict.

The corrected [GitOps #3809](https://github.com/thaynes43/haynes-ops/pull/3809)
merged as `553367814d60ad9e15ebda2b05e921e7591bd3d6` after required CI and native
independent review. Installed explicit `agentd project-refresh` is accepted:
second one-shot check exit 0, exact full release commit/version, trusted catalog
UID/resourceVersion/content revision, clean fresh private dev-env source
`68c76e3c725a7478529ba061eb572994956bd966`, and receipt hash
`b6bbf6b7f049c642c506fd663976e980db9d45b087246cf3f899faeab97c4e9b`.
The admitted pod had no init containers, provider state or model turn and kept
its resource/deadline/placement/access constraints. Check Flux is Ready; a later
main revision may retain the same manifest bytes.

Attempt 1 is retained Failed: its verifier expected a 12-character commit,
while the released binary reports all 40. Attempt 2 passed; logical count is
2 of 3 and no further trial was run. Both terminal model-free fixtures remain
as explicit evidence. Protected pod identities/images/imageIDs/main and init
restarts and the original ledger UID/resourceVersion/hash match the baseline.
Both activity declarations ended after evidence capture. Private artifacts are
under `/home/dev/work/orders/dev-env-bootstrap-1011-runtime-2/`; publish no
private transcripts or authentication state.

This accepts the explicit refresh path against the current one-public-repo
catalog. It does not accept managed daemon startup, private GitHub auth,
permanent roots, native routing or actual Claude/Codex rule loading. Private
project admission remains disabled. #173 and #91 remain open.

Initial read-only verification confirmed operator `sha-7a634e5` and worker template
2.12.0 (the template is now 2.13.0 above) *(historical: the installed template is
now 2.20.0; see the [pickup brief](CLAUDE-RESUME-2026-10-12.md#what-is-actually-delivered))*,
two Ready operator replicas, private-project admission disabled and no
AgentSessions. The trusted catalog exists with one dev-env repository and empty
common-rule text. This does not establish actual provider rule loading.
Protected runtime identities and the failed fixture ledger have a private
baseline for delivery verification; no fixture or storage route is restarted.

The parallel finite Codex routing audit must distinguish complete native chat
isolation from isolated command executors. One display name retained across
sequential replacement is not concurrent routing proof. No enrollment or
credential copying, pairing challenge or daemon change is authorized by that
audit. Record its supported contract and ask one owner question if the fleet
UX needs a tradeoff. Claude, Codex and a future local provider each retain
independent session failure boundaries; local inference waits for GPU readiness
and household reservations.

## Owner scope reset — 2026-10-10, America/New_York

Tom clarified the purpose: upgrades preserve sessions, work spreads across nodes,
powerful access has guardrails, sessions/context are visible, and model usage/cost
is attributable. **Share rules, messages, task claims and authorized transcripts,
memory and handoffs; keep repos and task worktrees per pod.** Bootstrap missing
repos and fetch safely, with another selected-repo fetch/pin before each new task.

[ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)
controls conflicting earlier shared-Git prescriptions. The
[workflow guide](../docs/workflow-guide.md), [coordination audit](../docs/session-coordination.md)
and [plan 11](sagas/distributed-dev-env/backlog/11-project-workspaces.md) are the new
product map. A coordination pilot is distinct from v1 retirement. This reset
changes no live access, deployment, login or session.

Retire shared-Git issue #130 and unmerged haynes-ops#3737 as superseded; preserve
receipts and branches. Do not retry their stopped routes or claim a NAS failure.
The earlier retry cards received owner replies expressing confusion, not bounded
extension approval. The pending-question language in the historical checkpoint
below describes that earlier moment only. Native recovery #160 and task budgets
#154 remain relevant. Preserve the failed native fixture and original ledger.

The source audit found that live TUI messaging is not a durable inbox, current
log reads need a Ready pod, private reap preserves Git but deletes provider history,
and unavailable Codex cost can appear as zero. Next delivery decouples catalog/rules
and history retention from RWX, then builds peer discovery/context, claims/messages
and truthful usage. Publish no private transcript bodies or authentication state.
The prior 7–14 hour shared-storage MVP estimate is withdrawn; new units require
concrete acceptance and bounded checkpoints under the unchanged stall policy.

## First #173 source slice — 2026-10-10, America/New_York

[DESIGN-005](sagas/distributed-dev-env/designs/005-private-project-tasks.md)
defines private declared-project admission/preparation. Catalog snapshots supply
source identity and rules without assigning shared storage. New tasks validate
the selected clone and pin a freshly fetched remote commit; resume preserves
WIP and its original snapshot. Unsupported new budget-bound private dispatch
is refused before reservation. Existing shared/budget authorization stays guarded.

Focused source checks, independent review, required CI and image/GitOps receipts
are recorded on [#173](https://github.com/thaynes43/dev-env/issues/173). Source
tests do not establish actual provider rule loading. Promotion keeps the new
private-project admission flag disabled and preserves running sessions, the failed fixture and
its ledger. Catalog-wide startup/bootstrap, permanent local project roots,
remaining clients and both-provider/two-pod acceptance keep #173 open. The next
finite source slice is local startup catalog bootstrap/refresh with preservation
checks. No retired storage investigation or native fixture is rerun.

---

*The front-door paragraph that stood here moved to the top of this page on
2026-10-12.*

## Overnight delivery, 2026-10-10 UTC

**08:37 UTC execution checkpoint:** source release **2.11.0** is published and signed.
The v2 operator is deployed, 2/2 Ready with zero restarts. Retained budget
storage and the isolated native-host admission profile are installed. The
one-shot native fixture ran and failed: preflight/profile checks and budget
creation passed, but the client returned no valid native acceptance result.
The client ran through a separate `kubectl exec`; PID 1 was a finite sleep,
so the container's exit code does not report client acceptance. Its original
container terminated; the failed Pod and ledger are retained.
Missing captured stderr leaves the failed stage Unknown. No native readiness,
owned-stop or ledger StopConfirmed is claimed. Read the
[morning test guide](../docs/morning-test-guide.md) for the usable subset and
the remaining MVP gates, and the
[overnight results](../docs/trials/2026-10-10-overnight-results.md) for timings
and failures. Storage observer and helper repair routes are stopped after
three failures each; native owner questions are pending, with no claimed
phone delivery or answer. Do not start a fourth attempt or erase their history.

One real v2 Claude task succeeded with two turns and exit code 0, using exact source
`739872fc`. The reviewed namespace injection fix
[haynes-ops#3741](https://github.com/thaynes43/haynes-ops/pull/3741) was applied;
the admitted task had zero init containers and CPU/memory limits. Supported
cleanup verified the session, Pod and PVC absent. Its final sentence was not
retained and rescue list was empty, so rescue and private-home retention remain
unverified. The morning guide names the existing built CLI and public CA.

The signed native image inherits `/home/dev/.codex`; the fixture's private HOME
requires its own explicit CODEX_HOME. [#170](https://github.com/thaynes43/dev-env/pull/170)
merged after required checks and actual advisory passed; it fixes the source
profile and adds independent regression checks. This concrete
defect does not identify the discarded historical error. Do not claim a new
native pass: no replacement fixture or campaign was run and the deployed
profile has not been updated. A private source-only diagnostic candidate
preserves bounded redacted stderr for a future reviewed trial.

Tom authorized continued work overnight toward a morning-testable MVP. Codex owns
implementation, review, delivery and the test guide. Do not wait on the earlier
hosted-CI or gateway-account question cards for ordinary authorized work.
Computer pairing and a real phone question/answer remain owner acceptance steps;
do not start an expiring challenge while he is asleep. The Windows project-list
visibility issue is a minor client workaround, not a delivery prerequisite.

The complete reviewed AgentSession schema is applied through
[haynes-ops#3722](https://github.com/thaynes43/haynes-ops/pull/3722), with exact source
schema equality and unchanged protected pod identities, images and restart counts.
The earlier three local Flux-selection failures remain closed history; one
reviewed hosted-CI route delivered the update. Shared-workspace, scoped-host and
child-decision runtime features remain off until their dependencies and actual
acceptance pass. The keeper stays independently pinned and Ready; account login
is separate from computer pairing.

Existing Proxmox SSH and its documented read-only sudo route work. The gasha01
VM is running, but QEMU guest-agent ping is unavailable and the existing hardware
key was refused once each for the two documented candidate guest accounts. Those
routes are closed; do not guess another username or repeat them. Guest SSH is
not required to inspect an existing NFS export from a disposable client.
[Haynes-ops#3725](https://github.com/thaynes43/haynes-ops/pull/3725) is reviewed and
merged for one CPU-limited, read-only NFS metadata Job. It writes no export files,
has no provider credentials, and preserves uncertain termination as Unknown.
The actual read-only check passed for UID1000 over NFS4.2; root mode permits
write/search in DAC, while server write authorization and quota remain untested.
A webhook injected an init container without limits; both containers terminated,
but the capsule did not meet the every-container-limit contract. Preserve that
deviation and disable injection in a reviewed successor. Its Job, pods, PVC,
PV and child Kustomization are now verified absent after reviewed inventory
recovery and retirement in [haynes-ops#3730](https://github.com/thaynes43/haynes-ops/pull/3730)
and [#3735](https://github.com/thaynes43/haynes-ops/pull/3735). The separate writable
comparison must pass before selecting storage. Capacity alone is not a permissions, quota or
performance pass.

The accepted [budget requirement](sagas/distributed-dev-env/requirements/2026-10-09-task-budgets.md)
remains mandatory: 60 minutes without evidenced progress or three failures at the
same unresolved blocker, across agents, pods and resumes. Owned foreground
executor, retained ledger/API, authenticated native readiness, managed-worker
enforcement and isolated inspector merged in
[#162](https://github.com/thaynes43/dev-env/pull/162),
[#163](https://github.com/thaynes43/dev-env/pull/163),
[#166](https://github.com/thaynes43/dev-env/pull/166),
[#167](https://github.com/thaynes43/dev-env/pull/167) and
[#168](https://github.com/thaynes43/dev-env/pull/168).
The [API design](sagas/distributed-dev-env/designs/003-task-budget-api.md) and
[isolated profile](sagas/distributed-dev-env/designs/004-isolated-native-lifecycle-fixture.md)
record the exact binding and pilot limits. Production opt-ins remain off;
the staged operator admits only the fixed zero-task fixture. Missing provider
usage is labelled Unknown or estimated and cannot disable time/attempt limits.
Current managed failure evidence covers nonzero container exit; arbitrary
native/tool failures, progress provenance, whole-campaign stop and actual phone
delivery still need acceptance. A successful fake process test is not a
native-host result. Keep [#154](https://github.com/thaynes43/dev-env/issues/154)
open until the full requirement passes.

Use the [workflow guide](../docs/workflow-guide.md) for the product and feature
set. Each further implementation unit needs a finite outcome, deadline and
checkpoint. Preserve blocker history; do not restart the same investigation with
a fresh agent or pod. Keep v1, its enrollment, the keeper refresh owner and the
existing shelf unchanged. Source changes require reviewed PRs, exact-head checks
and GitOps delivery; no local image builds or CPU burners on shared nodes.

*Expired: this 2026-10-10 delivery window has closed and grants no current
authority. The current order is the pickup brief's six-step roadmap.*

The coordinator's current delivery window is 05:29–09:30 UTC with at most eight
hours of aggregate child effort. At the morning checkpoint, report the verified
workflow and precise remaining gates, preserve WIP and stop new implementation
unless Tom steers further. This is a finite delivery window, not permission for
silent extensions. Earlier checkpoint records below are historical wherever
this section supplies newer evidence.

## Active delivery to an owner-testable v2, 2026-10-09

**Resumed checkpoint, 2026-10-09 America/New_York:** the owner refreshed usage
and released the temporary PC-visibility hold. A device test found asymmetric
project-list visibility: Windows-created remote projects appear on Windows,
MacBook and iPhone; MacBook-created projects are missing from Windows while
their chats remain accessible. Registering the existing remote folder from
Windows is a workaround. The working pod host was not restarted or re-paired.
The owner considers this minor; do not make it a v2 prerequisite.

Project preparation #153 is source-merged at `2e1c395`. The workflow guide and
owner-test budget contract merged in #156 (`4c2df47`). Fresh keeper account login
succeeded and reports `Ready`, generation 1; native process-group absence and
private staging/reservation cleanup passed. Computer pairing remains separate
and pending. The v1 host was not restarted or re-paired.

**Funded review checkpoint, 2026-10-09 America/New_York:** one authorized Claude
Code reviewer finished in 5m54s within its 20-minute cap. It identified child
answer provenance and heartbeat clock skew in #155, and startup recovery status
in #157. Host source #157 merged at `ed64941` after all required checks and a clean
actual final advisory review. Child #155 fixes passed focused checks, but the
automatic review found an additional status-concurrency race and unrelated-outcome
clobber. Child source now preserves unrelated status writes while comparing the
exact answer authority, and changes only an outcome owned by that decision.
Repeated question heartbeats must preserve the first escalation timestamp and
avoid optimistic locking when their final outcome does not change. Required
checks and actual advisory findings govern source merge; runtime and phone
acceptance remain separate. Ordinary
automatic reviews then completed with actual public findings for #156 and
haynes-ops #3693. Review delivery #158 is resolved; the three prior failures
remain history and were not rerun. The missing Q-23/D-84 saga decision-log row
found by the automatic documentation review is included with #155.

Retained host source distinguishes incomplete startup history as `NeedsReview`.
It cannot safely rearm an already-absent host by inventing a stop acknowledgment
or deleting its receipt. [Issue #160](https://github.com/thaynes43/dev-env/issues/160)
records the separate evidence-bound recovery decision. Computer pairing and
runtime host acceptance remain pending.

Required [task-budget enforcement](sagas/distributed-dev-env/requirements/2026-10-09-task-budgets.md)
remains open in #154: 60 minutes without progress OR three failed attempts at the
same blocker, whichever comes first, across agents/pods/resumes. A retained
managed-task ledger is a useful next source unit but cannot cover native
coordinator turns alone. A bounded pinned-source check found that native hooks
and selected-turn cancellation do not establish universal future admission
control. Do not call the first owner workflow ready until coordinator coverage,
trusted accounting and native phone escalation are proven.

*Withdrawn: #130 closed as not planned, haynes-ops #3737 closed unmerged, and
[ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)
removed the storage prerequisite. No storage trial follows from this paragraph.*

The [bounded NFS comparison](../docs/trials/2026-10-09-external-storage-comparison.md)
is the next storage candidate. Reviewed haynes-ops #3693 merged and its exact
TCP/9283 rule is observed in the live policy. One bounded external Ceph metrics
read returned HTTP 200 in 21 ms: WARN health, 31/32 OSDs up/in, and observed OSD
latency gauges of 0–4 ms. Current reachability is proven; continuous sample
freshness and gateway metrics are not. Export settings, permissions/quota and
approved gateway access remain preflight gates. A native question requests that
specific read-only access path; no answer or phone-delivery confirmation is yet
recorded. Do not start another storage trial automatically.

The runtime audit found a stale AgentSession CRD: GitOps/live lack merged
workspace and private-home retention fields. Synchronize the complete reviewed
source CRD before the operator pin that needs it. Accepted project catalog,
its named GET permission, workspace settings and test-host manifests are absent;
shared/coordinator/Codex/decision features remain disabled. Preserve the keeper's
independent `sha-257287a` pin, broker, v1 and shelf during promotion. This checkpoint
supersedes the dated `NeedsLogin`, stopped-review and draft-#3693 facts below;
other runtime and cutover gates remain open.

The exact source CRD copy is prepared locally. Direct rendering and source schema
tests passed, but three attempts to select its local Flux test failed. That route
stopped under Q-23/D-84. A native owner question proposes one hosted Flux CI run
with a ten-minute checkpoint; its answer is pending. Do not silently substitute
another test route, publish the schema PR or deploy it before that bounded next
step is authorized and required checks pass.

*Superseded: Claude took over v2 on 2026-10-12 under the
[pickup brief](CLAUDE-RESUME-2026-10-12.md), and ADR-003 replaced the
shared-project route and #130's storage diagnosis.*

Tom clarified that Codex must continue until v2 provides a workflow he can test.
The documentation checkpoint did not end that assignment. Read the
[active delivery milestone](handoffs/2026-10-09-testable-v2.md): existing native
apps/CLI, two distinct Codex hosts sharing projects, managed fresh-source tasks,
real project/repo rule loading, owner-safe resume/rescue and phone questions.
Source work proceeds disabled by default while #130's storage diagnosis and
acceptance are completed. V1 remains available; full parity and cutover gates
still apply. Codex owns this work rather than leaving it for another implementer.

**Actual keeper rollout, 2026-10-09 23:46Z:** signed source #148 and haynes-ops
#3687 delivered keeper-only DNS and login-helper corrections. Exact artifacts,
admission and bounded connectivity checks passed; PVE minting remains disabled.
The fresh native device login presented a challenge, then expired without
credential adoption. Process-group absence, staging cleanup and reservation
clearance were verified; auth status is `NeedsLogin`, protected peers stayed
unchanged, and the activity ended. The owner prompt has no reply and its code
has expired. Start a fresh ceremony when the owner is available, rather than
reusing that code or automatically repeating login attempts. Managed Codex
runtime sessions and the two remote hosts remain disabled.

Strict-stop #140, catalog/rules #145, private-home retention #147, project CLI
and trusted catalog checks #150, and child-scoped API/managed Codex #152 are
merged source units. The model-free project Job is under final source review;
durable child decisions and retained Codex host supervision remain in progress.
Node Yama policy prevents the syscall evidence prepared in #146. The separate
in-process Git-marker fixture #149/#151 passed hosted checks and publication at
source `0e0931ad`; independent signature/admission/telemetry checks remain before
a bounded storage observation. No new storage trial follows automatically from
these source changes. Resolve #130 and the independent real-client,
lifecycle, host and phone gates before claiming the owner-test milestone.

> **Superseded by [ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)
> (2026-10-10).** The shared-workspace, CephFS diagnostic and D-80/D-81 shared-file
> prescriptions in the next three paragraphs no longer gate delivery. Repositories
> and task worktrees stay per pod. Kept as history.

**Latest delivery checkpoint, 2026-10-09 22:13Z:** shared-workspace core #135
merged `316c03c`; keeper-owned Codex auth #138 merged `bcb2599` and bounded
single-refresh #143 merged `f810766`. These are source
units with runtime opt-ins still off. Strict executor-stop rescue #140 merged
`7b3ce4c`. Its private-home archive/reap is deliberately blocked: task-only rescue
does not preserve provider enrollment/history. A retained-home detach route is
still required before shared reap acceptance. Catalog/rule implementation follows
[D-79](../docs/shared-project-catalog.md); catalog primitives merged #145 `084b66a`;
management/provider integration is in progress. Managed Codex execution and two
retained coordinator hosts remain unbuilt. No running agent or v1 restart follows from
these source merges.

The [third storage diagnostic result](../docs/trials/2026-10-09-cephfs-cost-result.md)
is incomplete. Peer Git status timed out inside index refresh without CPU
throttling; its exact filesystem wait is unproved. Normal UID-safe Job cleanup,
GitOps claim/PV cleanup and a five-minute post-window completed; all seven
protected pods were preserved and activity ended. Missing required telemetry and
60–70s household sampling intervals keep household acceptance open. #139 fixes
interrupted measurement receipts source-only. The older two-trial/prepared
statements below are dated history; no further run launches automatically.

D-80's [first host route](../docs/codex-coordinator-hosts.md) defines two retained
Codex coordinators with read-only shared files and dedicated child-scoped API
identities. Build that caller boundary, managed Codex native launch/resume,
retained host supervision and actual pairing/phone acceptance under plans 04/11.
Native app threads remain on their selected host; forwarding is later work.
D-81's [private-home retention](../docs/shared-private-home-retention.md) must be
implemented before shared reap: verified task rescue does not export provider
state. V1 stays available; no runtime host, drain or cutover is accepted here.

## Workflow guide, revised projects and shipped freshness, 2026-10-09

Read the [workflow guide](../docs/workflow-guide.md) before further architecture
work. It now draws the user journeys first, with two rendered images and four
Mermaid diagrams, working quick starts, feature states, and cutover gates.
The built orchestrator is the operator API/controller; `agent-run` or an agent
is its requester. There is no separate requester-agent service. The planned
console is unbuilt; the normal management surface still needs workflow review.
Owner questions must arrive as native phone prompts, not get buried in comments
or documentation.

> **Superseded by [ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)
> (2026-10-10).** R7's shared workspace files, ADR-002/Q-21's RWX workspace and the
> CephFS trial in the next three paragraphs no longer gate delivery; #130 closed as
> not planned. Repositories and task worktrees stay per pod. The Q-22 node-trust
> facts in the third paragraph still stand. Kept as history.

Tom supplied [project requirements R1–R6](sagas/distributed-dev-env/requirements/2026-10-09-project-roots.md)
and added R7: multiple Codex remote links across pods sharing workspace files.
Both providers use one project concept; R2 supersedes Q-20. The supplied v1
baseline is `~/codex/<project>`, outside swept `~/work` tasks. Manual anchors
exist; haynes-ops #3633 was open/unmerged at audit. Catalog/sync/add, generated
trust, both-provider project rules, task-rule propagation, cross-pod ownership
and shared workspace storage are not built. [Plan 11](sagas/distributed-dev-env/backlog/11-project-workspaces.md)
and D-74 require revision of plan 04's single-hub and D-15/D-22's private Git/task
assumptions. Do not implement a single replacement hub as if it satisfied R7.
The primary management UI and credential migration are not selected.

Read [Accepted ADR-002](sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md)
for the concrete topology and storage alternatives. Q-21 ratified dedicated RWX
workspace storage with private provider homes and explicit task ownership.
Expiry alone cannot fence a partitioned writer; uncertain ownership blocks
transfer and reap. Tom answered the structured Q-21 prompt on 2026-10-09:
"Accept ADR-002 and the bounded CephFS trial (Recommended)". The bounded trial
is authorized first; normal rollout requires the delivery gates. ADR-001 is
unchanged, with its affected storage/cloning decisions superseded by ADR-002.
Phone delivery remains unverified; an owner answer does not establish device
delivery, and the earlier probe explicitly had a desktop reply.

[Node trust](../docs/keeper-node-trust.md) is installed on all five PVE nodes.
Tom answered Q-22, "Delegate the trust installation to Codex" (D-76). Haynes-ops
#3647 (`9dabb4d5`) delivered only the saved public key through a bounded Job and
pipe to the existing accounts. Each guarded append preserved the original key
bytes and passed standing-key SSH/read-only PVE checks. #3651 (`b8f83ff9`) removed
the temporary delivery app; its Job/pods, child Kustomization and ExternalSecret
are gone. Owner-policy cleanup is corroborated by fresh Secret metadata: the
public target is absent while the keeper-only CA target remains present. Cleanup
checks used metadata only. Backups/receipts remain on the nodes. Seven protected pod identities,
images and restart counts were preserved; minting stays off, private CA keeper-only.
The [bounded CephFS trial record](../docs/trials/2026-10-09-cephfs-feasibility.md)
records two incomplete attempts, cleanup and positive latency signals. The first
harness startup defect is fixed; the corrected run exceeded the original 15s
peer Git status cap. [Issue #130](https://github.com/thaynes43/dev-env/issues/130)
holds the specific diagnosis and next bounded proposal. No third run or normal
workspace rollout follows automatically. Neither task advances composite parity
rows by itself; provider/rule/host/fencing and household acceptance remain open.
Source-only [#131](https://github.com/thaynes43/dev-env/pull/131), `191b8c9e`,
now preserves timed-out command measurements in failure receipts. Its finite
simulated checks passed; this revised helper has not run in the cluster and does
not resolve the slow Git operation.

The independent stale-start fix is **shipped**: dev-env #124 merged
`759f084f`, release #123 produced **2.9.1** at `b6c0f3c`, and publish-agent run
`37944933337` passed smoke and exact-tag signing verification. Haynes-ops #3636
merged `b6e858b5` and reconciled only agent templates/shelf. Both use:

```text
ghcr.io/thaynes43/dev-env:2.9.1@sha256:a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf
```

A CPU-limited worker Job ran the published `agentd` binary against local Git
fixtures: failed fresh fetch produced zero launches, creation used a pinned
commit across a moving ref, offline resume preserved WIP/conversation, and
stopped-rebase resume preserved detached HEAD and operation state. All four
passed in five seconds at the stubbed model-launch boundary. The admitted final
pod had no injected init, no credentials/token/PVC, and no model/API calls.
Both trial Jobs/pods were removed and both activity declarations ended.

At final runtime verification `2026-10-09T14:52:53Z`, templates/shelf were Ready;
new shelf UID `572bd232-c8b4-4caa-8a68-8bdc8b0891eb` had zero restarts. V1 UID
`cf7abc47-0363-491a-9420-12db9731ea8d` and all five controller identities/images
were preserved, Ready, zero restarts. Sessions remained zero and grant/policy/job
counts unchanged. Controller pins remain `sha-eeb15e3`. This is v2 agentd
fresh-source/resume protection, not acceptance of the new shared-project flow.
Full [delivery and scope record](handoffs/2026-10-09-workflow-session-guide.md).

**Keeper CA projection shipped:** haynes-ops
[#3642](https://github.com/thaynes43/haynes-ops/pull/3642) merged `4bccaa899` at
`2026-10-09T16:15:42Z`. The keeper-only ExternalSecret selects the existing
item's private Base64 field once and public field unchanged; it became
`Ready=True / SecretSynced` at `16:16:02Z`. Keeper-only Flux/Helm reconciliation
passed. New keeper UID `8b5be82c-e9f3-437a-821e-328a15a0330a` is Ready, zero
restarts, unchanged controller image, with the read-only `/etc/dev-env-keeper/ssh-ca`
mount, mode `0440`, and no injected init container. Its existing CPU limit is
`200m`. Both PVE enable flags remain absent/default false. V1, shelf, operator
and broker UIDs/images/restarts were preserved; sessions/jobs/policies remain
zero. The activity declaration ended; no fixture was created and no key values
were accessed. The root check confirmed ESO status, keeper readiness and v1 UID.

CA storage, projection and PVE node trust are complete. Key-pair parsing,
private target/host-key/egress configuration and real-provider acceptance remain
pending. Disabled keeper readiness proves GitHub issuance, not CA validity.
All twenty parity closures remain open. Headlamp migration/retirement and Q-16
remain under their existing owner rulings. No v1/session restart occurred.
The historical parking evidence below remains dated baseline context.

## Parked for the Codex reset, 2026-10-08 America/New_York

Tom requested a safe stopping point and a reset handoff with 7% usage remaining.
Read the [reset work order](handoffs/2026-10-08-codex-reset.md) before resuming.
Q-19 is complete by owner confirmation: the fresh CA fields are saved in existing
`HaynesKube/dev-env`. No values were sent to or read by this coordinator. Do not
ask for another CA or another item. The next owner step is Proxmox node CA trust,
after concrete instructions are prepared; the original work order assigns that
step to Tom unless he delegates it. Keeper CA projection and minting remain off.
Twenty parity gaps still block cutover; Headlamp stays until guarded parity and
caller migration are proven.

## Target clarification on 2026-10-09 UTC

Tom confirmed that guardrails may be needed and Headlamp should go away once
guarded parity is reached (D-71, README decision 47). The target is a guarded
replacement for every accepted v1 task, including owner-directed Headlamp work.
Headlamp is a migration fallback, not the final architecture. Prove the replacement
and migrate callers before retiring it through GitOps. No approval route or blanket
standing admin access is selected. The existing owner requirements remain. The
Q-19 owner storage step is complete: Tom confirmed both fresh CA fields saved
in the existing `HaynesKube/dev-env` item (D-72). Instructions are in DESIGN-001
section 6.12. Projection and delegated PVE trust are now delivered, as recorded
above. Offline key-pair validation has now passed after keeper-only source
#133 (`3b0d043`) and deployment haynes-ops #3669 (`ab023471`): one invocation
returned exit 0/all validation booleans true, with other six protected pods
unchanged and activity ended. Configuration and real certificate/provider
acceptance still gate activation; no key values are exposed.

## State on 2026-10-08

*Historical state. Its work order (plan 02, approvals, parity, keeper minting)
and its note on the 2026-10-12 weekly-limit reset are historical. The current
order is the pickup brief's
[six-step roadmap](CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap).*

- **dev-env v2 resumed on Codex on 2026-10-08**, under Tom's coordinator work
  order. Work proceeds in order: finish plan 02, assess approvals inside the
  Claude Code app, check v1 capability parity, then build keeper SSH minting.
  The Claude weekly limit still resets 2026-10-12. Resume at:
  - **Plan 02:** steps 1 to 11 and acceptance items 1 to 3 are done and deployed
    (#94, [backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md)).
    Step 12's laptop client (D-68) is built in #103 and released as signed
    agent `2.8.0` (#104). Its operator image is signed `sha-12a97c4`.
    [haynes-ops #3579](https://github.com/thaynes43/haynes-ops/pull/3579)
    deployed both pins; Flux, rollout and fleet checks passed. **Tom corrected
    the laptop requirement on 2026-10-08 (Q-17, README decision 45):** he uses
    `agent-run` or asks agents to start sessions; a web UI is another possible
    session-management client. Q-17 is withdrawn. Step 12 is delivered as an
    optional external path, with no claim of a real external-machine run.
    **Plan 02 is done under that corrected scope.** Step 13 passed with idle
    local session `dev-env-1008-125420`: create, list, show, attach, detach and
    reap through v1's existing CLI identity. Rescue `20261008-1256` was
    `CleanAndPushed`; the operator archived it and removed its pod and home
    volume. The v1 and shelf UIDs and every restart count were preserved.
    No laptop kubeconfig ceremony or test blocks progress. The
    [external CLI instructions](handoffs/2026-10-08-laptop-access.md)
    are optional.
  - **Plan 07:** steps 1 to 5 are built and H2 (the broker Deployment) is deployed
    and verified. **The docs-only approval spike is complete:**
    [R-03](sagas/distributed-dev-env/research/R-03-claude-code-approvals.md)
    records the native hooks, managed policy, forgeable soft gates and the
    guarded route's unresolved phone, authority and shared OAuth checks.
    **Q-18's earlier premise is withdrawn (D-70):** the Headlamp route already
    gives agents cluster-admin task scope on Tom's live directive for the task or
    access scope. D-71 targets a guarded replacement, then Headlamp retirement.
    No approval route is selected or enabled.
    The complete v1 parity audit is recorded in
    [R-04](sagas/distributed-dev-env/research/R-04-v1-capability-parity.md).
    Twenty gaps in backlog/07 block cutover. The audit's OPERATOR-only distinction
    was corrected after Tom pointed to Headlamp: effective parity includes accepted
    Headlamp exec/ServiceAccount workloads and GitOps self-merge. Direct grants,
    expiry and attribution are mechanism enhancements, not new Kubernetes powers.
    No blanket direct admin grant is authorized. The omitted baseline references are
    restored and verified after the
    rescue-mount fix (#108, haynes-ops #3584/#3583). A fresh full session passed
    presence/ADC permission checks and read-only Omni/API calls, then was rescued
    and removed. PVE LAN access and the remaining service checks still block
    full closure. D-69's Proxmox backend is built with minting disabled: broker
    jobs, keeper SSH/journal/cleanup and typed agentd/PVE support. Key-pair validation,
    private targets/host keys/egress, standing policies and real provider acceptance
    still block its activation. General hw-ssh also needs P-19's connection/revocation
    contract. See the 2026-10-09 delivery checkpoint above for installed node trust.
    No approval implementation choice blocks this work. Q-16 was ruled on 2026-10-08
    (parity first; approvals inside the Claude Code app), and PR #90 and haynes-ops
    #3550 were closed because their Pushover and web approval surface is ruled out.
    The resume point is the last coordinator comment on
    [issue #91](https://github.com/thaynes43/dev-env/issues/91). The spike proposes
    a bounded plan 03 login/Remote Control prerequisite before testing a guarded
    approver; no v1 refresh token is copied. Draft code: branch
    `agent/plan07-approvals-round3` (dev-env) and `agent/plan07-catalog-round3`
    (haynes-ops).
  - **Latest deployment, 2026-10-08:** operator, broker and keeper use reviewed
    main `sha-eeb15e3` (#111), signed/verified in publish run `37798469467`.
    Their updated/ready/available counts are 2/2, 2/2 and 1/1. Agent template and
    shelf use signed `2.9.0` (#110, publish run `37799418992`). The shelf's stale
    2.7.0 pin was updated with the template, as its deployment instructions require.
    Haynes-ops #3589 deployed and verified schema/RBAC/admission/journal first;
    #3595 applied the image pins at `d7b845a6`. Minting stays disabled, with no CA
    mount, trust, egress or policy enabled. P-14/P-15/P-18 remain open for activation
    and real provider acceptance. General hw-ssh remains unbuilt.
    An idle full S session on 2.8.0, `dev-env-1008-152312`, kept its pod UID and
    zero restarts through the controller upgrade. It remained Running and was
    correctly marked Outdated; v1 kept its original UID and zero restarts.
    Fresh full S session `dev-env-1008-153405` started at `2.9.0-5030ab2483` with
    zero restarts. Its grants directory is tmpfs; typed credential listing returned
    `[]` and Proxmox availability exited 4 without provider access. No prompt or
    inference was sent. Both fixtures were rescued as CleanAndPushed, archived and
    removed with their pods and home volumes. The fleet is empty; only the Ready
    shelf and shared volume remain. All five runtime Flux targets and Helm releases
    are Ready. Keeper GitHub readiness passed; no CredentialJobs exist. The scoped
    activity declaration was ended. CA projection, owner node trust and real provider
    acceptance remain required; CA storage is now owner-confirmed complete. Q-18's
    old prompt is withdrawn, with no route selected; it does not hold the next owner
    node-trust step. Ask the owner steps one
    at a time when they arise, with exact item/field names and no secret values.
    The earlier baseline restore (#3584/#3583) remains applied and verified;
    PVE LAN access and the remaining service checks still block full closure.
    New dependency PRs appeared after the bench; green
    release PR #102 (2.7.1) was merged, signed and included in the signed 2.8.0
    release. Issue #91 remains open; #90 and haynes-ops #3550 remain closed.

## State on 2026-10-07

*Historical state. "Next is plan 02" and plan 07's "next step" below are past;
plan 02 is done. The current order is the pickup brief's
[six-step roadmap](CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap).*

- **Phase 1 is built: plan 01 is done (2026-10-07, #60).** From the v1 pod,
  `agent-run` (built from main) started task session `dev-env-1007-045709` on
  talosw02 at size M. It ran a real docs task on the static token and opened #57,
  which merged. An operator rollout restart during the task left its pod alone (same
  UID, no restart). A reap of a session with an uncommitted file wrote a verified
  bundle to the shared volume that gives the file back, and only then archived the
  volume. A session that cannot fit stayed Pending, and `agent-run` printed the
  scheduler's reason in 4 s. The session pod reached the web but no LAN address and no
  service outside the platform tier, and the guard refused its writes in the dev-env
  namespaces. S-8 is done: `gasha01-rbd` is 1.55 to 1.81 times slower than
  `ceph-block`, so every size stays on it. Plan 01's Acceptance lists the evidence
  (pod UIDs, nodes, the bundle path). The v1 pod and `dev-env-ops` did not restart.
  **Next is plan 02** ([interactive sessions and lifecycle](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md)).
- **Plan 07 round 1 was deployed and verified** (2026-10-07, coordinator work
  order after README decision 40). Steps 1 to 5 are built: CRDs (D-54),
  `/v1/grants` (D-56), broker mode (D-61), kube installation into a session's
  memory-backed grants volume (D-63), and egress grants with the operator's expiry
  backstop (D-64). Signed agent `2.5.0` and operator/broker `sha-763fe77` are
  deployed ([haynes-ops #3542](https://github.com/thaynes43/haynes-ops/pull/3542)).
  One idle local session verified baseline denials, private tmpfs files, kube
  scope/context, token revocation and egress release. An actual ten-minute egress
  grant expired with the broker stopped and the operator restarted after approval:
  the operator deleted its policy at 23:32:14Z while the audit record stayed Active.
  Restoring the broker ended it Expired; the session was reaped and its home PVC
  and temporary fixtures removed ([haynes-ops #3544](https://github.com/thaynes43/haynes-ops/pull/3544)).
  Session and v1 pod UIDs/restart counts were preserved through every rollout.
  Existing pods gain the grants volume on a later resume; rollouts do not restart them.
  Step 6 (human approval and the break-glass catalog remainder) was the next step;
  since 2026-10-08 it is redesigned (Q-16, see State above). Standing policies are the only approval path until then;
  the temporary smoke policies are gone. No new secret or Tom-only step was needed
  for this round. Q-15 remains ruled (A), so step 8's minting identity is settled.
  [backlog/07](sagas/distributed-dev-env/backlog/07-access-broker.md) tracks the remaining scope.
- **What the run found and fixed (2026-10-07).** haynes-ops still had #24's CRD, so
  the API server pruned the heartbeat's task result and the rescue verdict from
  status; haynes-ops #3502 synced it before any reap. A PR here that changes
  `config/crd/` now needs its haynes-ops copy before the operator pin that writes the
  new fields (CLAUDE.md, layout). The advisory review failed on release PRs, and
  release-please used the deprecated `app-id` input; #59 fixed both.
- **The session that waited for plan 02 is archived.** `dev-agents/dev-env-1007-050756`
  was the Pending check (size L, via haynes-ops #3504, reverted by #3505). Plan 02's
  step 11 rescued and archived it (D-51); its home volume is gone.
- **The dev-env v2 design is complete.** It covers the architecture (ADR-001), the
  details (DESIGN-001, D-01 onward), 16 spikes, backlog plans 00 to 10 and two
  research notes.
- **Q-01 to Q-16 are ruled.** Tom answered Q-01 to Q-11, Q-13 and Q-14 on
  2026-10-06, Q-15 on 2026-10-07 and Q-16 on 2026-10-08 (index below). Q-12 (branch protection) was settled on 2026-10-07: Tom
  made the repo public (B) after Actions billing stopped CI on the private repo. The
  settings only Tom can click are in a handoff for an agent on his laptop (below).
- **The repo is public since 2026-10-07.** Everything committed, history and PR
  branches included, is published, so keep secrets, household details and
  transcripts out (CLAUDE.md, hard rules). On 2026-10-07 gitleaks and a pattern grep
  over every branch and PR ref found no secret. The `@claude` workflow now runs only
  for the owner, members, collaborators and haynes-dev-bot. The code is MIT
  ([`LICENSE`](../LICENSE)); [`images/THIRD_PARTY.md`](../images/THIRD_PARTY.md) lists
  what the images bundle (the agent image carries proprietary Claude Code), and
  [#55](https://github.com/thaynes43/dev-env/issues/55) put the upstream license texts
  into the images (`/usr/share/licenses/<tool>/` in the agent image, the Go module
  texts and this repo's `LICENSE` and `THIRD_PARTY.md` in both; CI checks the paths).
- **ADR-001 is Accepted.** Tom ratified it on 2026-10-06: "Accept as written".
  DESIGN-001 is Accepted with it.
- **KICKOFF B1, the Go skeleton, is built** (#14). The module, the `AgentSession` types and
  CRD, the four binaries and the Makefile are on main; only `agent-run version` does
  real work. D-38 records that the keeper is its own binary. B2 added `ci.yml` and the
  operator Dockerfile; B3 added `publish.yml`, which pushes and signs
  `dev-env-operator:sha-<short>` from main. The agent image (B5, D-53, #40) is built and
  smoke-tested by CI on PRs and published by `publish-agent.yml` from a `v2.x.y` release
  tag. 2.0.0 shipped on 2026-10-07: release PR #52 tagged `v2.0.0`, and the publish run
  pushed and signed `ghcr.io/thaynes43/dev-env:2.0.0@sha256:8bab980d6beea9eb8f1576d38a3fd9837150193414728baf1bcc3e07e28a371a`
  (`cosign verify` with the `publish-agent.yml@refs/tags/v2.0.0` identity passes).
  haynes-ops #3501 pinned it in `dev-env-templates`, with a Renovate regex manager
  for that line. The operator package is public (Q-13; an anonymous pull works since
  2026-10-07).
  **B4 added Renovate and release-please** (#18): one repo version on the agent image's
  `2.x` line. The release App is live since 2026-10-07, so release PRs run CI like any
  PR. Agents squash-merge a green release PR themselves (Tom, 2026-10-07: "Merge, and
  let agents merge releases"; CLAUDE.md). #52 was merged on the overnight work order,
  before that ruling, to unblock the first end-to-end run; #61 (2.0.1) was merged under it.
  Renovate auto-merge was off at B4 and is on since 2026-10-07 (the Protect Main
  ruleset exists; minor and patch Go modules and GitHub Actions only).
- **Plan 01 step 1, the `AgentSession` CRD, is built** (#24). The schema enforces
  the per-session rules as CEL, and spec is immutable after create except
  `operatingMode` and `lifecycle` (D-39). `make test` runs an envtest suite against
  kube-apiserver 1.35 (the main cluster's minor) that proves each rule;
  `internal/testenv` starts it, for later suites too.
- **Plan 01 step 4, agentd, is built** (#25, #27, #28). `agentd run` renders the
  config (the `dev-init.sh` port), clones the repo and adds the worktree, runs the
  task once in tmux session `agent` on the static token, heartbeats to
  `POST /v1/sessions/{name}/heartbeat`, and forwards the pod's SIGTERM to the CLI.
  `agentd ctl status` and `ctl rescue` answer the operator (D-40 to D-43). The
  operator's heartbeat route is step 3's; the pod that sets agentd's inputs is
  step 2's.
- **Plan 01 step 5, rescue, suspend and archive, is built** (#32,
  #36). `agentd ctl rescue` writes one git bundle per clone of the refs origin
  lacks, and `manifest.json` last, to `rescue/<session>/<stamp>/` on the shared
  volume, and checks them there; `--stop-agent` stops the CLI first (D-48). The
  operator runs it by exec before a suspend deletes a pod that ran, writes the
  verdict to `status.rescue` before the delete, supersedes it on a resume,
  and archives only a reaped session's volume, after a verified rescue of its last
  pod (D-51). A reaped session whose last pod is gone, ended or never started waits
  with `RemovalBlocked` for plan 02's rescue pod; plan 02 also has the archive
  timer, bundle pruning and the `RescueFailed` page.
- **Plan 01 step 2 is built** (#29, #30). `dev-env-operator` runs a
  controller-runtime manager whose reconciler builds each session's pod and volume
  from `dev-env-templates` (D-44: the format, placement, no probes, 60 s grace). It
  creates what is missing and never updates a pod or volume; envtest proves that
  across an operator restart and a template change. Deleting a session is a reap
  (D-45): a finalizer keeps the session and its volume until rescue, and the
  guarded deletes are the only ones; step 5 filled in the rescue (D-51).
- **Plan 01 step 3, the `/v1` API, is built** (#31). Every operator replica serves
  `POST/GET /v1/sessions`, `GET/DELETE /v1/sessions/{name}`, agentd's heartbeat
  route and `GET /v1/fleet` over HTTPS on 8443, each call checked by a TokenReview
  for the audience `dev-env-operator` (D-46). Callers are Tom's `dev-env-human`,
  trusted clients (the workbench, and the v1 pod until cutover) and sessions by
  their pod's token; the token sets a new session's parent, depth and profile. A
  reap deletes the session, which the rescue finalizer holds (D-45). `agent-run`
  (step 7) imports the wire types from `internal/apiserver/apiv1`. Step 8.9 deployed
  it on 2026-10-07 with the Service and the Certificate that D-46 lists (D-46 and D-50
  as built).
- **Plan 01 step 6, the minimal keeper, is built** (#39). `dev-env-keeper` mints
  the haynes-dev-bot installation token v1's way (same permission set) and merges it
  into `dev-agents/dev-env-gh-token`, key `gh_token`, every 40 minutes or two thirds of
  its life, with retries from 10 s to 5 minutes. It reads the App's key from a
  mounted directory at every mint, refreshes only while it holds its Lease, is ready
  while its token lives, and logs nothing secret (D-52). 8.8 and 8.9 deployed it on
  2026-10-07 (haynes-ops #3480, #3497); its first mint was written at deploy.
- **Plan 01 step 7, `agent-run` v2, is built** (#34). `agent-run --repo <r> -p
  "<task>"` creates a task session, then waits up to 30 s and prints the node it runs
  on or the scheduler's reason; `list`, `show <name>`, `reap <name>...` and `fleet`
  read and reap; `-o json` prints the API's own document, and the exit codes are in
  D-50. A session pod uses its projected token; any other pod, such as the v1 pod,
  mints a token for its own ServiceAccount (the v1 pod can since 8.4, for audience
  `dev-env-operator` only). It trusts the API's CA from the file `DEV_ENV_API_CA_FILE`
  names; in the v1 pod, write that file first from ConfigMap `dev-agents/dev-env-api-ca`
  with `kubectl get configmap` (D-50; agent-run itself reads no ConfigMap); elsewhere, pass
  `--api-url` and `--token-file` until plan 02. v1's own `agent-run` is unchanged and
  still starts every session today.
- **The v1 bounce landed on 2026-10-06.** haynes-ops #3381, #3342, #3336, #3294 and
  #3274 merged at about 21:05Z, and #3330 at 21:15Z. #3241 was closed. The v1 pod
  restarted at 21:17Z on `ghcr.io/thaynes43/dev-env:0.6.8` with a CPU limit of 8.
- **Spikes S-1, S-1b and S-7 are done** (2026-10-06). S-1 and S-1b passed, so D-11
  takes the keeper-owned target (DESIGN-001 6.2). S-7 found no repo needs a mirror.
- **S-6, S-15 and S-16 are done** (2026-10-06). S-6 passed: `--resume` brings back the same
  Remote Control entry with its history. It also found that a SIGTERM to the CLI
  archives the entry and the resume unarchives it (so agentd forwards the pod's
  SIGTERM to the CLI), and that agentd must seed
  `oauthAccount` (DESIGN-001 6.2, 6.7). S-16: the keeper can read plan usage with
  `GET /api/oauth/usage` (7.3). S-15 passed: the CLI's archive call works on an
  access token and the entry leaves Tom's list; `claude --resume` alone unarchives
  it (6.7).
- **Plan 01 step 8 is done in haynes-ops** (2026-10-06 to 2026-10-07). Items 8.1 to
  8.10 and 8.8a are merged and verified (#3458, #3462, #3463, #3468 to #3471, #3474,
  #3477, #3479, #3480, #3491, #3494, #3497). 8.9 runs `dev-env-operator` (two replicas,
  `/v1` on `dev-env-operator.dev-env-system.svc.cluster.local:8443`) and
  `dev-env-keeper` from `dev-env-operator:sha-8b388b2`, and the v2 `agent-run fleet`
  answers from the v1 pod. The API's CA is pinned as ConfigMap
  `dev-agents/dev-env-api-ca` (D-50, as built). Step 9, the first end-to-end run, is
  done (first bullet).
  8.4 is the RBAC and the baseline guard: three ValidatingAdmissionPolicies and a
  Kyverno exec rule. Spike S-12 passed against it, 45 of 45 checks from a Job running
  as `dev-env-agent`, and the VAP sees `CONNECT` for exec (D-19). The v2
  ServiceAccounts exist now, so 8.9 names them and does not create them. Three
  gaps came out of 8.1 to 8.7: the `dev-agents` ceiling is a Kyverno policy, not a
  LimitRange (D-47); the config ConfigMaps had no owner, so 8.8a (D-49) now builds
  four of them in `dev-agents`, with a v2 `CLAUDE.md` and Codex's `requirements.toml`
  at `/etc/codex` in every pod; and `dev-env-templates` held a placeholder image
  digest until B5's follow-up, haynes-ops #3501 (2026-10-07), set `dev-env:2.0.0` and
  its digest.
- **v1 keeps running.** The single dev-env pod and the `dev-env-ops` executor are
  deployed from haynes-ops until the cutover (plan 05) and plan 10.
- **Q-08 is live.** Kyverno `default-cpu-request` (haynes-ops #3406) went live on
  2026-10-06. At 03:34Z no Running pod in the cluster was BestEffort.

## What happens next

**Current order:** the [Current state — 2026-10-20 UTC](#current-state--2026-10-20-utc-v2-release-2381)
block at the top of this page (the [2026-10-17 next work](#next-work--2026-10-17-utc) is history). It supersedes the 2026-10-13 brief's enumerated
list where they differ (v2.29.4/2.29.5 pins, rendezvous cohort r3–r13, inbox CLI
shape, freshness gate, expired r13 window, Codex service-account kubeconfig, held H7/Q-37). The
[Cursor wind-down handoff](DEV-ENV-CURSOR-HANDOFF-2026-10-16.md) is history only.

The [2026-10-13 next-work list](CLAUDE-RESUME-2026-10-13.md#next-work-in-order)
and [six-step roadmap](CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap)
remain the requirement-history source. The list below is the historical 2026-10-07
start order, first titled "What happens first". Each item in it is done or superseded.

1. **The laptop handoff** ([`handoffs/2026-10-06-tom-laptop-settings.md`](handoffs/2026-10-06-tom-laptop-settings.md)).
   All three parts are done (2026-10-07; the checklist below says how each was
   checked). The Protect Main ruleset is live and Renovate auto-merge is on.
2. **Plan 02: interactive sessions and lifecycle.** Plan 01, with KICKOFF B1 to B5,
   is done ([plan 01](sagas/distributed-dev-env/backlog/01-foundation.md)). Plan 02
   ([backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md))
   is done with the corrected in-cluster CLI acceptance check (see State on
   2026-10-08). Plan 07's docs-only approval spike and corrected parity audit are
   complete. Q-18's earlier prompt is withdrawn under D-70; parity restoration and
   keeper activation prerequisites remain. Run one plan at a time (README
   decisions 40, 44, 45 and 46).
3. **Collect the spike results.** S-1, S-1b and S-7 are done, and S-2 is already
   answered (the static token cannot register Remote Control). S-6, S-15 and S-16
   are done, and S-3 passed (keeper-owned Codex auth, DESIGN-001 D-12 step 2), so
   group 1 is complete. In group 2, S-12 passed on 2026-10-07 (its break-glass half
   runs with plan 07), and S-8 is done (2026-10-07: every size stays on
   `gasha01-rbd`). Phase 1's spikes are complete.
   Each result lands in its own PR. The order and pass criteria are in
   [KICKOFF section 2](sagas/distributed-dev-env/KICKOFF.md#2-track-a-spikes).
4. **The rest of the MVP, then beyond it.** The MVP ends at the cutover (plan 05);
   [the saga README](sagas/distributed-dev-env/README.md#plan-backlog) lists which
   plans are in it, their order, and the plans that follow.

## Where everything is

| Path | What it holds |
|---|---|
| [CLAUDE.md](../CLAUDE.md) | The rules for this repo, the single source. Codex reads [AGENTS.md](../AGENTS.md), which points there. |
| [.agents/sagas/README.md](sagas/README.md) | Saga conventions: statuses and the D/Q/C/S id schemes. |
| [distributed-dev-env/README.md](sagas/distributed-dev-env/README.md) | Tom's vision, the architecture at a glance, the hard news, the decision log and the plan index. |
| [CLAUDE-RESUME-2026-10-13.md](CLAUDE-RESUME-2026-10-13.md) | The current Claude pickup brief (2026-10-13): deployed state, counters, rulings with receipts, next work with owner gates, boundaries and open questions for Tom. |
| [CLAUDE-RESUME-2026-10-12.md](CLAUDE-RESUME-2026-10-12.md) | The Claude pickup brief: current owner requirements, what is delivered, the six-step roadmap and stopped routes. *Superseded for current state by the 2026-10-13 brief; its requirement history and stopped routes still hold.* |
| [distributed-dev-env/KICKOFF.md](sagas/distributed-dev-env/KICKOFF.md) | The work order for the first build session (historical; continue from the pickup brief). |
| [adrs/001-distributed-dev-env.md](sagas/distributed-dev-env/adrs/001-distributed-dev-env.md) | The architecture decision (Accepted 2026-10-06), with the ratification summary at the top and consequences C-01 to C-21. |
| [adrs/002-shared-project-workspaces.md](sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md) | Accepted 2026-10-09 and kept as history. ADR-003 supersedes its shared workspace, RWX storage and CephFS trial. |
| [adrs/003-session-coordination-private-repositories.md](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md) | The controlling scope since 2026-10-10: share rules, coordination and authorized context; keep repositories and task worktrees per pod. |
| [adrs/004-coordination-record-retention.md](sagas/distributed-dev-env/adrs/004-coordination-record-retention.md) | Q-29, Accepted 2026-10-12: coordination records are archived, then deleted, 30 days after their Sessions are gone. Amends DESIGN-010 in the reap case only. |
| [adrs/005-private-home-expiry.md](sagas/distributed-dev-env/adrs/005-private-home-expiry.md) | Q-31, Accepted 2026-10-12: a private Session's HOME is archived, then deleted, 30 days after its executor's recorded termination. Closes ADR-004's known gap; canaries never expire. |
| [adrs/006-private-home-expiry-corrections.md](sagas/distributed-dev-env/adrs/006-private-home-expiry-corrections.md) | D-87, Accepted 2026-10-12 (agent, after the review of #233): corrects ADR-005. Only a reap stops a private executor, and an owner hold blocks it; the archives get a guard against v1's exec and Jobs; the terminal record sits beside `lastExecutor`; a fifth guard exception; copies of user files are not exported. |
| [designs/001-dev-env-v2.md](sagas/distributed-dev-env/designs/001-dev-env-v2.md) | The detail: components, API, lifecycle, credentials, RBAC, egress, GPUs. Spikes are in section 13, risks in 14, Q-01 onward and their resolutions in 15, the decisions (D-01 onward) in 16. |
| [designs/](sagas/distributed-dev-env/designs/) | DESIGN-002 to DESIGN-025: the bounded contracts for budgets, private projects, rules, history, directory, claims, inbox, provenance, the once-only clients, coordination record retention and private HOME expiry. The pickup brief's review table maps each to source paths, except DESIGN-012 (owned wrapper permissions) and DESIGN-024 (no source yet). |
| [research/R-01](sagas/distributed-dev-env/research/R-01-summoned-agents-audit.md) | An audit of summoned agents today, with v2 requirements V-01 to V-17. |
| [research/R-02](sagas/distributed-dev-env/research/R-02-remote-control-identity.md) | Remote Control identity, the evidence behind S-1, and proposals P-1 to P-12. |
| [research/R-03](sagas/distributed-dev-env/research/R-03-claude-code-approvals.md) | Claude Code in-app approval research: hooks, managed policy, forgery paths and the plan 03 core dependency. D-70 withdraws Q-18's earlier premise; no route selected. |
| [research/R-04](sagas/distributed-dev-env/research/R-04-v1-capability-parity.md) | Complete v1 parity audit: twenty cutover blockers, exact scopes, fixes and runtime closure evidence. |
| [backlog/00-spikes.md](sagas/distributed-dev-env/backlog/00-spikes.md) | S-1 to S-16: steps, safety rules and pass criteria. |
| [backlog/01-foundation.md](sagas/distributed-dev-env/backlog/01-foundation.md) | Plan 01: operator, agentd, `agent-run`, the agent image, task mode on the static token. |
| [backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md) | Plan 02: interactive sessions, idle detection, suspend, resume and optional external CLI access. Done under Tom's corrected scope. |
| [backlog/03](sagas/distributed-dev-env/backlog/03-remote-control.md) | Plan 03: Remote Control, the keeper-owned Max login and the console. |
| [backlog/04](sagas/distributed-dev-env/backlog/04-rolling-updates-codex.md) | Plan 04: drain on idle with resume, multiple Codex remote hosts and one refresh owner. |
| [backlog/05](sagas/distributed-dev-env/backlog/05-cutover.md) | Plan 05: cutover from v1 (needs Tom's written approval). |
| [backlog/06](sagas/distributed-dev-env/backlog/06-later.md) | Later items, each a future plan. |
| [backlog/07](sagas/distributed-dev-env/backlog/07-access-broker.md) | Plan 07: the access broker, grants and break-glass. |
| [backlog/08](sagas/distributed-dev-env/backlog/08-tool-pods.md) | Plan 08: tool pods (Blender, audio, image, whisper, printer, video). |
| [backlog/09](sagas/distributed-dev-env/backlog/09-gpu-local-llm.md) | Plan 09: the VRAM budget, LLM pools, satellites and opencode. |
| [backlog/10](sagas/distributed-dev-env/backlog/10-summoned-sessions.md) | Plan 10: summoned sessions move into v2 and `dev-env-ops` retires. |
| [backlog/11](sagas/distributed-dev-env/backlog/11-project-workspaces.md) | Plan 11 under ADR-003: common rules, session coordination and fresh per-pod repositories and task worktrees. R1–R7 are read with the 2026-10-10 correction. |
| [workflow guide](../docs/workflow-guide.md) | High-level quick start, architecture/flow diagrams, features, current limits and cutover gates. |
| [.github/workflows/](../.github/workflows/) | CI (`ci.yml`, with the aggregate `CI - Success`), the operator image (`publish.yml`, from main), the agent image (`publish-agent.yml`, from a `v2.x.y` tag), release-please, the Claude advisory review and the `@claude` handler. |
| haynes-ops [`.agents/sagas/dev-env/adrs/001-v2-lives-in-own-repo.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/sagas/dev-env/adrs/001-v2-lives-in-own-repo.md) | Accepted: v2 lives here, and v1 and every manifest stay in haynes-ops. |

## Rulings

Tom's rulings from 2026-10-06 to 2026-10-13. Full text for Q-01 to Q-23 is in
DESIGN-001 section 15; later rows link their own record. This heading first read
"Rulings (Tom, 2026-10-06 to 2026-10-08; full text in DESIGN-001 section 15)".

| Id | Ruling |
|---|---|
| Q-01 | Build our own small operator, modelled on kubernetes-sigs/agent-sandbox. |
| Q-02 | Go for the operator, broker, agentd and a static `agent-run`. |
| Q-03 | When the image or config changes, drain each session on idle and resume its conversation on the new version. |
| Q-04 | No fleet cap. Every pod gets requests and limits from S/M/L presets (default M) and the scheduler places it. |
| Q-05 | A block volume per session on `gasha01-rbd`, plus a small shared CephFS volume in the cluster. |
| Q-06 | A dynamic VRAM budget per card, control-plane cards included. Larger models run on satellite machines. |
| Q-07 | The Proxmox operator token and the hw-ssh key leave session pods and come as short-lived broker grants. |
| Q-08 | A Kyverno LimitRange gives every non-system namespace a 50m default CPU request (live, haynes-ops #3406). |
| Q-09 | No household GPU app lends VRAM. Agents get only what is left, and that grows as GPUs are added. |
| Q-10 | Satellites are used only while awake and not in use by Tom, and are never woken. |
| Q-11 | The link that survives restarts is the Claude Max login. The keeper owns it, and the console renews it each month. |
| Q-12 | The repo is public (B), so the ruleset is enforced free. Tom made it public himself on 2026-10-07: "I made dev-env public so I can go to bed but make sure it's good and safe". Actions billing had stopped CI on the private repo. |
| Q-13 | Make `ghcr.io/thaynes43/dev-env-operator` public (A). "Public package write a prompt for an agent on my laptop to flip it": a laptop agent flips it after B3's first publish, because GitHub has no API for package visibility. |
| Q-14 | A GitHub App key secret for release-please (A): "GitHub App key secret (Recommended)". Repo variable `RELEASE_APP_ID`, repo secret `RELEASE_APP_PRIVATE_KEY`; the App also needs Issues read and write for `autorelease:` labels. |
| Q-15 | The keeper mints a Proxmox grant's token over SSH (A): "Keeper mints over SSH (Recommended)". With a certificate from its own SSH CA it runs `sudo pvesh create /access/users/dev-env@pve/token/<grant> --expire <end> --privsep 0` on a node and deletes the token at the grant's end. No new Proxmox user, and v2 never holds the long-lived token. Port 22 from the keeper to the nodes. |
| Q-16 | Parity first, approvals in the Claude Code app (Tom, 2026-10-08). "I am open to advanced security but I must be able to grant permission from my phone." No new guard cuts what agents do today (no Authentik, outpost, Traefik or postgres16 lockdown, no git review or CODEOWNERS gate); standing auto-approved grants cover everything a v1 agent does (OPERATOR kube tier, Proxmox via the keeper's SSH minting, hw-ssh), and any gap blocks the cutover. Human approval gates only capabilities beyond today's (secret reads, break-glass above OPERATOR). Approvals happen inside the Claude Code app, not Pushover plus a web page. Residual risk accepted: Flux is cluster-admin and agents self-merge. The approval surface is the next design spike (issue #91). **Coordinator correction, later 2026-10-08 (D-70):** the OPERATOR-only examples above omit accepted Headlamp cluster-admin access. Those operations are existing reachable powers, with Tom's live directive still required for the Headlamp task or access scope. This factual correction is appended to the original ruling; it is not a new owner ruling or blanket standing admin authorization. |
| Q-17 | Withdrawn after Tom corrected its premise (2026-10-08): use `agent-run` or ask agents to start sessions; a web UI is another possible session-management client. No laptop kubeconfig or test blocks plan 02. D-68 remains an optional external path whose real external-machine use is unverified. Q-16 remains in force. |
| Q-18 | Earlier prompt withdrawn by the coordinator after Tom corrected its OPERATOR-only premise (2026-10-08, D-70). Headlamp's cluster-admin route already exposes the claimed additional Kubernetes powers. No approval route or blanket direct admin grant is selected. Any future approval question must name an actual new capability or workflow and preserve effective parity and existing owner rules. R-03 remains design research; no human path or new guard is enabled. |
| Q-19 | **Complete by owner confirmation, 2026-10-08 America/New_York:** fresh CA fields `SSH_CA_PRIVATE_KEY_B64` and `SSH_CA_PUBLIC_KEY` saved in existing `HaynesKube/dev-env` (D-72 corrects the originally proposed separate item). No values were sent to or read by this coordinator; format and delivery validation still belong to keeper provisioning. Do not ask to generate/store it again. Minting stays disabled; node trust and activation follow separately. |
| Q-20 | **Superseded.** The early question of which app registers the proposed v1 project folders was premature. Tom's R2 (2026-10-09: both providers share one project concept) supersedes it (README decisions 49 and 50). |
| Q-21 | **Accepted 2026-10-09, then superseded by ADR-003.** Tom answered "Accept ADR-002 and the bounded CephFS trial (Recommended)" (D-75, README decision 51). [ADR-003](sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md) (2026-10-10, decision 56) removed shared Git/RWX and its trial from delivery; #130 closed as not planned. |
| Q-22 | **Delegated 2026-10-09.** "Delegate the trust installation to Codex" (D-76, README decision 53). The saved public CA was installed on all five PVE nodes (decision 54); minting stays off. |
| Q-23 | **Accepted 2026-10-09.** "60 minutes without progress or 3 failed attempts at the same blocker" (D-84, README decision 55, [budget contract](sagas/distributed-dev-env/requirements/2026-10-09-task-budgets.md)). Counters carry across agents, pods, methods and resumes. |
| Q-24 | **Pending.** Codex native routing for the MVP: separate paired session hosts (recommended) or one coordinator with isolated workers sharing a native failure boundary. The native question went out 2026-10-11; no owner answer is recorded and phone delivery is unverified. Do not ask again; check for an actual answer first. [DESIGN-006 Q-24](sagas/distributed-dev-env/designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing), README decision 57. *Update, 2026-10-13:* still no owner answer. A peer relay of a reported direction (bounded paired hosts, perhaps one per node) is saved locally as data only, not a ruling, and a peer-relayed owner quote is not authority. The [2026-10-13 brief](CLAUDE-RESUME-2026-10-13.md#questions-for-tom-one-at-a-time) records the recommendation of session `task-dev-env-1012-105043`, not an owner ruling: when Codex routing is the next unit, ask one new confirmation question under the next free Q id. |
| Q-25 | **No decision needed.** The local schema-host DNS route stopped after three attempts. Shipping uses hosted CI and saved real-API schema evidence; no DNS investigation or retry ([installation record](sagas/distributed-dev-env/verification/installation-2026-10-11.md#owner-questions-and-stopped-checks)). |
| Q-26 | **Answered 2026-10-11:** "Allow one correction (Recommended)". One final R4 checker correction ran and Ops #3824 activation was accepted. No R5, alternate method or retry ([fixed-rule activation record](sagas/distributed-dev-env/verification/fixed-rule-activation-2026-10-11.md), [DESIGN-021](sagas/distributed-dev-env/designs/021-canary-cli.md)). |
| Q-27 | **Answered 2026-10-12** by Tom through the Claude native prompt: "Fix once, then read (Recommended)". One offline correction of the Codex reader preparation, with a fixed list of already accepted evidence, focused tests and independent review. Then exactly one named read and one result read of the Codex original. No retries and no counter reset ([Codex probe record](sagas/distributed-dev-env/verification/codex-fixed-rule-probe-2026-10-12.md#q-27-one-final-reader-preparation-correction)). Carried out 2026-10-12: both reads ran once and returned both markers ([result record](sagas/distributed-dev-env/verification/codex-original-result-2026-10-12.md)). |
| Q-28 | **Answered 2026-10-12** by Tom through the Claude native prompt: "Yes, after fixes deploy (Recommended)" (other options: "Yes, run it now", "Not now"; owner answer receipt `daadc6b5`). Question: "Codex's canary passed: it loaded both the common and the repository rules in its private pod. Claude's single canary only returned a weekly-limit error, so Claude's rule loading in a v2 pod is still unproven, and its ID is used up. May I run one new Claude canary? It needs a new fixed ID in the operator config in haynes-ops (a rolling operator update that keeps running sessions), then one create, one named read and one result read, with no retries." Allows one new canary `rule-canary-claude-1012`, run only after the review fixes deploy; no retries, no reuse of the 1011 ID, no quota or credential repair, no paid or model fallback, no cleanup. Carried out 2026-10-12 after v2.24.1 deployed: both markers returned, Root acceptance `88909059` ([Claude canary 1012 record](sagas/distributed-dev-env/verification/claude-canary-1012-2026-10-12.md), README decision 63). |
| Q-29 | **Answered 2026-10-12** by Tom through the Claude native prompt: "Archive, then delete (Recommended)". Coordination records are released 30 days after the Sessions they depend on are first proved absent, archived first to a shelf-only volume and kept there indefinitely, then deleted by a leader-only operator runnable; reads fall back to the archive. Amends DESIGN-010's no-release rule in the reap case only. Private Sessions are never absent until a HOME expiry policy exists (plan 11 follow-up F1). [ADR-004](sagas/distributed-dev-env/adrs/004-coordination-record-retention.md), [DESIGN-024](sagas/distributed-dev-env/designs/024-coordination-record-retention.md), README decision 62. |
| Q-30 | **Answered 2026-10-12** by Tom through the Claude native prompt, in free text. Question: "To close #173 I need one more Claude canary on a private repo, proving private GitHub access and a fresh pinned task worktree. The dev bot can't create repos on your personal account. Could you create an empty private repo thaynes43/dev-env-private-probe (a README on main is enough)? Global private admission stays off; the canary reuses the spent 1012 slot." Options: "I'll create it (Recommended)", "Use an existing private repo", "Not now". Answer: "All the Sigo alumni repos are private and all are needed for dev-env, we use them now. Why can't you use those?" Interpretation: use the existing private repositories `thaynes43/sigo-alumni` and `thaynes43/sigoalumni-org` instead of a new probe repository, declared as v2 catalog project `sigo-alumni`, and run the one approved private-repo Claude canary `rule-canary-claude-sigo-1012` against it: one create, one named read, one result read, no retries. Global private admission stays off; repository visibility is unchanged. Owner answer receipt `root-q30-owner-answer-r1.json`, sha256 `1490331885be14f5939fe85c81d91f186274dac089286e2343ca373bdd0b3722`. GitOps: haynes-ops [#3845](https://github.com/thaynes43/haynes-ops/pull/3845). Run on 2026-10-12: bootstrap `Partial` on a tracked symlink, so not accepted; fixed in [#237](https://github.com/thaynes43/dev-env/pull/237) ([DESIGN-011 Q-30 amendment](sagas/distributed-dev-env/designs/011-private-managed-rule-canaries.md#private-repo-canary-on-the-sigo-alumni-project--2026-10-12-q-30-173), README decision 65). Root acceptance `dd5dee24`: items 1 and 3 to 7 hold and item 2 (bootstrap) fails, so the run is not accepted for #173 under DESIGN-011's gate; the Root accepts private GitHub authorization and a fresh task pin separately, as the named deviation `partial-acceptance-by-property` ([Sigo private-repo canary record](sagas/distributed-dev-env/verification/sigo-private-canary-2026-10-12.md)). |
| Q-31 | **Answered 2026-10-12** by Tom through the Claude native prompt: "Archive, then delete (Recommended)". A private Session's HOME is archived, then deleted, 30 days after its executor's recorded termination: Git rescue in a hold pod bound to the HOME UID, provider history (an allowlist, never credentials) and Git bundle to a shelf-only `Retain` archive, readable under DESIGN-013's scope, then a guarded delete by the operator's leader. A retained Pod is not a stop proof; absence never is. Canary originals never expire; hold and release-now are Tom's alone. Deletion waits for the installed archived read (#174). Closes ADR-004's known gap. [ADR-005](sagas/distributed-dev-env/adrs/005-private-home-expiry.md), [DESIGN-025](sagas/distributed-dev-env/designs/025-private-home-expiry.md), README decision 64. Corrected the same day by [ADR-006](sagas/distributed-dev-env/adrs/006-private-home-expiry-corrections.md) (D-87, README decision 66): only a reap stops a private executor, never the suspended deadline. |
| Q-32 | **Answered 2026-10-12** by Tom through the Claude native prompt: "Yes, after deploy (Recommended)" (other option: "Not now"). Question: "The Sigo canary proved private GitHub access and a fresh pinned worktree, but catalog bootstrap ended Partial: one Sigo repo tracks a symlink our checkout check wrongly refused. The fix is merged and deploying now as 2.25. May I run one more Claude canary on the Sigo project after the deploy, to prove bootstrap reaches Ready and the new status codes work? One create, one named read, one result read, no retries." Grants, after the 2.25 deploy is post-checked, one new Claude canary record for project `sigo-alumni` through a reviewed haynes-ops PR, replacing the consumed `rule-canary-claude-sigo-1012` record (`codex-1011` byte-identical): exactly one create, one named read and one result read, no retries; global private admission stays off. Not granted: reuse of a consumed canary ID, any retry, a second rerun, credential repair or cleanup of any original. Owner answer receipt `root-q32-owner-answer-r1.json`, sha256 `48ab104fa0c140b139ce537a0d3eb4b59139111a785d2f97145e4ff459736609`. GitOps: haynes-ops [#3855](https://github.com/thaynes43/haynes-ops/pull/3855) (`rule-canary-claude-sigo-1012b`). Run on 2026-10-13 UTC: all seven DESIGN-011 items hold, bootstrap `Ready`; Root acceptance `a3aa0b7d` (`ACCEPTED_PRIVATE_REPO_CANARY_ALL_ITEMS`) accepts #173's private-repository evidence under the all-items gate ([Sigo rerun record](sagas/distributed-dev-env/verification/sigo-rerun-2026-10-13.md), README decision 68). |
| Q-35 | **Recorded 2026-10-13 through oversight, directly confirmed at 21:33 UTC:** relay "v1 must remain the same until it’s decommissioned after the MVP has been accepted"; direct asynchronous answer "Keep v1 trusted during migration (Recommended)". [ADR-007/D-89](sagas/distributed-dev-env/adrs/007-preserve-v1-archive-trust-boundary.md), README row 70, amends only ADR-006's v1 archive narrowing and C-42. Preserve v1 behavior, configuration and current access. Guard untrusted v2 paths; claim no isolation from v1 and grant no new access, activation or automatic decommissioning. Original relayed turn remains unobserved; the direct confirmation is observed. Local relay receipt `dev-env-h7-archive-guard-1013/q35-relayed-answer-r1.json`, SHA-256 `b8b96a86eb81628733dd4e86068f029b9b99fd7eeed38842f5e01988d7b32903`; `dev-env-h7-archive-guard-1013/q35-direct-owner-confirmation-r1.json`, SHA-256 `d7728c571d7373417c1ca1d9dc52fd8670468483dc6140535cf447b21f3789e9`. |
| Q-36 | **Recorded 2026-10-13 as relayed operational direction:** "Go for it" for holding the new reader and collecting workload/guard/webhook evidence through existing authorized Mac access. [DESIGN-025/D-90](sagas/distributed-dev-env/designs/025-private-home-expiry.md#21-existing-access-evidence-and-hold-q-36), README row 71. The relay arrived after #3904 deployed and its automatic Job failed. Hold further reader access/deployment, preserve the failed attempt; no undo, retry, credential export or later activation. Original owner turn and native delivery unobserved. Local receipt `dev-env-h7-archive-guard-1013/q36-relayed-direction-r1.json`, SHA-256 `96ea06588570ba276c09956e0799e539202d53cfbb2e584155da7ca48a4d373c`. |
| Q-37 | **Answered through the source Mac conversation; one-time action consumed.** Tom answered "Yes" at 19:34:06 EDT (23:34:06 UTC) to the exact history-PV Delete-to-Retain scope. Source-corroborated approval relay; original user turn not directly exposed to Root. [D-91/section 22](sagas/distributed-dev-env/designs/025-private-home-expiry.md#22-one-time-pv-retain-q-37-d-91), README 72. One reviewed client launch, GET/PATCH/GET all 200, exit 0, same bound PV/claim verified Retain at 19:40:32 EDT. The attempt is consumed; no repeat, new permission or activation. Owner receipt `q37-owner-approval-mac-r1.json`, SHA-256 `c86616cb671435d375317451d3032c8c371d8d2e0100c67181d01151624b62e1`; [acceptance](sagas/distributed-dev-env/verification/h7-retain-acceptance-2026-10-13.md). |
| Pacing | **Answered 2026-10-13** by Tom through the Claude native prompt: "Slow down (Recommended)" (other options: "Keep full speed", "Pause after H5/H6"; asked 04:00Z, answered 11:02Z). Question: "Today's dev-env push has used a lot of Opus subagent time: about 40 reviewed PRs, 4 deploys and 4 canaries. The Max plan is shared with your own Claude use, and hitting the weekly limit stops every session, yours included. How hard should I keep pushing after H5/H6 finish?" Means: one Opus subagent unit (`claude-opus-5-5`) at a time, Sonnet (`claude-sonnet-5-5`) for routine work, no parallel Opus waves until Tom says otherwise. It limits concurrency; it pauses nothing and changes no other gate. Owner answer receipt `root-pacing-owner-answer-r1.json` (in the coordinator's `dev-env-claude-handoff-1013` orders, written from the session record after the answer), sha256 `a70a80ccef09e0d7bd7c8521ce7156dc2dd4a8d00a23a429365e3844be32cf20`. README decision 69; it follows decision 40's 2026-10-07 pacing. |

**Next (historical, 2026-10-08; the current order is the 2026-10-13 brief's
[next work](CLAUDE-RESUME-2026-10-13.md#next-work-in-order), which continues the
[six-step roadmap](CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap)):**
effective parity and keeper activation prerequisites. Q-18's earlier
prompt is withdrawn; no approval route is selected. The spike and corrected v1 parity audit are complete;
parity restoration and keeper SSH minting are in progress. No gap is waived. Any future
approval proposal must preserve existing owner requirements; a change to the
historical break-glass login-freshness requirement needs its own explicit ruling.

**Settings only Tom can click** (no decision needed). An agent on his laptop does
them from [`handoffs/2026-10-06-tom-laptop-settings.md`](handoffs/2026-10-06-tom-laptop-settings.md),
which lists exactly what to change:

- [x] Part 1, before B4 and B5: on the `ghcr.io/thaynes43/dev-env` package, "Manage
      Actions access": give `thaynes43/dev-env` Write (before KICKOFF B5). Done: the
      2.0.0 publish pushed to it (2026-10-07).
- [x] Part 1: repo settings: allow auto-merge (Renovate's `platformAutomerge` needs
      it). Done: `allow_auto_merge` is true (2026-10-07).
- [x] Part 1: the Mend Renovate app covers this repo. Done: Renovate opens PRs here.
- [x] Part 1: the release-please GitHub App (Q-14), with repo variable
      `RELEASE_APP_ID` and repo secret `RELEASE_APP_PRIVATE_KEY` (before B4's
      release-please part). Until then the workflow's `GITHUB_TOKEN` fallback cannot
      open the release PR at all (it failed on 2026-10-06), so also allow Actions to
      create PRs (handoff part 1, step 5). Done: the App minted the token and opened
      release PR #52 on 2026-10-07.
- [x] Part 2, after B3's first publish: make `ghcr.io/thaynes43/dev-env-operator`
      public (Q-13). Done: an anonymous pull works (checked 2026-10-07).
- [x] Part 3, after `CI - Success` has reported once (B2): the Protect Main ruleset on
      the default branch, with `CI - Success` required. Done and checked 2026-10-07
      with `gh api repos/thaynes43/dev-env/rulesets`: ruleset id 24655718 is active on
      `~DEFAULT_BRANCH` with `deletion`, `non_fast_forward`, `required_linear_history`,
      `pull_request` (0 approvals) and `required_status_checks` (`CI - Success`,
      integration id 15368 = GitHub Actions, up-to-date off). Also checked: private
      vulnerability reporting is enabled. Set by Tom but not visible to the bot (its
      token reads `security_and_analysis` as null, and the fork-PR approval endpoint
      answers 403): approval for all outside contributors' fork PRs, secret scanning
      and push protection. Renovate's auto-merge was flipped on in the PR that
      recorded this: `automerge: true` in the first two `packageRules` entries of
      `.github/renovate.json5` (Go modules and GitHub Actions).

## Working rules (the summary; CLAUDE.md is the source)

- **Worktree per task.** Branch `agent/<task>` and never push to main.
- **Open ready PRs, not drafts.** The `Claude advisory review` check posts findings.
  Read every one. Fix it, or answer it on the thread with a concrete reason it is
  wrong. "Merging anyway" is not an answer.
- **Merge your own green PRs** with a squash merge, once required checks pass and
  every finding is handled.
- **Only questions wait on Tom.** Ask one at a time, when it comes up, with the
  recommended option first. Claude Code uses AskUserQuestion; Codex uses the
  available native question tool, with phone delivery verified before relying
  on that route. If unavailable, report the capability gap and keep only the
  dependent decision pending; prose in the stream is not delivery. Record each
  question as a `Q-NN` in the design, then fold Tom's
  answer back in as a dated ruling and update the decision log. Check a question's
  premise before you ask it.
- **Docs first.** A behaviour change starts in the saga and lands in the same PR as
  its code. An Accepted ADR is never edited; a new ADR supersedes it.

## Hard constraint: no CPU burners

No CPU burners, stress tools, busy loops or wide parallel test runs on any shared
node. Set a CPU limit on anything you run in the cluster. Cap test parallelism
(`go test -p 2`, vitest `--maxWorkers=2`) and run one suite at a time.

Why: from 23:42Z on 2026-10-05 to 00:12Z on 2026-10-06, a dev-env subagent's busy
loops plus parallel vitest runs saturated talosm02. Its BestEffort pods (EMQX,
traefik, authentik, cloudnative-pg) got no CPU and went into liveness-kill loops.
The kubelet itself peaked at only 0.18 cores.

## Inside the cluster or outside it

| Capability | In the v1 dev-env pod | Outside (Tom's machine) |
|---|---|---|
| gh, git push, GHCR | Yes, as haynes-dev-bot. No prefix: `~/.local/bin/gh` reads `/creds/gh_token` on every call (haynes-ops #3476, 2026-10-07). | Yes, with your own gh login, so PRs author as Tom. GHCR pulls of public images are anonymous. |
| Code, Go builds, unit tests, envtest | Yes, lightly, under the pod's CPU cap. | Yes. |
| `kubectl`, `flux` | OPERATOR tier: read plus targeted runtime writes. | Optional external CLI access needs a caller's existing admin kubeconfig (D-68). No cluster credential is supplied by this repo, and Tom's current workflow needs no laptop setup (Q-17). |
| `declare-activity` | v1 writes files on the pod's volume; v2 `agent-run declare-activity` calls the operator API. | The v1 file command is unavailable. The v2 API command is available to an authenticated external CLI caller. |
| MCP servers on cluster DNS (grafana-mcp, home-assistant, blender, audio, vexa, the haynesnetwork hop) | Yes. | No. |
| The Claude Max login and Codex `auth.json` | Yes, on the pod's volume. Never copied out. | No. Never fetch them. |

**Spikes by where they can run:**

- **Hand to an in-pod session.** These need the live Max login or Codex auth on the
  v1 volume, or a measurement taken inside the cluster: S-1 (and S-1b), S-3, S-6,
  S-7, S-15, S-16, and the S-2 re-probe on each CLI bump.
- **Through haynes-ops PRs that deploy test workloads.** An in-pod session then reads
  the results: S-8 and S-12 in phase 1, and S-9 and S-13 on talosw04 before plan 09.
- **Inside v2 session pods, once they exist:** S-10 (phase 2), S-5 (phase 3), S-4
  (phase 4) and S-11 (before plan 09).
- **Outside is fine:** S-14 steps 1 to 3, on Tom's own machines with Tom there. Step
  4 needs a session pod.

To hand a spike to the pod from outside, file an issue here that links the spike's
section in `00-spikes.md`. Then ask Tom (one question) to dispatch it:
`agent-run --repo dev-env --agent claude --effort xhigh -p "Run spike S-N per
thaynes43/dev-env#<issue>"`.

## Cross-repo boundaries

- **This repo** holds code (operator, broker, keeper, agentd, `agent-run`,
  `dev-env-satellite`), image builds, CI and the saga.
- **thaynes43/haynes-ops** holds every manifest and all pod config, changed only
  through PRs that Flux applies. Never `kubectl apply`. No v2 PR touches
  `kubernetes/main/apps/dev/dev-env/app/resources/**`, because that restarts v1 and
  every session in it.
- **haynes-ops is public.** Nothing secret goes there: no tokens, login URLs or codes.
- **Most v2 changes are two PRs:** the code and image here, then the pin bump in
  haynes-ops.
- **Cluster-scoped haynes-ops PRs** (CRDs, admission policies, clusterwide network
  policies, Kyverno) may be authored from outside. They are merged and verified by
  an in-pod session that can declare activity and read the cluster.

## Open items elsewhere that touch v2

- [haynes-ops #3392](https://github.com/thaynes43/haynes-ops/issues/3392): v1's
  OPERATOR tier lets an agent act as another namespace's ServiceAccount. The fix
  for v1 is Tom's decision. v2 answers it with the baseline guard (D-19, S-12).
- [haynes-ops #3414](https://github.com/thaynes43/haynes-ops/issues/3414)
  (**pending**): gives `dev-env-ops` its own second Max login, so `wo-*` and `esc-*`
  sessions register Remote Control. The wiring is merged as
  [#3422](https://github.com/thaynes43/haynes-ops/pull/3422). The login itself still
  needs Tom's relay ceremony (haynes-ops
  [`.agents/runbooks/agentic-remediation.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/runbooks/agentic-remediation.md),
  section "dev-env-ops Max login"). `rc-selftest.sh` printing
  `SELFTEST: REGISTERED` verifies it. The issue stays open until then. Until plan 10,
  Tom renews up to three Max logins a month.
- [haynes-ops #3415](https://github.com/thaynes43/haynes-ops/issues/3415): the
  executor is fragile. Config merges restart it, and a busy escalation lane holds
  new escalations without paging. Plan 10 retires the executor.
- [haynes-ops #3405](https://github.com/thaynes43/haynes-ops/issues/3405): Kyverno
  `kyverno.io/v1` ClusterPolicies need migrating to the CEL policy types. Write
  v2's new Kyverno policies on the new API where it can express them.
- Also relevant: [#3092](https://github.com/thaynes43/haynes-ops/issues/3092)
  (Kyverno reads legacy `.sig` cosign signatures, but cosign v3 writes bundles; this
  bears on how v2 images are signed) and the held v1 CPU cap
  [#3381](https://github.com/thaynes43/haynes-ops/pull/3381).
