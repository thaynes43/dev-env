# 00: spikes

**Status:** backlog
**Depends on:** nothing. S-1, S-2, S-3, S-6 and S-7 run in the v1 pod today; S-4 and
S-5 need two session pods and run in phases 3 and 4; S-8 and S-12 run in phase 1,
S-10 in phase 2, S-9, S-11, S-13 and S-14 before plan 09.
**Parallel with:** Tom answering Q-09 and Q-10 (Q-01 to Q-08 were answered on
2026-10-06)

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

Decides DESIGN-001 D-11 (keeper-owned Max login vs the coordinator host).

1. `mkdir -p /tmp/spike-s1/claude && chmod 700 /tmp/spike-s1/claude`; write
   `.credentials.json` there from the live file with the refresh token removed
   (`jq '.claudeAiOauth |= del(.refreshToken)'`).
2. In a tmux session, run `CLAUDE_CONFIG_DIR=/tmp/spike-s1/claude env -u
   CLAUDE_CODE_OAUTH_TOKEN claude --remote-control spike-s1`. Does it register (a
   claude.ai URL in the pane)?
3. After the live login next refreshes, copy the new access token into the scratch
   file the same way. Does the running session use it without a restart?
4. Let the scratch token near expiry. Does the CLI try to refresh, and what does it
   do without a refresh token? Also try `CLAUDE_CODE_HOST_CREDS_FILE` pointing at the
   scratch file, and note whether `claude --help` or the docs mention it by then.

Pass = 2 and 3 work, and 4 fails harmlessly (an error or a wait, never a write to the
live login).

## S-2: static token and Remote Control

One probe: `CLAUDE_CONFIG_DIR=/tmp/spike-s2 claude --remote-control spike-s2` with
`CLAUDE_CODE_OAUTH_TOKEN` set and no `.credentials.json`. If it registers, the keeper
is not needed for Claude at all; record the CLI version.

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
Does the phone show the same entry, with the history? Decides DESIGN-001 6.7.
This spike runs on the live Max login, like any v1 Remote Control session: keep it to
a few minutes and stop it when done.

## S-7: clone time per repo

For each of haynes-ops, haynesnetwork, hass-sandbox, cigar-journal and haynes-quest,
one at a time: `time git -c pack.threads=2 clone --filter=blob:none <url>
/tmp/spike-s7/<repo>` then `git worktree add`, then delete it. Record the wall time.
A repo over two minutes gets a shared mirror (D-15).

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
`CONNECT` for exec; if not, the Kyverno rule carries it. Decides D-19.

## S-13: the dynamic GPU budget (before plan 09)

On talosw04 with Tom's lend label set, after S-9: run an agent-priority pod holding
8 units, then create a reserve pod at priority -1 for 8 units. Check the scheduler
preempts the agent pod with its termination grace and places the reserve pod. Then
check that a priority-0 pod preempts the reserve pod, and that a gated pod's node
affinity can be narrowed before its gate is removed.
Separately, read each household probe once a minute for an hour of normal use, never
writing: Ollama `GET /api/ps`, ComfyUI `GET /queue`, Immich `GET /api/jobs`,
llama-server `GET /slots`, beside the exporter's used VRAM. Record whether each probe
shows demand before the VRAM rises, and by how much. Decides D-34's probes and its
15-minute cool-down.

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

## Acceptance

- Each spike has a result line in DESIGN-001 section 13 and a dated note in the
  section it decides.
- D-11 and D-12 say which path the build takes; D-19, D-22, D-29, D-30, D-33, D-34
  and D-35 record their spike's result.
