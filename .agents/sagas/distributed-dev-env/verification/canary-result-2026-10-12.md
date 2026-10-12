# Fixed canary result-reader delivery — 2026-10-12 UTC

## Built and released

[Source #208](https://github.com/thaynes43/dev-env/pull/208) merged as
`cd966d17c8d1a2bca82c27cc01be4d9a11ae5256`. DESIGN-022 adds one named retained
result GET bound to the initiating ordinary client, trusted fixed record and
original Session UID. It preserves general context ACLs, publication identity
and immutable source authority. It needs no live source Pod, exec, provider HOME
or credential read. Missing publication or capture acknowledgement stays Unknown.
Partial managed output remains distinct from complete native history or billing.

Initial fake API creation exposed the pre-create empty-UID refusal. The existing
admission-only DeepCopy preview now covers fixed capture creation; the original
object still has no UID when sent to Kubernetes. Real runtime UID checks remain.
The regression proves one HTTP201 original and HTTP200 idempotent replay with one
Create. Failed fixtures and earlier findings remain retained.

Focused API/client checks and affected lint passed. Required source CI
`38189968708` and native review `f854b781` passed the final fourteen-path candidate;
root source acceptance is `4ae29a11`. [Release #209](https://github.com/thaynes43/dev-env/pull/209)
merged as `ca0dc3f1f441e8a3d1625b273445d50392ace48f`; its required CI and native
review passed. [v2.22.0](https://github.com/thaynes43/dev-env/releases/tag/v2.22.0)
was published at 01:16:38 UTC.

Agent workflow `38191262989`, job `114624320497`, published
`ghcr.io/thaynes43/dev-env:2.22.0@sha256:e8d9af9e9d96d68424143acd2be69028738034d81a4a12495cdd84f2b50dd17f`.
One seven-command proof `388b2d74` and independent saved review `6fcd95d6`
bind the actual tag/release commit, source ancestry, unchanged workflow and
hosted keyless signature verification. Root acceptance is `bff62f2a`.
This signed release is not deployed; the fixed task image stays v2.20.

One cached, offline, low-parallelism build produced the local `dev` client
`0b7c8401` from actual merged source `cd966d17`. Native saved-build review
`b2decdba` passed. It has not been invoked or installed globally and is not a
signed release binary.

Operator workflow `38190613818` published
`ghcr.io/thaynes43/dev-env-operator:sha-cd966d1@sha256:cc03ae820a2c81e65af153c8f97eb6edb129c1158a0b8c51a3222df9e018e345`.
The finite saved proof `20e0ac0a` and native review `4915754e` bind the exact source,
digest and keyless GitHub workflow identity/issuer. Root built/publication
acceptance is `465c3bbb`. Signed publication is not deployment.

## Deployment and remaining acceptance

[haynes-ops #3825](https://github.com/thaynes43/haynes-ops/pull/3825), candidate
`30cf563323599c14b3026dd832731a19da0c26ff`, changes only the operator image and
its adjacent comment plus the deployment record. Native review `ef2f9610`
verified all 1,651 other tracked paths unchanged, including the current dev-env
reloader resources. Required Diff Scope `38191983016` and Flux Local
`38191982995` passed. All 28 operator arguments, two fixed records, task image
and template v2.20 pins, global admission flag and authority remain unchanged.

A single fresh 21-GET baseline was independently accepted before merge.
#3825 merged at 02:15:34 UTC as `b80cac131d9631c26da7e386a5425f401f1f358c`.
After normal Flux settling, one observation verified the new signed image and
preserved peers, fixtures/ledgers, schemas, authority, guards, networks and
catalog/rule ConfigMaps, subject to the separately recorded source-revision
reconciliation below. The image observer's first source
preparation was held for missing semantic checks on its post gates. The retained
R2 correction adds exact native PASS, actual candidate/squash merge and timezone
aware settling bindings before any GET. This is a separate image-delivery unit;
Q-26's final R4 correction already passed and is not retried.

Final binding held R2 because a Python source input was passed to a JSON-only
verifier. The failed plan `b3314f2f` and independent HOLD `04dce093` are retained.
R3 changes only the helper's fixed path, keeps all 31 functions unchanged and
preserves that code provenance in a bound JSON proof. Native final-plan review
`a0d7767b` passed helper `61fe4379` and plan `448d81f5`. One fresh baseline
`a77e5c58` completed with 21 GETs and all 29 predicates passed. Independent saved
review `069a074d` checked 550 raw comparisons and bound the exact eight-field
baseline gate `06dcbf8d`; root acceptance is `d3b6f515`. Neither held preparation
ran. The actual merge receipt `5933a044` and settling receipt `ea71ee2c` require
the single post observer to wait until 02:53:34 UTC. No polling or reconcile is
authorized during this interval.

The one post observer completed after the gate, with 21 GETs and zero provider
turns or creates. Original report `c82b9070` is HOLD_OR_CHANGED: only
`operatorFluxExactReviewedMergeReady` is false. Flux's saved Ready revision is
`0167e37ae41b41bd76984b3ddfcb75e368bbf931`, the one subsequent Ollama #3826
commit. It changes only the assist02/prime image tags. The original receipt
remains unchanged. Root source/saved-evidence contract `06c126d3` allows only an
independent ancestry/scope audit and a separately reviewed reconciliation of the
saved preservation evidence, with no extra GET or observer retry.

Independent source review `a6271339` verifies that `0167e37a` directly follows
`b80cac13`, with only the two Ollama image tags changed. All 1,651 other tracked
paths match in mode, blob and bytes, including all 119 dev-env-system paths and
the 19 v1 reloader resources. Independent saved review `d3e78362` checked 925
bindings/raw comparisons and confirmed the other 28 predicates: the new signed
image, two owned Ready pods and all peer/authority/fixture preservation pass.
The original exact-revision HOLD remains unchanged. Independent combined review
`5541773d` passed 3,350 checks against the source audit and saved observation.
Separate root acceptance `f610a2d6`, recorded at 03:10:31 UTC, accepts the exact
image delivery and preservation within that saved window. It does not claim
continuous liveness or authorize provider execution. Activity `act-021455-2595312`
ended at the same checkpoint.

The latest accepted operator image deployment is #3825. Directory contents remain Unknown;
the first ordinary operator/API read and all fixed creates/result reads remain
unexecuted. Both permanent IDs are preserved and no key is consumed. Before each
first create, require an installed-image/client binding and a fresh, complete
authorized Directory page. Preserve each original Session UID and all uncertain
receipts; there is no alternate Job-output, live-log, identity or rearm route.

The finite source audit `7918f0c4` verifies that the existing authorized named
Session GET supplies retained executor provenance for the ordinal-zero context
name. Its current `show` client retries/follows redirects, so the separately
published DESIGN-023 contract adds an explicit `show --json --once` client before
any provider starts. Its first focused checks passed, but independent source
review `afc20186` found that JSON decoding could strip padding before the size
check. [Source #210](https://github.com/thaynes43/dev-env/pull/210), candidate
`231e5b901da68e140a7e4dee08d0bf2a2c120c04`, adds a default-off response option
enabled only on the once connection clone. It checks the wire body before
RawMessage decoding. The actual HTTP regression, focused tests and affected
lint passed; independent full eight-path review `366e6d8e` and root source
acceptance `d92f07ec` passed. Required CI `38196682673` passed and #210 merged as
`4c1e0c37cb228d54556237276b09b502914c027b` at 02:49:29 UTC. Root actual-source
binding `2d545ffd` verifies the squash tree and all eight reviewed files in a
fresh task worktree. [Release #211](https://github.com/thaynes43/dev-env/pull/211)
merged as `c2ac2ab70ef34adc17145ac6f71b5051a3e1bfae`; native release review
`3393548e` and required CI `38197327898` passed. [v2.23.0](https://github.com/thaynes43/dev-env/releases/tag/v2.23.0)
was published at 03:09:41 UTC. The actual merged-source local client remains a
gate before provider execution. Its first inert build preparation remains HOLD
for an incorrect source-file classification; the successor must bind the actual
merge and all 67 unchanged, four changed and two added source files.
The held candidate and prior checks are preserved.
Private delivery ACK and complete native history remain
Unknown. No runtime request was used to discover this client gap.

Closed installed binding `1061d476` records the ordinary initiating caller and
the catalog, template and both common-rule ConfigMap UID/resourceVersion values
from the saved post observation. Independently accepted common serialization
`680b0ea1` supplies snapshot digest `95ab08b1`. First Directory final plan
`751813d5` binds the accepted D22 local client and source, one complete-page read
and no creation authority. Independent final preparation review and separate
root one-read authorization remain required; neither helper has been executed.

Actual Claude/Codex common and repository rule loading, private GitHub access,
live/stopped discovery, full native history, claims/inbox consumer behavior,
whole-task budgets/recovery and concurrent isolated owner acceptance remain open.
Global private-project admission is disabled. Local-model contracts remain in
scope; inference waits for GPU readiness and household reservations. Q-24's
Codex routing decision remains pending and gates full native-session isolation,
as well as fleet UX. Separate paired session hosts remain recommended; choosing
coordinator/workers retains a shared native harness failure boundary. Parent issues
remain open and source tests do not establish a working MVP.

Claude review is optional during the outage. #208 and #3825 error-only runs were
read; no findings were produced and no retry or auth repair was attempted. Required
CI and native independent review remain shipping gates. Q-25's stopped local
DNS/schema route stays stopped; hosted CI and saved real-API schema evidence apply.

## Parity acceptance checkpoint

ADR-003 and the owner build order continue to govern delivery. Their acceptance
is operational: shared rules and authorized data through the operator/API, with
each independent session's home, repository, worktree, credentials and enrollment
private. These gates precede later UI work.

| Requirement | Current boundary | Remaining acceptance |
| --- | --- | --- |
| Shared connector configuration | The v1 MCP source feeds both subscription clients; local-provider contracts are included. | Prove managed provider loading of the common connector/rule version. |
| Guarded homelab access | Existing authorized access remains; required-access changes use separate guarded slices. | Verify parity through ordinary identities; new persistent or elevated access needs its own approval. |
| Usage, quota and cost provenance | Usage foundations expose observations and Unknown states; requested model is not served-model evidence. | Prove exact served-model and API-cost provenance per provider; admin UI follows parity. |
| Coordination and history | Directory, claims, inbox and stopped-context source slices are shipped. | Prove authorized consumer behavior and durable private stopped history in the pilot. |
| Upgrades and native recovery | #3825 image delivery and saved peer preservation are accepted; provider-specific recovery remains distinct. | Prove live-provider upgrade preservation and truthful resume without restarting existing sessions. |
| Complete isolated sessions | Bounded session pods are required; separate executors alone do not isolate a native harness. | Resolve Q-24 and prove placement, concurrency and one disposable termination leaving peers usable. |
| Observability and future control | Operator/API evidence is required now; inspection/control UI is later scope. | Verify live session/usage evidence before adding the post-parity UI. |
| Meatball control | The supported native/API route and auditable authorization must be explicit for each harness. | Prove discovery, reads, start/steer/stop and history; report unsupported operations truthfully. This is a control acceptance item, not a UI milestone. |

Local inference waits for GPU readiness and household reservations. Additional
harnesses, including the Gemini request relayed by oversight, remain unverified
scope: record a supported native/subscription route or explicit alternative
before claiming support. That relay grants no new access, credentials, paid API
substitution or cutover authority. No autonomous v1 retirement or restart is
authorized. Q-24 is already asked and must not be repeated.
