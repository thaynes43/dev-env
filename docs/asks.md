# Asks and approvals

Source only. The operator serves asks only with `--enable-asks` (default off).
Nothing here is installed, and turning the flag on in a cluster still needs the
Ask CRD and RBAC in haynes-ops. An ask is not an approval until a portal reader
answers it, and an expired approval is not an approval.

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
| POST | `/v1/asks/{id}/withdraw` | owning session, while open | 200 |
| GET | `/v1/portal/asks?state=open` or `all` | portal reader | 200 |
| GET | `/v1/portal/asks/{id}` | portal reader, any state | 200 |
| POST | `/v1/portal/asks/{id}/answer` | portal reader, while open and unexpired | 200 |

`state=open` is the effective state: an open ask whose `expiresAt` (or, for an
approval, whose `scope.expiresAt`) has passed is not listed as open. Pages use
`limit` (default 20, maximum 100) and an opaque `cursor`. Order is `createdAt`
descending, then `id` descending.

Session responses include `answeredBy.displayName` only (empty becomes
`Portal user`). Portal responses include issuer, subject and display name.
Whether an approval is still in force (`approved` and now before
`scope.expiresAt`) is computed by the client. It is not a stored field.

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

Creates count, then create (the same pattern as coordination record room
checks). The bounds are 16 stored-open asks per requester, 512 asks per
requester, and 4096 asks in the namespace. A lapsed ask still counts as open
until its stored state changes; the expiry sweeper is a later change. At or
above 80 percent of a bound the operator logs and increments
`dev_env_asks_near_limit_total`. A retry of the same request id is not a new
create and is not refused for room.

## Status codes

Validation failures, including a fractional `scope.expiresAt`, are 400.
The same `requestId` with a different body is 409. Withdraw of an ask that is
not open is 409. An answer when the ask is answered or withdrawn is 409. The
same issuer and subject repeating the identical answer is 200. An answer when
the ask or its approval scope has expired is 410 (`expired`). A count bound is
429. Another session's ask is 404.

## Operator RBAC

Before the flag is turned on in a cluster, the operator ServiceAccount needs
`get`, `list`, `create`, `update` and `patch` on `asks` (the state label is a
merge patch, so the immutable spec is not rewritten), and `get` and `update`
on `asks/status`, in `dev-agents`. This repository does not grant that.
