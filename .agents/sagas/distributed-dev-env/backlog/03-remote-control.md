# 03: Remote Control

**Status:** backlog
**Depends on:** 02; 07's deployed broker core; spike S-1 (which auth
path), S-5 (cross-pod messaging), S-6 (resume keeps the phone entry), S-15 (archive on
reap). S-2 is answered: the static token cannot register Remote Control.
**Parallel with:** 04

**Dependency correction under design (2026-10-08, R-03/Q-18).** Q-16 rules out
the web approval console. A guarded in-app approver would need this plan's login,
refresh and Remote Control core first; making that core wait for human approvals
would create a cycle. R-03 proposes separating the bounded core prerequisite from
the management UI. D-70 withdraws Q-18's earlier premise; no route is selected.
D-71 targets guarded Headlamp replacement, to which these approval candidates
remain relevant. No plan 03 code or login is built by the spike. Preserve the
current work order: approval design, parity audit, keeper
SSH minting, then the prerequisite if chosen. Never copy v1's refresh token.

## Interim pod-owned login slice (2026-10-19): source support only

Using it needs Tom's explicit authorization for one isolated v2 Max login, which is
**pending**; until then no v2 login is created, and nothing here is a ruling. The
keeper-owned login (Q-11), the console and the standby stay in this plan. Story110
T03 builds the source slice; it is not released, installed or accepted.

What the slice does:

- `agent-run --interactive` (Claude only) creates a `remote`-mode session. The
  operator leaves `CLAUDE_CODE_OAUTH_TOKEN` out of the pod and agentd removes it
  from the agent's environment, so a profile `envFrom` Secret cannot bring it back.
- The pod's own CLI owns its login. No refresh token is copied from v1, the keeper
  or another pod. agentd reads `~/.claude/.credentials.json` for two facts only,
  that a login is there and when its refresh token lapses, and never runs
  `claude auth status` against that HOME. No token value reaches a status, log,
  error, event, rescue bundle or history export (the credentials file stays
  `HistoryClassCredential`; the rescue copies repositories only).
- With no usable login agentd only reports `status.agent.status: login-required`
  and `status.remoteControl.state: login-required`, and holds the agent. **It never
  starts the login by itself.** `claude auth login` runs only after an explicit
  `agent-run login <name> url` (`agentd ctl login url`). The ceremony is the v1
  renewal one, driven by a coordinator: `url` prints the sign-in link, the owner
  signs in and sends back the code, and `agent-run login <name> code` reads it from
  stdin. Both run `agentd ctl login` in the pod by `kubectl exec`; the link and code
  are never logged or stored. agentd starts `claude --remote-control <session-name>`
  once the login exists.
- Renewal and recovery use the same command. `login url` is allowed whenever no
  agent CLI runs, even if the login still reads as usable (renew before it lapses,
  or replace one the server revoked); it is refused while the agent's CLI runs,
  because only one CLI may own the login. The refusal says how to proceed: exit the
  TUI (`agent-run attach`, then `/exit`), then run `login url` again. When the
  ceremony's window ends, agentd starts or resumes the agent by itself, at boot or
  later.
- The exec uses kubectl's SPDY transport (`KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false`),
  which is a POST. The v1 pod's service account may create `pods/exec` in
  `dev-agents` but not get it, and kubectl's default WebSocket exec begins as a GET.
  No RBAC is added. Live exec is unverified; if it is refused, that is a reported
  gap, not an API route in this slice.
- agentd reports `status.remoteControl.{name,sessionId,url,state}` from
  `~/.claude/sessions/<pid>.json`: `starting` until the record holds a
  `bridgeSessionId`, then `registered` with the link
  `https://claude.ai/code/<bridgeSessionId>`; `offline` once this boot's CLI is gone.
- Stop, resume and a pod roll on the same HOME use agentd's existing SIGTERM
  forwarding and `claude --resume <id> --remote-control <name>` (S-6). A remote
  session idle for 72h (the default idle window) is suspended, which archives its
  phone entry until it resumes. Update survival is not accepted until a real pod
  roll shows the same entry and history.
- Egress needs no change: session pods may reach any public address on TCP 80 and
  443 (`dev-env-session-web`).

Limits: each remote session has its own login and needs its own ceremony; reaping
a session deletes its login, and a rescue restore never carries one. Staying in
plan 03: the keeper-owned login, refresh and fencing, `dev-env-claude-live`, the
console and its renewal page, the standby, archive on reap, messaging tier 2, and
the 401-window resume.

## Shared-login projection consumer (Story110 T09 increment 1, 2026-10-19): source and unit tests only

Q-11/D-11 stays the target. This is the first of the six increments of the
keeper-owned shared login, and only the consuming side. It is **not released,
installed or accepted**; nothing publishes `dev-env-claude-live` yet, so no pod
runs it.

Delivered in source (`internal/claudeauth`, `internal/agentd/claude_auth.go`):

- Projection mode is selected only by the profile variable `AGENTD_CLAUDE_LIVE_FILE`
  (the mounted `access.json`). Unset, the task's static token and the interim
  pod-owned login above behave exactly as before.
- `claudeauth` holds the strict projection document (generation, access token,
  expiry, scopes, subscription type, rate-limit tier, account and organization
  uuids). A refresh token is not representable, and a document that carries one
  (any member naming `refresh`) is refused.
- agentd merges only `claudeAiOauth.{accessToken, expiresAt, scopes,
  subscriptionType, rateLimitTier}` into the pod's own `.credentials.json`: 0600,
  atomic rename, every CLI-owned member (`mcpOAuth`, unknown keys) kept, nothing
  rewritten when the fields already match. It follows new generations on the
  daemon's existing 5 s poll.
- A generation receipt on the volume refuses an older generation and a changed
  document of the same generation, across restarts.
- `.claude.json` `oauthAccount` is seeded from the projection's uuids, also when the
  projection lands after boot.
- Fail closed: a credentials file that already holds a refresh token (a pod-owned
  login) is never merged into, the agent is not started, and the boot reports
  `login-conflict`. Projection mode refuses `login url|code` and never runs the
  ceremony; a missing, invalid or expired projection holds the agent as
  `login-required`.
- `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` joins the remote-mode unset list, for both
  projection and pod-owned pods.

Increment 2 (2026-10-19): source and unit tests only. After agentd merges a new
generation, a Claude remote or local TUI whose last turn ended in the revoked or
expired token error gets one `continue` through `Deliver` (`internal/agentd/claude_continue.go`).
Signal: the last user or assistant entry of the session's own transcript
(`projects/*/<conversation id>.jsonl`); agentd keeps no turn record for a TUI, so
there is no session record to prefer. The CLI's real error text is **Unknown** until
the checkpoint; the fixture shape (an assistant entry flagged `isApiErrorMessage`, or
text starting "API Error", naming a revoked or expired OAuth token) is what to confirm.
`claude-continue.json` records the generation last evaluated and is written before the
send, so repeated polls and an agentd restart send once; nothing is sent when the last
turn is healthy, the merge was held back, or the pod is in task mode or pod-owned
mode. A user turn last (a request that may still be on the old token) and an agent
that is not running decide nothing and are looked at again on the next poll, as is a
Deliver failure before the paste; a failure after the paste is not repeated.

Still in this plan (increments 3 to 6): keeper journal, transport, publisher and the refresh worker with its Lease fence
(`--enable-claude-auth`, default off); the keeper-side login helper; the GitOps
profile, mounts, RBAC and image pins; then the bounded real checkpoint (CLI 2.1.296
revalidation, merge latency, a session past 13 hours, two pods on one login). Unproven
until then: that the CLI never refreshes a token file without a refresh token, merge
latency through the kubelet's Secret propagation, and many pods on one account.

Increment 3 status (2026-10-19): the keeper's Claude transport, private journal and access-only publisher (`internal/keeper/claude_*.go`) are source and unit tests only. Nothing calls them yet: no worker, flag or wiring (increment 4), and they are not released, installed or accepted.

Increment 4 status (2026-10-19): the keeper's Claude refresh worker (`internal/keeper/claude_worker.go`) and its wiring are source and unit tests only, behind `--enable-claude-auth` (default off; `Options.ClaudeAuth`), so deploying the binary changes nothing. The worker is a leader-only runnable using the same uncached client as Codex's, so RBAC stays get+patch by name. Per cycle: Lease budget check, journal `RefreshIntent` (CAS), budget check again, one POST, journal the replacement (CAS), budget check, publish the access-only projection. Due once per access-token life, with the last third as lead. A `RefreshIntent` found at the start of a tick (a crash or a lost save after a POST) becomes `NeedsLogin` with the refresh credential dropped; the POST is never replayed. After a successful POST the save and the publication run on a context detached from shutdown (each step keeps its own 2 s timeout), so a keeper stopping right then still keeps the replacement; a stop during the POST itself, or an API server that is unreachable, still ends in `NeedsLogin`. Excluded and still open: the login ceremony (increment 5), haynes-ops flags, RBAC, mounts and image pins, the console, metrics, and the live-provider checkpoint (increment 6). Not released, installed or accepted.

## Goal

Tom drives v2 sessions from his phone. The keeper is the sole owner of the Max login
(Q-11, Tom 2026-10-06), and Tom renews it each month on a console page behind
Authentik instead of a chat relay. The console lists every session's link and state
with an archive button. The operator keeps a standby session ready the way v1's
post-ready does. DESIGN-001 3.8, 6.2, 6.7; R-02.

## Scope

- **If S-1 passed (the target).** *S-1 passed on 2026-10-06, so this branch is the
  build (DESIGN-001 6.2 has the result).* The keeper makes its own fresh `/login` and
  owns it. It refreshes once per access-token life, well inside the 8 hours, and
  writes `dev-env-claude-live` (access token, expiry, scopes, subscription type,
  rate-limit tier; no refresh token) the moment the refresh returns. Each refresh
  revokes the previous access token in every pod at once (S-1).
  - **Fence each refresh (D-52).** The keeper's Lease alone does not fence writes: a
    keeper paused past its renew deadline can finish a refresh after the next
    leader has started, and two refreshes of one token revoke the family. Right
    before the refresh call, the keeper re-reads its Lease and refreshes only if it
    still holds it with more than the call's timeout left. Decide here whether the
    Max login's writes need 6.11's `get` and `update` on the Secrets, which plan 01
    left out; drop them from 6.11 if not.
  - agentd watches that Secret and merges those keys into the pod's own writable
    `~/.claude/.credentials.json` (0600, atomic rename, keeping the CLI's `mcpOAuth`)
    as soon as it changes; the Secret is never mounted as the file. Its merge latency
    is the 401 window: a turn that lands in it fails once, and
    `CLAUDE_CODE_OAUTH_401_WAIT_MS` does not cover a credentials file (S-1).
  - After each merge, agentd resumes a session whose last turn ended in "OAuth token
    revoked" since the previous token was revoked: it sends one `continue` turn, so an
    unattended session does not stall at "Please run /login" (DESIGN-001 6.2). This
    plan picks the signal, the transcript's last entry or the session record.
  - agentd seeds `.claude.json` with the onboarding flags and worktree trust (S-1: a
    cold TUI otherwise stops on the theme, security and trust prompts), and with
    `oauthAccount` (the account and organization uuids from the keeper's Secret). It
    copies nothing per machine. `oauthAccount` is required: with the flags seeded,
    the Remote Control check runs before the CLI's own profile fetch can land and
    refuses without it (S-6, 2026-10-06; this bullet said it was optional).
  - Remote pods unset `CLAUDE_CODE_OAUTH_TOKEN` and set
    `CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env`. agentd removes the token
    from the agent's environment in remote mode: the operator leaves it out of a
    remote pod, but a profile's `envFrom` Secret could still carry it (D-44). An image CI check fails if
    the image sets `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, `DISABLE_GROWTHBOOK`,
    `DISABLE_TELEMETRY` or `DO_NOT_TRACK`.
- **If S-1 failed** (not taken; kept for a CLI release that breaks the target): the
  coordinator host (DESIGN-001 6.2): one long-lived session
  pod of kind `coordinator-host` that owns `.credentials.json` on its volume and runs
  every Remote Control session as a tmux window, summoned `remote` sessions included.
- **Links:** agentd reports `status.remoteControl.{sessionId, url, state}` from
  `~/.claude/sessions/<pid>.json`, not from the pane; the URL only after registration.
  The name it reports is the one it passed: the record's `name` is a local name
  derived from the cwd (S-6).
- **Archive on reap:** after the bundle is verified, the operator asks the keeper to
  archive the entry (S-15, passed 2026-10-06: 200 on an offline entry, 200 again on a
  repeat), records the result, and counts unarchived offline entries in
  `agent-run fleet`. The SIGTERM agentd forwards already makes the CLI
  archive its own entry (S-6), so the call matters when the CLI died first (SIGKILL,
  OOM, node loss).
- **Shutdown and drain:** on its own SIGTERM, agentd sends SIGTERM to the CLI's pid
  (from its own record, DESIGN-001 D-42) and waits for it to exit inside the grace
  period. `tini` signals only its own child, and the CLI runs under tmux, so nothing else
  reaches it. The CLI then archives its entry, and agentd's `claude --resume <id>
  --remote-control <name>` unarchives it and reattaches (S-6). agentd does not
  SIGKILL the CLI to keep the entry listed.
- **The console** (the broker's web UI, 3.8): sessions with link, state and an
  archive button; the Claude login page (days left; Renew runs the ceremony in the
  page; the link and code are never logged or stored; a waiting login ends after 10
  minutes); credential status for every credential; the codex hub's state.
- `agent-run auth status|login|code` as the laptop fallback; the keeper's daily check
  pages Tom at 7 days or fewer, with a link to the console page (replacing v1's
  auth-watch for this credential).
- `remote` mode in `agent-run --interactive`; the Remote Control name is the session
  id. The coordinator system prompt that v1's agent-run adds for `both` mode carries
  over.
- The standby: on start, and when the last one is reaped, the operator creates one
  `remote` session on haynes-ops; three creations inside 30 minutes trip the circuit
  breaker.
- Messaging tier 2 per S-5.

## Acceptance

- Tom starts and drives a v2 Remote Control session from his phone, and the console
  shows its link as registered.
- A coordinator session dispatches a v2 task pod with `agent-run -p` and gets the
  result back.
- Tom renews the keeper's login from the console page on his phone, with no chat
  relay, and the page shows the new expiry.
- A reaped remote session's entry ends archived, by the CLI on SIGTERM or by the
  keeper's call, and the keeper's result is recorded in status. A repeat archive
  returns 200 (S-15), so that result does not show whether the CLI archived first.
- A drained remote session comes back as the same phone entry, with its history: the
  same `bridgeSessionId`, unarchived by the resume (S-6). In a v2 pod the CLI gets
  agentd's forwarded SIGTERM and archives its entry on the way out (`Torn down
  (archive=200)` in its debug log); it is not SIGKILLed at the end of the grace
  period. A reaped one leaves the
  phone's active list (or, if S-15 failed, is archived from the console).
- No pod other than the credential owner holds a refresh token (checked by listing
  the credentials files' keys, never their values).
- A Remote Control session older than 13 hours still takes a message from the phone.
  By then it has lived through at least one keeper refresh and one re-mint of its
  bridge worker JWT (46800 s), which S-1 did not reach.
- agentd's merge latency after a keeper refresh is measured and recorded: the time
  from the Secret's update to the pod's file changing.
- A turn that fails in the 401 window is resumed by agentd after its merge, with no
  message from Tom. The test forces it once by holding back one merge in a test pod.
