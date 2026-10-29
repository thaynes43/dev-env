# ADR-010: Agent asks and approvals belong to the operator

- **Status:** Proposed. Tom's design review is pending.
- **Date:** 2026-10-29
- **Decider:** Tom Haynes
- **Tracking:** [Story #255](https://work.haynesnetwork.com/work_packages/255),
  [Epic #254](https://work.haynesnetwork.com/work_packages/254), Saga #85
- **Work order:** ASKS255-DESIGN-FIX-20261029-02. Documentation only.
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
have exact Session name/UID target lists. Human access follows the portal
group under DESIGN-029's October 29 ruling. Messages use an HMAC `actorRef`
instead of forwarding the human's OIDC issuer and subject to the operator.
That workflow does not yet supply a durable human approval record.

The durable inbox accepts only original Session senders through CEL, and
its recipients need a saved project snapshot. Many live Sessions have no
such snapshot. Human answers cannot use that schema unchanged. Grant
attestations and managed-child decisions do not provide portal-principal
approval either; reusing those labels would hide a trust change.

Epic #254 journal 2539 defines the v1 contract and lane custody. Journal
2556 amends answer notes and the explicit approval boolean. Journal 2579
(v1.1) controls wherever it differs. DESIGN-034 maps each part to those
journals. ADR-010 avoids the ADR-009 introduced by open PRs #563/#565;
PR #658's earlier Ask ADR-009 is superseded by this proposal.

## Decision

Make an **Ask** a first-class operator record for information or approval.
It contains the question, choices, recommendation, context, original
requester UID, deadline and, for approval, exact action and target scope.
Store it in the protected coordination API group with immutable spec and
a set-once answer written through compare-and-swap. Use portal PostgreSQL
for browser sessions and private form receipts, not a second Ask ledger.

Agents create Asks through the API and `agent-run mcp`. The call returns
the Ask JSON immediately, with a portal link only when the client has a
configured portal origin. It returns no revision. The portal provides the
board and answer form. **In v1, agents read answers from the Ask itself**
using get, list mine, `ask_status` or `list_my_asks`. `delivery` stays empty;
there is no inbox delivery, stored acknowledgement or automatic model wake.
Operator-origin inbox delivery requires the separate F1 CRD change.

Only a human admitted by the portal group can answer through the BFF.
The BFF derives the principal from its current request-private session and
fresh group check. The operator accepts that assertion only from its
admitted portal-reader caller. Sessions and coordinators cannot answer.

**This changes DESIGN-029's actor boundary for approval records:** the
operator stores `answeredBy {issuer, subject, displayName}` for audit,
rather than only a message HMAC `actorRef`. Portal Ask routes return all
three. Every session-facing `/v1/asks` response omits issuer and subject
and returns only `answeredBy.displayName`. The display name is presentation,
not an identity proof. The message actor boundary remains as documented
in DESIGN-029. Ask content and raw principals stay out of public logs,
page records and notifications.

An approval requires an explicit `approved: true` or `false` and exact
scope. **There is no revocation in v1.** Its scope expires no later than
24 hours after Ask creation, and the Ask's answer deadline cannot exceed
that scope expiry. Answering requires an open, unexpired Ask and future
scope expiry. Clients label `approved && now < scope.expiresAt` as
**computed** validity; it is not a stored server field. F2 introduces
revocation and its audit, subject to Q-255-01.

An information answer is never approval. A saved approval grants no
credential or proof of execution. Q-255-04 remains open: Tom must decide
whether the exact unexpired portal approval counts as his action-specific
approval once under the common rules. Existing permissions, explicit
holds and each action's enforcement continue to apply. Grant, budget and
native-decision adapters need separate reviewed stories.

T4, transcripts for all compatible Sessions, is outside v1 and awaits
Q-255-03. The proposal uses an operator-owned profile allowlist of `full`
and `dev`, makes `workbench` opt-in, and excludes Sessions holding
credential or break-glass grants. It preserves registration/Pod fences,
safe projection and honest Unavailable states. Send keeps its own scope.

## Alternatives considered

| Alternative | Why it loses as the primary record |
|---|---|
| Keep AskUserQuestion | Blocking and tied to one harness. Keep a supported fallback until board acceptance and instruction migration. |
| OpenProject comments | Useful for scope and handoff, but they do not bind a live requester UID, typed action and expiry. A tracker author is not automatically an admitted portal approver. Link the canonical work item. |
| Slack or Pushover replies | Add identity mapping and another answer surface. A notification acknowledgement or ordinary reply cannot establish the authenticated portal answer. A push links to the form. |
| Portal PostgreSQL as the Ask store | Adds an operator database dependency and duplicates coordination custody. It would not solve a later SQL-to-CRD inbox transaction without recovery work. |

## Consequences

Supported authenticated harnesses get the same nonblocking question and
answer path. Tom can answer later from a phone. V1 proves a saved answer
and subsequent agent read; it does not prove inbox delivery or action use.
The keeper sends optional pushes using a narrow Role to get/list Asks;
there is no operator-to-keeper adapter or shared notification credential.

The Ask kind needs schema, API, RBAC and client qualification. F1 adds
operator-origin inbox delivery and crash recovery; F2 adds revocation.
Extend DESIGN-024 explicitly before archiving and deleting Ask records.
T4 would increase transcript exposure within the allowed profiles;
redaction alone does not make credential-bearing Sessions safe to expose.

## Review and delivery

This proposal installs nothing, changes no live policy and approves no
action. The lead owns independent review, one-at-a-time verified question
delivery and merge. Q-255-01 is deferred until F2; quiet hours (Q-255-02),
T4 (Q-255-03) and common-rule approval meaning (Q-255-04) are unsettled.
Source tests, signed publication, GitOps installation and Tom's usable
acceptance remain separate evidence. This worker opens one ready PR
superseding #658 and stops once CI starts, without closing #658 or merging.
