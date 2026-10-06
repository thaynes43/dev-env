# 00: spikes

**Status:** backlog
**Depends on:** nothing. S-1, S-3, S-6, S-7, S-15 and S-16 run in the v1 pod today;
S-2 is answered (a one-probe check per CLI bump); S-4 and S-5 need two session pods
and run in phases 3 and 4; S-8 and S-12 run in phase 1, S-10 in phase 2, S-9, S-11,
S-13 and S-14 before plan 09. S-1 comes first: it decides how Remote Control gets the
Max login (DESIGN-001 D-11).
**Parallel with:** Tom's ratification of ADR-001 (every question, Q-01 to Q-11, was
answered on 2026-10-06)

## Goal

Turn the design's unknowns into facts before code depends on them. Record each
result in DESIGN-001 (section 13 and the section the spike decides) and tick it here.

## Safety rules for every spike

- **Light work only.** A handful of CLI invocations, one at a time. No test suites,
  no loops, nothing parallel (the 2026-10-05 incident). `git` runs with
  `-c pack.threads=2`.
- **Never copy a refresh token.** Scratch config directories get an access token at
  most, written by `jq` straight into a 0600 file. Never print a token, an OAuth URL
  or a login code, and never put one in git, a PR or a log.
- **Scratch directories** live under `/tmp/spike-<id>/` and are deleted afterwards.
- **Remote Control entries** a spike creates appear briefly in Tom's session list.
  Name them `spike-<id>` and stop them when done.

## S-1: Claude on an access-token-only credential

Decides DESIGN-001 D-11 (keeper-owned Max login vs the coordinator host). Background
and the evidence so far: research note
[R-02](../research/R-02-remote-control-identity.md) section 5.2.

1. **Cold home.** `mkdir -p /tmp/spike-s1/claude && chmod 700 /tmp/spike-s1/claude`.
   Write `.credentials.json` there from the live file with the refresh token removed
   (`jq '.claudeAiOauth |= del(.refreshToken)'`), mode 0600, and nothing else: no
   `.claude.json`, as a fresh v2 pod has.
2. In a tmux session, run `CLAUDE_CONFIG_DIR=/tmp/spike-s1/claude env -u
   CLAUDE_CODE_OAUTH_TOKEN claude --debug-file /tmp/spike-s1/debug.log
   --remote-control spike-s1`. Does it register (a `bridgeSessionId` in
   `sessions/<pid>.json`)? If it fails for want of an organization, seed
   `.claude.json` with `oauthAccount` (account and organization uuids only) and try
   again; record which was needed. Check the debug log's request bodies against R-02
   section 2.1, reading key names only.
3. After the live login next refreshes, merge the new access token into the scratch
   file by atomic rename, as agentd will. Does the running session use it without a
   restart?
4. **The 401 window.** At that refresh, check once whether the previous access token
   still works (one `claude -p ok` with it, in another scratch dir). If it is revoked,
   time how long the running session sees 401s, with and without
   `CLAUDE_CODE_OAUTH_401_WAIT_MS=60000`.
5. Let the scratch token near expiry. Does the CLI try to refresh, and what does it do
   without a refresh token?
6. **S-1b, the env-token variant.** In an empty config dir, run
   `claude --remote-control spike-s1b` with `CLAUDE_CODE_OAUTH_TOKEN` set to the
   current access token and `CLAUDE_CODE_OAUTH_SCOPES` set to the login's scopes (and
   `CLAUDE_CODE_SUBSCRIPTION_TYPE` if the CLI asks). Does it register? It is the
   fallback, because an env var cannot change inside a running process.

Pass = 2 and 3 work, 4's window is covered by agentd's re-merge or the 401 wait, and 5
fails harmlessly (an error or a wait, never a write to the live login). Never print a
token; read key names and expiry times only.

## S-2: static token and Remote Control

**Answered 2026-10-06: it cannot register.** The docs say "Remote Control requires a
full-scope login token", the 2.1.284 binary refuses tokens without `user:profile`
(`token_scope_limited`), and R-01 F-01 found all 45 `wo-*`/`esc-*` executor sessions
since 2026-09-03 rejected on a credentials file synthesized from the setup token. On
each CLI bump, one probe confirms it still holds:
`CLAUDE_CONFIG_DIR=/tmp/spike-s2 claude --remote-control spike-s2` with
`CLAUDE_CODE_OAUTH_TOKEN` set and no `.credentials.json`.

## S-3: Codex on an access token

Scratch `CODEX_HOME=/tmp/spike-s3`. Feed the access token from the live `auth.json`
to `codex login --with-access-token` through a pipe. Then `codex exec "reply ok" <
/dev/null`. Then start the app-server with remote control in the scratch home
**without pairing** and see whether it runs. Note how codex behaves when the token
expires. Pass = exec and app-server run on the access token alone.

## S-4: Codex exec-server (phase 4)

Run `codex exec-server` in one session pod and register it from the codex hub with
`codex exec-server forward`. Can a phone-started thread run commands in that pod?
Decides DESIGN-001 D-12 step 3.

## S-5: cross-pod SendMessage (phase 3)

Two `remote` sessions in two pods. From one, SendMessage to the other by name. Does
it arrive, and can the receiver reply? Decides D-16 tier 2.

## S-6: resume with Remote Control

In the v1 pod: start `claude --remote-control spike-s6` in a scratch worktree, send
one message, exit, then `claude --resume <session-id> --remote-control spike-s6`.
Does the phone show the same entry, with the history? Decides DESIGN-001 6.7. The
docs already say resume reconnects to the session recorded in the conversation, and
the owner check is the account and organization, not the machine (R-02 2.3); one run
confirms it.
This spike runs on the live Max login, like any v1 Remote Control session: keep it to
a few minutes and stop it when done.

## S-7: clone time per repo

For each of haynes-ops, haynesnetwork, hass-sandbox, cigar-journal and haynes-quest,
one at a time: `time git -c pack.threads=2 clone --filter=blob:none <url>
/tmp/spike-s7/<repo>` then `git worktree add`, then delete it. Record the wall time.
A repo over two minutes gets a shared mirror (D-15).

- [x] **Done 2026-10-06.** All five repos clone in under 25 seconds, so no repo gets a
  shared mirror. Each was cloned alone with `nice -n 19` and `pack.threads=2` from the
  v1 pod, over the pod's normal GitHub egress, through a `GIT_ASKPASS` script that read
  the bot token without printing it. The clone time includes the checkout. Results are
  in DESIGN-001 section 13 and D-15.

| Repo | Clone plus checkout | `git worktree add` | `.git` plus checkout | Commits |
|---|---|---|---|---|
| cigar-journal | 2.0 s | 0.1 s | 13M | 336 |
| haynes-ops | 2.6 s | 0.1 s | 27M | 6619 |
| hass-sandbox | 3.3 s | 0.1 s | 24M | 324 |
| haynesnetwork | 10.9 s | 0.2 s | 134M | 734 |
| haynes-quest | 22.9 s | 3.2 s | 1.9G | 142 |

## S-8: gasha01-rbd against ceph-block (phase 1)

In one task pod at size M, once with its volume on `gasha01-rbd` and once on
`ceph-block`, never both at once: `git -c pack.threads=2 clone --filter=blob:none`
haynesnetwork, `pnpm install`, then one test file with `--maxWorkers=2`. Record the
wall time of each step. If gasha01 is more than twice as slow overall, size L
defaults to `ceph-block` (DESIGN-001 D-22).

## S-9: VRAM units through the device plugin (before plan 09)

On talosw04, where nothing household runs, and with Tom's lend label set: give the
pinned device plugin a time-slicing config of 12 replicas under
`nvidia.com/gpu.shared`, chosen by a node label that an NFD rule sets. Check that a
pod requesting 8 units schedules, a second requesting 8 stays Pending, and a pod at
default priority preempts a `dev-env-agent` pod holding the units. One small CUDA
container at a time; no load test. Also read whether NVIDIA's DRA driver supports
splitting one card's memory between claims on these cards yet. Decides D-30's
mechanism.

## S-10: lazy MCP tools (phase 2)

In one session pod, register a loopback MCP server whose `initialize` and
`tools/list` come from a cached manifest, and whose first `tools/call` waits for a
tool pod. Check Claude Code, Codex and opencode each list the tools at start and
complete the call. Then add a server mid-session and see whether each CLI picks it
up without a restart. Decides D-29.

## S-11: opencode with a local coder model (before plan 09)

In one session pod, with a lease on the shared LLM pool and one request at a time:
`opencode run` a small real task (a one-file change and its test) against a Qwen
coder model on llama-server. Check headless run, session resume, MCP over HTTP, the
allow-all permission config, and that tool calls parse. Decides D-33.

## S-12: the baseline guard (phase 1)

In a scratch namespace, as the agent ServiceAccount: try each #3392 path (a Job as
another ServiceAccount, a Job mounting a Secret, a Deployment image patch, a Flux
`spec.path` patch, exec into a pod whose ServiceAccount is on the privileged list)
and each runbook action (rollout restart, CronJob suspend, Flux reconcile and
suspend, a volsync unlock Job, ExternalSecret force-sync). Every path must be
refused and every action allowed. Then, as a `grant-<id>` ServiceAccount bound to
`dev-env-grant-breakglass`: a write and an exec in each dev-env namespace, a pod
under another ServiceAccount, a privileged pod, a Secret read, a TokenRequest, a
new pod or Deployment that mounts a Secret, a Flux Kustomization, an ExternalSecret,
a CRD delete and a write in `kyverno` must all be refused, while an eviction in
`kube-system` is allowed. Check whether the ValidatingAdmissionPolicy sees
`CONNECT` for exec; if not, the Kyverno rule carries it. Decides D-19. The break-glass
half needs the `dev-env-grant-breakglass` role, which plan 07 ships; if phase 1 does
not have it yet, that half runs with plan 07.

## S-13: the GPU budget (before plan 09)

On talosw04 with Tom's lend label set, after S-9: run an agent-priority pod holding
8 units, then create a reserve pod at priority -1 for 8 units. Check the scheduler
preempts the agent pod with its termination grace and places the reserve pod. Then
check that a priority-0 pod preempts the reserve pod, and that a gated pod's node
affinity can be narrowed before its gate is removed. Then remove and restore the lend
label and check that the budgeter drops and re-adds the node with no config change.
Decides D-34's mechanism.

## S-14: satellites (before plan 09, with Tom present)

One machine at a time, Tom at the keyboard:

1. Install a llama.cpp build (Metal on the Mac, CUDA on Windows) and serve one pool
   model behind a throwaway bearer token on the LAN. Record tokens per second for
   that model and the load time from local disk.
2. On the Mac, the same model through `mlx_lm.server`; record tokens per second.
   Note the GPU wired-memory default and the `iogpu.wired_limit_mb` setting.
3. On a PC, start a game and check that the owner-first signal (another process
   holding VRAM) fires within 10 seconds. On the Mac, unplug power and check the
   battery signal.
4. From one session pod, with a temporary egress grant to that machine, call the
   endpoint once.

No load test. Remove the throwaway token afterwards. Decides D-35's engine per OS and
its owner-first signals.

## S-15: archive on reap

Decides DESIGN-001 6.7 (the operator archives a reaped session's Remote Control
entry through the keeper). In the v1 pod, in a scratch config dir with an
access-token-only credential (as S-1):

1. Start `claude --remote-control spike-s15`, send one message, stop it.
2. Archive that session with the CLI's archive call (`POST
   /v1/code/sessions/{id}/archive`, Bearer access token; undocumented, R-02 section 4).
   200 or 409 counts as done.
3. Does the entry leave the phone's active list?
4. Does the documented way back still work: `claude --resume` the conversation, then
   `/remote-control`, which "reopens an archived session"? Record whether `--resume`
   alone brings it back.

A drain never archives, so P-6 does not depend on step 4. If step 2 fails, reaped
entries stay offline, Tom archives them from the console or the app, and the fleet
view counts them.

## S-16: reading plan usage

Decides DESIGN-001 7.3 (quota priority for summoned callers, V-14). In the v1 pod,
once: can the 5-hour and weekly usage that the CLI's `/usage` shows be read with an
access token, without side effects, and how fresh is it? Record the shape (key names)
only. If it cannot, the operator counts quota errors instead.

## Acceptance

- Each spike has a result line in DESIGN-001 section 13 and a dated note in the
  section it decides.
- D-11 and D-12 say which path the build takes; 6.7 records S-6 and S-15, 7.3
  records S-16; D-19, D-22, D-29, D-30, D-33, D-34 and D-35 record their spike's
  result.
