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
   (`jq '{claudeAiOauth: (.claudeAiOauth | del(.refreshToken))}'`), mode 0600, and
   nothing else: no `.claude.json`, as a fresh v2 pod has. Drop `mcpOAuth` too: its
   connector entries carry refresh tokens of their own (corrected 2026-10-06; the
   earlier `.claudeAiOauth |= del(.refreshToken)` kept them).
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

- [x] **Done 2026-10-06: passed, with one caveat.** CLI 2.1.292, in the v1 pod, with a
  cold `CLAUDE_CONFIG_DIR` and `HOME` and a cleared environment:
  - **Step 2:** it registered (a `bridgeSessionId`) with no `.claude.json` seeding. The
    CLI fetched the profile and its feature flags and wrote `oauthAccount` and
    `cachedGrowthBookFeatures` itself. A cold TUI first stops on
    three prompts: theme, security notes, folder trust. (S-6 found that with those
    prompts' flags seeded, Remote Control also needs a seeded `oauthAccount`.)
  - **Step 3:** after the merge, the next request used the new token. No restart.
  - **Step 4:** the refresh revoked the previous token at once ("OAuth token revoked"
    on the first check, 22 s after the refresh). A turn in the gap fails after two
    tries, about 2 s, and asks for `/login`. Remote Control stays registered, and the
    first turn after the merge works. `CLAUDE_CODE_OAUTH_401_WAIT_MS=60000` changed
    nothing: per the binary, the wait only polls for a rotated env or file-descriptor
    token. The window is agentd's merge latency.
  - **Step 5:** with no refresh token the CLI never refreshes. An expiry inside the
    5-minute margin, or already past, still sends the token; a revoked token ends in an
    error. It never wrote a credentials file.
  - **Method for 4 and 5:** besides the running session, one-shot `claude -p` calls in
    their own scratch dirs, with `expiresAt` edited to put a valid or a revoked token
    inside its margin, past its expiry, or in the future (the state a pod is in right
    after a keeper refresh). The live login was only read.
  - **S-1b** registered with `CLAUDE_CODE_OAUTH_TOKEN` and `CLAUDE_CODE_OAUTH_SCOPES`;
    `CLAUDE_CODE_SUBSCRIPTION_TYPE` was not needed.
  - **Debug log:** `--debug-file` logs endpoints, not bodies: `POST /v1/code/sessions`,
    `/bridge` (a worker JWT for 46800 s), the worker event stream, `/client/presence`,
    and `/archive` on `/exit`. No `/v1/environments/bridge`, so no machine identity.
  - Details and consequences: DESIGN-001 6.2 (D-11) and section 13.

## S-2: static token and Remote Control

**Answered 2026-10-06: it cannot register.** The docs say "Remote Control requires a
full-scope login token", the 2.1.284 binary refuses tokens without `user:profile`
(`token_scope_limited`), and R-01 F-01 found all 45 `wo-*`/`esc-*` executor sessions
since 2026-09-03 rejected on a credentials file synthesized from the setup token. On
each CLI bump, one probe confirms it still holds:
`CLAUDE_CONFIG_DIR=/tmp/spike-s2 claude --remote-control spike-s2` with
`CLAUDE_CODE_OAUTH_TOKEN` set and no `.credentials.json`.

- [x] **Re-probed 2026-10-06 on CLI 2.1.292: still refused.** The probe ran as above,
  in a cleared environment with its own `HOME`, and the token was read from the pod's
  environment, never put on a command line. A cold home first stops on the theme,
  security and trust prompts, and the trust prompt's default is "No, exit". Past them,
  the CLI printed "Remote Control requires a full-scope login token. Long-lived tokens
  (from `claude setup-token` or CLAUDE_CODE_OAUTH_TOKEN) are limited to inference-only
  for security reasons" and "--rc flag ignored". `sessions/<pid>.json` has no
  `bridgeSessionId`. The refusal names the token's scope, so it needed no
  `oauthAccount` seeding (S-6) to be clear.

## S-3: Codex on an access token

Scratch `CODEX_HOME=/tmp/spike-s3`. Feed the access token from the live `auth.json`
to `codex login --with-access-token` through a pipe. Then `codex exec "reply ok" <
/dev/null`. Then start the app-server with remote control in the scratch home
**without pairing** and see whether it runs. Note how codex behaves when the token
expires. Pass = exec and app-server run on the access token alone. (Corrected
2026-10-06 by the run: `--with-access-token` does not take this token. The credential
that works is an `auth.json` written by `jq`, as below.)

- [x] **Done 2026-10-06: passed, on an `auth.json` with no refresh token, not through
  `--with-access-token`.** codex 0.160.1, in the v1 pod, one call at a time, in a
  scratch `CODEX_HOME` and `HOME` with a cleared environment. The live `auth.json` was
  only read: its hash and mtime were the same after every step. The `codex-remote`
  daemon was not touched.
  - **`--with-access-token` refuses a ChatGPT token.** It exits 1 with "agent identity
    JWT payload is not valid JSON" and writes nothing. In 0.160.1 that flag and
    `CODEX_ACCESS_TOKEN` take only an Agent Identity JWT or an `at-` personal access
    token (`codex-rs/login/src/auth/access_token.rs`). The ChatGPT OAuth access token
    in `auth.json` is neither.
  - **The credential that works** is S-1's method, applied to Codex: `jq` writes
    `auth.json` (mode 0600) from the live file with `auth_mode: chatgpt`,
    `tokens.{id_token, access_token, account_id}`, an empty `refresh_token` (the field
    is a required string) and `last_refresh`.
  - **`codex exec "reply ok"`** answered `ok`, exit 0 (`gpt-6-luna`, effort low).
  - **The unpaired app-server** (`codex app-server --remote-control --listen
    stdio://`: the daemon's remote-control transport, without the managed daemon or its
    updater) enrolled and reached `connected` in 1.4 s, and one turn answered `ok`. A
    small stdio JSON-RPC driver sent `initialize`, `remoteControl/status/read`,
    `thread/start` and `turn/start`.
  - **Enrolment.** The app-server enrols one server per `installation_id`, named by
    `gethostname()`. There is no name override, and the v1 pod allows no UTS
    namespace, so the entry could not be named `spike-s3`. It carries the v1 pod's
    hostname, `dev-env-577b4d8f7c-nnfzv`, and was never paired. The CLI has no call to
    remove an enrolment (only enroll and refresh), so that offline server stays on the
    account.
  - **Token life and refresh.** The access token lives 10 days (`iat` to `exp`). Codex
    refreshes when the cached token is within 5 minutes of its `exp`, or after a 401.
    The 8-day `last_refresh` rule applies only when `exp` cannot be read. With an empty
    refresh token, nothing can rotate.
  - **Expiry.** With a synthetic JWT whose `exp` was an hour past (unsigned, not a real
    token), remote control went `errored` at once. The turn failed after 25 s of
    retries (`workspace routing discovery unauthorized (401)`). Meanwhile codex called
    the refresh endpoint 85 times in 26 s, about three a second, each with the empty
    refresh token, and each was refused with `400 empty_string`. Nothing from the token
    family is sent, so the live login is safe, but the calls go on until the file
    changes.
  - **Pickup without a restart.** A valid file renamed into place, in the same
    process: remote control was `connected` again 4 s later, and the next turn answered
    `ok`. Before it refreshes, codex reloads `auth.json` and uses a changed file (the
    "guarded reload" in `refresh_token()`). The first step of its 401 recovery is the
    same reload.
  - **Not tested:** whether an OpenAI refresh revokes the previous access token. The
    live login next refreshes near 2026-10-09T00:59Z. D-12 step 2 holds either way: a
    revoked token gives a 401, and the 401 reload picks up agentd's merge.
  - **Also in 0.160.1:** `account/login/start` with `chatgptAuthTokens` lets an
    app-server client supply the access token and answer
    `account/chatgptAuthTokens/refresh`. The source marks it "FOR OPENAI INTERNAL USE
    ONLY - DO NOT USE", so it was not tried.
  - The scratch files were shredded. Consequence: DESIGN-001 6.3 (D-12 step 2).

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

- [x] **Done 2026-10-06: passed, with one finding.** CLI 2.1.292, in the v1 pod. It ran
  on a scratch access-token-only home set up as S-1's (a cleared environment, its own
  `HOME` and `CLAUDE_CONFIG_DIR`), not on the live login, so nothing could refresh the
  live login. The account and organization are the same, so the owner check is the
  same, and this is the state a v2 pod runs in.
  - **Run:** `--remote-control spike-s6` in a scratch worktree, one message, then
    SIGTERM sent to the CLI's pid (the signal a pod deletion sends; in a v2 pod
    agentd must forward it, DESIGN-001 6.7). Then `claude --resume <conversation-id>
    --remote-control spike-s6` and a second message.
  - **Result:** the new `sessions/<pid>.json` holds the same `bridgeSessionId`. The
    debug log says "Reattaching to session" and creates no new session. The server's
    event list for that id (`GET /v1/code/sessions/{id}/events`, the CLI's own read)
    holds both turns, so the phone entry keeps its history.
  - **Finding: SIGTERM archives the entry.** On SIGTERM the CLI tears its bridge down
    and archives the entry (`archive=200`, exit 143), as `/exit` does (S-1). The resume
    then unarchived it (`Unarchive … status=200`) and reattached. So a drain archives
    and the resume reopens it: the same entry and history come back, but the entry is
    off Tom's active list while the session is drained. SIGKILL does not archive; the
    entry goes offline (S-15 step 1). The binary has no setting to skip the archive on
    exit: only internal paths set `skipArchive`, such as the CLI's self-update restart.
  - **Seeding needs `oauthAccount`.** With the onboarding flags and trust seeded and no
    `oauthAccount`, Remote Control was refused ("Unable to determine your
    organization", `--rc flag ignored`). The eligibility check reads only the cached
    `oauthAccount`, and it ran 0.3 s after start. The CLI's own profile fetch in that
    run failed (the log redacts why; the same `GET /api/oauth/profile` from curl, with
    or without the beta header, returned 200). S-1's unseeded run had the three
    onboarding prompts to give that fetch time. Seeding `oauthAccount` with the account
    and organization uuids only, step 2's fallback in S-1, fixed it.
  - **The record's `name` is not the title.** `sessions/<pid>.json` → `name` is a local
    name derived from the cwd (`wt-bc`, then `wt-d4` after the resume). The Remote
    Control title is the name on the command line: `GET /v1/code/sessions/{id}`
    returned `title=spike-s6`.
  - The entry was archived at the end (SIGTERM again, `archive=200`) and the scratch
    files were shredded. Consequences: DESIGN-001 6.2 (D-11) and 6.7.

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

- [x] **Done 2026-10-07: `gasha01-rbd` is 1.55 to 1.81 times slower overall, under the
  line of two, so size L stays on `gasha01-rbd` (D-22) and the templates do not
  change.** The harness was a Job in `dev-agents`, created from the v1 pod (its
  OPERATOR tier allows batch Jobs) and deleted after the run, rather than a session's
  own volume: one pod could then mount a 20Gi volume of each class and run them one
  after the other on the same node, CPU and network. The pod used the agent image
  `dev-env:2.0.0@sha256:8bab980d…` with size M's requests and limits (250m and 2Gi;
  4 CPU and 8Gi) on talosw02. Each run put `HOME`, the pnpm store and the caches on
  the volume under test, then ran `git -c pack.threads=2 clone --filter=blob:none`
  of haynesnetwork, `pnpm install --frozen-lockfile`, and
  `vitest run lib/__tests__/app-error.test.ts --maxWorkers=2` in `apps/web`. The
  second run swapped the order, so warm registry caches favour neither class. Pod
  UIDs `9b4d7976-18bf-4d76-9da5-d95f1e32a72e` and
  `00417e85-a094-41e8-a611-ecaeae0781ff`. Seconds:

  | Run | Class | Clone | Install | Test | Total |
  |---|---|---|---|---|---|
  | 1 | `gasha01-rbd` (first) | 6.1 | 18.9 | 0.7 | 25.7 |
  | 1 | `ceph-block` | 4.6 | 8.9 | 0.7 | 14.2 |
  | 2 | `ceph-block` (first) | 4.7 | 9.4 | 0.7 | 14.8 |
  | 2 | `gasha01-rbd` | 5.9 | 16.4 | 0.7 | 23.0 |

  The install is most of the gap (1.7 to 2.1 times); the one test file is too small
  to tell the classes apart. Recheck with a heavier step if size L sessions feel slow.

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
a CRD delete, proxy or port-forward access and a write in `kyverno` must all be refused, while an eviction in
`kube-system` is allowed. Check whether the ValidatingAdmissionPolicy sees
`CONNECT` for exec; if not, the Kyverno rule carries it. Decides D-19. The break-glass
half needs the `dev-env-grant-breakglass` role, which plan 07 ships; if phase 1 does
not have it yet, that half runs with plan 07.

- [x] **Done 2026-10-07: the baseline half passed, 45 of 45 checks.** haynes-ops #3477
  deployed the RBAC and guards (#3479 fixed a target and re-ran). Job
  `dev-agents/s12-baseline-guard-2` (pod `s12-baseline-guard-2-bzwzp`, UID
  `f917d2f6-9ac2-4466-b60c-9d2282914b86`, talosw01) ran as `dev-env-agent` at 02:08Z.
  It used `flux-cli` 2.9.6, a 250m CPU limit and one `kubectl` call at a time. It
  tested real objects, not a scratch namespace: every write was a server-side dry run,
  so a guard that failed would have let nothing through. Exec ran `true`. One
  `flux reconcile source git haynes-ops` was real. The script is
  `kubernetes/main/apps/dev-env-system/rbac-s12/app/s12.sh` at haynes-ops `efcf9860`.
  #3490 removed the Job, and plan 07 redeploys the script for the break-glass half.
  Results are in DESIGN-001 section 13 and D-19.
  - **The ValidatingAdmissionPolicy sees `CONNECT`.** Exec into the Job's own pod
    (N1) and API-server proxy into it (N2) were refused by `dev-env-identity-guard`.
    The VAP also refused exec into `flux-system`, `kyverno` and `kube-system` pods. So
    the VAP carries the namespace half of the exec rule, and Kyverno carries the
    pod-lookup half (D-19).
  - **First run, 44 of 45.** P5f picked a Completed CronJob pod that runs as
    `dev-env-ops`, and kubectl refused to exec before sending a request. A test
    fault; the re-run selects only Running pods.

Where both VAP guards refuse a request, the API server reports one of them, and which
one varied between the two runs; "a VAP guard" marks those rows.

| Check | Expected | Decided by |
|---|---|---|
| P1 Job as `frontend/headlamp` | refused | a VAP guard |
| P2, P2b Job with a Secret outside the short list (envFrom; volume) | refused | a VAP guard |
| P2c to P2e privileged, hostPath, host-PID Job | refused | a VAP guard |
| P2f Job named `volsync-src-s12` with `SYS_ADMIN` and Unconfined seccomp (Kyverno's PSS exception matches the name) | refused | a VAP guard |
| P2g Job with a hostPort | refused | a VAP guard |
| P3, P3d Deployment image; `spec.paused` | refused | a VAP guard |
| P3b StatefulSet env | refused | a VAP guard |
| P3c DaemonSet ServiceAccount | refused | a VAP guard |
| P3e CronJob jobTemplate image | refused | a VAP guard |
| P4, P4b Kustomization `spec.path`; HelmRelease values | refused | a VAP guard |
| P4c, P4d GitRepository url; Kustomization label | refused | a VAP guard |
| P4e ExternalSecret spec | refused | a VAP guard |
| P5 exec into the headlamp pod (cluster-admin) | refused | Kyverno exec guard |
| P5b exec into a host-PID `node-exporter` pod | refused | Kyverno exec guard |
| P5c to P5e exec into `flux-system`, `kyverno`, `kube-system` pods | refused | identity guard (VAP, `CONNECT`) |
| P5f exec into the v1 `dev-env-ops` pod | refused | Kyverno exec guard |
| N1, N2 exec and proxy into a `dev-agents` pod | refused | identity guard (VAP, `CONNECT`) |
| N3, N4 pod delete, Job create in `dev-agents` | refused | identity guard |
| N5 Secret list | refused | RBAC |
| A1 to A1c rollout restart Deployment, StatefulSet, DaemonSet (the patch `kubectl rollout restart` sends; a DaemonSet's server-bumped `deprecated.daemonset.template.generation` is allowed) | allowed | |
| A2 CronJob `spec.suspend` | allowed | |
| A3, A3b reconcile annotations on a Kustomization; HelmRelease with `forceAt` | allowed | |
| A3c `flux reconcile source git haynes-ops` (real) | allowed | |
| A4, A4b `spec.suspend` on a Kustomization and a HelmRelease | allowed | |
| A5 volsync unlock Job (`default` SA, `envFrom` the repo's `-volsync-aws-secret`) | allowed | |
| A6 ExternalSecret `force-sync` | allowed | |
| A7 pod delete; A8 exec into the rook toolbox (real) | allowed | |
| A9 API-server proxy to Prometheus; A10 CNPG PVC delete in `database`; A11 read `agentsessions` | allowed | |

- [ ] **The break-glass half** (a `grant-<id>` identity bound to
  `dev-env-grant-breakglass`) runs with plan 07. Until then, only a local
  kube-apiserver 1.35 has checked the identity guard's grant rules (D-19's note).

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

If step 2 fails, reaped entries stay offline, Tom archives them from the console or
the app, and the fleet view counts them. (Corrected 2026-10-06 by S-6: this said "a
drain never archives, so P-6 does not depend on step 4". A SIGTERM to the CLI does
archive, and S-6's `--resume <id> --remote-control <name>` unarchived the entry, so
P-6 rests on that unarchive.)

- [x] **Done 2026-10-06: passed.** CLI 2.1.292, in the v1 pod, on a scratch
  access-token-only home set up as S-6's, with `oauthAccount` seeded.
  - **Step 1:** `--remote-control spike-s15`, one message, then SIGKILL, so the CLI
    could not archive its own entry (a SIGTERM would have, S-6). The entry went
    offline: `GET /v1/code/sessions/{id}` showed `status=active` and
    `connection_status=disconnected` about 50 s after the kill.
  - **Step 2:** `POST /v1/code/sessions/{id}/archive` (the `cse_` form of the id),
    body `{}`, the access token as `Bearer` and `anthropic-version: 2023-06-01`,
    returned 200 with a `session` object. A repeat call also returned 200, not 409,
    so the call is idempotent. The server then showed `status=archived`.
  - **Step 3:** Tom looked at his Claude app's Code session list, offline entries
    included, after the archive: "Not listed".
  - **Step 4:** `claude --resume <conversation-id>` alone, with no `--remote-control`,
    brought it back. The CLI reattached the persisted bridge session
    ("restored_owner_match"), unarchived it (`Unarchive … status=200`) and kept the
    same `bridgeSessionId`. A new message landed in the same entry, next to the first,
    so `/remote-control` was not needed.
  - The entry was archived again at the end (SIGTERM, `archive=200`), and the scratch
    files were shredded. Consequence: DESIGN-001 6.7 (P-7).

## S-16: reading plan usage

Decides DESIGN-001 7.3 (quota priority for summoned callers, V-14). In the v1 pod,
once: can the 5-hour and weekly usage that the CLI's `/usage` shows be read with an
access token, without side effects, and how fresh is it? Record the shape (key names)
only. If it cannot, the operator counts quota errors instead.

- [x] **Done 2026-10-06: the read works, with no side effects.** One `GET
  https://api.anthropic.com/api/oauth/usage` with the access token as `Bearer` (and
  `anthropic-beta: oauth-2025-04-20`) returned 200 in 0.2 s. It is the call the CLI's
  `/usage` makes: its default `plain` variant, read from the 2.1.292 binary. It is a
  GET made without a refresh token, so it can neither write nor rotate anything.
  - **Shape (key names only):** `five_hour` and `seven_day`, each with `utilization`,
    `resets_at`, `limit_dollars`, `used_dollars`, `remaining_dollars` and
    `locked_reason`. Then `seven_day_opus`, `seven_day_sonnet`,
    `seven_day_oauth_apps` and more per-model or promotional buckets, each null or the
    same shape. `extra_usage` has `is_enabled`, `monthly_limit`, `used_credits`,
    `utilization` and more. `limits[]` entries have `kind`, `group`, `percent`,
    `severity`, `resets_at`, `scope` and `is_active`, with kinds `session`,
    `weekly_all` and `weekly_scoped`. `utilization` is a whole-number percent,
    `resets_at` an ISO 8601 time, and the dollar fields were null on this Max plan.
  - **Freshness:** computed per request. There is no `cache-control` or `age` header,
    `cf-cache-status` is `DYNAMIC`, and `seven_day.utilization` changed between two
    reads 2 minutes apart (22:28 and 22:30Z) while the v1 pod was busy. `resets_at` is
    the top of an hour with sub-second jitter per response, so compare it to the
    minute.
  - The same numbers also reach a Remote Control session: its
    `external_metadata.rate_limit_info` (`status`, `rateLimitType`, `resetsAt`) and
    its `rate_limit_event` stream events (seen in S-6).
  - Values were read locally only and the scratch files were shredded. Consequence:
    DESIGN-001 7.3.

## Acceptance

- Each spike has a result line in DESIGN-001 section 13 and a dated note in the
  section it decides.
- D-11 and D-12 say which path the build takes; 6.7 records S-6 and S-15, 7.3
  records S-16; D-19, D-22, D-29, D-30, D-33, D-34 and D-35 record their spike's
  result.
