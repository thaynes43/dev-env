# 10: summoned sessions (dev-env-ops into v2)

**Status:** backlog
**Depends on:** 02 (lifecycle), 03 (Remote Control, so `remote` summoned sessions
register), 07 (the broker; `ops` sessions' grants go to Tom); spike S-16 (plan usage)
**Parallel with:** 04, 08

## Goal

Every automated caller that summons a Max-plan Claude Code session today keeps doing
so, through the v2 API (Tom, 2026-10-06: "we need to preserve the functionality").
The v1 executor `dev-env-ops` is retired, with its second monthly Max login.
DESIGN-001 3.7 (D-36), 6.1, 6.4, 6.10 (`ops`), 7.3 (V-14); research note
[R-01](../research/R-01-summoned-agents-audit.md), requirements V-01 to V-17.

## In this repo

- `CallerPolicy` CRD and its checks in `POST /v1/sessions`: kinds, name prefixes,
  profiles (never `full`), models with a distinct fallback, lanes, limits, priority,
  no grants (V-01, V-04, V-10).
- `task` kind with timeout and turn cap; `remote` kind registered under the caller's
  name (V-02).
- `name` and `idempotencyKey`: return the unfinished session for a repeated key;
  suffix the name when the earlier one finished (V-03).
- agentd's one-turn pre-flight on the primary model and the switch to the fallback
  (V-04).
- Plan credentials only: `503 plan credential unavailable` when none works; no
  metered key anywhere (V-05).
- `agent-run wait <name> --registered` and the verified link in status (V-06).
- `POST /v1/sessions/{id}/outcome` and `agent-run report working|done|failed|escalate
  [--note]`; `status.outcome`; `ops-event` log lines; the daily digest on Pushover at
  priority -1; the guaranteed outcome (`onUnreported`) (V-08).
- Lanes with the start order escalation, upgrade, remediation, curation, and a page
  for an escalation queued over 15 minutes (V-09).
- Watchdogs per lane, `session lost`, and the escalation exemption (V-11).
- The static token's mint date and days left in `GET /v1/auth`, pages at 30 and 7 days
  (V-12).
- No drain for summoned sessions (V-13).
- Quota priority from the keeper's usage read (S-16), with the error-count fallback
  (V-14).
- Retention: 1 day after `done`, 7 days after `failed` or `escalated` (V-15).
- `status.usage` and its metrics (V-16).
- The keeper mints the `haynes-ops-bot` token into `dev-env-ops-gh-token`.
- Tests: a caller cannot create a `full` session, a prefix outside its policy, or a
  model not in its policy; a repeated idempotency key returns the same session; an
  unreported `task` session is closed as its policy says.

## In haynes-ops (GitOps PRs)

- `CallerPolicy` for each caller: alert-responder, upgrade-shepherd, shepherd-triage,
  upgrade-health-gate, the curation CronJob. Curation's model is the current Opus
  full id (its `opus` alias is not allowed in v2).
- Profile `ops` in `dev-env-templates` (the ops bot token, the cigar-journal MCP token,
  the Proxmox read token) and its clusterwide ops egress tier (v1's shepherd-class
  list, no open web).
- The operator's ingress from the callers' pods in `upgrade-agent`.
- The `minted` field on the static token's 1Password item, and its ExternalSecret.
- **Callers move one at a time**, each a PR that points its script at the API
  (`agent-run` in its image, its ServiceAccount token, the same arguments):
  1. the curation CronJob (the largest plan draw, the least risk);
  2. `remediate.sh` (`rem-*`);
  3. `escalate.sh` (`esc-*`), from every caller;
  4. `work-order.sh` (`wo-<PR>`).

  After each move, the next real run of that caller completes on v2 before the next
  caller moves. `dev-env-ops` keeps serving callers that have not moved.
- When no caller is left on the ConfigMap queue: scale `dev-env-ops` to zero, keep
  its PVC 30 days, and stop renewing its Max login (it lapses; nothing is copied).
  Then delete the `upgrade-work-orders` ConfigMap writers' Role rules and the
  executor's app. These PRs restart nothing in the dev-env pod.

## Acceptance

- Every caller in R-01's table runs on v2 for a week with no lost order.
- An escalation page carries a link that opens the session on Tom's phone.
- A remediation that exits without reporting is closed as `escalate` and files an
  `esc-rem-…` session.
- A re-fired signature returns the running session, and after it finishes, a new one
  with a suffixed name; the first record is kept.
- With the 5-hour window past 80 %, the curation order waits and Tom's interactive
  sessions are unaffected.
- No metered spend on any summon path (the spend ConfigMaps stay at their July
  values).
- `dev-env-ops` is scaled to zero and its login is no longer renewed.
