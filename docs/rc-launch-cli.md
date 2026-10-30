# Claude Remote Control launch observations

`agent-run rc` implements the client-only 2a slice of [DESIGN-038 / PR
#684](https://github.com/thaynes43/dev-env/pull/684), under OpenProject
[#268](https://work.haynesnetwork.com/work_packages/268), Epic #91.
Work orders: `RCLAUNCH268-2A-CLAUDE-CLI-20261029-01` and fix round
`RCLAUNCH268-2A-FIX-20261029-02`. This source contract does
not establish installation or usable phone acceptance.

```sh
agent-run rc start --project example --agent claude \
  --model claude-opus-5-5 --handoff /private/handoff.txt --wait 5m -o json
agent-run rc status SESSION -o json
```

The project catalog selects the accepted repository and base. `--repo` optionally
selects a repository within that project. A full model ID is required; effort is
optional. The handoff file must be nonempty UTF-8, at most 8192 bytes, with no
C0, DEL or C1 controls except newline and tab. Its text never appears in output.
Keep handoffs and authorized Remote Control links private.

The command uses only the existing session create, named session read and
message routes. Existing authentication, project admission and coordinator
refusals apply. The ordinary create command's project restrictions stay intact.
Codex returns `blocked` / `codex_adapter_not_implemented` locally, with exit 1
and the fixed message "Codex remote launch is not implemented in 2a; no inventory
was probed".
There is no operator, CRD, runtime or flag change. `--enable-rc-launches` remains
reserved. Nothing runs until the command is invoked.

The result is one JSON object, at most 32 KiB, with `link` first. It includes the
always-sent idempotency key (generated unless supplied), session handle, phase
observations, `pollIntervalMs: 2000`, `polls`, `totalMs` and `deadlineMs`.
`polls` counts named-session reads, excluding the create and message attempts.
Elapsed timings use the client's monotonic clock; `firstObservedAt` is UTC when
observed and null for phases never observed.
`sourceAt` is null because the existing view supplies no source timestamp for
these observations. Observations describe this invocation; status does not
recover a previous invocation's timing or handoff receipt.

| Phase | Evidence and bound |
| --- | --- |
| `api_accepted` | Create answered 201, or 200 for key reuse |
| `project_inventory_ok` | Create echoed the accepted repository and base; status alone cannot recover this create evidence |
| `pod_scheduled` | Session conditions: PodScheduled true, PodReady true, or a node-bound PodReady false condition reporting Starting/container startup; 60s |
| `pod_ready` | PodReady condition true; 120s after `api_accepted`, including scheduling time |
| `agent_started` | Agent running with nonempty conversation ID; 90s |
| `provider_auth_ok` | Always unknown: `provider_readiness_unavailable` |
| `rc_ready` | Always unknown: `provider_readiness_unavailable`; `rcRegistered` separately records registration; 90s |
| `link_available` | URL matches the existing portal Remote Control URL pattern; 10s after registration |
| `pairing_required` | Not applicable for Claude |
| `handoff` | At most one message attempt for a 201 create; 202 means state `ok`, reason `transport_submitted` only; a 200 reuse never sends a handoff |
| `cli_recorded` | Always unknown: `input_record_unavailable` |

Each phase carries state (`ok`, `error`, `blocked`, `unknown` or
`not_applicable`), first observation time, milliseconds since request,
null source time, evidence source and reason. Scheduling and PodReady budgets
both start at acceptance; later stage budgets run serially. All are capped by
`--wait` (default 5m, maximum 10m), including network requests. `--wait 0`
submits the create and prints its handle without polling or sending a handoff.
Only this zero-wait start returns `accepted`; an expired positive wait returns
`timed_out` with the handle. Status makes one GET and never sends a message;
an active session without a link is `pending`.

A reused idempotency key (HTTP 200) never sends a handoff, including after a
previous uncertain or unsent attempt and when the handoff text has changed.
It follows the session to its link, then returns outcome `unconfirmed`, error
`handoff_unconfirmed`, exit 6, with handoff state `unknown` and reason
`reused_by_key_not_resent`. A shorter wait, terminal session or observation
failure can stop following before a link is available.

A successful start returns outcome `link_available`, exit 0, after transport
submission. It never claims `ready`. A status read can report a validated link
without proving handoff submission. Provider connectivity and CLI input recording
remain unknown in both cases. Any known valid link remains in partial results,
including a rejected or unconfirmed handoff.

| Error | Meaning |
| --- | --- |
| `invalid_input` | Invalid local input or rejected request syntax |
| `codex_adapter_not_implemented` | Codex remote launch is not implemented in 2a; no inventory was probed |
| `unauthenticated` | Missing/invalid API authentication or HTTP 401 |
| `launch_forbidden` | HTTP 403, including the coordinator refusal |
| `project_unavailable` | HTTP 422 identifying project admission; phase `project_inventory_ok` |
| `request_conflict` | HTTP 409 on create, such as a changed request under a reused key |
| `scheduling_timeout`, `pod_ready_timeout`, `agent_start_timeout` | Corresponding stage budget expired |
| `login_conflict` | Recognized agent conflict requires immediate owner action; no login is attempted |
| `auth_projection_unavailable` | Keeper projection warning persisted for 30s within agent startup; a transient warning can clear; one status read cannot establish persistence |
| `rc_registration_timeout`, `link_unavailable` | Registration or validated link did not appear in its budget |
| `session_ended` | Session is Failed, Suspended, Archived or being reaped; start/status return `failed`, exit 1, even with a retained link |
| `handoff_rejected` | Message refused, including 409 when not addressable |
| `handoff_unconfirmed` | Message attempt had no confirmed transport result, or the create reused a key; never resent |
| `launch_deadline` | Overall wait expired; retain the handle and observe status |
| `status_unavailable` | Session observation or create response unavailable/unconfirmed |

Exit 1 means failure/blocker; 2 means local invalid input; 3 means API
authentication/authorization; 5 means nothing was sent; 6 means an unconfirmed
exchange; 7 means accepted/pending, timed out with a handle, or owner action
required. These commands never use exit 5 for an accepted pending launch.
Before a handoff is sent, a local token failure returns `unauthenticated`, exit 3.
A failed dial records handoff state `unknown`, reason `not_sent`, and exits 7;
rerunning start with that key cannot deliver the handoff.
A key makes session creation idempotent; it does not make handoff delivery
idempotent. Do not rerun start to retry an unconfirmed handoff. Use status to
observe the retained session, and preserve uncertainty about the handoff.
