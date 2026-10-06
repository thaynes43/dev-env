# 03: Remote Control

**Status:** backlog
**Depends on:** 02; 07 (the console is the broker's web UI); spike S-1 (which auth
path), S-5 (cross-pod messaging), S-6 (resume keeps the phone entry), S-15 (archive on
reap). S-2 is answered: the static token cannot register Remote Control.
**Parallel with:** 04

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
    `CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env`. An image CI check fails if
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
  (from `sessions/<pid>.json`) and waits for it to exit inside the grace period.
  `tini` signals only its own child, and the CLI runs under tmux, so nothing else
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
- A reaped remote session's entry is archived, by the CLI on SIGTERM or by the
  keeper's call, and the operator records which in status (S-15).
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
