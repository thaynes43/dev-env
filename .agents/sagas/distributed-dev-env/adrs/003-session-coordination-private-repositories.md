# ADR-003: Coordinate sessions; keep repositories and worktrees per pod

- **Status:** Accepted scope correction; implementation and acceptance remain open
- **Date:** 2026-10-10, America/New_York
- **Decider:** Tom Haynes, through his explicit scope clarification in this conversation
- **Supersedes:** ADR-002's mandatory shared reference clones, project/task files,
  RWX workspace and storage-trial dependency. Also supersedes derived requirements
  that make those files necessary for multiple Codex computer links.
- **Retains:** ADR-001's operator/API, independent bounded agent pods, private
  persistent state, rescue, credential ownership, guarded access and GitOps.
- **Owner requirement:** [2026-10-10 scope](../requirements/2026-10-10-owner-scope.md)
- **Delivery:** [plan 11](../backlog/11-project-workspaces.md)

## Why this correction is needed

The owner clarified that agents need common rules, communication, discovery of
live and stopped sessions, and access to each other's authorized transcripts,
memory and prior work. They do not need a shared Git filesystem. Each pod can
bootstrap repositories, refresh them and create ordinary task worktrees.

Agent-authored prescriptions turned shared Git storage into an MVP dependency.
The NFS helper and observation failures were test-code failures; they did not
establish that the NAS was unsuitable. Shared Git testing no longer advances a
required outcome. Accepted ADR-001 and ADR-002 remain unchanged as historical
records; this later ruling controls conflicting implementation plans.

The five outcomes are: upgrades preserve sessions; work spreads across the
cluster; powerful access has guardrails; sessions are visible and manageable;
model usage and cost are attributable, with local LLMs when GPUs return.

## Decision

### Independent execution and Git

Keep a persistent home and repository cache per session or logical host. Clone
missing declared repositories and fetch their remotes during preparation.
Refresh clean reference views safely; report and preserve dirty or ambiguous
state. Agents implement only in task worktrees. Immediately before a new task,
fetch the selected repository, resolve an immutable base commit and record it.
Startup refresh is useful but does not replace that task-start check.

The same path can exist in both pods without naming the same filesystem. A
project identifies common rules and repository identities. It does not imply
shared checkout bytes, common Git administration or filesystem locks across pods.
A restored task has explicit provenance; resume preserves WIP and never resets
an existing checkout to make it appear current.

### Shared coordination and context

Use the existing operator/API as the platform service. The coordinator is the
agent talking with the owner, not another permanently reasoning service.
`agent-run`, an eventual frontend and automations are clients of the same API.

The shared layer provides:

- Versioned global/project rules composed with repository instructions for both
  providers, including task worktrees and delegated work.
- A session directory covering live, idle, stopped and archived sessions, logical
  hosts/native threads, provider/model, current task, node, source and rules revision.
- Durable task claims and ownership. A peer sees work already underway and can
  assist or request a handoff. Explicit task identity is the coordination key;
  the platform cannot infer every semantic duplicate from arbitrary prose.
- Attributed messages with durable delivery state, including an offline inbox.
- Authorized transcript, memory, handoff and result access for prior sessions,
  without starting a model turn or reviving the stopped executor.
- Usage and budget records associated with task, session and exact model.

Provider live homes remain private. Sharing context means controlled read access
or export, not shared writable credential files, sockets, enrollment databases or
conversation registries. Git-only rescue is insufficient for this history goal.
Preserve provider state independently of the old shared-workspace feature gate.
Archive must confirm retained history before deleting a provider home.

A stopped session may be read without continuing its exact native conversation.
Cross-host continuation uses an explicit handoff and Git commit/rescue into the
recipient's own worktree. Importing the exact provider conversation is a separate
capability that requires supported provider behavior and verified ownership.

### Access and security

Each v2 session starts with declared routine scope. Elevated cluster, hardware
or external-system actions require a recorded scope, bounded lifetime, an
attributable identity and policy/owner authority. Test refusal as well as use,
expiry and revocation. A native question transports a decision; it does not by
itself implement a trusted privileged approval mechanism.

Prevent the standing Headlamp admin bypass and unrestricted hardware credentials
in the v2 baseline. Supply a usable guarded replacement for required work before
retiring legacy callers. Existing Omni is Reader; new mutation permissions need
explicit scopes and guardrails. GitOps self-merge is also an authority path and
must appear in the access design. This scope ruling grants no new runtime power
and does not remove v1's access or restart its sessions.

### Updates, distribution and cost

Operator/config deployment is independent of running agent homes and pods.
Image updates affect new sessions first; replacement of an existing agent waits
for a safe idle boundary and retained-state proof. Multiple pods need demonstrated
placement and resource isolation; worker affinity alone does not prove spreading.
A Codex daemon's native threads stay on that host unless a tested dispatch path
creates independent worker pods. List their actual execution placement honestly.

Record exact model, provider and available usage with provenance. Distinguish API
billing, CLI-reported equivalent cost, subscription consumption and estimated
elapsed effort. Missing cost or quota is Unknown, never inferred as zero. Local
LLM execution is a later backend using the same session, rules, access and budget
contracts; GPU allocation must preserve household reservations.

## Storage consequence

Per-pod homes and the existing memory/rescue shelf remain useful. Transcript and
message retention still require reliable storage and access control. That does
not require shared Git, a new NFS/CSI deployment or a Git filesystem benchmark.
Choose and validate context retention against its actual workload when needed.

Issue #130 and NFS proposal haynes-ops#3737 become historical, withdrawn shared-Git
work. Preserve their receipts and failure history. Do not retry, repurpose their
pilot or infer that closing them fixes the observed filesystem costs. The native
lifecycle and budget work remains relevant to safe execution and recovery.

## Delivery and acceptance

1. Make this ruling visible in the front doors, guide, requirements and plans.
2. Decouple catalog/rule preparation and provider-home retention from RWX Git.
3. Deliver fleet-wide session/history discovery, durable messaging and task claims.
4. Demonstrate Claude and Codex on independently bounded pods, actual loaded rules,
   fresh worktree starts, operator upgrades and a stopped-session history read.
5. Complete guarded required access and truthful model usage/cost reporting.
6. Run the owner workflow; schedule UI, safe agent drains, local LLMs and migration
   according to their remaining dependencies.

A usable pilot is distinct from retiring v1. The historical twenty-row capability
inventory helps migration planning; it is not a universal prerequisite for every
pilot task. V1 retirement still requires preserved required workflows and explicit
owner approval. The accepted 60-minute stall/three-failure policy remains in force.
No storage retry or new infrastructure mutation follows from this document.
