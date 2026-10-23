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

The canonical attempt digest is SHA-256 of the whole parsed immutable
envelope, including its observation and heartbeat. It is not a digest of the
snapshot alone and not of the raw input bytes. Times are normalized to UTC
RFC3339Nano. Refreshing that envelope, including a heartbeat refresh, changes
the digest and conflicts with a priorAttempt. There is no cross-heartbeat
replay promise. Only an independent snapshot change that still passes the
existing consistency checks can leave the digest unchanged. The same attempt
id with the same digest is a replay. The same attempt id with a different
digest conflicts and is not merged. A later attempt uses a new attempt id and
names its predecessor. This checker has no store, so it cannot prove a later
attempt kept the original expiry. It will not extend an expiry by itself.

An `authorized` field is rejected. Authority is an evidence URL the runtime
boundary can inspect later. Offline input never becomes a grant.

Schema fields use exact JSON names. Before typed decode, every schema object
must use the canonical spelling of each field. A case-variant is invalid on
its own, and it is invalid beside the canonical spelling even when both values
match. An escaped spelling is read after JSON decoding, so it cannot hide the
same key or a case-variant. A Unicode case-fold that Go's decoder would accept,
such as a long-s alias of `state`, is invalid too. This covers the root
document, envelope, snapshot, prior attempt, nested objects, and objects in
arrays. The checker does not rewrite keys or digests. Alert label maps stay
byte-for-byte case-sensitive: `Service` and `service` are different keys.
Decision 82 in the [saga decision log](../.agents/sagas/distributed-dev-env/README.md)
records the rule. The source step is in
[plan 10](../.agents/sagas/distributed-dev-env/backlog/10-summoned-sessions.md).

## Match, expiry, and custody

A candidate requires every one of these: state `active`; the same operation,
attempt, desired revision, and service; a requested action inside the recorded
scope; one owner UID bound to the session, claim, and live snapshot; a fresh
live observation; an unexpired activity, task deadline, and authorization
window; an exact alert predicate; impact inside the recorded bounds; a usable
baseline; and no unexplained worsening.

Exact means the same service, label map, resource, severity, and symptom.
`cluster`, `*`, and regular expressions do not match.

An alert with startedAt strictly before the activity start is pre-existing.
The result is `respond`, the predicate is `alert-predates-operation`, and the
condition stays visible with its service and index. No cause is attributed.
That alert is not matched. An earlier alert with that failure stays visible
when a later alert matches. An alert at the activity start is eligible only
when every other predicate passes. A baseline observedAt later than the
activity start is invalid input and is rejected before evaluation, for a
healthy or degraded baseline. There is no clock-tolerance exemption across
that boundary. Comparing an alert's observed duration with
impactIntervalSeconds is deferred and is not enforced here.

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

The offline checker's 8h ceiling does not override a shorter activity expiry,
task deadline, or authorization limit, and it does not prove that an Activity
was declared with any particular scope. A synthetic named-service fixture,
including one whose window is 3h, is not permission for cluster maintenance.
Trusted live scope and expiry binding remains a later gate.

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

`sourceCI`, `nativeReview`, and platform `fluxRevision` are untrusted ancillary
inputs. They are not authenticated CI, review, or deployment evidence. They do
not independently gate a candidate today. Core snapshot, owner, and
desired-revision predicates still apply. Actual pull-request CI and native
review gates are verified outside this checker. Full trusted binding and richer
surfacing of these fields are deferred. That limitation is not an implemented
policy.

## Proposed numeric defaults

These retained bounds are conservative offline fixture and validation defaults.
Saga decision 82 records them on 2026-10-23 under the repair authorization.
They are not agreed production SLOs, runtime authorization, deployed heartbeat
changes, or a fresh human ruling. The 45m default, the 8h Activity maximum,
and the 2h cluster cap are existing API facts, not new knobs.

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
Exit 2 is used for invalid use and for invalid input. Schema validation
errors carry JSON that names the fields. Usage errors, input or output errors,
and an invalid `--now` may print a plain diagnostic with no JSON. The command
does not read a token, call the API, launch a session, or suppress an alert.
`--now` injects the clock. Without it, the process wall clock is used.

Every result says the input is unverified and that a candidate is not
operational deferral. `retry` is false. Denied or uncertain launch is not
retried.
