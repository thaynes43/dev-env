# DESIGN-032: Provider-neutral reminders

Date: 2026-10-28. OpenProject Story #206 ("Provider-neutral reminders with Google
Workspace calendar and due Pushover delivery"), Epic #95. Tom approved overnight
Claude development at 2026-10-28T03:21:36Z for this bounded MVP only. That approval
is not security approval: no credential, grant, public endpoint, RBAC or egress
policy is created or changed by this source.

## State: source and tests only

| Layer | State |
|---|---|
| Source (CRD, engine, scheduler, API, CLI) | written and unit/envtest tested in this repository |
| Calendar coverage | the **fake** adapter only; there is no real adapter |
| Pushover coverage | a fake HTTP server only; no real Pushover request was made |
| Installed | no. Every part is default off, and the CRD, the Roles and the flags are not in haynes-ops |
| Usable / accepted | no. No real reminder exists, no page was sent, no calendar entry or invite was made |

Nothing here claims real scheduling or real delivery.

## What it is

A minimal, platform-neutral way for any agent (Claude, Codex or another harness) to
create, list, show, edit and cancel reminders, one-shot or recurring, that survive a
process restart. A reminder wants two effects:

1. a calendar entry in the haynesnetwork.com Google Workspace calendar;
2. a Pushover notification when it is due.

The existing Mullvad reminder lives in ChatGPT, outside this repository, and is left
unchanged. Migrating it needs a separate request.

## Integration inventory (reference names only, no values)

| Need | What exists | Status |
|---|---|---|
| Pushover delivery | The keeper's dedicated "dev-env" Pushover application: `internal/keeper/notifier.go` (`NoticeSender`, `PushoverSender`), credential files under `/etc/dev-env-keeper/pushover` filled by an ExternalSecret in `dev-env-system`, egress to `api.pushover.net` in the keeper network policy | **Reused as is.** No new credential |
| Other Pushover senders | Alertmanager receivers `pushover` and `pushover-daytime`, Gatus, scribe-notes (separate applications and tokens) | Not reused |
| Calendar | **dev-env has NO haynesnetwork.com Google Calendar integration: no backend credential, no calendar ID and no egress to Google APIs.** haynesnetwork.com Workspace is used for mail submission only. The sigoalumni app has its own Workspace impersonation credential and events calendar; Home Assistant holds calendars of a different Google account | **Gap** |
| Human-connected Google Calendar connector in a client | Exists in the client, but it is a person's connector and not a reusable backend credential | Neither used nor inspected, and not to be copied or migrated |

None of this says there is no Google account. It says the platform has no backend route
to the haynesnetwork.com calendar, so the MVP cannot create real calendar entries.

## Exact integration requirements for the real calendar adapter (reference names)

Each one needs its own action-specific approval from Tom. None is part of this change.

1. **Identity.** One of: a Workspace service account with domain-wide delegation
   limited to the Calendar events scope, impersonating one calendar owner; or an OAuth
   client with a refresh token for that owner. A refresh token needs a single owner, as
   with the Claude and Codex logins.
2. **Calendar.** The calendar ID of the haynesnetwork.com calendar, a reference name
   such as `calendar:haynesnetwork` in the operator flag `--reminder-calendar-refs`, and
   the real ID in the keeper's configuration, never in a request.
3. **Egress.** HTTPS to `oauth2.googleapis.com` and `www.googleapis.com` (Calendar API)
   from the keeper pod, as a Cilium policy in haynes-ops.
4. **Credential mount.** An ExternalSecret in `dev-env-system`, mounted read-only into
   the keeper's `app` container only (as `dev-env-keeper-pushover` is), with the
   reference name `dev-env-keeper-calendar`. The mount is optional, so a missing field
   cannot stop the credential keeper.
5. **RBAC and CRD.** The `Reminder` CRD copy in haynes-ops, a Role for the keeper
   (get and list on `reminders`, update on `reminders/status`; it uses an uncached
   client, so no watch), and a Role for the operator (get, list, create and update on
   `reminders`).
6. **Adapter.** A `CalendarAdapter` implementation (create or replace an event by the
   deterministic ID, delete it) in the keeper. Until then the scheduler records the
   calendar target as `skipped`, reason `calendar: unconfigured`.

## Design

### Data contract

`Reminder` is a namespaced CRD in `dev-env.haynesops.com/v1alpha1` with a status
subresource. Bounds on every string, list and number are in the schema and tested in
`api/v1alpha1/reminder_validation_test.go`.

- `spec`: `title` (1 to 120), `body` (up to 1024 bytes, Pushover's message limit), `owner` (from the token, immutable),
  `due` (local `YYYY-MM-DDTHH:MM`) with `timeZone` (IANA), optional `recurrence`
  (`freq` daily, weekly or monthly; `byDay` or `byMonthDay`; `until` or `count`),
  `targets` by reference, `cancelled`.
- `status`: `state` (pending, sent, failed, cancelled), `nextDue`, `occurrence` (the
  1-based index in the series), `planHash`, `deliveries` (one per Pushover target for
  the current occurrence: claimed, retry, sent, failed, ambiguous, skipped, with
  attempts and a reason), `calendar` (the sync state per calendar target, pruned to the CRD's 8 on every pass
  whatever the adapter's state), a bounded
  attempt log (20) and a message.
- Targets are references from a fixed allowlist: `pushover:default`, and
  `calendar:<ref>` for the references the operator was given. An address, a URL or a
  key is not a reference, and is refused by the CRD pattern and by the API.

### Idempotency

- **Create.** An optional `key` makes the name `rem-` plus a hash of owner and key. A
  retried create returns the same reminder; the same key with different content is a
  409.
- **Calendar.** The whole series is one event with an RRULE. Its ID is a hash of the
  namespace and name, so a replayed upsert finds its own entry. The sync state keeps
  the hash of the fields written, so nothing is rewritten without a change.
- **Pushover.** Pushover has no idempotency key. Each target's delivery of an occurrence
  is **claimed with a compare-and-set status write before any request is made**. The
  result is written after. A claim found on a later pass was never resolved (the
  process died, or the result write failed), so it becomes `ambiguous` and is **never
  sent again automatically**. That is at-most-once, chosen on purpose over
  at-least-once: a missed page is visible in `show`, a duplicate page is noise.
- A failure that proves no request reached Pushover (missing credentials, a 429 or a
  5xx answer) is retried at most 3 times with a 1 and 5 minute backoff, then `failed`.
  A 4xx answer is a refusal and is `failed` at once. A transport error or timeout is
  `ambiguous`, because the request may have been accepted.

### DST semantics (explicit, tested)

`due` is a local wall time read in `timeZone`; a recurrence keeps the wall clock.

- **Gap** (a time the zone skips, such as 02:30 on the spring-forward day): the time is
  read with the offset in force before the gap, so it fires one gap-length later, 03:30.
  This is RFC 5545's rule and the calendar's.
- **Fold** (a time the zone repeats): the first of the two.
- **Month end:** a monthly day a month lacks is clamped to that month's last day (31
  becomes 28 in February). The calendar RRULE follows RFC 5545 and skips such months, so
  for a day above 28 the calendar entry shows fewer occurrences than the scheduler
  fires. The notification is the authority.
- `until` is inclusive; `count` includes the first occurrence.
- The tests cover America/New_York 2026 (March 8 gap, November 1 fold), a daily series
  across both, month-end clamping including a leap year, weekly `byDay`, `until` and
  `count`.

### Edit and cancel against the due time

Every write carries the resourceVersion it read.

- A fire claims its occurrence with a status write. If an edit or a cancel changed the
  object after the scheduler read it, the claim conflicts, nothing is sent, and the next
  pass reads the new state: a cancel wins and nothing fires; an edit replans from the new
  schedule.
- **An edit always yields the new schedule.** The status keeps the hash of the schedule
  fields (`planHash`) its `nextDue` was planned from. An occurrence that is already
  claimed, backing off or finished when the edit lands resolves **only for itself**:
  - a claim never resolved becomes `ambiguous`, a request that was made keeps its
    result, and a sent or ambiguous delivery is never sent again;
  - a retry waiting out its backoff is dropped as `skipped` ("superseded by an edit"),
    because nothing was sent and the old time is no longer wanted;
  - when the occurrence closes, the next one is planned from the **current** spec, not
    from the closed occurrence's schedule, so a one-shot is not closed by an occurrence
    of a schedule that no longer exists. A new one-shot fires however late; a new series
    starts at the first occurrence after the closed one (within the miss grace).
  The tests cover an edit during the claim, during the backoff and after the send.
- If the claim wins, the cancel or edit then retries against the new state. A cancel
  that arrives while a delivery is claimed succeeds but says that a request in flight
  cannot be recalled (`warning`). A cancel of a one-shot that has already been sent is a
  409.
- The result of a request already made is never lost to an edit or a cancel: the
  scheduler re-reads and writes it again.
- Only a pending reminder can be edited. A cancel removes the calendar entry before the
  state becomes `cancelled`, retrying while the delete fails.
- A recurring occurrence later than the miss grace (6 hours) is recorded as `missed` and
  skipped; a one-shot always fires, however late.

### Placement and durability

The scheduler is a leader-only runnable (`NeedLeaderElection` is true) in the **keeper**,
behind the default-off flag `--enable-reminders`. The keeper is the one pod that already
holds the Pushover credential, its egress and a Lease. All state is in the Reminder
objects, so a restart or a new leader reads it back and repeats nothing. If the Pushover
credential files are missing the scheduler does not start and says so, as the notifier
does. The scheduler is not given a calendar adapter, so calendar targets are skipped
honestly.

The operator serves `/v1/reminders` behind the default-off flag `--enable-reminders-api`.

### API and CLI

| Route | Who |
|---|---|
| `POST /v1/reminders` | any caller; the owner is the caller's token, never the body |
| `GET /v1/reminders` | the caller's own; `all=true` for a human or a coordinator |
| `GET /v1/reminders/{name}` | the owner, a human or a coordinator; anyone else gets 404 |
| `PATCH /v1/reminders/{name}` | the owner, a human or a coordinator |
| `POST /v1/reminders/{name}/cancel` | the owner, a human or a coordinator; idempotent |

The body is held to Pushover's 1024-byte message limit by the CRD and the API, so the
keeper never has to cut it; if it must (an object made by other means), it cuts on a
rune boundary and ends with "…", for the body (1024 bytes) and the title (250).

A caller may hold 100 live reminders. A finished one does not count; finished
reminders are not yet garbage collected (a follow-up).

`agent-run remind create|list|show|edit|cancel`, standard library only, with `-o json`.
`create` takes `--due`, `--tz`, `--body`, `--repeat daily|weekly[:MO,TH]|monthly[:15]`,
`--count` or `--until`, `--targets` and `--key`.

## Gaps and follow-ups

- The real calendar adapter and its approvals (above).
- haynes-ops: the CRD copy, the Roles, the flags, the calendar egress and credential.
- Retention of finished reminders.
- A way to re-send an `ambiguous` delivery on a person's decision.
- Pushover receipts: standard priorities return none, so `receipt` stays empty.
- Metrics and an alert for failed and ambiguous deliveries.
- No test page, calendar entry or invite was made for this change.

## Approval list

| Needs approval before live use | Why |
|---|---|
| haynes-ops CRD copy and the keeper and operator Roles | new RBAC |
| Enabling `--enable-reminders` and `--enable-reminders-api` | new runtime behaviour |
| The calendar identity, calendar ID, mount and Google egress | new credential and egress |
| A first real reminder and a first real page | a real send to Tom's phone |
| Migrating the Mullvad reminder | a separate request |
