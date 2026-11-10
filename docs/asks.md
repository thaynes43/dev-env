# Asks and approvals

Source only. Lane A stores and serves asks; lane B adds operator expiry and
keeper notifications for
[DESIGN-034](../.agents/sagas/distributed-dev-env/designs/034-agent-asks-and-approvals-board.md)
and
[ADR-010](../.agents/sagas/distributed-dev-env/adrs/010-agent-asks-and-approvals-board.md)
(OpenProject Epic #254). The operator serves asks only with `--enable-asks`
(default off), including its leader-only expiry sweeper. Nothing here is installed, and turning the flag on in a cluster
still needs the Ask CRD and RBAC in haynes-ops. An ask is not an approval until
a portal reader answers it, and an expired approval is not an approval.

A grant ask is a third kind ([ADR-021](../.agents/sagas/distributed-dev-env/adrs/021-grants-through-the-asks-board.md)).
It carries a typed grant scope (`uid`, `scopeDigest`, `type`, `target`, `ttl`,
and optional `detail`) and omits `expiresAt` and the approval scope. It does
not expire. An info or approval ask cannot carry a grant object, and a grant
ask cannot carry an approval scope or an expiry. Info and approval behaviour
is otherwise unchanged.

`POST /v1/asks` refuses `type=grant`, and refuses a `requestId` that starts
with `grant-`. That prefix is the broker's. The session route also drops any
labels on the body, including `coordination.dev-env.haynesops.com/ask-origin`,
so a session create does not store that label.

Rollout precondition: no grant Ask from an earlier build exists.
`--enable-grant-asks` has never been set in any cluster, so no flag-on grant
Ask is stored. A stored grant Ask whose scope text is not the text this build
renders is denied, and the request must be made again. The AccessGrant CRD
that stores `status.ask` and `status.askUnreadAt` is installed before the
flag is set. That CRD is not rolled back while a grant with a route exists:
a rollback drops the route, and the broker then creates no Ask.

The broker writes `status.ask` on the grant, the Ask's object name, before it
creates that Ask ([ADR-022](../.agents/sagas/distributed-dev-env/adrs/022-grant-ask-route.md)).
It sets the field once and does not clear it. A separate live read of a new
object must show the route before the Ask is created. If that read does not
show it, no Ask is created and the grant is denied in that reconcile, by the
broker. The message is that the CRD does not store the Ask route. A Warning
Event says the same. There is no wait. A storage failure on the Ask path is
an immediate deny: a wait is a state, and a state needs its own fence. From
a stored route on, that Ask is the only approval. If it is missing, not this
grant's, or unreadable for two minutes, the grant is denied. The two minutes
start at `status.askUnreadAt` and survive a restart. After that instant is
written, a separate live read of a new object must show it. If it does not,
the grant is denied at once, with its own message, that the CRD does not
store the Ask unread time. A create the apiserver refused before commit is
retried only until the grant's original 30-minute deadline. While that
pre-create is outstanding, the broker checks the deadline with a fresh clock
before it branches on the Ask read, and again at the create itself, including
the first attempt. The store checks that same deadline once more, immediately
before it sends the create, after its own count list. It never creates after
the deadline. The outstanding marker is cleared only after that timeout
denial is stored, or a fresh read shows the grant is no longer Pending. A
failed denial leaves the marker, so a later read error does not open a new
unread episode. A read error before the deadline does not open one either.
A create that may have committed is not retried, and a missing Ask denies
the grant. A grant with
no route is still denied at 30 minutes. A grant an attested policy could
match, from one live policy list in that reconcile, is not given a route or
an Ask.

When the broker denies a routed grant for a reason other than that Ask's own
answer, it withdraws its own open Ask after the deny is stored. An empty
stored route still withdraws: cleanup computes the Ask name the same way a
decision does, and the identity and UID checks run before any write. A failed
withdraw is retried later. The first wait is one second and each later
failure doubles it, up to five minutes. A read that fails and is not NotFound
keeps that backoff and leaves the Ask untouched on that pass. A Delete
that conflicts leaves that wait in place, so a repeated conflict does not
start again at one second. The wait is cleared when the Delete succeeds or
returns NotFound. Retention deletes the grant at its time even when the
card could not be read. Every attempt checks the expected Ask UID and the
full grant identity, including the label write after the withdrawal is
stored. That patch tests the Ask UID
and resourceVersion. An answered Ask stays answered. A foreign Ask is left
alone. The card for a session that has ended stays open for Dismiss, and an
Ask event does not enqueue it. A create or update of an Ask that matches a
terminal grant enqueues that grant for this cleanup. A failed lookup of the
owning grant is retried only when that scheduled retry is due. An unrelated
Ask event does not run it. One retry reads the grants once for every held
Ask. At most 32 are held, oldest first. A newer Ask is not added when the
set is full, and the next reconcile of its grant still finds it: that
reconcile loads the Ask from the grant and does not use the held set. The
grant is already terminal, so the enqueue cannot approve it. A pending grant
the requester releases is stored Released first. The Ask is then withdrawn
from that terminal path, with the same backoff as a deny.

The store accepts a grant ask and applies the caps below. It records the
requester, the open state and the ask type. It records `ask-origin=broker`
only when the broker caller sets that origin on the same create. A session
caller cannot set it. `POST /v1/asks/{id}/withdraw` refuses `type=grant`.
The broker withdraws a grant Ask when the session releases the grant.

The broker puts the complete access the digest covers into the Ask detail,
with explicit field names (`namespaces:[...]`, `allNamespaces:true`,
`reserved:true`, and the secret, storage, destination and port fields). A
namespace role includes every Secret name. Egress includes every destination
with its ports, protocols and label selectors. When that text does not fit the
detail bound, the broker opens no Ask. The grant keeps the 30-minute denial,
and a Warning Event says the request is too large to show and should be split.

A grant answer requires `approved` and a `username` matching
`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,62}$`. An info or approval answer omits
`username`. Session responses omit `username` along with issuer and subject.

Asks live in the `dev-agents` namespace as
`asks.coordination.dev-env.haynesops.com`. The spec is immutable. The status
answer is set once. v1 does not create an InboxMessage: `delivery` stays empty,
and the requester reads the answer from the ask (GET, list mine, and later the
MCP status tools).

## Routes

Session callers create, list their own, read their own and withdraw their own.
There is no session answer route. The portal reader lists, reads and answers.
A portal answer carries the actor principal in the body (issuer, subject, and
an optional display name), the same way the portal message route carries the
principal the portal authenticated.

| Method | Path | Caller | Success |
| --- | --- | --- | --- |
| POST | `/v1/asks` | session | 201 created, 200 identical `requestId` |
| GET | `/v1/asks?mine=true` | session | 200, newest first |
| GET | `/v1/asks/{id}` | owning session | 200 |
| POST | `/v1/asks/{id}/withdraw` | owning session, while open, not `type=grant` | 200 |
| GET | `/v1/portal/asks?state=open` or `all` | portal reader | 200 |
| GET | `/v1/portal/asks/{id}` | portal reader, any state | 200 |
| POST | `/v1/portal/asks/{id}/answer` | portal reader, while open and unexpired | 200 |

`state=open` is the effective state: an open ask whose `expiresAt` (or, for an
approval, whose `scope.expiresAt`) has passed is not listed as open. A grant
ask has no `expiresAt` and stays open. Pages use
`limit` (default 20, maximum 100) and an opaque `cursor`. Order is `createdAt`
descending, then `id` descending.

Session responses include `answeredBy.displayName` only (empty becomes
`Portal user`). They omit issuer, subject and `username`. That includes
create, an identical create retry, list mine, get and withdraw. Portal
responses include issuer, subject and display name, and `username` when the
stored answer has one.
Whether an approval is still in force (`approved` and now before
`scope.expiresAt`) is computed by the client. It is not a stored field.

A missing `status.state` reads as open. A list that cannot read one record
skips that record, logs its id, and marks the page incomplete. It does not
fail the page. `Complete` is false when `NextCursor` leads to more, or when
the server skipped an unreadable record. An empty `NextCursor` ends the list.
`?mine=true` keeps a record only when `spec.context` names the
calling session; the requester label is not enough.

## Time and approval scope

The server truncates its clock to whole seconds once per request and stores
`createdAt`, `expiresAt` and `answeredAt` at that precision. `expiresIn` must
be a whole number of seconds. `scope.expiresAt` must also be a whole second;
a fractional second is a 400, so a value Kubernetes would truncate cannot fail
validation after it is stored.

An approval scope lasts from 1 minute to 24 hours after creation. An approval
ask expires no later than its scope. With no `expiresIn`, an approval ask
expires at `scope.expiresAt`.

## Limits

An identical `requestId` is resolved before validation and the room check, so
a retry needs no slot and is not refused because the clock has moved. A new
create counts, then creates. The bounds are 16 open info and approval asks
per requester, 512 asks per requester, and 4096 asks in the namespace. Grant
asks are counted apart: 3 open grant asks per session and 48 open grant asks
in the namespace. Those opens still sit inside the 512 and 4096 lifetime
bounds, and a grant ask does not take one of the 16 info and approval slots.
Open info and approval asks are counted from the state label, excluding the
grant type; a missing type label still counts toward the 16. Open grant asks
are counted from the state label and the grant type label. When an open count
is already at its bound, the operator reads the matching open-labelled asks
(at most 512) and counts only those whose stored state is open or absent and
whose effective state is still open. It repairs the other labels. An
unconfirmed count is 503. At or above 80 percent of a bound the operator logs
and increments `dev_env_asks_near_limit_total`.

## Expiry sweeper

The operator reuses `--enable-asks` for a leader-only sweeper; enabling asks
requires leader election. Every minute it scans at most nine pages of 500
records, without trusting the state label. It applies `EffectiveState` to the
Ask deadline and, for approval, the scope deadline. An absent state is open.
A grant ask is left as it is: the sweeper does not expire it because a clock
passed. Info and approval asks still expire. The list snapshot selects only
records needing a status or label change for
an authoritative GET; unchanged, answered and withdrawn records need no GET.
An expired open info or approval Ask becomes `expired` through a status update carrying its
resourceVersion. On conflict it rereads, so an answer that already committed
wins. Answered and withdrawn Asks are untouched, even after approval expiry.

Label repair uses an optimistic CAS patch. A crash after the status write
leaves a label the next pass can repair. An incorrect label on an open Ask
is also repaired. With the flag off, no sweeper is registered and no Ask scan
or write runs. Answer-time expiry checks still apply independently.

## Keeper notifications

The keeper adds `--enable-ask-notifications` (default false). It requires
leader election, `--ask-notify-portal-origin=https://<portal-host>` and the
existing Pushover credential files/endpoint flags. It runs independently of
login notices. **Q-255-02 is unanswered; live Ask pushes remain held.**
Invalid Ask notification settings log one fixed diagnostic and disable only
Ask notifications; the credential keeper continues minting tokens.

Every 30 seconds, the keeper reads a bounded Ask list from the API server and
rechecks the selected Ask with GET. Only effectively open Asks can notify.
An unreadable Ask does not block pushes for readable records. An incomplete
scan preserves all dedupe entries because it cannot prove any Ask absent.
The title and body are both `An agent needs your answer`, priority 0. The
link is the configured HTTPS origin plus
`/portal/projects/dev-env/environments/internal/asks?ask=<id>`. It opens an
authenticated GET; the push carries no question, PR title, principal, token
or approval material.

Pending Asks from the same original requester coalesce into one push linking
the oldest pending Ask. All those IDs are marked done for notification
purposes with their representative, including after a failed attempt. If the
representative closes before its eligible retry, its coalesced siblings get
no separate push; their durable questions stay visible on the board. Later
Asks wait for an eligible slot. Caps are
one Ask push per requester per five minutes and six Ask pushes per configured
recipient per rolling hour. Reservations, including failed attempts, consume
slots conservatively. The keeper has one configured Pushover recipient; its
Ask ledger applies across credential rotation. Login notices use their own
existing policy.

A reservation allows at most five seconds for the ConfigMap write. Caps use
the rounded-up latest permitted dispatch time, so slow storage cannot shorten
the intervals. A late write, closed window or expired selected Ask prevents
dispatch after the reservation. Cap intervals include that five-second budget
and whole-second rounding.

The GitOps-precreated ConfigMap `dev-env-keeper-ask-notifications`, in the
keeper's namespace, is selected by `--ask-notify-state-configmap`; it must
differ from the login-notice ConfigMap.
`state.json` holds only Ask/group IDs, SHA-256 requester UID IDs and UTC
Unix-second attempt/retry timestamps, plus a schema version. It retains at
most 4096 Ask IDs and groups, with at most two attempts per group. Entries for
existing Asks remain deduplicated; an absent Ask's entry can be removed only
after a complete authoritative scan and the one-hour rate window. Size bounds
are 896 KiB for state and 960 KiB for state plus page records. Capacity, ledger/list read,
corruption and CAS failures stop sending; they never reset dedupe to empty.
Keep this ConfigMap across upgrades and flag changes.

The same ConfigMap holds `pages.json`, using DESIGN-031's bounded page schema
(at most 100 records / 256 KiB). Correlation is the representative Ask ID and
source/event kind is `ask-new`; title, body, URL and human identity are absent.
State, rate reservation and the `recorded` page commit in one CAS before the
send. Confirmed Pushover acceptance becomes `sent`; it proves neither phone
delivery nor reading. A transport timeout, 5xx, unconfirmed 2xx or lost outcome
is Unknown (`ambiguous`, or `recorded` when no outcome was saved), and is never
automatically repeated. Missing credential files or dispatch refused before
the sender call (late reservation/window boundary) mean definitely no request;
that failure may retry once after at least five minutes, while the selected
Ask is still open and all caps/windows allow. Other 4xx responses are `failed` without an
automatic retry. The existing pages lookup is not extended to this separate
ConfigMap by lane B.

`--ask-notify-window` defaults to empty: all hours within caps. When set,
it names the **allowed delivery window**, for example `09:00-21:00`, with
`--ask-notify-timezone=America/New_York` (the default timezone). Start is
inclusive, end exclusive; overnight windows are supported. Outside the window,
pending Asks wait until its next start, observed on the next 30-second poll.
IANA timezone rules handle DST. A skipped start waits for the first real
minute in the window; either occurrence of a repeated minute is eligible,
with caps still measured in elapsed UTC time. Agents cannot override it.
This source default does not settle Tom's quiet-hours ruling or permit go-live.

## Status codes

Validation failures, including a fractional `scope.expiresAt`, are 400.
`POST /v1/asks` with `type=grant` is 400. Withdraw of an Ask whose type is
`grant` is 400. A session cannot set labels. On this route `ask-origin` is
dropped and is not stored.
The same `requestId` with a different body is 409. Withdraw of an ask that is
not open is 409. An answer when the ask is answered or withdrawn is 409. The
same issuer and subject repeating the identical answer is 200, including when
only the display name differs. A different subject is 409. An answer when the
ask or its approval scope has expired is 410 (`expired`). A count bound is
429 (`limit_exceeded`). An unconfirmed count is 503. Another session's ask is
404.

## Who can answer a grant Ask

The broker trusts the stored answer. It cannot tell who wrote it. Whoever may update `asks/status` in `dev-agents` can store an answer the broker will apply. Whoever may create an Ask, patch its metadata, or delete an Ask can occupy the grant's Ask name, change the origin label, or remove the Ask the grant already named. By design the operator stores the portal's answer for a signed-in person, and the broker creates the grant Ask. The coordinator checks the live writers of create, metadata patch, delete, and `asks/status` before the flag is set.

## Broker verbs on Asks and TaskClaims

These are the Kubernetes verbs the broker's grant-Ask code uses. This repository does not grant them. ADR-021's build plan also lists `get` on TaskClaims, which this code does not use, and it does not list `patch` on Asks, which the state-label repair uses. Origin is set in the create, not by a patch.

| Verb | Resource | Function |
| --- | --- | --- |
| get | asks | `loadGrantAsk` reads the grant's Ask. `KubeStore.load` reads it again from `KubeStore.Create` and `KubeStore.Withdraw`. |
| list | asks | `KubeStore.countAtMost`, from `KubeStore.checkRoom` during `Create`. `KubeStore.recountMatching` lists when an open count is already at its bound. |
| watch | asks | `Broker.SetupWithManager` watches Asks and wakes grants through `enqueueGrantAsk`. A pending grant is woken for its answer. A terminal grant is woken so its Ask can be withdrawn. A failed owner list is retried only when that retry is due, with one grant list for the held Asks. At most 32 are held, oldest first. A newer Ask is dropped and found again when its grant is next reconciled. A session that has ended is not woken. |
| create | asks | `KubeStore.Create`, called from `ensureGrantAsk`. The origin label is on this create. |
| update | asks/status | `KubeStore.ensureOpen`, from `Create`, and `KubeStore.Withdraw`, from `withdrawBrokerDenial` and `withdrawOpenGrantAsk`. |
| patch | asks | `KubeStore.setStateLabel`, from `syncLabel`, which `Withdraw` and `recountMatching` call. This patches the state label only. A grant withdrawal checks the Ask UID and the full identity on each read, and the patch tests that UID and the resourceVersion. |
| list | taskclaims | `grantAskWorkPackage` lists the session's TaskClaims for the story link. |

## Operator RBAC

Before the flag is turned on in a cluster, the operator ServiceAccount needs
get, list, create and patch on `asks`, and update on `asks/status`, in
`dev-agents`. Patch updates the state label without rewriting the immutable
spec. This repository does not grant that. DESIGN-034 section 4 records the
same rights.

For the separate haynes-ops installation, give the keeper only `get,list` on
`coordination.dev-env.haynesops.com/asks` in `dev-agents` (no watch, patch,
status update or answer rights). Precreate its ConfigMap in the keeper's
namespace and grant `get,update` on that exact ConfigMap name only; no create,
delete or list. It needs neither a new credential nor general inbox rights.
Review its network policy for the API server and Pushover. No haynes-ops,
runtime or RBAC change is included in lane B.
