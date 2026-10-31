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
  nothing.
- `helper-failed`. The client emitted `failed` with a fixed reason code.

`autonomousQuestionReply` is false on every verdict. There is no true value.

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
cross-session id, a prompt completion while a request is still pending, events
after `prompt-complete` or `failed`, and streams past the helper's bounds.
Reason codes are fixed words. Question text, option labels, tool arguments,
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
