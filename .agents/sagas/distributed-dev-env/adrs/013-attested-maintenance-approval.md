# ADR-013: A maintenance task may be approved by a message to the requesting agent

- **Status:** Accepted 2026-10-31 for source preparation only. The new route
  is off by default in code (`--enable-attested-maintenance`), and no
  attested maintenance GrantPolicy exists. This ADR authorizes no policy,
  grant, flag change, release or pin. The live state below is what the lead
  and this change's author read on 2026-10-31; the next section separates it
  from code defaults.
- **Date:** 2026-10-31
- **Decider:** Tom, workflow rule relayed by lead `dev-env-1029-132039` in the
  OP233 work order. The original message's timestamp is not in this record.
- **Amends:** [ADR-012](012-bounded-maintenance-task-grants.md), where it says
  the human decision "is independent of unverified agent coordinator:string
  attestation". That stays true of the human route. This ADR adds a second,
  separate route.
- **Retains:** everything else in ADR-012: the immutable envelope, command
  freedom inside it, the isolated executor, the 1 hour cap, expiry, release
  and restore. An extension still needs the human route.
- **Builds on:** DESIGN-001 D-88 (agent attestation, 2026-10-27).
- **Design:** the dated amendment in the
  [bounded maintenance packet](../verification/bounded-maintenance-2026-10-30/README.md#amendment-2026-10-31-approval-by-message-to-the-requesting-agent-adr-013),
  and the D-88 amendment in [DESIGN-001](../designs/001-dev-env-v2.md).
- **Tracking:** OP233.

## Context

ADR-012 approves a bounded maintenance task through one route only: the
authenticated human identity on the operator's API writes a bound decision,
and the broker hands it to `broker.Decide`. Kube, egress and credential
grants have a second route since D-88: the requesting session records an
agent attestation (`agent-run grant attest`), and the broker approves through
a GrantPolicy that names `spec.attested`. Maintenance grants could not use
it. The broker handled them before it read any policy, and the policy schema
refused every maintenance policy.

Tom, 2026-10-31: "I or an agent can approve with a message to the agent
requesting the grant."

That sentence names two paths:

- **(a) The requester's self-attestation.** The requesting session records
  that it was told, by message, that the user or a coordinator approved.
  This ADR builds path (a).
- **(b) Authenticated delegate approval.** A coordinator's own identity
  records the decision on the operator's maintenance-decision route, from a
  ServiceAccount allowlist, recorded as `delegate/<ref>`. This is the planned
  path for an agent approver. It is a separate follow-up and is not built
  here.

## Decision

1. A maintenance grant may also be approved by an agent attestation whose
   binding the broker verifies (not verified human identity), through an
   attested GrantPolicy whose `spec.maintenance` matches the grant's
   envelope exactly. Exactly means: node and node UID, resource (the GPU
   UUID), runtime profile and digest-pinned image are equal; the
   capabilities are a subset; CPU and memory are at or below the policy's
   caps; every worker, device file and read-only mount the grant names is
   one the policy names exactly; and the TTL is at most the policy's
   `maxTTL`, itself at most 1 hour.
2. Only an attested policy can carry a maintenance scope. No standing
   policy auto-approves maintenance. The schema refuses one, and the broker
   does not trust that the schema ran.
3. The human route is unchanged and is checked first. It wins only while
   the grant is Pending: a bound human decision, approve or deny, recorded
   before the broker approves, is applied and the attested route is not
   consulted. Once the broker has approved through a policy, the human
   decision route refuses the grant, because it is no longer pending. Then
   revocation is by releasing the grant, deleting or narrowing the policy,
   or turning a flag off: the next broker pass (about 5 seconds) ends the
   lease.
4. Only a top-level requesting session may approve maintenance for itself.
   Its live AgentSession has no parent session: its `spec.parent` is the
   human, client or coordinator caller's ServiceAccount (`namespace/name`),
   or empty, and its depth label is absent or 0. An example is
   `haynes-ops-1029-223503`, with parent `dev-env-system/dev-env-human`. A
   child session never self-approves; it waits for the human route. A
   top-level session does self-approve, whatever its mode, including a
   headless task that the human, a client (the workbench, the v1 pod) or a
   coordinator host created. Before activation, the bundle adds one guard
   (DESIGN-042): a broker list of creators whose top-level sessions may
   self-approve, defaulting to the human ServiceAccount only.
5. An extension (`extensionOf`) is never approved this way. It needs the
   human route, as ADR-012 says.
6. Before it approves, the broker checks what it checks for every attested
   approval: the statement is for this grant's UID, it comes from the
   grant's requesting session UID and that session's current Running pod,
   and its scope digest is the grant's. The scope digest now covers the
   whole maintenance envelope, so a changed envelope voids the statement.
   The broker also runs the identity, worker and node checks of the human
   route. A separate explicit check requires the attestor to be the
   envelope's coordinator pod. The earlier checks already imply this; the
   second check guards against a later change to them.
7. This route approves one lease per node UID and resource at a time. A
   second matching grant waits while another maintenance grant on that
   target has not ended, however it was approved. "Not ended" means the
   grant is Active (its stop path included), is Pending with a human
   approval recorded, or has an executor that is running, stopping or of
   unknown state. The broker reads the grants from the API server for this
   check, not from its cache.
8. The approval is recorded as `approvedBy policy/<name>`, with the D-88
   message "agent attestation, not verified human identity". Before the
   executor exists, and on every later pass, the broker re-checks:
   - the flags;
   - that the requester is still top-level;
   - that the approving policy, read by name, still exists and still
     matches;
   - that the statement still verifies.

   If any check fails, the broker ends the lease through the existing stop
   path. A read error is retried and creates nothing.
9. Code defaults: the broker approves this way only with
   `--enable-maintenance-grants`, `--enable-attested-grants` and the new
   `--enable-attested-maintenance` all on. The operator needs
   `--enable-maintenance-api` for the request and
   `--enable-grant-attestation` for the attestation. All five default to
   false in code.

## Live state and what activation still needs (2026-10-31)

Live on 2026-10-31. The lead verified this, and this change's author re-read
it with `kubectl`:

- broker `--enable-attested-grants=true` and `--enable-maintenance-grants=true`;
- operator `--enable-grant-attestation=true` and `--enable-maintenance-api=true`.

So once a broker built from this change is deployed, two things gate this
route: the new flag and an attested maintenance GrantPolicy.

ADR-012 packet parts that are installed:

- the annotation guard `dev-env-maintenance-annotations`;
- the broker's maintenance RBAC (`dev-agents/dev-env-broker-maintenance` and
  its target and stop-evidence roles);
- the executor ServiceAccount;
- the admission-lookup RBAC;
- the baseline `PolicyException` `pss-baseline-dev-env-maintenance`;
- the maintenance exclusions on the session network policies.

Missing: the executor-shape admission (the packet's executor CREATE and
network ValidatingPolicies). No admission policy matches pod CREATE for the
executor. The lead verified this. This change's author could not list Kyverno
ValidatingPolicies with its own identity.

Activation still needs:

1. The regenerated GrantPolicy CRD copied to haynes-ops, before any pin that
   needs it. A stale CRD prunes `spec.maintenance`, and a policy without it
   matches nothing.
2. A release, and the operator image pin.
3. `--enable-attested-maintenance=true` on the broker, through GitOps.
4. One attested maintenance GrantPolicy in haynes-ops that Tom approves.

The missing executor-shape admission is ADR-012's own prerequisite. It is
not specific to this route.

## Consequences

- The agent attestation is a claim, not verified human identity. A session
  that lies about an approval can obtain a lease inside a policy Tom
  already approved in git. The policy is the bound on that risk: it names
  one node, one resource, one image and the caps, per lease. This route
  holds one lease per target at a time and never approves an extension. It
  does not bound how many fresh leases follow one another: each needs its
  own attestation, and each attestation is the same unverified claim.
- The operator's exec route checks the lease, not its approver. Between
  broker passes, and while the broker is down, commands run until the
  broker ends the lease or it expires (at most the policy's `maxTTL`).
- A rollback to a broker that predates this ADR approves nothing through a
  maintenance policy. An attested policy leaves `profiles`, `repos` and
  `agents` empty, and older brokers never read policies for maintenance.
  An older broker also does not re-check a lease this route already
  approved. Before rolling back, turn `--enable-attested-maintenance` off
  and wait until no policy-approved maintenance lease is Active.
- A pod roll of the coordinator ends the route's lease. Before approval it
  voids the attestation. After approval the identity check ends the lease.
- Workers are pinned by session and pod UID, so a policy that names workers
  goes stale when they roll. That is deliberate: most tasks name none.
- Follow-up: record the approving policy's UID beside `approvedBy`. Today
  the re-check reads the policy by name, so a policy deleted and recreated
  under the same name with the same scope keeps an Active lease.
- No one is notified when an agent self-approves maintenance. This change
  adds no phone push, because of Tom's quiet-hours rule and his rule against
  routine notifications. The broker logs the approval and records an Event.
  A notification is a possible follow-up, if Tom wants one.
- `agent-run grant attest` prints the type, TTL and scope digest, but not
  the envelope's node, GPU, image or capabilities. An approver answering by
  message should read `grant show -o json`. Printing the envelope is a
  follow-up.
