# Keeper-owned Claude authentication

The elected keeper owns the shared Claude refresh credential in its private
journal. It saves replacements before publishing the access-only projection.
Session pods follow that projection as generations advance. Consumer rejection
history supports incident investigation.

## Early recovery configuration

DESIGN-069 slice 6 adds an opt-in profile probe for the keeper's current,
unexpired Ready token. The settings belong to `dev-env-keeper`.

| Flag | Default | Effect |
| --- | --- | --- |
| `--enable-claude-early-recovery` | `false` | Enables early recovery when `--enable-claude-auth` and leader election are active. |
| `--claude-early-probe-interval` | `1m` | Sets the cadence for the keeper's read-only profile GET. Minimum `30s`. |
| `--claude-early-min-interval` | `5m` | Sets the cooldown across generations and restarts, and the time allowed to verify a replacement. Minimum `2m` and at least twice the probe interval. |
| `--claude-early-max-attempts-per-day` | `2` | Caps early attempts per login incarnation in a rolling 24 hours. Accepts 1 to 32. Tom's decision on the default is recorded as pending under Q-938-01 in DESIGN-069. |

A profile 401 can start one attempt for that login and generation. The keeper
rechecks the journal and its Lease after the GET, then uses the same fenced
refresh dispatch as scheduled rotation. Profile success confirms profile
access. The keeper skips probes within the request budget of access expiry.
A 403, timeout, 429, server error, malformed profile, or account or
organization mismatch records a fixed diagnostic. The consumer history and
keeper control socket provide no early-refresh trigger.

The private journal's `early-recovery.json` v1 key holds the attempt, cooldown,
probe observations and early transition evidence. The keeper saves probe
transitions, the first 401 bracket, and at most one heartbeat per 15 minutes
between transitions. It keeps the latest healthy probe in memory. After a
restart, the last healthy probe before a 401 comes from the last journaled
observation. At the default cadence that can be up to 15 minutes older than
the last completed healthy probe in steady state. A generation with a
consumed latch or a hold gets at most one GET every 15 minutes. A tombstoned
login gets no profile GET.

A proved-unsent rollback returns the credential to Ready and keeps the early
latch consumed. A saved
replacement with failed publication retries publication while its access is
valid. An ambiguous dispatch or refused refresh follows the keeper's existing
`NeedsLogin` path. Ordinary scheduling can rotate the saved refresh credential
after an unpublished replacement expires.

A published replacement closes the incident when the keeper verifies it at or
after the cooldown. A published scheduled rotation verified healthy also closes
an incident that has no hold. Another 401 on a successor before verification
holds further early refreshes. A 401 that would exceed the rolling cap also
sets a hold and records a page. The cap counts journaled latches, including
proved-unsent attempts. Holds and counts survive restarts. A separately
authorized adoption clears them and retains the cooldown. If the key is
undecodable, adoption replaces it with fresh v1 state and waits the full
configured cooldown before another early attempt.

## Incident evidence and rollback

A failed early attempt, a missed recovery deadline, or a hold records one
page entry per unresolved incident in `early-recovery.json`. Only someone with
Secret read access on the private journal can see the page record. The entry
carries a UTC Unix-nanosecond timestamp, a generation, a fixed numeric
failure class, and delivery code `1` for undelivered. Its enclosing incident
carries the random reference as two integers. The existing login notifier
handles `NeedsLogin` and lapse notices. Ready-state incidents are visible in
the metrics below.

The probe classes, in order from `1` to `9`, are healthy, 401, forbidden, rate
limited, server error, timeout, malformed profile, account or organization
mismatch, and unavailable. The page failure classes, in order from `1` to `9`,
are proved unsent, possibly spent, refresh status rejected, refresh response
unreadable, invalid refresh response, changed account, recovery deadline, and
flapping, and attempt limit.

### Prometheus

Use the keeper scrape target in Prometheus or Grafana. The metrics are served
at `/metrics` on the existing `--metrics-bind-address` (default `:8080`).
The early-recovery metrics are registered when the feature is enabled.

| Metric | What to look for |
| --- | --- |
| `dev_env_keeper_claude_early_recovery_incident_open` | A value of 1 means an incident awaits resolution. |
| `dev_env_keeper_claude_early_recovery_flapping_hold` | A value of 1 means early attempts are held after a repeat rejection or the rolling cap. |
| `dev_env_keeper_claude_early_recovery_journal_unavailable` | A value of 1 means the keeper cannot read or decode the journal. |
| `dev_env_keeper_claude_early_recovery_outcomes_total` | Incident events by fixed `outcome`, including `replacement-saved`, `resolved`, `journal-repaired` and the page classes. |
| `dev_env_keeper_claude_probe_results_total` | Completed probes by fixed `result`. A 401 increments `unauthorized`. |
| `dev_env_keeper_claude_refresh_outcomes_total` | Refresh transport and save outcomes by fixed `outcome` and `trigger` (`scheduled` or `early`). Success means the replacement was saved. |

The gauges reflect the active keeper's latest journal view. The counters are
per process. For example, query
`sum by (outcome) (increase(dev_env_keeper_claude_early_recovery_outcomes_total[1h]))`
to see incident events in the past hour. These metrics have no session,
generation or account labels.

Disabling the feature stops probes and slice 6 journal updates. A binary
rollback leaves `early-recovery.json` in place because older keepers patch only
the journal keys they own. Existing `refresh-provenance.json` v1 rows keep their
schema and normal retention. Both rollbacks use the ordinary journal handling
for in-flight intent, rollback or tombstoning.

Source qualification uses fake providers and clocks. Installing this code and
enabling profile probes require separate authorization. [DESIGN-069](../.agents/sagas/distributed-dev-env/designs/069-shared-claude-token-invalidation.md#54-keeper-only-early-revocation-recovery)
