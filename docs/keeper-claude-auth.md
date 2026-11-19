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
| `--claude-early-probe-interval` | `1m` | Sets the fixed cadence for the keeper's read-only profile GET. |
| `--claude-early-min-interval` | `5m` | Sets the minimum time between early attempts across generations and restarts. It also bounds the time to verify that a replacement is usable. |

A profile 401 can start one attempt for that login and generation. The keeper
rechecks the journal and its Lease after the GET, then uses the same fenced
refresh dispatch as scheduled rotation. Profile success confirms profile
access. A 403, timeout, 429, server error, malformed profile, or account or
organization mismatch records a fixed diagnostic. The consumer history and
keeper control socket provide no early-refresh trigger.

The private journal's `early-recovery.json` v1 key holds the attempt, cooldown,
probe observations and early transition evidence. A proved-unsent rollback
returns the credential to Ready and keeps the early latch consumed. A saved
replacement with failed publication retries publication while its access is
valid. An ambiguous dispatch or refused refresh follows the keeper's existing
`NeedsLogin` path. Ordinary scheduling can rotate the saved refresh credential
after an unpublished replacement expires.

A published replacement closes the incident when the keeper verifies it at or
after the cooldown. Another 401 on a successor before that verification holds
further early refreshes, including after a restart. A separately authorized
adoption clears the hold and retains the inter-attempt cooldown. An operational
decision following attribution is required for any other hold release.

## Incident evidence and rollback

A failed early attempt, a missed recovery deadline, or a flapping hold records
one page entry per unresolved incident in `early-recovery.json`. Read only the
approved incident fields through the keeper's authorized journal boundary. The
entry carries a UTC Unix-nanosecond timestamp, a generation, a fixed numeric
failure class, and delivery code `1` for undelivered. Its enclosing incident
carries the random reference as two integers. The existing login notifier
handles `NeedsLogin` and lapse notices. The slice 6 record gives Ready-state
incidents a durable delivery status while separate notice activations stay held.

The probe classes, in order from `1` to `9`, are healthy, 401, forbidden, rate
limited, server error, timeout, malformed profile, account or organization
mismatch, and unavailable. The page failure classes, in order from `1` to `8`,
are proved unsent, possibly spent, refresh status rejected, refresh response
unreadable, invalid refresh response, changed account, recovery deadline, and
flapping.

Disabling the feature stops probes and slice 6 journal updates. A binary
rollback leaves `early-recovery.json` in place because older keepers patch only
the journal keys they own. Existing `refresh-provenance.json` v1 rows keep their
schema and normal retention. Both rollbacks use the ordinary journal handling
for in-flight intent, rollback or tombstoning.

Source qualification uses fake providers and clocks. Installing this code and
enabling profile probes require separate authorization. [DESIGN-069](../.agents/sagas/distributed-dev-env/designs/069-shared-claude-token-invalidation.md#54-keeper-only-early-revocation-recovery)
