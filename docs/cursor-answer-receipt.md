# Assistant answer receipt decoder

`decodeAssistantAnswer` in `internal/agentd/cursor_answer_receipt.go` reads one `assistant-answer-v1` document from an `io.Reader`. It is unexported, uses only the standard library, and has no production caller. Nothing in dispatch, protocol, API enums, or runtime configuration calls it. Adding the file does not turn the decoder on.

The decoder returns answer text only after every check below succeeds. Any failure returns a zero result and a fixed error class. The class does not contain the document, the answer text, session IDs, hashes, model labels, or canaries. The decoder does not log, print, or call a callback.

## Format and limits

The document is one JSON object. The producer shape is compact JSON, `ensure_ascii` false, with a trailing newline. The decoder accepts that shape and ordinary JSON whitespace. Key order is not significant. A second value, or any trailing non-whitespace, is rejected.

| Field | Required value |
| --- | --- |
| `receipt` | `assistant-answer-v1` |
| `stage` | `complete` |
| `stopReason` | `end_turn` |
| `sessionID` | Caller-supplied label, exact match |
| `promptSHA256` | Caller-supplied 64 lowercase hex digits, exact match |
| `modelRequested` | Caller-supplied label, exact match |
| `servedModel` | `Unknown` |
| `utf8Bytes` | Integer equal to the UTF-8 length of `text`, from 1 through 65536 |
| `chunkCount` | Integer from 1 through 65536 |
| `text` | Nonempty JSON string |

A label matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`. `chunkCount` counts producer chunks, including empty chunks, so it can be larger than `utf8Bytes`. It is not derived from the text.

The encoded input, including the envelope, must be at most `6*65536+4096` bytes. That covers a 65536-byte answer escaped as `\u00XX` plus the fixed fields. A longer read is `oversized` and is not parsed. The cap bounds memory. It is not a deadline. A blocked `io.Reader` stays the caller's I/O and cancellation problem.

## Example

```json
{"receipt":"assistant-answer-v1","stage":"complete","stopReason":"end_turn","sessionID":"fixture-session","promptSHA256":"abababababababababababababababababababababababababababababababab","modelRequested":"fixture-model","servedModel":"Unknown","utf8Bytes":5,"chunkCount":1,"text":"hello"}
```

This example is only a shape. It is not a trusted artifact and not evidence of a live exchange.

Valid Unicode is kept. A real U+FFFD character stays U+FFFD. Valid JSON escapes, including `\n`, `\t`, `\u0001`, and a surrogate pair such as `\uD83D\uDE00`, decode to the corresponding text. A lone surrogate, a broken `\u` escape, or an invalid UTF-8 byte is rejected. The decoder does not replace those with U+FFFD.

## Failure classes

On failure the text, byte count, and chunk count are zero.

| Class | Condition |
| --- | --- |
| `read-error` | The reader was nil, or a read returned an error other than end-of-file. Bytes already received are discarded |
| `oversized` | Encoded input exceeds the cap |
| `invalid-utf8` | The encoded bytes are not UTF-8 |
| `truncated` | The document ends before the JSON value is complete |
| `malformed` | The top-level value is not one object, or the syntax is not JSON |
| `bad-escape` | A string escape is unknown, truncated inside a non-EOF hex check, or a surrogate is unpaired |
| `duplicate-field` | An object key is repeated after escape decoding |
| `wrong-type` | A value is null, bool, array, object, or the wrong scalar for its field, including a quoted number |
| `bad-number` | A count is negative, fractional, an exponent, a leading zero, or out of int64 range |
| `missing-field` | A required key is absent |
| `extra-field` | A key outside the ten required names is present |
| `bad-receipt` | `receipt` is not `assistant-answer-v1` |
| `bad-stage` | `stage` is not `complete` |
| `bad-stop-reason` | `stopReason` is not `end_turn` |
| `bad-served-model` | `servedModel` is not `Unknown` |
| `binding-mismatch` | Session, prompt hash, or requested model does not match the caller context, or is not the required label or hash form |
| `bad-text` | `text` is empty |
| `bad-utf8-bytes` | `utf8Bytes` is outside 1..65536 or is not the UTF-8 length of `text` |
| `bad-chunk-count` | `chunkCount` is outside 1..65536 |
| `trailing-data` | Non-whitespace follows the one object |

There is no partial answer. A truncated or invalid document does not return the text decoded so far. A non-EOF read error discards every byte already received, including a prefix that would otherwise be a complete document.

## Caller context, not provenance

`answerExpectation` carries the session ID, prompt SHA-256, and requested model the caller already has. The decoder checks that the document repeats those values. That is a consistency check against caller-supplied context. It does not prove an admitted task, a Pod, an author, or an authorization, and it does not prove that the frozen Python helper produced the bytes.

The field set matches `AssistantAnswerReceipt.finalize` in that helper: the ten names above, `servedModel` fixed to `Unknown`, and the same byte and chunk caps. Matching that shape means the document is consistent with the helper. It is not a trusted origin. A unit test copies one frozen producer document as literal bytes and decodes that copy. The copy checks serializer consistency. The test does not run the helper, and a match is not provenance.

`modelRequested` is the requested label. `servedModel` must stay `Unknown`. The decoder does not infer a served model, a tool result, or a provider exit.

## Boundaries

The decoder does not open files, take a lease, start a provider, or attach to a live session. It is not a default-on path and it is not a native or live readiness claim. Filesystem acquisition, lease checks, and live integration remain outside this file.
