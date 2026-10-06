# 00: spikes

**Status:** backlog
**Depends on:** nothing. S-1, S-2, S-3, S-6 and S-7 run in the v1 pod today; S-4 and
S-5 need two session pods and run in phases 3 and 4.
**Parallel with:** Tom answering Q-01 to Q-05

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

## Acceptance

- Each spike has a result line in DESIGN-001 section 13 and a dated note in the
  section it decides.
- D-11 and D-12 say which path the build takes.
