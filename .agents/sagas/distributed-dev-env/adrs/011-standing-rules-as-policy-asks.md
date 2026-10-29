# ADR-011: Standing rules are policy Asks

- **Status:** Proposed. Tom has ruled on the distinction; this design still
  needs review.
- **Date:** 2026-10-29
- **Tracking:** OpenProject #261, Epic #254 (Asks board), Saga #85.
- **Work order:** ASKS261-STANDING-RULES-DESIGN-20261029-01. Docs only.
- **Design:** [DESIGN-036](../designs/036-standing-rules.md).
- **Builds on:** [ADR-010](010-agent-asks-and-approvals-board.md),
  [DESIGN-034](../designs/034-agent-asks-and-approvals-board.md),
  [DESIGN-029](../designs/029-portal-foundations.md#group-based-action-scopes-2026-10-29)
  and [DESIGN-001 §6.12](../designs/001-dev-env-v2.md#612-access-no-prompts-in-the-pod-control-at-the-platform).

## Context

Tom's Q-255-04 ruling, OpenProject #255 journal 2680, distinguishes approval
of one exact action from approval of a rule used across requests. Repeated
approval of the same permitted work should not be necessary. One approved
draft message must never become permission to send other messages.

DESIGN-034 v1 records one-off approvals. Its stored answer is not a policy
writer or an executor. The current grant system already has standing
approvals: `GrantPolicy` objects in haynes-ops, applied by Flux. The broker
reads them and issues time-limited `AccessGrant` objects. Its RBAC permits
policy get/list/watch, with no policy write.

That mechanism does not yet provide the whole requested feature. Policies
match profiles and repositories, not session groups or projects. The
current kube catalog cannot express exec into one pod. The default agent
identity already holds pods/exec in every namespace except the dev-env
namespaces, and never into privileged pods, so a grant alone would add no permission for exec; today
the common rules are its only control. Q-261-05 asks how standing rules
cover that case. Policy removal is
not an immediate revocation barrier, and active kube and egress grants do
not recheck the approving policy. DESIGN-036 identifies these source gaps.
Kube role names also refer to mutable permissions. Kube activation must
wait for immutable role revisions bound into consent and enforcement;
checking role drift only at admission would leave existing grants exposed.

## Decision

Add a second **approval kind** of Ask: a **standing rule**. Keep information
Asks and the existing one-off approval kind. Propose `type: standing_rule`
alongside `info` and `approval`; the discriminant is immutable at creation.
Approving a standing-rule Ask authorizes a bounded policy change. It does
not approve an action performed by that Ask's requester.

Each rule names who may use it, the action, target, limits, a review or
expiry date, and a reason. Who must name a profile, trusted session group
or declared project. Unsupported scopes and rules such as “agents may send
email” are refused. The default profile alone counts as “any agent” and
is refused. Nothing compiles to broader authority than Tom saw.

Use an **operator-written GitOps PR** to activate the rule. A narrow writer
may propose the exact approved policy diff in haynes-ops. It cannot merge
the PR, apply cluster configuration or write a live `GrantPolicy`. Tom's
portal consent is recorded once; required source checks and the authorized
GitOps merge remain delivery steps. If the approved meaning changes, a new
standing-rule Ask is required. The portal shows approval and installation
as separate facts. The guarantee that no agent can approve or widen its
own managed rule holds only after SR6's legacy-policy path guard is
installed and tested. Today an identity allowed to merge in haynes-ops
can add a legacy policy without portal consent. SR6 must protect
`grant-policies/` from general agent mergers, or restrict broker approval
to a GitOps-pinned list of legacy policy names and digests. The PR writer
needs its own GitHub identity, forbidden from merging by the ruleset.

Do not add broker-owned `GrantPolicy` writes. The current code and deployed
RBAC do not support that ownership, and Flux would become a competing
writer. A rule-aware policy format must refuse use on older brokers.

Add an audited runtime revocation barrier to the broker. It may narrow
access only. Once the broker confirms revocation, this rule cannot issue
another grant, even while its GitOps policy still exists. A GitOps PR then
removes the policy. Propose release of grants already issued under the
rule, with cleanup progress and limits shown honestly. This behavior and
its authority need the decisions recorded in DESIGN-036; they do not exist
today.

List rules, exact scope, installation evidence, issued grants and revoke
controls in the portal. Use DESIGN-029's fresh group checks and
DESIGN-034's authenticated answer attribution. A copied answer, agent
attestation or rewritten question cannot create a rule. An active rule
permits matching requests without another human approval. It supplies no
credential by itself and lifts no other hold.

## Alternatives

| Alternative | Reason to reject it |
|---|---|
| Infer rules from repeated one-off approvals or answer text | Changes what Tom approved and silently widens authority. |
| Have the broker create live `GrantPolicy` objects | Requires new policy write authority and changes GitOps ownership. There is no safe existing path to reuse. |
| Revoke only by deleting the GitOps policy | Flux and informer delay leave a window for new grants. Existing grants survive policy deletion in current kube/egress code. |
| Ask again on every matching grant | Defeats the standing rule. Ask again only for a changed rule or work outside its scope. |

## Consequences and delivery

Activation needs reviewed schema, API, broker, portal and GitOps work.
Revocation needs a durable admission barrier and grant provenance, not
just a new button. Current attestation remains an agent claim under D-88;
it is not verified human consent to a standing rule.

DESIGN-034 F2 and Q-255-01 still govern the unresolved one-off revocation
and audit work. Standing-rule authority must be settled explicitly too.
DESIGN-036 records recommendations as Q-261-NN, not owner rulings.

This ADR installs nothing and changes no permission. Source qualification,
signed publication, GitOps installation and usable acceptance are separate.
