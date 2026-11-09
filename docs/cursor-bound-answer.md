# Bound helper answer acceptance

`acceptCursorBoundAnswer` in `internal/agentd/cursor_bound_answer.go` joins one completed `cursorBoundTaskResult` with one `assistant-answer-v1` document. It is unexported and has no production caller. Dispatch, provider registration, launch, and runtime configuration do not call it. The frozen helper, scaffold, identity binding, and answer decoder stay unchanged.

The function does not launch, replay, signal, or start a provider. It does not add a fence or a restart proof.

## Inputs

The caller supplies the expected `cursorBoundTask`, the completed `cursorBoundTaskResult`, the error from that run, a callback that returns the trusted current task context, a `context.Context`, and an `io.Reader` for the answer document. The structs and the document do not prove provenance. A future caller has to supply the actual trusted run result and the actual trusted current context.

`cursorBoundAnswer` carries that bound helper result and the decoded `answerResult` only after every check succeeds. `requestedModel` remains the helper's requested label. `servedModel` remains the helper's `Unknown`. Answer text is not a served-model claim, a tool result, or a provider exit.

## Order

A non-nil run error returns `bound-answer-run` and does not read the answer. The helper must already show `promptComplete`, `stopReason` `end_turn`, a nonempty session label, `servedModel` `Unknown`, the requested model on the task, and the SHA-256 of `plan.prompt`. `completed.binding` must equal the expected binding. Anything else returns `bound-answer-helper` and does not read.

The function then calls `bindCursorTask` on the callback's context. The binding must equal the original, including native invocation and digest, task, Session, Pod, boot, workspace, repository, worktree, and owner generation. It reads only after that check. After `decodeAssistantAnswer` returns, it binds the same way again. A missing callback, a callback error, cancellation, or a missing or replacement identity returns a zero result. The callback error text is not copied into the result.

A nil context, a canceled context, or a callback error is `bound-answer-context`. An identity mismatch is the existing fixed binding error. Decoder failures keep the decoder's fixed class. No failure returns answer text.

## What the two checks are not

Two observations are not an atomic lifecycle lease. Freshness and a trustworthy source stay outside this function. Checking the context before and after the reader does not interrupt a blocked read. Acquisition, deadlines, and leases stay with the caller. The helper session ID is not the platform Session UID.

Matching the frozen helper shape and the frozen decoder is a consistency check. It is not admission of Cursor, a credential, an author, or a live session. Filesystem acquisition and live integration remain outside this file. This is not a native or live readiness claim.
