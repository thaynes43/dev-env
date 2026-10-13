# Maintenance-aware response and scoped alert mutes

Recorded 2026-10-12, America/New_York.

**Status:** owner-requested outcomes; the detailed contract and phase below are
proposed for review. This is a requirements record, not runtime authorization.
It changes no alerts, grants, dispatcher behavior, deployment or cutover gate.

## Problem and required outcome

Tom reported that a responder opened Remote Control to investigate alerts caused
by another agent's intentional node reboot. An active maintenance task and a
genuine outage need different responses. The v2 central dispatcher must check
declared work and ownership before deciding whether to launch a responder.
Agents must also be able to request a temporary mute for the area they work in.

An agent merely being active is not evidence that an alert is expected. Preserve
raw alerts and investigate unexpected impact, including harm caused by otherwise
authorized maintenance.

## Reuse the existing architecture

- [DESIGN-001 section 6.9](../designs/001-dev-env-v2.md#69-declare-activity)
  defines authenticated `Activity`, resource scope and expiry. Extend that
  contract rather than create a parallel maintenance registry. An Activity is
  context, not an existing alert-mute grant.
- [Plan 10](../backlog/10-summoned-sessions.md) owns automated callers, dispatch
  policy, lanes, idempotency, limits and outcomes. Put the decision to launch or
  defer a responder in that operator/API path, shared by shepherd, triage and
  responder callers. This does not select a separate dispatcher service.
- [Plan 11](../backlog/11-project-workspaces.md),
  [TaskClaim ownership](../designs/010-atomic-task-claims.md),
  [the session directory](../designs/017-durable-session-directory.md) and
  [the durable inbox](../designs/014-durable-offline-inbox.md) supply discoverable
  work, authenticated owners and attributed coordination. Preserve their access
  boundaries and uncertainty; stale metadata does not prove a live executor.
- [Session coordination](../../../../docs/session-coordination.md) supplies the
  API-first visibility model. The [summoned-agent audit](../research/R-01-summoned-agents-audit.md)
  already requires Activity checks (V-17), caller limits (V-10) and idempotency
  (V-03). This requirement makes the maintenance decision and its evidence
  explicit.

## Proposed behavior

### Decide before launching

For each alert or correlated incident, the dispatcher checks active session
observations, explicit task claims, declared Activity and maintenance outcomes.
Match stable affected-resource identities, alert identity, observed timing and
the declared expected symptoms. A shared node or nearby timestamp alone is not
a match.

Record one explained disposition: expected transient impact; an existing
responder owns it; new response required; or insufficient evidence. Include the
observations and their timestamps, matched activity/task, policy revision,
reason and next reevaluation time. Expected impact requires current, specific
evidence; missing, stale or unavailable coordination data must not silently
classify an incident as harmless.

Before dispatch, recheck ownership and the maintenance window. Use the existing
atomic claims/idempotency and lane controls to prevent races between callers.
An existing investigation can receive attributed evidence or an assistance
request without acquiring a second owner or launching a competing fix. A lost
or uncertain owner does not authorize takeover without the existing stop and
handoff contract.

### Request a bounded mute

A mute request identifies the authenticated requesting agent, owner session and
logical task, linked Activity/maintenance record, reason, exact resource and
alert scope, expected symptom/severity bounds, start and expiry, and the outcome
that ends the mute early. It records the policy/approver that permitted it and
the actions deferred, such as duplicate paging or responder launch.

An agent may request a mute only within its existing authority. Approval rules,
schema and precise duration limits need a separate reviewed design; this
document creates no new permission. Preserve current Activity TTL ceilings
unless a separately accepted change revises them. No implicit cluster-wide
scope, indefinite duration or automatic renewal follows from agent activity.

Expose requested, active, denied, expired and revoked state. Revocation, early
completion, failure or expiry stops suppression. Renewal is a new bounded
decision with fresh evidence, not a reset of the task's elapsed work or failure
budget. A dispatcher restart must not extend the saved expiry.

### Keep alerts visible and escalate deviations

Mute notification/dispatch effects only within the approved scope; retain raw
alert observations and an inspectable audit trail. Show who requested and
authorized the mute, what it covers, its expiry, correlated alerts, every
decision to defer or dispatch, and why it ended. Support bounded API/CLI reads
before any later web-client work. Keep private context under existing ACLs.

Escalate when symptoms exceed the declared resources, time window or severity;
maintenance fails; recovery is not observed by the deadline; or the task/owner
is abandoned or no longer verifiably responsible. Preserve unrelated alerts and
the ability to raise genuine unexpected impact inside the maintenance area.
Unknown ownership must not leave a mute active indefinitely. Reevaluate retained
firing alerts at expiry/revocation without requiring a new alert edge.

### Operator visibility and control

Owner clarification, 2026-10-12, America/New_York: Tom and Meatball are both
first-class operators. Supported API/MCP access for Meatball is as important as
Tom's web UI. Extend the existing [visibility and management outcome](2026-10-10-owner-scope.md#required-outcomes),
[coordination API model](../../../../docs/session-coordination.md) and
[later console plan](../designs/001-dev-env-v2.md#38-the-console).

Define the read contracts first, so the API/MCP and later web UI expose the same
authorized facts: active Activities and task claims; logical task, owner/session
identity and provider; affected resources; status and expiry or lease state where
the contract defines one; conflicts, stale or unknown ownership; concrete progress
and why work is blocked. Correlate these records with maintenance, mute requests,
dispatch decisions and retained history. Include source/provenance, record revision
and observation time; distinguish last observed state from current proof. Missing
or inaccessible evidence stays Unknown, with its reason.

Linked coordination messages expose their sender/recipient, delivery and explicit
acknowledgment state. Separate queued, delivered, API-read acknowledgment and an
agent's acceptance when evidence exists; none alone proves execution or completion.
Expose supported, unsupported and currently unavailable provider/control capabilities
and reasons, rather than implying every harness has the same controls.

Meatball must be able to discover work, read progress/blocker context, and start,
steer, stop or coordinate sessions through supported controls within its existing
authority. This is core controllability acceptance, not only a future dashboard.
Minimize manual copy-paste relays without bypassing required human approvals or
security boundaries. Keep read visibility separate from each action's authorization;
record the actor, delegated scope, applicable policy/approval and observed outcome.
When approval is required, preserve the request and context through the supported
approval path. Existing valid approval/delegation must remain usable within its
scope without redundant routing or confirmation.

These requirements grant no universal administrator access, new credentials,
runtime authority or approval delegation. They select no new frontend or control
architecture; implement bounded contracts through the existing roadmap, with the
web UI consuming them later.

## Observable acceptance

Use bounded fixtures and fake time first; any live trial needs a separate
reviewed scope. No node reboot or alert mutation is authorized by this record.

| Scenario | Required observation |
| --- | --- |
| Intentional reboot of node A | An authenticated task declares node A, expected alert families and a finite recovery window. Matching transient alerts remain visible with a correlation decision; no duplicate responder or Remote Control session is launched. |
| Unrelated outage while maintenance is active | An outage on node B, or an unrelated alert on node A, follows normal response/escalation policy. The dispatcher explains why the maintenance record did not cover it. |
| Impact exceeds the declaration | A broader resource impact, higher severity or missed recovery deadline causes escalation even while the mute's nominal duration remains. |
| Failed maintenance | A failure outcome ends suppression and escalates the retained firing incident with the original task and evidence attached. |
| Expired or abandoned mute | Fake time crosses expiry, or the task is abandoned. Suppression stops, still-firing alerts are reevaluated, and the audit records the reason; a dispatcher restart does not renew it. |
| Two callers report the same incident | Concurrent shepherd/responder requests produce one confirmed response owner. The other caller receives the existing owner or an explicit uncertain/queued result, never a competing fix. |
| Revocation or unauthorized scope | Authorized revocation restores response promptly. A requester outside the permitted resource scope cannot create or broaden a mute; denial is observable. |
| Coordination data is unavailable | The decision reports uncertainty and applies the existing bounded escalation policy; it does not blanket-suppress alerts or create an uncontrolled responder storm. |
| Shared operator visibility | An authorized API/MCP consumer and, when built, Tom's web UI can inspect the same Activity/claim, owner/session/provider, resource scope, status, expiry where defined, and reason work is blocked, linked to maintenance, mute/dispatch decisions and retained history. |
| Stale ownership or pending coordination | The read exposes conflict/staleness, provenance and observation time, plus linked message delivery/ACK state. A stale claim, queued message or API ACK is never presented as confirmed live ownership, agent acceptance or completed work. |
| Authorized operator control | Meatball discovers a task and reads its progress/blocker context, then uses a supported start, steer, stop or coordination operation under an existing scoped authorization. The receipt identifies actor, target, policy/delegation and observed outcome; unsupported capability and uncertain delivery/stop remain explicit. |
| Read access without action authority | Both operator clients can inspect permitted evidence without gaining mutation rights. A gated action follows the existing human approval path with its context intact; denied or absent authority leaves the action unperformed, while valid scoped delegation needs no manual relay or duplicate approval. |

Acceptance must retain decision IDs, task/owner bindings, matched alert/resource
identities, timestamps, expiry/revocation events and responder counts. Report
source tests, deployment and observed runtime behavior separately.

## Sizing and recommended phase

Capture and size this now as scope added by the owner. Recommended delivery is
a bounded Plan 10 dispatcher/caller increment using Plan 11 discovery, claims
and messaging, after the current provider-rule/canary and parity milestone.
The maintenance-aware response acceptance belongs before migrating the affected
automated callers to v2 or claiming their parity. It is not an invitation to
redesign the dispatcher during the current canary, or to make a future web UI a
prerequisite for that milestone.

Core operator discovery, context and authorized control follow the existing
[coordination and controllability roadmap](../../../CLAUDE-RESUME-2026-10-12.md#next-bounded-work-and-the-six-step-roadmap).
They are not deferred with the later web UI or made dependent on this Plan 10
maintenance increment. Record API acceptance separately from UI delivery.

The implementation owner should identify missing dependencies, propose the
smallest source slice and confirm phase placement. Keep the current parity
milestone moving. Any alert, policy, access or deployment change needs its own
reviewed change; existing stopped attempts, consumed probe IDs and owner
approval requirements remain intact.
