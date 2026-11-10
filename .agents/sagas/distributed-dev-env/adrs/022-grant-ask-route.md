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
and not given another Ask. The two minutes start at `status.askUnreadAt`,
the time of the first failed read of the current episode. The field is
optional and set once per episode: a later write may keep that instant or
clear it, and may not move it later. A failed deny does not clear it. The
bound covers the outer Ask read and the reads inside `Decide` that have to
succeed before the answer can be consumed. A restarted broker reads the
stored instant and applies the same two-minute limit. It does not start
another full wait. The field records the start, not a deadline, so the
limit stays one constant.

A create the apiserver refused before any commit may be retried, and only
until the grant's original 30-minute deadline (`CreationTimestamp` plus 30
minutes). That memory is in the process that has not sent the create. There
is no durable marker for it. Any error after the create request left the
client, including a failure of the store's status initialisation or its
final read, may have committed. The broker does not create again. A missing
Ask then denies the grant. A restart has no memory of an unsent create, and
a routed grant whose Ask is missing is denied the same way. A grant with no
route keeps the 30-minute deny, including while Ask reads fail.

The route write is not proof the route was stored. The response is decoded
into the object that was sent, so a pruned field can remain there. After
the write, a separate live read into a new object must show the route
before any Ask is created. If it does not, no Ask is created, the grant
keeps the 30-minute deny, and a Warning Event says the grant's CRD does
not store the route. The updated AccessGrant CRD is installed before
`--enable-grant-asks` is set, and it is not rolled back while a grant with
a route exists.

A grant that a policy with an attestation requirement would match gets no
route and no Ask. The statement need not be present yet. One live,
uncached list of GrantPolicy in that reconcile serves both the approval
match and this decision. A failed list opens no Ask. A policy added after
the route is committed does not take the grant off the Ask path. The
attestation approves an unrouted grant, or that grant is denied at 30
minutes, as it is today.

When the broker itself denies a routed grant for any reason other than the
Ask's own answer, it withdraws that Ask if the Ask passes the full identity
check. The deny is written first and stands whatever the withdraw does. A
failed withdraw is retried on a later reconcile of the denied grant until
the Ask is closed or gone. A foreign or unreadable Ask is left alone. An
Ask that is not open is left as stored: the Ask CRD does not let an
answered Ask become withdrawn. This amends ADR-021's sentence "The Ask
stays open" for a broker denial of a routed grant whose session is still
there. It does not amend section 5. When the session has ended, the card
stays open for Dismiss.

This amends ADR-021's build plan. `make generate` commits the deepcopy and
the CRD. The flag has never been set; docs/asks.md states that no grant Ask
from an earlier build exists.

## Consequences

| Id | Consequence |
|---|---|
| C-61 | Good: losing sight of the Ask cannot reopen a policy, an attestation, or a delegate or reserved decision. |
| C-62 | Bad: a routed grant whose Ask is deleted is denied. The session asks again. A create refused before commit is retried only until the original 30-minute deadline, and only in the process that has not sent it. A create that may have committed is not retried. |
| C-63 | Neutral: a grant with no route, including flag off, is unchanged. An attested policy still decides the grant without an Ask. |
| C-64 | Neutral: `status.askUnreadAt` is the start of the unread episode, set once per episode. A restart and a failed deny keep it. The two-minute limit stays one constant. |
| C-65 | Neutral: one live GrantPolicy list serves the approval match and the decision that an attested policy could match. A failed list opens no Ask. |
| C-66 | Bad: if a live read after the route write does not show the route, no Ask is created and the grant is denied at 30 minutes. The AccessGrant CRD that stores the route is installed before the flag is set, and is not rolled back while a routed grant exists. |
| C-67 | Neutral: a broker denial of a routed grant whose session is still there withdraws the broker's own open Ask after the deny is stored. An answered Ask stays answered. The ended-session card stays open for Dismiss. |
