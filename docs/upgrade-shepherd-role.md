# Upgrade-shepherd role

Version `upgrade-shepherd-role/v1`. This file is a repo-pinned source contract.
It is supplied through the existing shared-rule and work-order mechanisms.
It is not a new registry, and it is not already installed in every provider.
A role name does not change a security profile, credential, or egress grant.

The digest of the role core is SHA-256 of the UTF-8 bytes between the markers
below. An envelope records that digest. This file does not embed the hex.

<!-- upgrade-shepherd-role-core:start -->
Upgrade-shepherd role core, version upgrade-shepherd-role/v1.
This role is not a security profile and does not grant access, credentials, or egress.
A candidate means supplied unverified evidence meets this source contract. It is not alert deferral, alert suppression, or operational authorization.
Match an alert only by exact service, labels, resource, severity, and symptom. Substring, regex, and cluster wildcard matches do not qualify.
Unknown baselines and unknown model provenance stay Unknown.
A stale, failed, stopped, or lost owner does not transfer custody.
Replay of the same attempt and canonical envelope preserves the original expiry and budget. A changed payload conflicts and is not merged.
Cleanup failure stays recorded and is not success.
A source-only dev-env assignment marks runtime health and reconciliation NotApplicable.
<!-- upgrade-shepherd-role-core:end -->

## What the role does

The shepherd inspects the recorded upgrade scope and dependency notes, records
pre-existing degradation, and batches only changes inside the original impact
and time bounds. It follows the repository's review and CI rules. It performs
only reconciliation and postchecks that the recorded action scope already
names. It rolls back only inside the recorded rollback scope. It keeps the
evidence, the cleanup result, and a bounded failure report.

The responder correlates one alert with that record before treating the alert
as maintenance. It keeps raw alerts. It investigates unexpected impact. It
does not assume maintenance caused every outage. Pre-existing degradation
stays visible on its own.

Workflow role and security profile are different. Launch still uses the
existing profile preflight, project snapshot, provider capability, and parent
profile ceiling. Native thread creation does not select a host profile.

## Envelope

`agent-run maintenance check` reads one JSON document of at most 64KiB. The
document holds an `envelope` and a `snapshot`. The envelope is the operation
record. The snapshot is the unverified observation being correlated. Neither
one is a database or an authorization policy.

Required envelope facts:

- contract version `upgrade-shepherd-envelope/v1`
- operation id, attempt id, and optional predecessor attempt id
- canonical work-package URL
- role version and content digest
- repository identity, desired revisions, and adapter name, version, digest, and runtime applicability
- session namespace, name, and UID
- claim reference and owner UID
- executor identity
- custody handoff evidence, which may be empty
- requested provider, model, and effort, plus actual provider, model, and state
- allowed actions and authorization evidence URLs
- target services and dependencies
- activity UID, start, and expiry
- task deadline and impact bounds
- authorization limit expiry
- one baseline per affected service
- exact expected alert predicates
- recorded owner observation, heartbeat, revision, and attempt
- rollback trigger, owner, and scope
- terminal outcome fields, empty until the operation is terminal
- evidence URLs
- state `planned`, `active`, `completed`, `rolled_back`, `failed`, or `held`

The canonical attempt digest is SHA-256 of the parsed envelope, not of the
snapshot and not of the raw input bytes. Times are normalized to UTC
RFC3339Nano. The same attempt id with the same digest is a replay. The same
attempt id with a different digest conflicts and is not merged. A later
attempt uses a new attempt id and names its predecessor. This checker has no
store, so it cannot prove a later attempt kept the original expiry. It will
not extend an expiry by itself.

An `authorized` field is rejected. Authority is an evidence URL the runtime
boundary can inspect later. Offline input never becomes a grant.

## Match, expiry, and custody

A candidate requires every one of these: state `active`; the same operation,
attempt, desired revision, and service; a requested action inside the recorded
scope; one owner UID bound to the session, claim, and live snapshot; a fresh
live observation; an unexpired activity, task deadline, and authorization
window; an exact alert predicate; impact inside the recorded bounds; a usable
baseline; and no unexplained worsening.

Exact means the same service, label map, resource, severity, and symptom.
`cluster`, `*`, and regular expressions do not match.

Missing, unknown, stale, or unavailable evidence cannot become a candidate.
A failed, stopped, expired, drifted, or conflicting record is `respond`.
Pre-existing degradation remains a visible condition even when the decision
is `candidate`. A stale or lost owner does not transfer custody. Handoff
evidence is recorded and ignored for the decision.

Activity expiry cannot be later than the task deadline, the authorization
limit, or the existing 8h Activity maximum. The existing 45m default is not
written in when an expiry is present. The existing 2h cap applies to Activity
scope `cluster`. This contract rejects cluster-wide matchers, so it does not
silently shorten a named-service window to 2h. That reading is a proposal for
review, not a change to Activity.

Terminal states are monotonic in the record: a same-attempt edit does not
overwrite them, because it conflicts. Completion or rollback without
`activityCleanup: succeeded` stays a cleanup failure and is not success.

Actual model provenance is copied as supplied. Empty actual provider, model,
or state is reported as `Unknown`. It is not filled from the request.

## Adapters

Claude and Codex use the same role core. The existing renderer keeps that
core in the Claude rules and appends it to the Codex header. Provider launch
syntax and provenance may differ. The correlation decision does not.

`runtime: applicable` is the platform adapter shape. A haynes-ops fixture
carries synthetic Flux, Gatus, and Renovate observations. Those observations
are unverified and are not credentials. An unknown platform field blocks a
candidate.

`runtime: not-applicable` is the source-only adapter shape. A dev-env fixture
sets runtime health and reconciliation to `NotApplicable` and does not carry
Flux, Gatus, or Renovate fields. It may carry source CI and native-review
tokens. Component procedures stay in the repository runbooks at a pinned
revision. They are not copied into this role.

## Proposed numeric defaults

These are offline validation bounds for review. They are not agreed
production SLOs and they do not change deployed heartbeats.

| Bound | Value | Standing |
| --- | --- | --- |
| Document size | 64KiB | proposed |
| Services, predicates, evidence | 32 | proposed |
| Owner staleness | declared heartbeat, at most 120s | proposed |
| Clock tolerance for observations | 5s | proposed |
| Activity default | 45m | existing, not rewritten |
| Activity maximum | 8h | existing |
| Cluster-scope Activity maximum | 2h | existing; cluster matchers are rejected here |
| Identifier | 128 characters | proposed |
| URL | 512 characters | proposed |

## Check command

```text
agent-run maintenance check --input <file|-> [--now <RFC3339>]
```

Exit 0 means the checker produced JSON. Read `decision`: `candidate`,
`respond`, or `insufficient-evidence`. Exit 0 does not authorize anything.
Exit 2 means the input is invalid and the JSON names the fields. The command
does not read a token, call the API, launch a session, or suppress an alert.
`--now` injects the clock. Without it, the process wall clock is used.

Every result says the input is unverified and that a candidate is not
operational deferral. `retry` is false. Denied or uncertain launch is not
retried.
