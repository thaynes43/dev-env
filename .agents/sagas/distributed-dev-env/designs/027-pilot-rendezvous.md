# DESIGN-027: finite pilot rendezvous and one-shot dispatch

This is a source/offline correction under backlog11's private-project pilot
(#353), using DESIGN-010 claims, DESIGN-014 inbox and DESIGN-013 published
contexts. It does not change their ownership, ACL or provenance contracts.
ADR-003 and the sealed two-slot PilotPolicy remain controlling. No actor,
window, retained seal, provider, operator admission or runtime change is granted.
OpenProject is the intended cross-repository coordination/history system;
status labels are not a security gate. Its readiness/migration is separate.

## Demonstrated failure and smallest correction

The closed 17:50 pilot was PARTIAL. A claimed at17:54:22; its successful claim
capture existed at17:54:43.485878; B invocation waited until17:55:31.640230.
That48.154352s coordinator/model/reporting gap contributed to B's message at
17:57:13 arriving18s after A exited. The combined eight-read budget was four
claim/inbox rounds, not eight rendezvous rounds. Neither ACK nor handoff read
was proved. Task exit0 did not establish private executor stopState, which was
Unknown. These are orchestration failures; no production message/context defect
was demonstrated. Original receipts and all failed attempts remain unchanged.

Use one small command in `scripts/trials/pilot_rendezvous`, not a new controller.
It is nonexecuting by default. The execution adapter reads original A through
existing `agent-run show --json --once` and `log --once -o json`, validates their
captured output, reserves a durable attempt, then invokes the exact prebound B
CLI arguments in the SAME synchronous call. There is no model/reporting callback
or returned command to execute on a later turn. Fake clock/observation/dispatch
adapters exercise this path offline, including the actual command adapter.

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
  attributed message, at most8 rounds45s apart, each round explicitly containing
  one claim-list and one inbox-list (maximum16 RPCs, not8). Stop early on success.
- ACK/reply phase ends at C+10m, never relative to a late model turn. A reads and
  validates B's exact message, ACKs it, publishes its handoff and replies with the
  exact publication/ACK references. B waits at most8 inbox reads30s apart. Both
  phases stop at their absolute deadlines even with unused read allowance.
- Task-terminal/handoff-read phase ends at C+12m. B preserves the full one-shot
  original-A response, exact original UID and actual task finishedAt/exitCode (or
  the existing reviewed terminal record) before the authorized published context
  read. At most8 metadata reads15s apart. Missing evidence means PARTIAL. A's
  task exit is NOT private-executor stopConfirmed; if that stronger condition is
  required, Unknown blocks that claim. No revival or alternate read route.

These paragraphs replace the old ambiguous eight-reads-per-wait sentence in
BOTH future reviewed immutable prompts; preserve all existing claim/conflict,
identity, ACK, context source/hash, nonce/peer-preservation and no-replacement
instructions. Do not insert loaded-rule marker values into either prompt.
Prompt changes require a fresh reviewed policy/seal package before any future
runtime authorization. This source story does not modify or reuse an old seal.

The harness's pre-claim gate wait has a separate cap:60 rounds,5s spacing, at most
5m and an earlier latest feasible window time; each round is two one-shot CLI
reads under one10s context. A successful claim must be no more than30s old and
its heartbeat no more than30s old. The durable attempt must reach invocation
within5s of validation or abort permanently. B's create has a10s deadline.
All CLI, parse, UID, timeout and durable-write errors abort with no fallback.
The `.attempt.json` is created exclusively and synced BEFORE invoking B. It is
never removed after unknown outcomes; a crash may consume the one attempt even
before transmission. No automatic recovery/replacement is allowed. Keep the
same receipt directory per approved cohort. The operator's sealed slots and
idempotency key remain the final guard across differing local directories.

## Offline acceptance and limits

Replay69+53+49=171s claim-to-message against A's153s old wait: retain the18s miss.
Then exercise same-call dispatch with the measured claim-capture offset and
both53s warm startup and the199s pull-to-container envelope+20s bootstrap+49s
first-tools delay. The cold envelope includes unpack/create, not pure pull time.
The next45s round is within360s; a separate30s ACK and terminal phase fit their
absolute ends. These synthetic times prove the budget and orchestration, not
future model latency or runtime behavior. Never-ready and wrong-UID cases must
send zero creates. An uncertain create must reserve exactly one attempt across
re-entry, with no duplicate/replacement action. In-memory CLI tests must show
one-shot refusal/no retry and ordinary retry behavior unchanged.

Scope excludes broader polling frameworks, general session control, automatic
closure/activation, provider research, history migration and speculative edge
hardening. This harness runs on the coordinator with a source-bound CLI; it is
not installed in actor Pods. CLI publication/use of the corrected binary is a
separate shipping gate; no operator/task image change is needed for these paths.
Required exact-head CI, available advisory disposition and independent Mac
review precede source acceptance. No source result establishes runtime acceptance.
