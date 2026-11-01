# Cursor ACP smoke receipt checker

Offline reader for newline-delimited JSON receipts from
`scripts/cursor-acp-session.py`. It classifies one captured stream and prints
one verdict. It does not launch the helper, start Cursor, open a socket, send a
decision, or approve a permission.

This is source-only. It is not live ACP acceptance, not an AgentSession
provider, and not a change to Cursor settings, credentials, network policy, or
sessions.

## What the receipt sender is

Every line is a client receipt from `cursor-acp-session.py`. The checker does
not see the provider process. Two origins sit behind those client lines:

- `question-required` is a provider request. The client projects the structured
  question id, prompt, options, and `allowMultiple` flag. The checker validates
  that shape, then drops the prompt and option labels.
- `permission-required` is a provider permission request. The client projects
  its option ids and kinds and, since dev-env #758, a bounded `context` object.
  The checker validates both, then drops all context text.
- `decision-sent` is a coordinator decision. The client emits it only after
  writing the decision frame. The receipt carries `requestID`, `sessionID`, and
  `kind`. It does not carry the selected option ids. The checker cannot recover
  them and does not invent them.

`decision-sent` means the client reported a transport send. It does not mean
the provider parsed, understood, or followed the decision. It is not
authenticated approval. `providerUnderstanding` in the verdict is
`not-inferred`.

## Three stages

The `stages` array names only what the receipts show.

1. `fresh-session-transport-established`. A `starting` line followed by one
   `session-created` line. `starting` alone is not this stage.
   `servedModel` is accepted only when it is the literal `Unknown`. The
   requested model id and the prompt hash are not copied out and are not
   served-model evidence. Cost is not read and not reported.
2. `correlated-question-decision-sent`. A `question-required` line and a later
   `decision-sent` line with the same request id, the same session id, and
   `kind` `question`. Integer `40` and string `"40"` are different ids. The
   stage means the coordinator's decision was reported as sent. It does not
   mean the provider accepted the answer.
3. `prompt-complete-after-decision`. A `prompt-complete` line for that same
   session after every pending question and permission request has a matching
   `decision-sent`. `stopReason` is copied only when it is a short label, such
   as `end_turn`. The label is not proof that the provider applied the
   decision, that tools did useful work, or that anyone approved anything.

Other tokens, when present:

- `prompt-complete-without-question`. The prompt finished and no structured
  question was in the stream.
- `permission-decision-sent`. A permission request and a matching
  `decision-sent` with `kind` `permission`. This is not a question/reply and
  not an approval. `permissionApproval` is `not-inferred`. The checker grants
  nothing. See "Question reply, permission rejection and approval" below.
- `helper-failed`. The client emitted `failed` with a fixed reason code.

`autonomousQuestionReply` is false on every verdict. There is no true value.

## Permission context

A `permission-required` receipt may carry one optional `context` key, added by
dev-env #758. A receipt without it (before #758) is still accepted. When the key
is present, `validate_permission_context()` checks it against the bounds the
session applies in `permission_context()` and refuses anything else with the fixed
reason `invalid-permission-context`. The accepted shapes are:

- `{"state":"insufficient-context"}`, the session's fail-closed state;
- that state plus `reason` as its only other field, where `reason` is exactly
  one of `no-correlated-tool-call`, `tool-call-unavailable`,
  `tool-call-conflict`, `tool-call-closed`, `tool-call-expired` or
  `tool-cache-bound`. Any other reason, a non-string reason, or any further
  field is refused. The verdict does not copy the reason;
- the redaction form of that state: `state` `insufficient-context`, a known
  `kind`, `title` exactly `[REDACTED]` and, optionally, `rawInput` exactly
  `[REDACTED]`. The session emits it when a secret-like value fails the whole
  context closed. A `reason` beside this shape is refused;
- `{"state":"available","kind":K,"title":T}` with optional `locations` and
  `rawInput`, and no other key.

The bounds: `kind` is one of the session's tool kinds; every string is at most
256 characters and 256 UTF-8 bytes, nonempty, printable ASCII, with no control,
format, surrogate or line-separator character; `locations` has at most 8
entries, each `{path}` or `{path,line}` with `line` an integer from 0 to
2147483647; `rawInput` is a string, a list of 1 to 8 items or a map of 1 to 8
entries, nested at most 3 edges deep, and a map may use only the keys `command`,
`args`, `path`, `paths`, `query` and `description`; and the whole context is at
most 2048 bytes as compact ASCII JSON. The session reduces path-like strings
to a basename. A `/` is accepted only in a structured `args` string, including
one nested under `args`, that matches the session's relative-argument grammar
(`./...` and `./internal/...` are the representative cases). A title, location,
`command`, `path`, `paths`, `query`, top-level `description`, or a backslash is
still refused. An argument such as `../up.py` is refused because it does not
match that grammar.
The helper rejects `..` under `command` and `args`, including a slash-free `..`
and a `..` segment nested under `path` or `paths`. The checker accepts shape
only. It refuses `.` and `..` only when that is the whole value of a path field
(a location path or `rawInput` `path`/`paths`). A slash-free `..` outside a path
field, such as an argument or a title, is a valid shape. That acceptance is not
the helper's command or args rule. The constants (tool kinds, raw keys, relative grammar,
correlation reasons, size, redaction marker) are copied from the session, and
a test pins them to its values.

The checker does not repeat the session's secret screening and does not claim a
context is safe. `available` means the shape is in bounds, not that the action is
harmless and not that anyone approved it.

The verdict records one state per permission request, in receipt order, and never
the text:

```json
"permissionContexts":[{"requestID":41,"permissionContext":"available"}]
```

`permissionContext` is `available`, `insufficient` (both insufficient shapes) or
`absent` (no `context` key). The key appears only when the stream has a
permission request, answered or not. The title, locations and raw input are not
copied into the verdict or into a refusal.

## Question reply, permission rejection and approval

Three different things can appear in a stream. The verdict keeps them apart.

| Receipts | What the verdict says | Counts as question/reply evidence |
| --- | --- | --- |
| `question-required` then `decision-sent` with `kind` `question` | `correlated-question-decision-sent`, `questionRequestIDs`, `questionDecisionSent` true | The only thing that does. Still transport-only; `autonomousQuestionReply` stays false |
| `permission-required` then `decision-sent` with `kind` `permission` | `permission-decision-sent`, `permissionRequestIDs`, `permissionDecisionMeaning` `client-reject-once-only-not-approval`, `permissionCountedAsQuestionReply` false | No |
| An approve kind (`allow_once`, `allow_always`) chosen | Not representable. A `decision-sent` has no option field, and one that names an option is refused as `unexpected-field` | No, and refused |

The session never approves. It sends only `reject_once` and refuses every other
option (its own tests cover that). The `decision-sent` receipt does not carry the
chosen option id, so the checker cannot see it. It reports the client's
contract, not an observed option, and it never infers an approval
(`permissionApproval` stays `not-inferred`). A permission decision cannot stand in
for a question's decision: a `kind` that does not match the pending request is
refused as `decision-kind-mismatch`, and a permission decision never produces
`prompt-complete-after-decision`, `questionRequestIDs` or `questionOrigin`.
The offered options may include approve kinds, because that is what the provider
asks; that is a request, not a decision.

## A finished prompt is not a question/reply

A fresh helper run can emit `starting`, `session-created`, and
`prompt-complete` with `stopReason` `end_turn`, and nothing else. That has no
`question-required` and no `decision-sent`. The verdict uses
`fresh-session-transport-established` and `prompt-complete-without-question`.
`questionDecisionSent` and `promptCompleteAfterDecision` are false. The reason
is `no-correlated-question-decision`.

That sequence must not be described as autonomous question/reply acceptance.

When a correlated question decision is present, including when
`prompt-complete-after-decision` is also present, the reason is
`decision-sent-is-transport-only`. Completion after the send is ordering
evidence in the client receipts. It is still not provider understanding and
not authenticated approval.

## Refusals

Exit 2 and one line, `{"event":"refused","reason":"..."}`, for malformed JSON,
duplicate fields, unexpected or missing fields, a served-model value other than
`Unknown`, duplicate lifecycle events, duplicate request ids, a stale or
already spent decision, a kind that does not match the pending request, a
cross-session id, an invalid `context` object on a permission request, a
prompt completion while a request is still pending, events
after `prompt-complete` or `failed`, and streams past the helper's bounds.
Reason codes are fixed words. Question text, option labels, permission context text, tool arguments,
assistant text, and claimed costs are not copied into the refusal.

A partial stream that ends without `prompt-complete` or `failed` is classified
as far as its well-formed lines go. Missing stages stay absent. The checker
does not fill them in.

## Runtime limitations

The checker is one process, the standard library, and one verdict on stdout.
It keeps no store, cache, daemon, or listener.

It does not approve tool permissions, plans, credentials, or access. Permission
receipts are classification only. A `decision-sent` line of kind `permission`
is a reported transport send of whatever decision the client already made; this
program does not send it again and does not treat it as approval.

It does not establish live provider compatibility, authentication, a served
model, or a cost. `cursor-acp-session.md` records the helper's own unverified
ACP points. Those remain unverified here. Runtime acceptance, if it is ever
authorized, is a separate step.

## Usage

```sh
python3 scripts/cursor-acp-smoke-check.py path/to/receipts.jsonl
python3 scripts/cursor-acp-smoke-check.py -
```

Exit 0 prints one compact JSON verdict. Exit 2 prints one refusal.

```sh
nice -n 19 python3 -I -B -m unittest discover -s scripts -p 'test_cursor_acp_smoke_check.py' -v
```

Tests use synthetic receipts only. They do not start a provider, read
credentials, or attach to a session.
