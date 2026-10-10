# Owner scope: solve the five dev-env problems

Recorded 2026-10-10, America/New_York. This is the owner's explicit correction
of agent-derived project requirements. It governs conflicting earlier text;
[ADR-003](../adrs/003-session-coordination-private-repositories.md) records the
architecture consequence. No additional confirmation is needed for this ruling.

## Required outcomes

1. Upgrade dev-env without killing every running session.
2. Distribute agents across the cluster instead of straining one node.
3. Replace weak standing access with useful guardrails. Headlamp's full-access
   bypass and powerful hardware SSH need a controlled replacement. Omni may need
   more permissions for some work, with guardrails; its present role is Reader.
4. Make live and stopped sessions visible and manageable without first opening
   VSCode, a pod shell or a special management session. An eventual frontend
   should expose the same management API.
5. Track model usage and cost so the owner can see what is consuming resources.
   Support local LLM execution when the GPUs are available.

## What agents share

Common global/project rules; session and task discovery; messages; ownership
and handoffs; authorized memory, transcripts and prior results. A distributed
agent must retain the practical coordination and context access available in
v1. Define and test parity from actual capabilities, not presumed file sharing.

Rules must reach both Claude Code and Codex in the task worktree. Sessions on
separate pods can discover work already underway, communicate and read stopped
session history. A durable history read must not spend a new model turn.

## What stays local to each pod

Repository checkouts, Git administration, task worktrees, provider runtime,
credentials and enrollment. Bootstrap missing repositories and fetch reference
code at startup. Fetch again before starting a new remote-based task, pin the
commit and report refresh failures. Preserve dirty references and resumed WIP.
Ordinary worktrees remain the implementation workflow.

Multiple Codex computer links can span pods. They coordinate through the shared
rules/session/context layer; they do not need identical live Git files. Choosing
another computer does not automatically transfer a conversation or task.

## Scope controls

Preserve useful existing source and delivered lifecycle/access capabilities.
Remove shared writable Git, RWX selection and NFS/Git benchmarks from the MVP
path. Keep the earlier trials as historical evidence. Their failures are not a
NAS diagnosis or a reason to make the owner debug our test harness.

Keep bounded execution, one credential refresh owner, explicit task ownership,
rescue and private transcript retention. Missing cost/usage is Unknown, not zero.
Do not equate a provider's estimated equivalent cost with subscription billing.

The first usable distributed workflow and full v1 retirement are different
milestones. Frontend and local LLM work remain visible in the plan without
turning unrelated historical requirements into blockers for every task.
