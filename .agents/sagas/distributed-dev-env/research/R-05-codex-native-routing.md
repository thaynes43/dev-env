# R-05: Codex complete-session routing gate

**Date:** 2026-10-11 UTC. **Scope:** finite read-only audit of pinned Codex
0.160.1 and existing evidence. No pairing, enrollment copying, credential reads,
daemon change or runtime fixture retry.

## Finding

One paired computer entry routing complete native chats across independent
app-server pods is **not established**. The inspected path owns loaded threads,
conversation execution and pending requests inside one app-server process.
This is a failed capability gate, not proof that a custom router or future
provider implementation is impossible.

| Required capability | Established behavior at the pinned version | Fleet routing result |
|---|---|---|
| Listing and resume | Server-local stored history and loaded ThreadManager | One-entry cross-pod route unestablished |
| Events | Connections handled by the same MessageProcessor | Cross-pod event ownership unestablished |
| Questions and answers | In-process pending request IDs and callbacks | Distributed answer correlation unestablished |
| History | Stored thread reads and local resume | Authorized platform export remains separate |
| Ownership and recovery | No inspected distributed assignment/fencing contract | Independent native pod ownership unestablished |
| Remote executors | Execution environment selects an executor URL | Isolates commands, not the native harness |
| Retained computer name | Enrollment reuse for a logical host | Sequential replacement is not concurrent proof |

## Evidence

Pinned annotated tag `rust-v0.160.1` resolves to
`d27764b82f7118f674371e6d6e76271d9d606edb`. Seven retained source files matched
their GitHub blob hashes. Key references:

- [ThreadManager's in-memory registry](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/core/src/thread_manager.rs#L402-L420).
- [App-server MessageProcessor construction](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server/src/lib.rs#L972-L1003).
- [Loaded-thread listing](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server/src/request_processors/thread_processor.rs#L2777-L2790)
  and [resume](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server/src/request_processors/thread_processor.rs#L3641-L3724).
- [Pending request callbacks](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server/src/outgoing_message.rs#L129-L157)
  and [answer delivery](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server/src/outgoing_message.rs#L467-L496).
- [Executor environment parameters](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server-protocol/src/protocol/v2/environment.rs#L10-L49).
- [Enrollment identity](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server-transport/src/transport/remote_control/protocol.rs#L19-L40)
  and [reuse](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server-transport/src/transport/remote_control/websocket.rs#L1718-L1753).

The [official app-server documentation](https://learn.chatgpt.com/docs/app-server#lifecycle-overview)
describes thread lifecycle, events and client replies over an app-server
connection. Current [remote-connection documentation](https://learn.chatgpt.com/docs/remote-connections#hand-off-a-chat-between-hosts)
describes explicit handoff between hosts, with interruption before transfer.
That is distinct from concurrent federation behind one paired entry; it does
not establish acceptance on pinned 0.160.1.

The owner requirement is recorded in
[#91's clarification](https://github.com/thaynes43/dev-env/issues/91#issuecomment-6099902031).
The inference from these sources is that complete native-session isolation needs
separate app-server failure domains. Two hosts protect A from B, while chats
loaded inside A still share A's process. One separately paired host per independent
native-session boundary is the supported candidate for the MVP, subject to real
pairing, lifecycle and owner acceptance.

## Decision boundary

[DESIGN-006 Q-24](../designs/006-private-catalog-bootstrap.md#q-24-codex-mvp-native-routing)
asks separate paired session hosts versus a coordinator with isolated managed
workers. The second option isolates task execution but retains a shared native
coordinator boundary. No fleet UX is committed while the ruling is pending.
No option permits credential/enrollment copying or paid-API substitution.
Local-model sessions follow the same independent execution, rules, context and
budget contracts; live inference waits for household-safe GPU capacity.
