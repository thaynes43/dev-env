# Native clean-session source handoff — 2026-10-30

Tom's current direct successor work order and OP252 journal3435 distinguish
routine worker/parent/coordinator questions from actual user questions. Routine
coordination uses the existing safe `agent-run` inter-agent/upward route, durable
inbox where supported; actual user decisions and observations retain native
phone escalation. Never impersonate a session, create a new notification channel,
or paste into a TUI while a native question modal is pending. This dated ruling
is reflected in AGENTS.md and CLAUDE.md; it grants no additional authority.

OP272 journals3416/3437/3438/3443 bound this source increment: the
[native client wrapper](../../../../scripts/codex-clean-session.py) and its
[usage/limits](../../../../docs/codex-clean-session.md) are separate from merged
operator-backed PR730. PR731 carries the code, isolated tests and CI wiring;
OpenProject remains the canonical queue. PR727 is Opus's release, untouched here.

Acceptance evidence is the exact PR head, fake-tool fixtures, local layout/diff
checks and independent review recorded on PR731/OP272. Existing OP267/3393,3395
live evidence predates this wrapper. No live thread, socket, pairing, trust
approval, credential, deployment, retained HOME/history or release action is
part of this source delivery. Installation and live acceptance remain unverified;
relay/Opus owns normal remaining source gates. OP252 S8 and OP705 holds remain.
