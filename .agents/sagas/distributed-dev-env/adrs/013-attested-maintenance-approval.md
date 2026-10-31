# ADR-013: A maintenance task may be approved by a message to the requesting agent

- **Status:** Accepted 2026-10-31 for source preparation only. Every new
  path is default off. No live policy, grant, flag or activation follows
  from this ADR.
- **Date:** 2026-10-31
- **Decider:** Tom, workflow rule relayed by lead `dev-env-1029-132039` in the
  OP233 work order. The original message's timestamp is not in this record.
- **Amends:** [ADR-012](012-bounded-maintenance-task-grants.md), where it says
  the human decision "is independent of unverified agent coordinator:string
  attestation". That stays true of the human route. This ADR adds a second,
  separate route.
- **Retains:** everything else in ADR-012: the immutable envelope, command
  freedom inside it, the isolated executor, the 1 hour cap, expiry, release,
  restore and the held admission policies.
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

## Decision

1. A maintenance grant may also be approved by a verified agent attestation,
   through an attested GrantPolicy whose `spec.maintenance` matches the
   grant's envelope exactly. Exactly means: node and node UID, resource (the
   GPU UUID), runtime profile and digest-pinned image are equal; the
   capabilities are a subset; CPU and memory are at or below the policy's
   caps; every worker, device file and read-only mount the grant names is
   one the policy names exactly; and the TTL is at most the policy's
   `maxTTL`, itself at most 1 hour.
2. Only an attested policy can carry a maintenance scope. No standing
   policy auto-approves maintenance. The schema refuses one, and the broker
   does not trust that the schema ran.
3. The human route is unchanged and is checked first. A bound human
   decision, approve or deny, recorded before the broker approves wins over
   any attestation. Once the broker has approved through a policy, the grant
   is no longer pending and the human decision route refuses it. Then the
   stop is to release the grant, or to delete or narrow the policy or turn
   a flag off: the next broker pass (about 5 seconds) ends the lease.
4. Before it approves, the broker checks what it checks for every attested
   approval: the statement is for this grant's UID, it comes from the
   grant's requesting session UID and that session's current Running pod,
   and its scope digest is the grant's. The scope digest now covers the
   whole maintenance envelope, so a changed envelope voids the statement.
   The broker also runs the same identity, worker and node checks that the
   human route runs. The coordinator in the envelope is the requester and
   the attesting pod.
5. The approval is recorded as `approvedBy policy/<name>` with the D-88
   message "agent attestation, not verified human identity". Before the
   executor exists, and on every later pass, the broker re-checks that the
   approving policy still matches and that the statement still verifies. If
   not, it ends the lease through the existing stop path.
6. This route approves one lease per node UID and resource at a time. A
   second matching grant waits while another maintenance lease on that
   target is Active, however that lease was approved.
7. Default off. The broker approves this way only with
   `--enable-maintenance-grants`, `--enable-attested-grants` and the new
   `--enable-attested-maintenance` all on. The operator needs
   `--enable-maintenance-api` for the request and
   `--enable-grant-attestation` for the attestation. Activation also needs a
   separate GitOps GrantPolicy in haynes-ops that Tom approves, and the held
   ADR-012 admission policies.

## Consequences

- The agent attestation is a claim, not verified human identity. A session
  that lies about an approval can obtain a lease inside a policy Tom
  already approved in git. The policy is the bound on that risk: it names
  one node, one resource, one image and the caps, per lease. It does not
  bound how many leases follow one another: each new request, extensions
  included, needs its own attestation, and each attestation is the same
  unverified claim.
- The operator's exec route checks the lease, not its approver. Between
  broker passes, and while the broker is down, commands run until the
  broker ends the lease or it expires (at most the policy's `maxTTL`).
- A rollback to a broker that predates this ADR approves nothing through a
  maintenance policy: an attested policy leaves `profiles`, `repos` and
  `agents` empty, and older brokers never read policies for maintenance.
  An older broker also does not re-check a lease this route already
  approved. Before rolling back, turn `--enable-attested-maintenance` off
  and wait until no policy-approved maintenance lease is Active.
- A pod roll of the coordinator before approval voids the attestation, as
  for other attested grants. The envelope also pins the coordinator's pod,
  so the coordinator must request again anyway.
- Workers are pinned by session and pod UID. A policy that names workers
  goes stale when they roll. That is deliberate; most tasks name none.
