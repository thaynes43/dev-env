# Provider-neutral multi-client session viewer/controller

**Status: UNGROOMED requirement / spike. Documentation only.** Owner request on
October 14, 2026, relayed by Meatball to the Mac Opus session (the original turn
was not directly observed here; preserve that provenance):

> "OK let's bake that into dev-env-v2 and haynes-os, we can create a multi-client
> viewer for me instead of using all these different vendor apps."

It authorizes no implementation, service, deployment, network/RBAC/credential
change, benchmark, agent kickoff or paid run, and does not lift any Chapter/saga
pause. Grooming and an explicit owner decision come first. The companion
Haynes OS note is `thaynes43/haynesnetwork` `.agents/plans/090-multi-client-agent-viewer-spike.md`.

## Why

Today each provider has its own surface. Claude Code uses `--remote-control` with
the claude.ai app, Codex uses `codex remote-control`, and Cursor uses its web/iOS app
for My Machines runs. Observed on October 14, 2026: an interactive Cursor CLI
terminal session in v1 is not visible on Cursor web/mobile through any inspected
help/doc, and needs extra egress for its chat backend. Seeing work currently depends
on vendor apps or an unlocked Mac browser. Tom and Meatball need one view of the
same sessions.

## Required behavior (to groom, not a design)

1. **Inventory:** one list of live and recent agent sessions across providers
   (Claude, Codex, Cursor, future local models), with host/pod, worktree,
   provider, model and status.
2. **Live output and history:** stream current output, reconnect after
   disconnects, and read back finished sessions without the vendor app.
3. **Input ownership:** an explicit single-writer lock (Tom, Meatball or the
   agent's own loop), with visible handoff, lifecycle actions (start, stop,
   interrupt) and an audit trail of who sent what.
4. **Shared capabilities and guardrails:** the same CLI/MCP inventory, rules and
   approval policy regardless of client; usage and cost visibility per session.
5. **Access:** authenticated through existing identities and ingress (no
   vendor-app or unlocked-Mac dependency); no anonymous/public access; no
   secret display.
6. **Preserve native options:** vendor apps keep working; the viewer adds and
   does not replace them.

## First acceptance checkpoint (small, usable)

Read-only: for sessions that already run in tmux in one dev-env pod, show the
inventory and live pane output for at least two providers, with reconnect and
scroll-back. Tom must be able to view it from a phone and Meatball programmatically,
through existing authenticated access. No input path yet. Input ownership is a
separate, later checkpoint.

## Open questions

- Q-a: Build on agentd/session directory (DESIGN-017, DESIGN-020) or the backlog
  03 Remote Control work, rather than a new service?
- Q-b: What is the minimum authenticated access path for the phone and for Meatball?
- Q-c: Can provider-native transcripts (Cursor API, claude.ai, Codex) be linked
  rather than copied?
