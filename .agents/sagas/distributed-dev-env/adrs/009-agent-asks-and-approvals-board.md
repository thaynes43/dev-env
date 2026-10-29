# ADR-009: Agent asks and approvals belong to the operator

- **Status:** Proposed. Tom's design review is pending.
- **Date:** 2026-10-29
- **Decider:** Tom Haynes
- **Tracking:** [Story #255](https://work.haynesnetwork.com/work_packages/255),
  [Epic #254](https://work.haynesnetwork.com/work_packages/254), Saga #85
- **Work order:** ASKS255-DESIGN-20261029-01. Documentation only.
- **Design:** [DESIGN-034](../designs/034-agent-asks-and-approvals-board.md)
- **Builds on:** [ADR-003](003-session-coordination-private-repositories.md),
  [DESIGN-014](../designs/014-durable-offline-inbox.md),
  [DESIGN-024](../designs/024-coordination-record-retention.md) and
  [DESIGN-029](../designs/029-portal-foundations.md).

## Context

Tom wants to read agent conversations and answer questions on the portal,
like a message board. A question should survive a closed browser, an idle
agent and an operator restart. Agents should keep doing independent work.

Claude's AskUserQuestion stops the calling session. Codex phone question
delivery is unverified. Neither gives all harnesses one shared record of
what was asked, who answered, and what an approval permits.

The portal has a registered-conversation reader and a message sender. Both
have exact Session name/UID target lists. Their human access follows the
portal group, under DESIGN-029's October 29 ruling. That accepted workflow
does not yet supply a durable question or human approval record.

The inbox is durable, but today only original Sessions can send into it.
Grant attestations are agent claims. Managed-child decisions carry a
coordinator's authority for a particular Codex task. None is a verified
portal-principal approval. Reusing those labels would hide a trust change.

Epic #254 journal 2539 records the lead's v1 wire contract and lane custody.
DESIGN-034 preserves that surface and identifies the follow-ups needed for
durable operator-origin delivery and revocable approvals. The lead received
the gaps in Epic journal 2555. This proposal changes no lane's authority.

## Decision

Make an **Ask** a first-class operator record. An Ask requests information
or approval. It contains its question, choices, recommendation, context,
original requester UID, deadline and, for approval, exact action scope.

Agents create Asks through a common API and `agent-run mcp`. Creation
returns a record and portal link immediately. The portal presents an Asks
board and includes open Asks in Needs attention. A human answers there.
The operator publishes the answer to the original requester's durable
inbox. Reading and acknowledging it are explicit agent actions. A bounded
wait is optional; routine asking never requires one.

Store Asks in the protected coordination API group. Keep the operator as
the source of truth, with compare-and-swap transitions and durable delivery
intent. Reuse portal PostgreSQL for browser sessions and private form
receipts, not as a second approval ledger. Extend DESIGN-024 explicitly
before any Ask or operator-origin inbox record can be archived and deleted.

An approval binds the authenticated OIDC issuer and subject, the admitted
portal group policy, answer time, original requester, exact scope digest
and expiry. It can be revoked. The BFF derives that identity from its
current request-private session and fresh group check. The operator trusts
that assertion only from its admitted portal BFF. The portal answerer is
an operation role on the current portal-reader caller in v1. An agent
or coordinator cannot answer, approve or relay an approval on that route.

An information answer is never approval. An approval record is not a
credential or proof that an action ran. Existing permissions, explicit
holds and each action's enforcement still apply. Grant, budget and native
decision adapters require separate reviewed stories before they consume
these records.

For portal chat history, propose a resource-based eligibility rule for all
compatible registered Sessions in the admitted portal resource. Replace
per-UID history lists only after Tom reviews that scope change. Preserve
current registration, Session/Pod fences, safe projection and honest
Unavailable states. Send retains its own scope.

## Alternatives considered

| Alternative | Why it loses as the primary record |
|---|---|
| Keep AskUserQuestion | It is blocking and tied to one harness and session. It lacks a shared offline board and a common approval identity. Keep it as a supported fallback while the board is unavailable. |
| OpenProject comments | They are useful for scope and handoff, but comments do not bind a live requester UID, typed action, expiry, revocation or inbox acknowledgement. A tracker author is not automatically an admitted portal approver. Link the canonical work item instead. |
| Slack replies | They would add an identity mapping, connector and another answer surface. A reply or relay cannot establish current portal-group authority without the same approval service. Slack may later link to an Ask. |
| Pushover replies | A notification acknowledgement does not answer a typed question or approve its exact scope. The current keeper sends pages, not authenticated portal answers. Use a push that links to the portal form. |
| Portal PostgreSQL as the Ask store | SQL transactions would simplify the answer and outbox, but would give the operator a new database dependency and duplicate coordination custody. The accepted browser-session recovery risk does not automatically apply to approvals. Coordination records already have private access and archive contracts. |

## Consequences

All supported harnesses get the same question and answer path. Tom can
answer later from a phone. Each side can distinguish an answer saved,
an inbox message stored, an agent acknowledgement and an action result.

The design adds a protected Ask kind, a portal answerer boundary and an
operator-origin inbox variant. These need schema, RBAC, API, retention and
client qualification. Existing session-only inbox publication remains
unchanged. Cross-store delivery needs reconciliation, not an exactly-once
claim. Approval revocation cannot undo an action already started.

Group-wide history makes future compatible Sessions readable without
editing each UID. It increases exposure within that resource. Transcript
redaction is imperfect; it does not make credential storage safe. Wider
projects, archived transcripts and new provider adapters remain separately
scoped work.

## Review and delivery

This PR proposes the decision and implementation contract. It installs
nothing, changes no policy and approves no action. The lead performs
independent review and delivers the pending questions to Tom one at a time.
The pending design decisions remain for Tom; this document does not alter
the lead's separately recorded source assignments. Source tests,
signed publication, GitOps installation and Tom's usable acceptance are
separate evidence, as specified in DESIGN-034.
