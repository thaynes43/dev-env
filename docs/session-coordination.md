# Session coordination and retained context

Scope corrected 2026-10-10, America/New_York. This is a design and source audit,
not a claim that the following fleet APIs are deployed. The controlling decision
is [ADR-003](../.agents/sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md).

## The experience we need

An agent can discover who is working on a task, read the relevant previous work,
send a message and request assistance or a handoff. The owner can see the same
live and stopped sessions from a management client. Git work stays in each
pod's local worktrees.

```mermaid
sequenceDiagram
    participant A as Agent A / pod A
    participant API as Operator / coordination API
    participant Store as Retained context
    participant B as Agent B / pod B
    A->>API: Claim task; publish activity and context references
    B->>API: Discover work on this project/task
    API-->>B: Existing owner, state, model and source revision
    B->>API: Read authorized history; send attributed message
    API->>Store: Read retained transcript/memory/handoff
    Store-->>B: Bounded context with provenance
    API-->>A: Deliver message or retain it offline
    A->>API: Report result or request controlled handoff
```

The operator/API observes and manages these records without spending model
turns. Agents reason about work. A future frontend is another API client.

## What is actually available

Audit source: dev-env `a5dc5e1`; v1 installed launcher/config matched audited
haynes-ops source. The audit inspected capability metadata and code, not private
transcript or memory bodies. Its findings are narrower than runtime acceptance.

| Capability | V1 | Current v2 | Missing for the target |
|---|---|---|---|
| Managed session discovery | tmux/worktree launcher list | AgentSession list/show, including extant stopped records | Durable records after reap and native host/thread indexing |
| Native peer discovery | Claude same-pod native discovery; Codex native task tree | No complete fleet provider registry | Cross-pod discovery with actual execution status |
| Messages | Same-pod provider/session mechanisms | Attributed relay to a live TUI | Offline inbox, headless-compatible delivery/read and acknowledgements |
| Prior conversation reads | Retained provider files under the same OS identity | Task log tail requires a Ready source pod | Full authorized transcript access when the source is stopped |
| Rules and memory | Common rules and retained local memory/history | Rule rendering and shared Claude memory support | Project/task composition acceptance and common context discovery |
| Task duplicate prevention | Conventions, work orders and peer messages | Same-parent unfinished-request idempotency | Atomic claims for an explicit task across independent parents |
| Archive | Provider history persists in v1's global home | Private home deleted after Git rescue | Provider history retention independent of shared Git |

Source references: [session listing](../internal/apiserver/sessions.go),
[live log/message API](../internal/apiserver/podcmd.go),
[message delivery](../internal/agentd/deliver.go),
[rules/memory rendering](../internal/agentd/render.go),
[private reap](../internal/controller/reconciler.go) and
[shared-only retained-home gate](../internal/controller/private_home_retention.go).

OpenAI documents stored Codex thread reads without resuming, separate archived
listing and runtime status. Loaded threads are not necessarily active, and
listing defaults exclude several execution sources. The fleet adapter must
handle those distinctions and be accepted on the pinned installed CLI; supported
provider APIs do not establish fleet integration. [Official OpenAI App Server documentation](https://learn.chatgpt.com/docs/app-server).

## Records and access

Keep a durable session identity and provider conversation reference, logical
host, pod/executor identity, lifecycle, model, task, source commit, rules revision,
last observed activity and retained artifact references. Report observation time
and uncertainty; a stored conversation is not proof that a process is alive.

Task claims identify the objective/issue and responsible session. Independent
worktrees and branches remain normal. Do not assume unique pod or branch names
prevent duplicated objectives. A peer may discover and assist without obtaining
control over the original session.

Separate rights for fleet metadata, project/task context, message delivery and
session mutation. A coordinator's current direct-child mutation scope should
not become fleet-wide control merely to enable peer reads. Keep credential,
refresh, enrollment, socket and private configuration material out of exports.
History can contain sensitive work; publish it only to authorized private readers,
never into this public repository or public logs.

## Retention and handoff

During a live turn, export bounded complete records with a cursor/version;
partial output must be identified. After stop, retain transcript, published
memory, handoff, result and source/rescue references. Verify retained availability
before provider-home deletion. A Git bundle preserves work, not the whole history.

For now the tested private suspend/resume path keeps the home. Suspended private homes also auto-archive at configured `archiveAfter`
(default 168 hours); retain required context before that deadline. The old reap path
can remove it; do not present that as transcript retention acceptance. New archive
requires an explicit retention policy, verified artifacts and a discoverable record.
Exact provider-state restoration and readable exported history are distinct checks.

A handoff proves the old executor stopped, records the next owner and restores
work into that owner's local checkout. A missing Pod or heartbeat is insufficient.
The recipient reads the predecessor's context and may use a new conversation.
No shared Git lock or writable shared provider home is required.

## Cost records

Available source parses provider-reported tokens/cost and attaches the result
to a model-labelled session. It does not provide trustworthy all-model billing.
In particular, token-only Codex results can persist a default cost of zero.
[Parser](../internal/agentd/codex_stream.go),
[heartbeat status conversion](../internal/apiserver/heartbeat.go),
[CLI display](../internal/agentrun/sessions.go).

Correct the data model and display to distinguish observed zero from unavailable
cost. Keep API-billed amounts, provider-reported equivalent estimates,
subscription usage/quota and local resource estimates separately labelled.
Attribute events to exact model/session/task, identify their source and avoid
counting parent/child totals twice. Show unavailable data as Unknown. Finite
wall-time and attempt limits still apply when provider counters are absent.

## Acceptance scenario

1. A Claude agent and a Codex agent run on distinct bounded worker pods, with
   independent repo caches and worktrees and the same loaded rule revision.
2. A claims a known task. B discovers it, reads permitted context and sends a
   message instead of starting duplicate implementation.
3. A stops. B can still read A's transcript, memory and result without a model
   call or source-pod readiness.
4. A's home is retained or safely archived under the verified history contract.
   A controlled handoff gives B its own restored worktree and task history.
5. An operator upgrade preserves running sessions; usage/cost displays their
   provenance and Unknown values; unauthorized reads/mutations are refused.

This scenario is the coordination pilot. Frontend implementation, automatic
agent drains and local LLM execution follow their own acceptance, and v1
retirement remains an explicit owner decision.
