# Scheduled builtin refusal localization

The installed scheduled helper now identifies both Claude and Cursor failures
at the builtin stage. Its control flow establishes that version, initial
identity metadata and the selected binding passed before those failures.
`capture_refused` does not identify the underlying builtin refusal, establish
missing authentication or authorize another provider invocation.

Keep the existing closed diagnostic frame, size bound and scheduled transport.
Add only fixed reason codes at the existing builtin refusal points. Distinguish
Claude event/report/result validation and missing completion from Cursor terminal,
prompt, input and display refusal. Never emit the rejected event type, subtype,
terminal character, vendor error, account value or output. An unqualified zero
counter is not a claim about actual billed tokens or cost.

The fixed vendor arguments, process ownership, timeout, kill/reap sequence,
zero-turn/zero-token requirement, terminal input fences and cleanup stop remain
enforced. The Cursor process must preserve a coded protocol error rather than
replace it with the generic capture error. The diagnostic codes add no accepted
input or capture; the separate renderer correction below recognizes the exact
selected inactive menu row.
Cleanup uncertainty still overrides a more specific diagnostic.

Synthetic parser and owned fake-process cases first failed against the existing
generic reason in 0.005s. They exercise privacy and retained refusals without a
provider invocation. Existing protocol, process and cleanup suites must remain
green. Source tests, publication, installation and the next actual scheduled
reason remain separate evidence; these diagnostics alone do not fix the live
acquisition or establish two successful three-provider cycles.

## Source counterexamples from the independent audits

The supplied version-qualified manual Cursor display describes one autocomplete
row containing the selected `/usage` command and its exact description. Its ANSI
cursor coordinates were not retained. A synthetic run through the real state
machine reproduced the resulting source limitation: the one inactive row did not
advance, while the existing two-row menu did. The regression failed in 0.004s,
then passed when the parser recognized exactly one inactive row with those fixed
contents. The old two-row form remains supported. Active-row checks and the
separate ready/menu/display states still apply; foreign commands, multiple
selected rows, appended text, and authentication or confirmation prompts never
authorize input. This is a guarded renderer correction, not a claim about the
live cursor position or the cause of the fast refusal. The unverified
question-worded workspace-trust suggestion does not change the prompt guards.

The Claude audit proposed a contradictory nested cache-token fixture. Execution
confirmed that nonzero nested counters were previously ignored when aggregate
counters, turns and cost were zero (RED 0.003s). The added walk validates nested
token counters as exact numeric zero and only tightens accepted input. A
previously accepted zero breakdown stays accepted; unqualified object-valued
top-level token fields stay refused. This closes a source safety gap, not a
known live failure. Current API documentation alone does not qualify additional
objects for the pinned CLI result protocol.

## Context failures and diagnostic priority

The available advisory review identified that cancellation closes the Cursor
terminal too, so a terminal/read error could be labelled as the cause of a real
timeout. Owned idle-process tests reproduced deadline and explicit cancellation
misclassification for both Cursor and Claude (RED 0.105s each). Context failure
now takes priority over terminal/stream diagnostics, using the existing closed
`deadline` and `cancelled` codes. Cleanup uncertainty still takes priority over
both. The focused process regressions passed in 0.206s. No timeout or process
cleanup mechanism changed.
