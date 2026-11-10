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
limit stays one constant. If a live read after the write does not show
that instant, the grant is denied at once rather than waiting.

A create the apiserver refused before any commit may be retried, and only
until the grant's original 30-minute deadline (`CreationTimestamp` plus 30
minutes). That memory is in the process that has not sent the create. There
is no durable marker for it. While a pre-create attempt is outstanding, the
broker checks that deadline with a fresh clock before it branches on the Ask
read, so a read error cannot open a new unread episode past the deadline.
That outstanding marker is cleared only after the timeout denial is stored,
or a fresh read shows the grant is no longer Pending. A failed denial leaves
the marker. It checks the clock again at the create boundary, after the
preflight and before the create is sent, including the first attempt. The store checks
that same deadline once more, immediately before it sends the create, after
its own count list. It never creates after the deadline. Any error after the create request left the client,
including a failure of the store's status initialisation or its final read,
may have committed. The broker does not create again. A missing Ask then
denies the grant. A restart has no memory of an unsent create, and a routed
grant whose Ask is missing is denied the same way. A grant with no route
keeps the 30-minute deny, including while Ask reads fail.

The route write is not proof the route was stored. The response is decoded
into the object that was sent, so a pruned field can remain there. After
the write, a separate live read into a new object must show the route
before any Ask is created. If it does not, no Ask is created and the grant
is denied in that reconcile, by the broker, with the message "the grant's
CRD does not store the Ask route; update the CRD and request again". A
Warning Event says the grant's CRD does not store the route. There is no
wait. A storage failure on the Ask path is an immediate deny. A wait is a
state, and a state needs its own fence. A deny is terminal and needs none.
The same rule applies to `status.askUnreadAt`. After writing that instant,
a separate live read into a new object must show it. If it does not, the
grant is denied at once, with its own message: the CRD does not store the
Ask unread time. The two-minute bound applies only when that instant was
stored. The updated AccessGrant CRD stores both fields. It is installed
before `--enable-grant-asks` is set, and it is not rolled back while a
grant with a route exists. The partial-CRD case was considered; the grant
becomes Active there only by Tom's yes on its own Ask, as on a correct
install, so it is accepted.

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
the Ask is closed or gone. The first retry waits one second and each later
failure doubles that wait, up to five minutes. It is not a fixed one-second
loop. Retention deletes the grant when its time comes even if the card
could not be read. A read failure that is not NotFound leaves the Ask
untouched on that pass. The expected Ask UID and the full grant identity
travel into the withdraw and are checked on every attempt, including each
conflict retry and the label write after the withdrawal is stored. That
label patch tests the Ask UID and resourceVersion before it writes. A
foreign Ask is left alone. An Ask that is not open is left as stored: the
Ask CRD does not let an answered Ask become withdrawn. When the stored
route is empty, cleanup computes the Ask name the same way a decision
does, and those identity and UID checks still run before any write. An Ask
create or update whose Ask matches a terminal grant enqueues that grant for
this cleanup. A failed lookup of the owning grant is retried. The grant is
already terminal, so the enqueue cannot approve it. This amends ADR-021's
sentence "The Ask stays open" for a broker denial of a routed grant whose
session is still there. It does not amend section 5. When the session has
ended, the card stays open for Dismiss and the Ask event does not enqueue
it. A pending grant the requester releases is stored Released first. The
Ask is then withdrawn from that terminal path, with the same backoff as a
deny.

This amends ADR-021's build plan. `make generate` commits the deepcopy and
the CRD. The flag has never been set; docs/asks.md states that no grant Ask
from an earlier build exists.

## Consequences

| Id | Consequence |
|---|---|
| C-61 | Good: losing sight of the Ask cannot reopen a policy, an attestation, or a delegate or reserved decision. |
| C-62 | Bad: a routed grant whose Ask is deleted is denied. The session asks again. A create refused before commit is retried only until the original 30-minute deadline, and only in the process that has not sent it. That deadline is a fresh clock reading before the Ask-read branch, again at the create boundary, and again inside the store immediately before the request is sent. A create that may have committed is not retried. |
| C-63 | Neutral: a grant with no route, including flag off, is unchanged. An attested policy still decides the grant without an Ask. |
| C-64 | Neutral: `status.askUnreadAt` is the start of the unread episode, set once per episode. A restart and a failed deny keep it. The two-minute limit stays one constant. A live read that does not show the instant denies the grant at once. |
| C-65 | Neutral: one live GrantPolicy list serves the approval match and the decision that an attested policy could match. A failed list opens no Ask. |
| C-66 | Bad: if a live read after the route write does not show the route, no Ask is created and the grant is denied at once by the broker. A storage failure on the Ask path is an immediate deny, because a wait is a state and a state needs its own fence. The AccessGrant CRD that stores `status.ask` and `status.askUnreadAt` is installed before the flag is set, and is not rolled back while a routed grant exists. |
| C-67 | Neutral: a broker denial of a routed grant whose session is still there withdraws the broker's own open Ask after the deny is stored. An empty stored route uses the computed Ask name, with the same identity and UID checks before any write. A pending release is stored Released first and retries the withdraw the same way. A read failure that is not NotFound backs off from one second to five minutes and does not touch the Ask. Retention is not held up when the card cannot be read. A matching Ask create or update enqueues a terminal grant for that cleanup, and a failed owner lookup is retried. Every attempt checks the Ask UID and the full grant identity, including the label patch and its UID and resourceVersion preconditions. An answered Ask stays answered. The ended-session card stays open for Dismiss and is not enqueued. |
