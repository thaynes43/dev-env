# Central work coordination across repositories

Recorded 2026-10-16 UTC from Tom's direction relayed through the tracker deployment
coordinator. This records owner requirements for dev-env v2; it does not activate
implementation or change the Chapter 7 coordinator's work.

## Canonical work and agent workflow

OpenProject is the central coordination and historical record for cross-repository
Saga → Epic → Story work. A work item can cover several repositories. Its identity
and lifecycle must not depend on one repository's GitHub issue queue.

Before creating work, every agent searches the central tracker for an existing item.
It claims and updates that canonical item, records its session and execution owner,
and links the relevant repositories, issues, branches and pull requests. Repository
issues and PRs remain useful execution artifacts linked to the central item. Creating
a repository issue when claiming work does not satisfy central intake or discovery.

Claude Code, Codex and future supported harnesses need the same common rules and
supported tool interface for search, create, read, update, comments, attachments,
hierarchy and dependencies. Preserve history when retiring work; do not introduce
hard deletion without the applicable owner approval. A migration must retain source
paths, commits, section IDs, original Markdown, dependencies and decision provenance.
Search/upsert using stable source keys so a rerun does not duplicate imported work.

## Owner refinement and execution decisions

Tom reviews and refines work centrally and records the green light there. Keep the
states Draft, Ungroomed, Needs answer, Needs approval, Approved, In progress,
In review, Accepted and Paused. Preserve paused work when importing it. Imported
items, an agent claim, or a status label alone do not authorize implementation.
Record who communicated a decision, the actual authoring actor, its source and scope;
an agent recording a voice decision must not impersonate Tom.

These states support coordination and history. Strict technical enforcement that
prevents a trusted agent from setting an approval status is not a requirement.
Usable agent create/read/update operations are required; do not delay them for a
status firewall, middleware or a vendor fix. Authentication, existing access controls,
credential handling and protection against irreversible data loss still apply.

## Delivery and acceptance

Demonstrate the core workflow first: search existing work, claim/update one central
item, preserve Saga/Epic/Story relationships, link execution artifacts, and retrieve
its history from another supported harness. Verify safe retry/optimistic conflict
handling with a small fixture. Defer speculative edge cases and polish until evidence
shows they matter; serious security concerns and required CI/reviews remain gates.

The first migration covers HaynesNetwork plans 078–089, with their original provenance
and paused states. Tracker deployment, migration completion and approval to execute
imported work are separate outcomes. Existing session claims remain runtime evidence;
this requirement does not enable a new integration or replace them automatically.

Implementation belongs with [plan 11](../backlog/11-project-workspaces.md). This
requirement adds central logical-work discovery and ownership to its existing session
coordination scope. It does not reopen shared writable Git or alter retained-history
and Chapter 7 access/storage decisions.
