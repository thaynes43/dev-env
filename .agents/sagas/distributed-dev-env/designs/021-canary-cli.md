# DESIGN-021: ID-only single-request canary client

Contract ratified 2026-10-11 UTC; prerequisite for the fixed #173 instruction
probes. The shipped CLI also lacks a canary route. Generic creation fills extra
request fields, retries POST and polls; it cannot safely call the API's strict
ID-only route. Do not invoke an invented subcommand, full CreateSessionRequest,
a raw credential/curl route or a different identity.

## Source contract

Add `agent-run canary create --id <fixedID> --json --once`, with `-o json` also
accepted. Parse the explicit subcommand, bounded server-compatible ID, required
once and all options before ordinary authentication. Unknown options, prompts,
model/provider/profile/repository selections and positional arguments refuse.
Route canary explicitly before the existing creation fallback.

Use the unchanged ordinary caller authentication/TLS and existing `callOnce`.
Clone only this command's HTTP client to refuse redirects. Send exactly one
POST `/v1/sessions`, with JSON containing only `canaryID`. No other request
fields, new idempotency key, provider default, wait/poll, retry or redirect.
The server retains trusted record, caller, policy, ownership and attempt control.

HTTP 201 acknowledges an original Session creation; HTTP 200 acknowledges the
existing original or recovered original after uncertain create acknowledgement.
Neither response establishes provider execution. Strictly validate UTF-8 and the
existing standard-library Session DTO, bounded nonempty original UID, task mode,
client-parent shape, original creation time and deterministic source name derived
by the official parent/client-prefix rule. Preserve the validated original raw
JSON, including field presence/nulls. Return a receipt envelope with canaryID,
httpStatus, disposition CreatedOriginalSession or ExistingOriginalSession, and
session. Errors or incomplete/ambiguous acknowledgements hold the attempt without
another POST, replacement Session, changed key or reconstructed identity.

The response omits the full accepted policy and Directory tuple. Its receipt
alone cannot authorize rearm or establish full source provenance. Root must bind
trusted source/policy/config and real original Directory/Session/HOME/Pod receipts
separately. Existing same-ID response may report the original; it cannot create a
second executor. Actual model, plan readiness, history, usage and stop stay
Unknown until evidenced. No paid API substitution or enrollment copy.

Author scope is dispatcher/name in `internal/agentrun/agentrun.go`, new
`canary.go` and `canary_test.go`. Root owns help, workflow/HANDOFF/saga and this
contract. No schema/controller/auth/client/dependency/GitOps authority changes.
Integrate on merged D20 without changing its validated Directory implementation.

## Verification and run gates

Focused tests prove exact ID-only POST, 201/200 original receipt distinction,
one-request errors, redirect refusal, unknown/pre-auth negative requests, and
malformed/foreign original receipt refusal. Use one bounded low-parallelism
nice19/GOMAX2 focused standard-library CLI test/lint check; no envtest, DNS retry,
local image build, wide test loop or stress. Retain every failed fixture/counter.
Native independent exact-head review and required CI precede squash merge.
Claude reviews are optional while unavailable; read any produced findings/errors.

Only a separately bound merged-source client may invoke the two fixed records.
First install and verify the reviewed trusted configuration. Before EACH first
create, save a fresh complete authorized Directory page through D20 `--once`.
Foreign/ambiguous/incomplete evidence or a same fixed original holds creation.
After creation retain the actual original response and separately confirm the
prospective Directory and immutable private bindings. Count one initial attempt
per provider; never reset it across clients, pods or agents. Uncertain POST keeps
its receipt and deterministic identity for safe observation, without another
creation attempt. No native provider turn occurs during source verification.

This client does not select Q-24 fleet UX, enable global private admission or
prove loaded rules. Actual both-provider instruction output, private repository
authorization and the concurrent owner pilot remain unaccepted.

## Q-26: bounded activation-checker correction

Activation source PR3824 has required CI/native source review, but its finite
observer route stopped at three unsuccessful preparation candidates before any
runtime reads/rollout. Native OBSERVER-METADATA-R1 proves actual operator Pod
labels/annotations are unchecked. Preserve inert R1/R2/R3 and native HOLD
`ac27f6f89aaa1fc8014e4fd7a26a0445905bbf0e061d32075f691c2bb29b1e09`.
A native one-question ask requests one final focused correction plus independent
review and the same bounded checks, or hold activation. Owner answer is pending.
No further checker attempt or rollout is authorized by this source CLI change.
The concrete proposed predicate compares complete Pod and owned-RS labels/
annotations against checked Deployment template metadata plus the verified RS
hash, without filtering/normalization or new permissions/reads. Counters stay
three until explicit additional authorization, with original evidence retained.
Independent D21 source work proceeds; no provider canary has been invoked.
