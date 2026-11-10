# ADR-022: A grant records its Ask route before the Ask exists

- **Status:** Proposed 2026-11-10, for the coordinator to review. It installs nothing.
- **Date:** 2026-11-10
- **Decider:** Proposed under OpenProject #525. Tom's behaviour rulings stay
  the ones [ADR-021](021-grants-through-the-asks-board.md) quotes. This ADR
  is the mechanism those reviews require.
- **Amends:** ADR-021's build plan, and the alternative there that rejected
  an AccessGrant status field. ADR-021 is Accepted and is not edited.
- **Supersedes:** nothing whole. The 30-minute deny stays for every grant
  that never takes this path, and for every deployment where the flag is off
  and the grant has no route.
- **Retains:** `--enable-grant-asks` default off; the `grant` Ask kind; one
  answer applied by the broker; attested policies; delegate, reserved and
  maintenance decisions; the broker as the writer of AccessGrant status.
- **Tracking:** OpenProject [#525](https://work.haynesnetwork.com/work_packages/525),
  dev-env pull request 1041.

## Context

ADR-021 sends a grant no policy decides through one Ask. The broker finds
that Ask by a name computed from the session and `grant-` plus the grant
UID. The grant itself does not record the choice.

Two independent reviews of pull request 1041 read that path. The first fix
round held on the path each test walked. It did not hold when the Ask was
missing, late, or foreign. Deleting the Ask, or reading it as absent, let a
policy or a delegate write Active, and a failed read of a grant that had
never had an Ask skipped the 30-minute deny. Creating the Ask does not
change the grant, so one reconcile can approve by policy while another
creates the Ask. ADR-021 rejected a status field because the story's reach
did not include an AccessGrant CRD change. The reviews show the computed
name is not a route.

## Decision

The broker writes one optional status field, `status.ask`, the Ask object's
name (`ask-` plus 16 hex digits, MaxLength 20). It sets the field once and
never clears it. The write uses the grant's resourceVersion as the
precondition, and it happens before the Ask is created. A conflict means
read the grant again and decide again from that object. The broker does not
open a second Ask.

From that write on, the grant is on the Ask path for the rest of its life,
flag on or later turned off. Every writer of an approval reads the route
from a live copy of the grant inside the attempt: the policy write in
`pending`, `Decide` on every attempt including each conflict retry (delegate
and reserved decisions call `Decide`), and the attested maintenance
approval. A routed grant is approved only when the decision is that Ask's
answer, re-read in the same attempt. The answer and the timeout exemption
need the full identity: grant kind, this grant's UID, this session's name
and UID, and broker origin. The origin label alone is not that identity.

If that Ask is then missing, not this grant's, or unreadable for longer
than two minutes, the grant is denied. It is not handed to another path
and not given another Ask. A create this process has not seen succeed is
retried; a restart has no such memory, and a routed grant whose Ask is
missing is denied. A grant with no route keeps the 30-minute deny, including
while Ask reads fail.

A grant that a policy with an attestation requirement would match gets no
route and no Ask. The attestation approves it, or it is denied at 30
minutes, as it is today.

This amends ADR-021's build plan. `make generate` commits the deepcopy and
the CRD. The flag has never been set; docs/asks.md states that no grant Ask
from an earlier build exists.

## Consequences

| Id | Consequence |
|---|---|
| C-61 | Good: losing sight of the Ask cannot reopen a policy, an attestation, or a delegate or reserved decision. |
| C-62 | Bad: a routed grant whose Ask is deleted is denied. The session asks again. A full board still leaves the grant pending and retries the one create. |
| C-63 | Neutral: a grant with no route, including flag off, is unchanged. An attested policy still decides the grant without an Ask. |
