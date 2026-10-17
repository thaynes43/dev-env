# DESIGN-027: finite pilot rendezvous and one-shot dispatch

This is a source/offline correction under backlog11's private-project pilot
(#353), using DESIGN-010 claims, DESIGN-014 inbox and DESIGN-013 published
contexts. It does not change their ownership, ACL or provenance contracts.
ADR-003 and the sealed two-slot PilotPolicy remain controlling. No actor,
window, retained seal, provider, operator admission or runtime change is granted.
OpenProject is the intended cross-repository coordination/history system;
status labels are not a security gate. Its readiness/migration is separate.

## Demonstrated failure and smallest correction

The closed 17:50 pilot was PARTIAL. A claimed at 17:54:22; its successful claim
capture existed at 17:54:43.485878; B invocation waited until 17:55:31.640230.
That48.154352s coordinator/model/reporting gap contributed to B's message at
17:57:13 arriving 18 seconds after A exited. The combined eight-read budget was four
claim/inbox rounds, not eight rendezvous rounds. Neither ACK nor handoff read
was proved. Task exit 0 did not establish private executor stopState, which was
Unknown. These are orchestration failures; no production message/context defect
was demonstrated. Original receipts and all failed attempts remain unchanged.

Use one small command in `scripts/trials/pilot_rendezvous`, not a new controller.
It is nonexecuting by default. The execution adapter reads original A through
existing `agent-run show --json --once` and `log --tail 5000 --once -o json`, validates their
captured output, reserves a durable attempt, then invokes the exact prebound B
CLI arguments in the SAME synchronous call. There is no model/reporting callback
or returned command to execute on a later turn. Fake clock/observation/dispatch
adapters exercise this path offline, including the actual command adapter.

The future reviewed A prompt must require exactly one single-line JSON receipt
in a SHORT COMPLETED tool call, before starting its separate long wait call:
`{"operation":"task-claim-create","exitCode":0,"stdout":"<actual returned JSON, encoded as a JSON string>"}`. Preserve the actual exit
code/stdout; never manufacture a successful receipt. A nonzero result stops A.
The preserved real log-6 already contains this format (its claim creation was
wrapped by the actor); it is not a universal agentd logging guarantee.
Python flush inside a still-running captured tool call does not prove that
stdout has reached the supported CLI log. The claim call must return its actual
wrapper before the wait call begins; do not combine create/receipt and the long
wait in one captured subprocess. Native tool-to-log visibility is still a
runtime evidence requirement; a fake adapter cannot establish it. The
checked-in captured-log projection proves compatibility with the actual output.

The gate checks original Session/Pod UIDs, sealed A prompt/key/profile/model,
fresh heartbeat/head/catalog, reported loaded markers and structured successful
claim receipt with exact owner/Pod/key/catalog/rules/head. A's task-start time is
derived from the supported log; an optional prebound timestamp must match it.
Log output is reported evidence, not independent attestation or access authority.
The operator still validates its CURRENT immutable policy and B request. This
is one uninterrupted coordinator step, not an atomic distributed transaction.

The existing create and log CLI paths automatically retry. Add opt-in `--once`:
create requires project task, caller-specified key, JSON and wait0; it sends one
POST without redirects or pod polls. A lost/invalid successful answer remains
unconfirmed, never a retry. Log requires JSON, one GET, no redirects, exact name
and untruncated strict response. Ordinary default retries remain unchanged.
No new endpoint, token handling, permission or credential source is introduced.
An old CLI rejecting `--once` aborts; there is no compatibility fallback.

## Timing contract and bounded prompt amendment

Let C be A's successful own-claim `createdAt`, T its actual task start, P its
Session creation time and W policy expiry. Reserve one minute before W for
admission closure. Accept a dispatch schedule only when all three phase ends fit
within `min(T+15m, P+20m, W-1m)`. Refuse an already-infeasible schedule instead of
shortening correctness checks or extending any task/Pod/window limit.

- Startup/rendezvous ends at C+6m. A waits for B's exact owned claim and first
  attributed message, at most 8 rounds 45 seconds apart, each round explicitly containing
  one claim-list and one inbox-list (maximum 16 RPCs, not8). Stop early on success.
- ACK/reply phase ends at C+10m, never relative to a late model turn. A reads and
  validates B's exact message, ACKs it, publishes its handoff and replies with the
  exact publication/ACK references. B waits at most 8 inbox reads 30 seconds apart. Both
  phases stop at their absolute deadlines even with unused read allowance.
- Task-terminal/handoff-read phase ends at C+12m. B preserves the full one-shot
  original-A response, exact original UID and actual task finishedAt/exitCode (or
  the existing reviewed terminal record) before the authorized published context
  read. At most8 metadata reads 15 seconds apart. Missing evidence means PARTIAL. A's
  task exit is NOT private-executor stopConfirmed; if that stronger condition is
  required, Unknown blocks that claim. No revival or alternate read route.

These paragraphs replace the old ambiguous eight-reads-per-wait sentence in
BOTH future reviewed immutable prompts; preserve all existing claim/conflict,
identity, ACK, context source/hash, nonce/peer-preservation and no-replacement
instructions. Do not insert loaded-rule marker values into either prompt.
Prompt changes require a fresh reviewed policy/seal package before any future
runtime authorization. This source story does not modify or reuse an old seal.

The harness's pre-claim gate wait has a separate cap: 60 rounds, 5 seconds spacing, at most
5 minutes and an earlier latest feasible window time; each round is two one-shot CLI
reads under one 10-second context. During container creation/bootstrap it reads only
show; it defers log until task/bootstrap metadata is ready (the captured earlier
log request returned404 no-log-yet). Actual CLI/authority errors are not ignored. A successful claim must be no more than 30 seconds old and
its heartbeat no more than 70 seconds old: the installed agentd sends every
60 seconds (`cmd/agentd/main.go`), plus the existing 10-second observation budget.
A missing, future or older heartbeat still refuses. This does not change the
30-second claim-age limit, identity/authority checks or any phase/window bound. The durable attempt must reach invocation
within 5 seconds of validation or abort permanently. B's create has a 10-second deadline.
All CLI, parse, UID, timeout and durable-write errors abort with no fallback.
One exclusive `.run.json` is reserved before ANY transport; prior runs cannot
restart in that directory or overwrite an earlier result. The `.attempt.json` is created exclusively and synced BEFORE invoking B. It is
never removed after unknown outcomes; a crash may consume the one attempt even
before transmission. No automatic recovery/replacement is allowed. Keep the
same receipt directory per approved cohort. The operator's sealed slots and
idempotency key remain the final guard across differing local directories.

## Offline acceptance and limits

Replay69+53+49=171s claim-to-message against A's 153 seconds old wait: retain the18s miss.
Then exercise same-call dispatch with the measured claim-capture offset and
both 53-second warm startup and the 199-second pull-to-container envelope+20s bootstrap+49s
first-tools delay. The cold envelope includes unpack/create, not pure pull time.
The next 45-second round is within360s; a separate 30-second ACK and terminal phase fit their
absolute ends. These synthetic times prove the budget and orchestration, not
future model latency or runtime behavior. Never-ready and wrong-UID cases must
send zero creates. An uncertain create must reserve exactly one attempt across
re-entry, with no duplicate/replacement action. In-memory CLI tests must show
one-shot refusal/no retry and ordinary retry behavior unchanged.

Log reads are capped at 5000 lines and 128 KiB; missing/truncated evidence
refuses dispatch. This finite limit is intentional; do not add broader log
retrieval or private history as a fallback.

Scope excludes broader polling frameworks, general session control, automatic
closure/activation, provider research, history migration and speculative edge
hardening. This harness runs on the coordinator with a source-bound CLI; it is
not installed in actor Pods. CLI publication/use of the corrected binary is a
separate shipping gate; no operator/task image change is needed for these paths.
Required exact-head CI, available advisory disposition and independent Mac
review precede source acceptance. No source result establishes runtime acceptance.

## Closed 19:16 run and local-only correction

The 19:16:08–19:46:08 UTC approved window ended PARTIAL and admission was removed.
Only original A was created. Its task started 19:26:56; the harness refused at
19:27:27.419783 after 9 rounds, with no B dispatch attempt. The installed heartbeat
period is 60s but the old guard rejected age > 30s. The 31.419783s offset matches that
mismatch, but the rejected observation was not retained: the exact old rejection
predicate remains inference, not recovered evidence. No actor/window is reopened.

Future gate refusals retain a bounded predicate/timestamp/SessionUID/PodUID and
heartbeat timestamp/limit in the result receipt, without log/prompt/token copies.
This records the actual failing guard, rather than diagnosing from a later show.

A's claim was created 19:28:42. Its flushed JSON appeared in the final supported
log after round 6 commentary with aggregated script output. The script ended
19:33:57.233877 (315.233877s after claim); exact first log visibility is unknown.
A exhausted 8 rounds/16 reads and its model task ended 19:34:15 exit 0, semantic
PARTIAL. Heartbeat correction alone therefore does not prove a runnable pilot.
Offline replay keeps the 315.233877s buffered counterexample failing, rejects an
already-old visible claim, and demonstrates one immediate dispatch only when a
short completed claim tool call makes the receipt visible within 30s. It models
that contract; it does not establish actual native output transport behavior.

The stopped claim kept this correction local. That claim did not push, open a
PR, trigger hosted CI, merge, release, publish an image, or start a runtime.
On 2026-10-16 Tom directed the Cursor pickup to deliver this exact patch.
This delivery is the source correction only. It does not admit a pilot, reuse
a consumed seal or actor, or start a model.

## Command contract before the next cohort — 2026-10-17

Cohort r10 proved the read command and r11 proved the claim argv. On task image
2.29.5, A passed the InboxMessage name to `inbox-read` as a positional and
exited 2. The working command is
`/usr/local/bin/agentd inbox-read --project dev-env --message EXACT_NAME`.
`inbox-ack` uses the same flags. B then ran `task-claim-create stdin` and
stopped. The working claim shape is
`printf '%s' 'JSON' | /usr/local/bin/agentd task-claim-create` with no extra
argv. The word `stdin` must not appear in either sealed prompt.

`PilotPolicy.ValidateRendezvousCommands` requires both prompts to contain that
claim pipeline, `task-claim-list --project dev-env`, `inbox-list --project dev-env`,
and `inbox-read` / `inbox-ack` with `--project dev-env --message NAME`. `JSON`
and `EXACT_NAME` in `RendezvousCommandText` are placeholders. The check refuses every `inbox-read` and `inbox-ack` that is not that exact
flag form, including a second line beside a good one (`--project dev-env NAME`,
or a message name followed by another token). It also refuses any extra
`task-claim-create` argv and the word `stdin` anywhere in either prompt. Operator `Validate` does not call it, so
existing admission of a short prompt is unchanged and this slice needs no new
operator or task image.

The rendezvous harness `validate` and `run` call the check. A failing prompt
returns before observation, reservation, or B's create. Nonexec of a reviewed
config must pass this check before either actor is created. Context publication
was never reached on r10, so this contract does not certify a `context-publish`
or `context-read` line. Passing the check does not open a cohort, bind a seal,
or accept rendezvous. The next live attempt still needs a fresh seal, a fresh
window, and a bound live ConfigMap UID.
