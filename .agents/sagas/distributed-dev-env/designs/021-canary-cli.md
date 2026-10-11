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
The complete metadata predicate remains unimplemented and pending review. It
must check source-controlled template metadata and the owned RS identity; any
generated metadata must follow an explicit finite, separately reviewed contract
bound to saved real-API evidence. Template-plus-RS-hash equality alone is not a
verified predicate: the saved original Pods contain other generated metadata.
No broad filtering, new permissions or extra reads are authorized. Counters stay
three until explicit additional authorization, with original evidence retained.
Independent D21 source work proceeds; no provider canary has been invoked.

## Built source candidate — 2026-10-11 UTC

The integrated candidate d4ebcfe69afeed1906353b07d6951b4980a3efd4 is based
on merged D20 c9493db0ab9f5b899e78e5617b650f0bd7077d9e. The first bounded
combined Canary/Directory dispatch suite passed in 1.535 seconds; affected lint
passed in 0.942 seconds. No TestMain, build, runtime GET, API or provider
invocation occurred. Validation packet
`6b8aa213720f31b516c3b69a7f76e784e698d4aa0e07524d53f47fdb4631fa3c`
retains the original pre-integration freeze, new combined-source freeze and
571 preserved incoming paths. D20's original failed fixture and corrected PASS
remain retained. This paragraph changes only source status: final native review,
required CI and merge remain gates; the actual fixed campaign is unstarted.

Native D21-DOC-R1 at original review head058b7df91f09797c44c7f6a5af60bcf5fa40baa4
found that the proposed Q-26 metadata wording omitted known generated Pod
metadata. The contract is corrected to mark the complete predicate pending and
require any generated fields to have a separate finite reviewed binding. No
checker candidate, runtime read, source-code change or verification rerun follows
from this docs correction; original native evidence and the three-attempt stop
remain retained.

## Source shipping record — 2026-10-11

Source #206 merged562d7df2 after corrected full nativeb82 and required
CI38181882755. Releasev2.21.0 includes D20/D21. The one bound cached client
build passed, with runtime invocation still held. See [client evidence](../verification/clients-2026-10-11.md)
for original failures, local/source/release proof and remaining gates. Q-26's
later owner ruling allows ONE final focused R4 correction, without resetting
three failed preparations; full finite metadata preparation remains pending.
No checker execution, baseline, rollout or provider was started. Original Q-26
pending text above records the earlier state; this ruling supersedes it. No fifth
candidate or automatic retry if R4 fails. Q-24 remains pending.
