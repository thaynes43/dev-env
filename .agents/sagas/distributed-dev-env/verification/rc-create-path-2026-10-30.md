# Codex existing-host create path, 2026-10-30

Source investigation for OpenProject [267](https://work.haynesnetwork.com/work_packages/267)
and [271](https://work.haynesnetwork.com/work_packages/271), DESIGN-038.
Coordinator: dev-env-1030-005602, requested gpt-6.1-sol medium.
Base: `866f561f40c27c7e811ef056a70ba7e622e39a77`.
The discovery author dev-env-1030-005251 retains its files and branch.

## Finding

Offline `codex --version` reports `codex-cli 0.160.1`.
`codex app-server generate-json-schema --experimental` generated the fixtures in
`internal/agentd/testdata/codex_rc_create/`. No socket or provider was contacted.
`ThreadStartParams` accepts `cwd` and `model`. `projectId` exists but is optional;
there are no required parameters. The Mac connector's reported ChatGPT-only
inventory is therefore a catalog limitation, not evidence of failed native
creation or pairing. No actual create POST/RPC failure was observed.

The [official protocol](https://developers.openai.com/codex/app-server/)
describes initialize, initialized, thread/start for a fresh conversation, and
separate turn/start for input. The pinned schema is authoritative for this
increment; newer documentation does not prove availability on a live host.

## Minimal source increment

`internal/agentd/codex_rc_create.go` adds a package-private existing-daemon
thread/start implementation. It connects to the existing local Unix control
socket with the existing peer PID/UID check, verifies pinned native version and
connected installation/environment identity, and sends the exact selected
cwd and model with `environments: []`. An explicit empty environment list in the
response is required. RC registration identity is distinct from execution-environment
selection; omission could select an unrelated default execution environment. The selection must come from accepted host/catalog and pairing
evidence; connected transport alone never proves phone pairing. A mandatory
one-use authorization callback runs immediately before mutation. Errors are
unconfirmed, with no retry, resume, turn, pairing or process launch.

The shared RPC reader now accepts an explicit null error in a successful
envelope, rejects null results, and refuses interactive server requests rather
than silently discarding them. Fixtures exercise an inert Unix WebSocket server;
they do not start Codex or touch a real daemon, credential or history.

## Exact remaining platform block

There is no current operator route for creating a thread on an existing paired
remote daemon. `internal/apiserver/native_control.go` permits authenticated-human
operations against an immutable task-budget-owned executor. That executor pins
Luna and disables remote enrollment; it cannot stand in for the paired host.
`internal/apiserver/validate.go` preserves the human/client/workbench restriction
on Codex remote-host creation, and this session has no exec authority into another
session pod. These are source-established boundaries, not newly attempted denials.

This increment exposes no command, endpoint, listener or new caller authority.
The new primitive has no production caller. A usable launch still needs an
existing authorized human/workbench transport to the selected daemon, bound
catalog/root and pairing evidence, durable one-use intent consumption, and
thread/link/handoff receipts. None is supplied by a callback alone. Do not wire
ordinary session credentials or the separate Luna executor around that boundary.

No new live RC/demo, installation, pairing, host restart, permission change,
Mac UI/private database access, template adoption, Ask 4418 or Terra retarget
action was performed. Existing host identities, active sessions and histories
were not changed. OP272 remains held; its cwd-only proposal is not RC acceptance.

## Delivery state

Independent native QA at `47dce51` found one P2: implicit execution-environment
selection. The fix sends an explicit empty selection and rejects missing, null
and nonempty response selections. No other actionable findings; QA performed no
runtime calls. Targeted tests and final source evidence are recorded in PR/OP271.

Source: prepared and independently reviewed, pending final checks.
Released: no. Installed: no. Usable/owner accepted: no.
The complete RC repair remains blocked on the authorized transport above.
