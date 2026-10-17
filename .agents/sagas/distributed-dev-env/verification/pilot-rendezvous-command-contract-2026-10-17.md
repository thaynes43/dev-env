# Pilot rendezvous command contract — 2026-10-17

Source check for the command shapes proved on task image 2.29.5 after cohorts
r10 and r11. [DESIGN-027](../designs/027-pilot-rendezvous.md) records the
contract. This file does not admit a cohort, bind a seal, or accept rendezvous.

r10: A ran `inbox-read` with the InboxMessage name as a positional and exited 2
with empty stderr. The working command is
`/usr/local/bin/agentd inbox-read --project dev-env --message EXACT_NAME`.
`inbox-ack` needs the same flags. No ACK and no PublishedContext were produced.

r11: B ran `task-claim-create stdin` and stopped. The working claim shape is
`printf '%s' 'JSON' | /usr/local/bin/agentd task-claim-create` with no extra
argv. The word `stdin` must not appear.

`PilotPolicy.ValidateRendezvousCommands` requires those shapes, plus
`task-claim-list` and `inbox-list` with `--project dev-env`, in both prompts.
It refuses every `inbox-read` and `inbox-ack` that is not that exact flag form,
including a second bad line beside a good one, plus extra claim argv and the
word `stdin`.
Operator `Validate` does not call it. The harness `validate` and `run` do, and
`run` returns before observation, reservation, or a B create.

Context publication was not reached, so no `context-publish` or `context-read`
line is certified here. A future live attempt still needs a fresh seal, a fresh
window, and a bound live ConfigMap UID. Run the harness nonexec, and require
this check to pass, before creating either actor.
