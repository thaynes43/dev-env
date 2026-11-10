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
- Nothing has run since the last reading. It ages, with its time shown. After
  its reset passes, the portal keeps the value and says the reported reset
  time has passed. The full staleness rule of DESIGN-059 section 8 (a greyed
  value after 30 minutes, no value after the reset) is a later portal story.
- A Codex remote host session wrote no rollout. That is not verified yet;
  Codex tasks do write one.
- agentd refused it: see `status.agent.problems` above.
- The flag is off, the ConfigMap is missing, or the operator's Role lacks
  `update` on it.

## The portal

A portal with the built-in rule shows the reading in **Last reported, not
current**: the percent, the window, the reported reset, "Received" with the
reading time, "Passive event" and "Account not verified". An older portal, which
lacks the rule, treats the `session-v1` item like any item no private context
trusts: the provider's tile shows Unknown with no value, and the slot's older
last-reported value for that provider is not shown. Nothing breaks. Roll the
portal first, then the operator with the flag, then the agent image. An older
operator ignores the heartbeat field, so agent images can roll at any time.

To turn it off, remove the flag. The ConfigMap keeps its last readings and is
no longer read or written.
