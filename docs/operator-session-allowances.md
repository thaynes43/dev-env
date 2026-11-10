# Session allowance readings

[DESIGN-059](../.agents/sagas/distributed-dev-env/designs/059-durable-usage-collection.md)
first slice, story [OP509](https://work.haynesnetwork.com/work_packages/509).
The portal's Usage page gets each provider's weekly percent and reset from the
sessions that already run. No provider request, login, approval window, host
session or CLI version list is involved.

## Where the numbers come from

- **Codex.** Each heartbeat, agentd reads the last 1 MiB of the session's
  rollout file under `~/.codex/sessions` and takes the newest `token_count`
  record that carries `rate_limits`. It keeps each window's length, used
  percent and reset, and the record's time. The weekly window is the one of
  10080 minutes, wherever it appears. Plan type, credits and every other field
  are dropped.
- **Claude.** For a remote or local Claude session (never a headless task)
  whose pod follows the keeper's Claude projection, agentd's render step
  `claude-statusline` sets `statusLine` in `~/.claude/settings.json` to
  `'/usr/local/bin/claude-statusline-usage' --output
  '/dev/shm/run-1000/dev-env-usage/claude-statusline.json'` (the directory is
  `$XDG_RUNTIME_DIR/dev-env-usage`, mode 0700). An existing display command is
  kept: the helper runs it with `--display /bin/sh -c '<command>'`. The CLI
  runs the helper at each reply; the helper writes only the five-hour and
  seven-day windows. agentd reads that file every 5 seconds and at each
  heartbeat, after checking its owner and mode. When no value changed since
  the last reading, the earlier reading time is kept. A callback that brings no
  new numbers can still make a reading look newer than it is, by at most those
  5 seconds.

A pod that does not follow the keeper's projection for its provider takes no
reading at all, because none could be sent; for Claude the render step's skip
note says so. Otherwise agentd sends a reading only while the pod's credential
is the keeper's current projection. A reading it found and did not send shows
in the session's `status.agent.problems` as `allowance-reading: warn: reading
not sent: <reason>`:

| Reason | Meaning |
| --- | --- |
| `credential_not_current` | The pod's installed credential is not the projection's current generation, is near its expiry, or holds its own refresh token. |
| `shape_unrecognized` | The record or file does not have the fields in the documented shape. |
| `out_of_bounds` | A value breaks a bound below, such as a reset before the reading. |
| `file_unsafe` | The helper's file or directory is not this user's, is open to others, is a link, or is too large. |

## The operator flag

`--enable-session-allowances` is off by default. Off, the operator ignores the
heartbeat field and `GET /v1/provider-allowances` answers exactly as before.

On, for each heartbeat that carries a reading, the operator checks:

- the provider is the session's own agent, and a Claude reading comes from a
  remote or local session;
- each percent is 0 to 100, and each reset lies after the reading time and
  within one window length (plus one hour, for a reset rounded up to the hour);
- the reading time is not before the pod started, and not more than one minute
  ahead of the operator's clock (it is then stored at the operator's time);
- at most one reading per session per minute is taken, with five seconds of
  slack for an early heartbeat. Each operator replica counts on its own.

A valid reading goes into the ConfigMap
`dev-env-system/dev-env-session-allowances`, one entry per provider
(`claude.json`, `codex.json`) holding its last two readings, newest first, and
the name of the session that reported each. The write is an Update with the
read resourceVersion, at most once per provider per five minutes. A reset that
moved by more than two minutes, or a window that came or went, allows a write
after one minute. An older reading never replaces a newer one. The operator does not
create the ConfigMap and never writes the active collector's slot
`dev-env-provider-allowances`.

The route then serves, per provider, the newer of the slot's item and the
session reading. A slot item that the private trust rejects never hides a
session reading. The session reading is served whatever the slot's state:
suppressed, missing or unreadable. Without the private slot selection, the route
is mounted GET-only for the portal reader and serves the readings alone. A
session reading is a `passive_event` item with source version `session-v1`,
judged by one built-in rule: its account is never verified and no value is ever
current.

Only the operator's own readings get that rule. Anyone who can write the slot
or a Source document can also write a well-shaped `session-v1` item, and its
content cannot show who built it. So the operator labels every `session-v1`
item it did not merge itself rejected before the merge. It is shown like any
untrusted item, exactly as before session readings existed, and a real reading
for that provider replaces it whatever its time. The built-in rule never lifts
a rejected label, so the portal, which projects the operator's bytes again,
keeps it rejected. A PUT to the slot that carries a `session-v1` item, or an
item or source status with producer `dev-env-session-allowances`, is refused.

The flag needs the API (`--api-bind-address`), the ConfigMap precreated with
metadata only, and `get` and `update` on that one name in the operator's Role.

## See a reading

```sh
kubectl -n dev-env-system get configmap dev-env-session-allowances -o yaml
kubectl -n dev-env-system get configmap dev-env-session-allowances \
  -o jsonpath='{.data.codex\.json}'
```

An entry looks like this (synthetic values):

```json
{"schema":"dev-env.session-allowances.v1","provider":"codex","readings":[
 {"session":"dev-env-1110-000930","source":"codex-rollout",
  "readAt":"2026-11-10T00:21:04.512Z","reportedAt":"2026-11-10T00:22:00Z",
  "windows":[{"minutes":10080,"usedPercent":"65","resetsAt":"2026-11-14T11:48:50Z"}]}]}
```

`readAt` is when the CLI received the numbers. `reportedAt` is when the
operator stored them. A running session that just replied should show a
`readAt` a few minutes old at most.

## No reading

No entry, or an old one, means one of these:

- No Codex task or session made a model call, and no Claude remote or local
  session got a reply, since the pod started. Headless Claude tasks never give
  a reading.
- Nothing has run since the last reading. It ages, with its time shown. Older
  than 30 minutes, the portal greys the percent and says how long ago it was
  reported, in plain words (for example "reported 3 hours ago"). A reading of
  exactly 30 minutes stays plain. One a few seconds older says "reported 31
  minutes ago". After its reset passes, the percent is not shown: that window
  is over, and the page says when the last reading was.
- A Codex remote host session wrote no rollout. That is not verified yet;
  Codex tasks do write one.
- agentd refused it: see `status.agent.problems` above.
- The flag is off, the ConfigMap is missing, or the operator's Role lacks
  `update` on it.

## The portal

A portal with the built-in rule shows a session reading's glance as the
number first. While the reading is 30 minutes old or newer and its reset is
still ahead, the headline is "<N>% of the week used", then "resets <weekday>
<day> <month> <time> UTC", then "reported <age> ago" ("reported just now"
under a minute). The percent is normal text. A reading of exactly 30 minutes
stays plain. Older than 30 minutes, that percent is greyed and the age line
says so, for example "reported 3 hours ago". One a few seconds older says
"reported 31 minutes ago". A window other than 7 days is a smaller line in
the same form, such as "12% of the 5 hours used". If that shorter window's
reset has passed while the week is still current, the smaller line names it,
for example "5 hours: This window is over: the reported reset has passed, so
its percent is not shown." Details name each window. One line under the headline
says "Reported by a running session; not checked with the provider." The
trust lines stay in the tile's Details and in the account section: received
time, "Passive event", "Account not verified", and the reported reset. The
words Unknown and No current percent are not on a tile that shows a percent.
Once the reported reset has passed, the percent is not shown. The line says
"This window is over: the reported reset has passed, so its percent is not
shown." and "Last reading was <time>." With no reading, Claude and Codex say
"Nothing has reported yet. A reading appears when a new Codex task, or a
remote Claude session, runs on a current image and the operator is collecting
those readings." Cursor says "Cursor's plan allowance can only be seen on
Cursor's own page." and links to https://cursor.com/dashboard. Grok stays not
connected. A last-reported line from any other source keeps the older
"Last reported, not current" layout.

An older portal, which lacks the rule, treats the `session-v1` item like any
item no private context trusts: the provider's tile shows Unknown with no
value, and the slot's older last-reported value for that provider is not
shown. Nothing breaks. Roll the portal first, then the operator with the
flag, then the agent image. An older operator ignores the heartbeat field,
so agent images can roll at any time.

To turn it off, remove the flag. The ConfigMap keeps its last readings and is
no longer read or written.

## Task usage totals

These counters are separate from the allowance readings above. A finished
task Session is deleted after clean-up, and the tokens and cost in its
status go with it (ADR-020 C-54). The operator's leader adds that Session
to the counters below once, then writes the annotation
`dev-env.haynesops.com/task-usage-counted`. Prometheus keeps the totals
after the Session is gone. The design, including the annotation value and
the known limits, is DESIGN-059's section "Totals that survive clean-up".

| Metric | Labels | Unit |
| --- | --- | --- |
| `dev_env_task_usage_tokens_total` | `agent`, `model`, `repository`, `cost_kind`, `token` (`input`, `output`, `cache_read`, `cache_write`) | tokens |
| `dev_env_task_usage_cost_usd_total` | `agent`, `model`, `repository`, `cost_kind` | USD |
| `dev_env_task_usage_sessions_total` | `agent`, `model` (the requested model) | sessions |
| `dev_env_task_usage_seconds_total` | `agent`, `model` (the requested model) | seconds |

Cursor adds sessions and seconds only. The series are on the operator's
existing metrics endpoint. A replica that is not the leader stays at zero.

Cost for one week:

```promql
sum by (agent, model, repository) (increase(dev_env_task_usage_cost_usd_total[7d]))
```

`increase` keeps the week across an operator restart. A new series stays
at 0 for five minutes, then takes its first add. A Session that already
has the annotation is not added again. If no scrape lands during the five
minutes after a series is created in a process, `increase` does not see
that series' first add; the raw counter still holds it. Totals appear up
to five minutes after a restart, or after a new label is first used. A
Session deleted before its series have settled is not counted.
