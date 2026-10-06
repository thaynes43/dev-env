# 03: Remote Control

**Status:** backlog
**Depends on:** 02; spikes S-1 and S-2 (which auth path), S-5 (cross-pod messaging),
S-6 (resume keeps the phone entry)
**Parallel with:** 04

## Goal

Tom drives v2 sessions from his phone, the monthly Max login renewal runs through
`agent-run`, and the operator keeps a standby session ready the way v1's post-ready
does.

## Scope

- **If S-1 or S-2 passed:** the keeper owns the Max login. It refreshes on a
  schedule and writes `dev-env-claude-live` (access token only); `remote` session
  pods mount it. If S-2 passed, `remote` pods simply use the static token.
- **If both failed:** the coordinator host (DESIGN-001 6.2): one long-lived session
  pod of kind `coordinator-host` that owns `.credentials.json` on its volume and
  runs every Remote Control session as a tmux window.
- `agent-run auth status|login|code`; the keeper's daily check pages Tom at 7 days or
  fewer (replacing v1's auth-watch for this credential).
- `remote` mode in `agent-run --interactive`; the Remote Control name is the session
  id. The coordinator system prompt that v1's agent-run adds for `both` mode carries
  over.
- The standby: on start, and when the last one is reaped, the operator creates one
  `remote` session on haynes-ops; three creations inside 30 minutes trip the circuit
  breaker.
- Messaging tier 2 per S-5.

## Acceptance

- Tom starts and drives a v2 Remote Control session from his phone.
- A coordinator session dispatches a v2 task pod with `agent-run -p` and gets the
  result back.
- The login renewal ceremony completes through `agent-run auth login claude` with the
  URL relayed bare and the code pasted back.
- No pod other than the credential owner holds a refresh token (checked by listing
  the mounted files' keys, never their values).
